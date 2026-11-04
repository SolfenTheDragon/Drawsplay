//! Everything that talks to the host, off the UI thread: connect, pair, receive video,
//! feed the decoder, and send queued input.

use dd_proto::identity::{authorize, config_dir, load_or_create_identity, TrustStore};
use dd_proto::secure::handshake;
use dd_proto::{read_message, write_message, Message, PointerPhase, PROTOCOL_VERSION};
use std::io::{Read, Write};
use std::net::TcpStream;
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
    /// The host accepted us; the video will be this size.
    Connected { width: u32, height: u32, fps: u32 },
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
}

/// Wall-clock microseconds, used as the opaque value in Ping so the echo yields an RTT.
fn now_us(epoch: Instant) -> u64 {
    epoch.elapsed().as_micros() as u64
}

pub fn spawn_decoder(ffmpeg: &str, hwdec: bool) -> std::io::Result<Child> {
    let mut c = Command::new(ffmpeg);
    c.args(["-hide_banner", "-loglevel", "error"]);
    if hwdec {
        c.args(["-hwaccel", "auto"]);
    }
    c.args(["-fflags", "nobuffer", "-flags", "low_delay", "-probesize", "32", "-analyzeduration", "0"])
        .args(["-f", "h264", "-i", "-", "-f", "rawvideo", "-pix_fmt", "bgra", "-"])
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
    // Set when the host ends the session on purpose: reconnecting would undo that.
    let bye: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
    let mut lost_at: Option<Instant> = None;
    let mut backoff = Duration::from_millis(500);
    let reason = loop {
        connected.store(false, Ordering::SeqCst);
        let r = session(&s, &input, &bye, slot.clone(), ui_inner.clone());
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
        eprintln!("connection lost ({why}); reconnecting in {:.1} s", backoff.as_secs_f32());
        let until = Instant::now() + backoff;
        while Instant::now() < until {
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
    let mut stream = TcpStream::connect(&s.host).map_err(e("cannot connect"))?;
    stream.set_nodelay(true).map_err(e("socket"))?;

    let dir = config_dir("client").map_err(e("config directory"))?;
    let id = load_or_create_identity(&dir).map_err(e("identity"))?;
    let mut trust = TrustStore::load(dir.join("trusted_hosts.txt")).map_err(e("trusted hosts"))?;
    stream.set_read_timeout(Some(Duration::from_secs(15))).map_err(e("socket"))?;
    stream.set_write_timeout(Some(IDLE_TIMEOUT)).map_err(e("socket"))?;
    let sess = handshake(&mut stream, &id, true).map_err(e("secure handshake failed"))?;
    // The pairing question waits for a person; the host may also take a while to create its display.
    stream.set_read_timeout(None).map_err(e("socket"))?;
    let first_time = !trust.contains(&sess.remote_public) && !s.trust_all;
    if !authorize(&sess, &mut trust, &format!("host at {}", s.host), s.trust_all).map_err(e("pairing"))? {
        return Err("pairing declined".into());
    }
    let raw = stream.try_clone().map_err(e("socket"))?;
    let mut reader = sess.reader(stream.try_clone().map_err(e("socket"))?);
    let writer = Arc::new(Mutex::new(sess.writer(stream)));

    let name = std::env::var("COMPUTERNAME").or_else(|_| std::env::var("HOSTNAME")).unwrap_or_else(|_| "client".into());
    write_message(
        &mut *writer.lock().unwrap(),
        &Message::Hello { version: PROTOCOL_VERSION, width: s.width, height: s.height, refresh_hz: s.fps, name },
    )
    .map_err(e("hello"))?;
    // On a first connection the host's user may still be comparing the pairing code.
    // The host's user may be answering a pairing question about this computer, whether or not we
    // trust the host already (for example after the host forgot us).
    let config_wait = Duration::from_secs(300);
    if !first_time && !s.trust_all {
        eprintln!("Connected. If the host asks to pair this computer, its code should read {}", sess.sas);
    }
    reader.get_ref().set_read_timeout(Some(config_wait)).map_err(e("socket"))?;
    let (w, h, fps) = match read_message(&mut reader).map_err(e("waiting for host"))? {
        Message::Config { width, height, fps } if width > 0 && height > 0 && width <= 16384 && height <= 16384 => {
            (width as usize, height as usize, fps)
        }
        _ => return Err("host sent an invalid Config".into()),
    };
    // The host streams continuously, so a quiet connection means it is gone (cable pulled, Wi-Fi lost).
    reader.get_ref().set_read_timeout(Some(IDLE_TIMEOUT)).map_err(e("socket"))?;
    ui(UiEvent::Connected { width: w as u32, height: h as u32, fps });

    let mut dec = spawn_decoder(&s.ffmpeg, s.hwdec).map_err(|x| format!("cannot start ffmpeg ({}): {x}", s.ffmpeg))?;
    let mut dec_in = dec.stdin.take().unwrap();
    let mut dec_out = dec.stdout.take().unwrap();
    let epoch = Instant::now();

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
                *bye_net.lock().unwrap() = Some(reason);
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
    let result = loop {
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
    let _ = raw.shutdown(std::net::Shutdown::Both);
    let _ = dec.kill();
    let _ = dec.wait();
    let _ = net.join();
    let _ = dec_thread.join();
    if let Some(reason) = bye.lock().unwrap().clone() {
        return Err(format!("the host ended the session ({reason})"));
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
}
