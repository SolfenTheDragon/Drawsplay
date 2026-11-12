//! Everything that talks to the host, off the UI thread: connect, pair, receive video,
//! feed the decoder, and send queued input.

use dd_proto::identity::{authorize_with, config_dir, load_or_create_identity, to_hex, TrustStore};
use dd_proto::secure::handshake;
use dd_proto::{read_message, video_size_ok, write_message, Message, PointerPhase, PROTOCOL_VERSION};
use std::io::{Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{Receiver, RecvTimeoutError};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

/// Longest silence from the host before the session is treated as lost.
const IDLE_TIMEOUT: Duration = Duration::from_secs(10);

/// Most queued messages merged per wake-up.
const MAX_BATCH: usize = 4096;

/// Drop pointer moves that a later move of the same pointer makes pointless. Order of everything
/// else (buttons, keys, touch down/up, wheel) is preserved.
fn coalesce(batch: Vec<Message>) -> Vec<Message> {
    let mut out: Vec<Message> = Vec::with_capacity(batch.len());
    for m in batch {
        if let Message::Pointer { id, source, phase, buttons, wheel_x, wheel_y, .. } = &m {
            let movement = matches!(phase, PointerPhase::Move | PointerPhase::Hover);
            if movement && *wheel_x == 0.0 && *wheel_y == 0.0 {
                if let Some(Message::Pointer {
                    id: pid,
                    source: psrc,
                    phase: pph,
                    buttons: pb,
                    wheel_x: pwx,
                    wheel_y: pwy,
                    ..
                }) = out.last()
                {
                    if pid == id && psrc == source && pph == phase && pb == buttons && *pwx == 0.0 && *pwy == 0.0 {
                        out.pop();
                    }
                }
            }
        }
        out.push(m);
    }
    out
}

/// Decoded frames: the UI keeps only the newest one, and recycles buffers through `pool`.
#[derive(Default)]
pub struct FrameSlot {
    latest: Mutex<Option<Vec<u32>>>,
    pool: Mutex<Vec<Vec<u32>>>,
    pub frames_decoded: AtomicU64,
}

impl FrameSlot {
    /// Newest undisplayed frame, if any.
    pub fn take(&self) -> Option<Vec<u32>> {
        self.latest.lock().unwrap().take()
    }
    /// Give a displayed buffer back for reuse.
    pub fn recycle(&self, buf: Vec<u32>) {
        let mut p = self.pool.lock().unwrap();
        if p.len() < 4 {
            p.push(buf);
        }
    }
    fn fresh(&self, len: usize) -> Vec<u32> {
        match self.pool.lock().unwrap().pop() {
            // Reused as is: the decoder overwrites every pixel (a short read discards the frame).
            Some(b) if b.len() == len => b,
            _ => vec![0u32; len],
        }
    }
    fn publish(&self, buf: Vec<u32>) {
        if let Some(old) = self.latest.lock().unwrap().replace(buf) {
            self.recycle(old); // never shown: drop it to stay current
        }
        self.frames_decoded.fetch_add(1, Ordering::Relaxed);
    }
}

pub enum UiEvent {
    Status(String),
    Decision {
        action: Option<String>,
        request: Arc<AtomicBool>,
    },
    /// The host accepted us; the video will be this size.
    Connected {
        width: u32,
        height: u32,
        fps: u32,
    },
    /// A new frame is waiting in the [`FrameSlot`].
    Frame,
    /// Round-trip time to the host in milliseconds.
    Rtt(f32),
    /// The session ended (reason for the user).
    Closed(String),
}

pub struct Settings {
    pub host: String,
    pub width: u32,
    pub height: u32,
    pub fps: u32,
    pub ffmpeg: String,
    pub trust_all: bool,
    pub hwdec: bool,
    pub online: Arc<AtomicBool>,
    pub cancel: Arc<AtomicBool>,
}

/// Wall-clock microseconds, used as the opaque value in Ping so the echo yields an RTT.
fn now_us(epoch: Instant) -> u64 {
    epoch.elapsed().as_micros() as u64
}

/// Start the decoder for a `width` x `height` stream. The video comes from another computer, so the
/// decoder is held to what was agreed: a single allocation cannot exceed 256 MiB (an oversized picture
/// declared in the stream fails instead of eating memory), and every frame leaves at exactly the agreed
/// size, so a stream that disagrees with its Config is scaled rather than misread as garbage.
pub fn spawn_decoder(ffmpeg: &str, hwdec: bool, width: u32, height: u32) -> std::io::Result<Child> {
    let mut c = Command::new(ffmpeg);
    c.args(["-hide_banner", "-loglevel", "error", "-max_alloc", "268435456"]);
    if hwdec {
        c.args(["-hwaccel", "auto"]);
    }
    c.args(["-fflags", "nobuffer", "-flags", "low_delay", "-probesize", "32", "-analyzeduration", "0"])
        .args(["-f", "h264", "-i", "-", "-s", &format!("{width}x{height}"), "-f", "rawvideo", "-pix_fmt", "bgra", "-"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    c.spawn()
}

/// How long the client keeps trying to get back to a host it was connected to.
const RECONNECT_WINDOW: Duration = Duration::from_secs(60);

/// Run a session, and reconnect when a working connection is lost. Blocks until the user closes the
/// window, the host is gone for good, or the session cannot start; always reports the end through `ui`.
pub fn run(
    s: Settings,
    input: Receiver<Message>,
    slot: Arc<FrameSlot>,
    ui: impl Fn(UiEvent) + Send + Sync + Clone + 'static,
) {
    let connected = Arc::new(AtomicBool::new(false));
    let seen = connected.clone();
    let ui_inner = {
        let ui = ui.clone();
        move |ev: UiEvent| {
            if matches!(ev, UiEvent::Connected { .. }) {
                seen.store(true, Ordering::SeqCst);
            }
            ui(ev)
        }
    };
    // Explicit disconnection, declined pairing or trust revocation must not reconnect automatically.
    let bye: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
    let mut lost_at: Option<Instant> = None;
    let mut backoff = Duration::from_millis(500);
    let reason = loop {
        if s.cancel.load(Ordering::Acquire) {
            break "connection closed".into();
        }
        connected.store(false, Ordering::SeqCst);
        let r = session(&s, &input, &bye, slot.clone(), ui_inner.clone());
        s.online.store(false, Ordering::Release);
        slot.take();
        if connected.load(Ordering::SeqCst) {
            lost_at = None; // it worked for a while: start counting afresh
            backoff = Duration::from_millis(500);
        }
        if bye.lock().unwrap().is_some() {
            break r.err().unwrap_or_else(|| "the host ended the session".into());
        }
        // The window is gone: nothing to reconnect for.
        if matches!(input.try_recv(), Err(std::sync::mpsc::TryRecvError::Disconnected)) {
            break r.err().unwrap_or_else(|| "connection closed".into());
        }
        let why = match r {
            Ok(()) => "host closed the connection".to_string(),
            Err(e) => e,
        };
        // Only a connection that once worked is retried; a first attempt that fails reports at once.
        let had_session = lost_at.is_some() || connected.load(Ordering::SeqCst);
        let first_loss = *lost_at.get_or_insert_with(Instant::now);
        if !had_session || first_loss.elapsed() > RECONNECT_WINDOW {
            break why;
        }
        ui(UiEvent::Status(format!(
            "Connection lost: {why}\nReconnecting in {:.1} seconds (up to 60 seconds)...",
            backoff.as_secs_f32()
        )));
        let until = Instant::now() + backoff;
        while Instant::now() < until {
            if s.cancel.load(Ordering::Acquire) {
                return ui(UiEvent::Closed("connection closed".into()));
            }
            // Input typed during the outage must not replay later.
            match input.try_recv() {
                Err(std::sync::mpsc::TryRecvError::Disconnected) => return ui(UiEvent::Closed(why)),
                _ => thread::sleep(Duration::from_millis(50)),
            }
        }
        backoff = (backoff * 2).min(Duration::from_secs(5));
    };
    ui(UiEvent::Closed(reason));
}

fn session(
    s: &Settings,
    input: &Receiver<Message>,
    bye: &Arc<Mutex<Option<String>>>,
    slot: Arc<FrameSlot>,
    ui: impl Fn(UiEvent) + Send + Sync + Clone + 'static,
) -> Result<(), String> {
    let e = |what: &'static str| move |err: std::io::Error| format!("{what}: {err}");
    let mut last_error = std::io::Error::other("host has no addresses");
    let mut connection = None;
    for address in s.host.to_socket_addrs().map_err(e("host lookup"))? {
        if s.cancel.load(Ordering::Acquire) {
            return Err("connection closed".into());
        }
        match TcpStream::connect_timeout(&address, Duration::from_secs(5)) {
            Ok(stream) => {
                connection = Some(stream);
                break;
            }
            Err(error) => last_error = error,
        }
    }
    let mut stream = connection.ok_or(last_error).map_err(e("cannot connect"))?;
    // Closing the window also interrupts handshake/config reads and withdraws a pairing dialog.
    let watcher_socket = stream.try_clone().map_err(e("socket"))?;
    let stop = Arc::new(AtomicBool::new(false));
    struct Watcher(Arc<AtomicBool>);
    impl Drop for Watcher {
        fn drop(&mut self) {
            self.0.store(true, Ordering::Release);
        }
    }
    let _watcher = Watcher(stop.clone());
    let cancel = s.cancel.clone();
    thread::spawn(move || {
        while !stop.load(Ordering::Acquire) {
            if cancel.load(Ordering::Acquire) {
                let _ = watcher_socket.shutdown(std::net::Shutdown::Both);
                break;
            }
            thread::sleep(Duration::from_millis(100));
        }
    });
    stream.set_nodelay(true).map_err(e("socket"))?;

    let dir = config_dir("client").map_err(e("config directory"))?;
    let id = load_or_create_identity(&dir).map_err(e("identity"))?;
    let mut trust = TrustStore::load(dir.join("trusted_hosts.txt")).map_err(e("trusted hosts"))?;
    stream.set_read_timeout(Some(Duration::from_secs(15))).map_err(e("socket"))?;
    stream.set_write_timeout(Some(IDLE_TIMEOUT)).map_err(e("socket"))?;
    let sess = handshake(&mut stream, &id, true).map_err(e("secure handshake failed"))?;
    // The pairing question waits for a person; the host may also take a while to create its display.
    stream.set_read_timeout(Some(Duration::from_millis(100))).map_err(e("socket"))?;
    ui(UiEvent::Status(format!(
        "Pairing code: {}\nCompare on the host if asked.\nWaiting for host approval...\nHost: {}\nIdentity: {}",
        sess.sas,
        s.host,
        to_hex(&sess.remote_public)
    )));
    let authorization = authorize_with(&sess, &mut trust, &format!("host at {}", s.host), s.trust_all, &mut |desc, sas| {
        dd_proto::desktop_ui::approve(
            &format!("{desc}\nThis client's identity: {}\n\nPairing code: {sas}\nCompare on BOTH computers. Trust only if the codes match.\nThis host will see input typed into the remote screen.", to_hex(&id.public)),
            Duration::from_secs(120),
            || {
                if s.cancel.load(Ordering::Acquire) { return false; }
                let mut byte = [0];
                match stream.peek(&mut byte) {
                    Ok(0) => false,
                    Ok(_) => true,
                    Err(e) => matches!(e.kind(), std::io::ErrorKind::TimedOut | std::io::ErrorKind::WouldBlock),
                }
            },
        )
    }).map_err(e("pairing"));
    match authorization {
        Ok(true) => {}
        answer => {
            let reason = answer.err().unwrap_or_else(|| "pairing declined".into());
            *bye.lock().unwrap() = Some(reason.clone());
            return Err(reason);
        }
    }
    let raw = stream.try_clone().map_err(e("socket"))?;
    let mut reader = sess.reader(stream.try_clone().map_err(e("socket"))?);
    let writer = Arc::new(Mutex::new(sess.writer(stream)));

    let name = std::env::var("COMPUTERNAME")
        .or_else(|_| std::env::var("HOSTNAME"))
        .or_else(|_| std::fs::read_to_string("/proc/sys/kernel/hostname").map(|h| h.trim().to_string()))
        .ok()
        .filter(|h| !h.is_empty())
        .unwrap_or_else(|| "client".into());
    write_message(
        &mut *writer.lock().unwrap(),
        &Message::Hello { version: PROTOCOL_VERSION, width: s.width, height: s.height, refresh_hz: s.fps, name },
    )
    .map_err(e("hello"))?;
    // On a first connection the host's user may still be comparing the pairing code.
    // The host's user may be answering a pairing question about this computer, whether or not we
    // trust the host already (for example after the host forgot us).
    let config_wait = Duration::from_secs(300);
    reader.get_ref().set_read_timeout(Some(config_wait)).map_err(e("socket"))?;
    let (w, h, fps) = match read_message(&mut reader).map_err(e("waiting for host"))? {
        Message::Config { width, height, fps } if video_size_ok(width, height) => {
            (width as usize, height as usize, fps)
        }
        _ => return Err("host sent an invalid Config".into()),
    };
    // The host streams continuously, so a quiet connection means it is gone (cable pulled, Wi-Fi lost).
    reader.get_ref().set_read_timeout(Some(IDLE_TIMEOUT)).map_err(e("socket"))?;
    let mut dec = spawn_decoder(&s.ffmpeg, s.hwdec, w as u32, h as u32)
        .map_err(|x| format!("cannot start ffmpeg ({}): {x}", s.ffmpeg))?;
    let mut dec_in = dec.stdin.take().unwrap();
    let mut dec_out = dec.stdout.take().unwrap();
    let epoch = Instant::now();
    // No input collected during handshake, approval or a reconnect may reach the new session.
    while input.try_recv().is_ok() {}
    s.online.store(true, Ordering::Release);
    ui(UiEvent::Connected { width: w as u32, height: h as u32, fps });

    // network -> decoder
    let pong = writer.clone();
    let ui_net = ui.clone();
    let timed_out = Arc::new(AtomicBool::new(false));
    let timed_out_net = timed_out.clone();
    let bye_net = bye.clone();
    let net = thread::spawn(move || loop {
        match read_message(&mut reader) {
            Ok(Message::Video { data, .. }) => {
                if dec_in.write_all(&data).is_err() {
                    break;
                }
            }
            Ok(Message::Ping(v)) => {
                if write_message(&mut *pong.lock().unwrap(), &Message::Pong(v)).is_err() {
                    break;
                }
            }
            Ok(Message::Bye { reason }) => {
                *bye_net.lock().unwrap() = Some(format!("the host ended the session ({reason})"));
                break;
            }
            Ok(Message::Pong(sent)) => {
                let rtt = now_us(epoch).saturating_sub(sent) as f32 / 1000.0;
                ui_net(UiEvent::Rtt(rtt));
            }
            Ok(_) => {}
            Err(err) => {
                if matches!(err.kind(), std::io::ErrorKind::TimedOut | std::io::ErrorKind::WouldBlock) {
                    timed_out_net.store(true, Ordering::SeqCst);
                }
                break;
            }
        }
    });

    // decoder -> newest frame
    let slot_dec = slot.clone();
    let ui_dec = ui.clone();
    let dec_thread = thread::spawn(move || {
        let len = w * h;
        loop {
            let mut buf = slot_dec.fresh(len);
            // BGRA bytes are exactly the little-endian 0xAARRGGBB u32 the window wants.
            if dec_out.read_exact(bytemuck::cast_slice_mut(&mut buf)).is_err() {
                break;
            }
            slot_dec.publish(buf);
            ui_dec(UiEvent::Frame);
        }
    });

    // input -> host (plus a periodic ping)
    let mut next_ping = Instant::now();
    let mut last_frame_at = Instant::now();
    let mut decoded = slot.frames_decoded.load(Ordering::Relaxed);
    let mut last_trust_check = Instant::now();
    let result = loop {
        if s.cancel.load(Ordering::Acquire) {
            break Ok(());
        }
        if !s.trust_all && last_trust_check.elapsed() >= Duration::from_secs(2) {
            last_trust_check = Instant::now();
            if TrustStore::load(dir.join("trusted_hosts.txt")).is_ok_and(|store| !store.contains(&sess.remote_public)) {
                let reason = "Host removed from this client's trusted devices.".to_string();
                *bye.lock().unwrap() = Some(reason.clone());
                break Err(reason);
            }
        }
        let current = slot.frames_decoded.load(Ordering::Relaxed);
        if current != decoded {
            decoded = current;
            last_frame_at = Instant::now();
        }
        if last_frame_at.elapsed() >= IDLE_TIMEOUT {
            break Err("video stopped responding for 10 seconds".into());
        }
        if net.is_finished() || dec_thread.is_finished() {
            break Ok(());
        }
        match input.recv_timeout(Duration::from_millis(100)) {
            Ok(m) => {
                // Take everything queued behind it and merge stale pointer moves.
                let mut batch = vec![m];
                batch.extend(input.try_iter().take(MAX_BATCH));
                let mut w = writer.lock().unwrap();
                if coalesce(batch).iter().any(|m| write_message(&mut *w, m).is_err()) {
                    break Err("connection lost".to_string());
                }
            }
            Err(RecvTimeoutError::Timeout) => {}
            Err(RecvTimeoutError::Disconnected) => break Ok(()),
        }
        if Instant::now() >= next_ping {
            let _ = write_message(&mut *writer.lock().unwrap(), &Message::Ping(now_us(epoch)));
            next_ping = Instant::now() + Duration::from_secs(2);
        }
    };
    s.online.store(false, Ordering::Release);
    ui(UiEvent::Status("Connection interrupted. Stopping the old stream...".into()));
    let _ = raw.shutdown(std::net::Shutdown::Both);
    let _ = dec.kill();
    let _ = dec.wait();
    let _ = net.join();
    let _ = dec_thread.join();
    if let Some(reason) = bye.lock().unwrap().clone() {
        return Err(reason);
    }
    if timed_out.load(Ordering::SeqCst) {
        return Err(format!("host stopped responding (nothing received for {} s)", IDLE_TIMEOUT.as_secs()));
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    use dd_proto::PointerSource;

    fn ptr(id: u8, phase: PointerPhase, x: f32) -> Message {
        Message::Pointer {
            id,
            source: PointerSource::Touch,
            phase,
            x,
            y: 0.0,
            pressure: 1.0,
            buttons: 0,
            tilt_x: 0,
            tilt_y: 0,
            wheel_x: 0.0,
            wheel_y: 0.0,
        }
    }

    #[test]
    fn consecutive_moves_collapse_to_the_last() {
        let out = coalesce(vec![
            ptr(1, PointerPhase::Down, 0.0),
            ptr(1, PointerPhase::Move, 0.1),
            ptr(1, PointerPhase::Move, 0.2),
            ptr(1, PointerPhase::Move, 0.3),
            ptr(1, PointerPhase::Up, 0.3),
        ]);
        assert_eq!(out.len(), 3);
        assert!(matches!(out[1], Message::Pointer { x, .. } if x == 0.3));
    }

    #[test]
    fn other_pointers_and_keys_keep_their_order() {
        let out = coalesce(vec![
            ptr(1, PointerPhase::Move, 0.1),
            ptr(2, PointerPhase::Move, 0.2),
            ptr(1, PointerPhase::Move, 0.3),
            Message::Key { code: 4, down: true },
            ptr(1, PointerPhase::Move, 0.4),
            ptr(1, PointerPhase::Move, 0.5),
        ]);
        assert_eq!(out.len(), 5);
    }

    fn have_ffmpeg() -> bool {
        Command::new("ffmpeg").arg("-version").stdout(Stdio::null()).stderr(Stdio::null()).status().is_ok()
    }

    /// Decode `frames` frames of a live 320x240 test stream (rate-limited, like a real host) with a
    /// decoder told to expect `w`x`h`.
    fn decode_test_stream(w: u32, h: u32, frames: usize) -> Vec<u8> {
        let mut enc = Command::new("ffmpeg")
            .args(["-v", "error", "-fflags", "nobuffer", "-re", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30"])
            .args(["-pix_fmt", "yuv420p", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency"])
            .args(["-g", "30", "-bf", "0", "-f", "h264", "-"])
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        let mut dec = spawn_decoder("ffmpeg", false, w, h).unwrap();
        let (mut from_enc, mut to_dec) = (enc.stdout.take().unwrap(), dec.stdin.take().unwrap());
        let feeder = thread::spawn(move || {
            let _ = std::io::copy(&mut from_enc, &mut to_dec);
        });
        let mut out = vec![0u8; (w * h * 4) as usize * frames];
        let got = dec.stdout.take().unwrap().read_exact(&mut out);
        let _ = enc.kill();
        let _ = dec.kill();
        let _ = enc.wait();
        let _ = dec.wait();
        let _ = feeder.join();
        got.expect("decoded frames");
        out
    }

    #[test]
    fn decoder_yields_frames_of_the_agreed_size_even_if_the_stream_differs() {
        if !have_ffmpeg() {
            return;
        }
        // Matching size: real picture content, not a black frame.
        assert!(decode_test_stream(320, 240, 3).iter().any(|&b| b != 0));
        // The stream is 320x240 but 160x120 was agreed: frames are scaled to the agreed size, so
        // reading whole frames of that size stays in step.
        assert!(decode_test_stream(160, 120, 5).iter().any(|&b| b != 0));
    }
}
