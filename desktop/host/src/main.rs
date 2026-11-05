//! dd-host: captures a screen (or a test pattern), encodes H.264 with ffmpeg and
//! streams it to one client, while injecting the client's mouse input.

#[cfg(target_os = "linux")]
mod active_record;
mod display;
mod encoder;
#[cfg(target_os = "linux")]
mod fake_displays;
mod inject;
#[cfg(target_os = "linux")]
mod linux_display;
#[cfg(target_os = "linux")]
mod portal;
mod status;
#[cfg(windows)]
mod vdisplay;
#[cfg(target_os = "linux")]
mod wayland_display;

use dd_proto::identity::{authorize, config_dir, load_or_create_identity, TrustStore};
use dd_proto::secure::{handshake, Keypair};
use dd_proto::{
    read_message_limited, video_size_ok, write_message, Message, DEFAULT_PORT, MAX_CONTROL_FRAME, PROTOCOL_VERSION,
};
use encoder::Encoder;
use std::io::Read;
use std::net::{TcpListener, TcpStream};
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

struct Args {
    bind: String,
    source: Source,
    bitrate_kbps: u32,
    ffmpeg: String,
    /// Top-left of the captured screen region in desktop coordinates, used to map
    /// the client's normalized pointer to absolute coordinates.
    origin: (i32, i32),
    size: Option<(u32, u32)>,
    /// Accept unknown clients without the pairing-code check. Testing only.
    trust_all: bool,
    /// Fail instead of mirroring when no virtual display can be created.
    require_virtual: bool,
    /// Requested encoder (`None` = automatic) and the one actually chosen at startup.
    encoder: Option<Encoder>,
    chosen: Encoder,
    show_key: bool,
    list_trusted: bool,
    forget: Option<String>,
    remove_fake: bool,
    trust_key: Option<String>,
    /// Clients served at the same time (each gets its own virtual monitor).
    max_clients: usize,
}

#[derive(Clone, Copy, PartialEq)]
enum Source {
    Test,
    Desktop,
    /// Create a virtual monitor (Windows: DrawSplay IDD driver; Linux/X11: a spare RandR output).
    Virtual,
}

fn parse_args() -> Result<Args, String> {
    let mut a = Args {
        bind: format!("0.0.0.0:{DEFAULT_PORT}"),
        source: Source::Virtual,
        bitrate_kbps: 12_000,
        ffmpeg: "ffmpeg".into(),
        origin: (0, 0),
        size: None,
        trust_all: false,
        require_virtual: false,
        encoder: None,
        chosen: Encoder::Software,
        show_key: false,
        list_trusted: false,
        forget: None,
        remove_fake: false,
        trust_key: None,
        max_clients: 1,
    };
    let mut it = std::env::args().skip(1);
    while let Some(k) = it.next() {
        let mut val = |name: &str| it.next().ok_or(format!("{name} needs a value"));
        match k.as_str() {
            "--bind" => a.bind = val("--bind")?,
            "--source" => {
                a.source = match val("--source")?.as_str() {
                    "test" => Source::Test,
                    "desktop" => Source::Desktop,
                    "virtual" => Source::Virtual,
                    o => return Err(format!("unknown source {o} (test|desktop|virtual)")),
                }
            }
            "--bitrate-kbps" => a.bitrate_kbps = val("--bitrate-kbps")?.parse().map_err(|e| format!("{e}"))?,
            "--trust-all" => a.trust_all = true,
            "--require-virtual" => a.require_virtual = true,
            "--show-key" => a.show_key = true,
            "--list-trusted" => a.list_trusted = true,
            "--remove-fake-displays" => a.remove_fake = true,
            "--forget" => a.forget = Some(val("--forget")?),
            "--trust" => a.trust_key = Some(val("--trust")?),
            "--max-clients" => {
                a.max_clients = val("--max-clients")?.parse().map_err(|e| format!("--max-clients: {e}"))?;
                if !(1..=MAX_CLIENTS).contains(&a.max_clients) {
                    return Err(format!("--max-clients must be between 1 and {MAX_CLIENTS}"));
                }
            }
            "--encoder" => {
                let v = val("--encoder")?;
                a.encoder =
                    if v == "auto" { None } else { Some(Encoder::parse(&v).ok_or(format!("unknown encoder {v}"))?) };
            }
            "--ffmpeg" => a.ffmpeg = val("--ffmpeg")?,
            "--origin" => {
                let v = val("--origin")?;
                let (x, y) = v.split_once(',').ok_or("--origin X,Y")?;
                a.origin = (x.parse().map_err(|e| format!("{e}"))?, y.parse().map_err(|e| format!("{e}"))?);
            }
            "--size" => {
                let v = val("--size")?;
                let (w, h) = v.split_once('x').ok_or("--size WxH")?;
                let (w, h) = (w.parse().map_err(|e| format!("{e}"))?, h.parse().map_err(|e| format!("{e}"))?);
                if !video_size_ok(w, h) {
                    return Err(format!("--size {w}x{h} is out of range"));
                }
                a.size = Some((w, h));
            }
            "-h" | "--help" => {
                println!(
                    "dd-host [--bind ADDR:PORT] [--source virtual|desktop|test] [--bitrate-kbps N]\n        [--size WxH] [--origin X,Y] [--ffmpeg PATH] [--trust-all] [--require-virtual]
        [--encoder auto|software|nvenc|qsv|amf|vaapi]
        [--show-key] [--trust CLIENT_KEY]\n        [--list-trusted] [--forget KEY_PREFIX]\n        [--max-clients N] [--remove-fake-displays]\n\nThe stream size defaults to the client's requested size."
                );
                std::process::exit(0);
            }
            o => return Err(format!("unknown argument {o}")),
        }
    }
    Ok(a)
}

/// How the encoder's input arrives when a helper process (not ffmpeg itself) captures the screen.
#[derive(Clone, Copy, PartialEq)]
enum Piped {
    No,
    /// `wf-recorder`'s nut stream.
    Nut,
    /// Raw BGRx frames of the target size (`gst-launch-1.0`, portal capture).
    Raw,
}

fn ffmpeg_command(a: &Args, enc: Encoder, origin: (i32, i32), w: u32, h: u32, fps: u32, piped: Piped) -> Command {
    let piped_nut = piped == Piped::Nut;
    let mut c = Command::new(&a.ffmpeg);
    c.args(["-hide_banner", "-loglevel", "error", "-fflags", "nobuffer"]);
    c.args(enc.input_args());
    match a.source {
        Source::Test => {
            // -re paces the synthetic source at real time, like a real capture.
            c.args(["-re", "-f", "lavfi", "-i", &format!("testsrc2=size={w}x{h}:rate={fps}")]);
        }
        Source::Desktop | Source::Virtual if piped == Piped::Raw => {
            c.args(["-f", "rawvideo", "-pixel_format", "bgr0", "-video_size", &format!("{w}x{h}")]);
            c.args(["-framerate", &fps.to_string(), "-i", "pipe:0"]);
        }
        Source::Desktop | Source::Virtual if piped_nut => {
            // Wayland: wf-recorder feeds raw frames on stdin.
            c.args(["-use_wallclock_as_timestamps", "1", "-f", "nut", "-i", "pipe:0"]);
        }
        Source::Desktop | Source::Virtual => {
            #[cfg(windows)]
            {
                c.args(["-f", "gdigrab", "-framerate", &fps.to_string()]);
                c.args(["-offset_x", &origin.0.to_string(), "-offset_y", &origin.1.to_string()]);
                c.args(["-video_size", &format!("{w}x{h}"), "-draw_mouse", "0", "-i", "desktop"]);
            }
            #[cfg(not(windows))]
            {
                let display = std::env::var("DISPLAY").unwrap_or_else(|_| ":0".into());
                c.args(["-f", "x11grab", "-framerate", &fps.to_string(), "-video_size", &format!("{w}x{h}")]);
                c.args(["-draw_mouse", "0", "-i", &format!("{display}+{},{}", origin.0, origin.1)]);
            }
        }
    }
    c.arg("-an");
    // wf-recorder's timestamps mean nothing to the encoder: stamp by arrival, then hold a steady rate.
    let rate = if piped_nut { format!("fps={fps}") } else { String::new() };
    match (enc.filter(), piped_nut) {
        (Some(f), true) => {
            c.args(["-vf", &format!("{rate},{f}")]);
        }
        (Some(f), false) => {
            c.args(["-vf", f]);
        }
        (None, true) => {
            c.args(["-vf", &rate]);
        }
        (None, false) => {}
    }
    if let Some(p) = enc.pix_fmt() {
        c.args(["-pix_fmt", p]);
    }
    c.args(enc.codec_args(a.bitrate_kbps, fps, fps * 2));
    c.args(["-f", "h264", "-"]);
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    c.stdout(Stdio::piped())
        .stdin(if piped != Piped::No { Stdio::piped() } else { Stdio::null() })
        .stderr(Stdio::inherit());
    c
}

/// How long a connected client may stay silent before the session is considered dead. The client
/// pings every 2 s, so a healthy one never gets near this.
fn idle_timeout() -> Duration {
    env_ms("DD_IDLE_TIMEOUT_MS", 10_000)
}

/// Defaults can be shortened through the environment so the integration tests run in seconds.
fn env_ms(name: &str, default_ms: u64) -> Duration {
    let ms = std::env::var(name).ok().and_then(|v| v.parse().ok()).filter(|&v| v > 0).unwrap_or(default_ms);
    Duration::from_millis(ms)
}
/// Time an unauthenticated or just-paired peer gets for each protocol step.
fn step_timeout() -> Duration {
    env_ms("DD_STEP_TIMEOUT_MS", 15_000)
}
/// How long to wait for the other person to confirm the pairing code on a first connection.
const PAIRING_TIMEOUT: Duration = Duration::from_secs(300);
/// Connections allowed to sit in handshake/pairing at the same time.
const MAX_PENDING: usize = 8;
/// Upper bound for `--max-clients` (the Windows virtual display driver has eight monitor slots).
const MAX_CLIENTS: usize = 8;

/// State shared by the connection threads.
struct Shared {
    /// Clients being served right now (at most `--max-clients`).
    sessions: AtomicUsize,
    /// Held while a session starts (count + monitor creation) and while the last one cleans up, so a
    /// new session's monitor is never swept away by the one that is leaving.
    lifecycle: Mutex<()>,
    /// Held while the pairing question is on screen, so strangers cannot queue up prompts.
    prompt: Mutex<()>,
    /// Connections still in handshake or pairing; bounds the threads strangers can occupy. A served
    /// session stops counting once it holds a session slot.
    pending: AtomicUsize,
    /// Set once a hardware encoder has failed in a session; later sessions go straight to software.
    software_only: AtomicBool,
}

/// One served client. Dropping it frees the slot; when it was the last session, every fake display
/// still around (whoever made it) is removed, so nothing is left behind on the desktop.
struct SessionSlot<'a>(&'a Shared);
impl Drop for SessionSlot<'_> {
    fn drop(&mut self) {
        self.0.sessions.fetch_sub(1, Ordering::SeqCst);
        let _life = self.0.lifecycle.lock().unwrap_or_else(|e| e.into_inner());
        if self.0.sessions.load(Ordering::SeqCst) == 0 {
            #[cfg(target_os = "linux")]
            fake_displays::remove_all("last session ended");
        }
    }
}

/// Closes the connection when dropped, including while unwinding from a panic.
struct ShutdownOnDrop(TcpStream);
impl Drop for ShutdownOnDrop {
    fn drop(&mut self) {
        let _ = self.0.shutdown(std::net::Shutdown::Both);
    }
}

struct PendingGuard<'a>(&'a AtomicUsize);
impl Drop for PendingGuard<'_> {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}

/// Start the encoder (and, on Wayland, the `wf-recorder` that feeds it).
fn launch(
    a: &Args,
    display: &display::Display,
    enc: Encoder,
    fps: u32,
) -> std::io::Result<(std::process::Child, Option<std::process::Child>)> {
    let (w, h) = display.target.size;
    let mut recorder: Option<std::process::Child> = None;
    #[cfg(target_os = "linux")]
    if a.source != Source::Test {
        if let Some(out) = &display.wayland_output {
            recorder = Some(wayland_display::spawn_capture(out, fps).map_err(std::io::Error::other)?);
        }
    }
    #[cfg(target_os = "linux")]
    if a.source != Source::Test && recorder.is_none() {
        if let Some(p) = &display.portal {
            recorder = Some(p.spawn_capture(w, h, fps).map_err(std::io::Error::other)?);
        }
    }
    let piped = match (recorder.is_some(), display.portal.is_some()) {
        (false, _) => Piped::No,
        (true, true) => Piped::Raw,
        (true, false) => Piped::Nut,
    };
    let mut cmd = ffmpeg_command(a, enc, display.target.origin, w, h, fps, piped);
    if let Some(r) = recorder.as_mut() {
        cmd.stdin(Stdio::from(r.stdout.take().unwrap()));
    }
    match cmd.spawn() {
        Ok(ff) => {
            track(&ff);
            if let Some(r) = recorder.as_ref() {
                track(r);
            }
            Ok((ff, recorder))
        }
        Err(e) => {
            if let Some(r) = recorder.as_mut() {
                let _ = r.kill();
                let _ = r.wait();
            }
            Err(std::io::Error::other(format!("cannot start ffmpeg ({}): {e}", a.ffmpeg)))
        }
    }
}

/// Read the encoder's output on its own thread so the session loop can notice a vanished client even
/// when the encoder is silent. An empty chunk means end of output.
fn spawn_reader(mut video: std::process::ChildStdout) -> mpsc::Receiver<std::io::Result<Vec<u8>>> {
    let (tx, rx) = mpsc::sync_channel(4);
    thread::spawn(move || {
        let mut buf = vec![0u8; 64 * 1024];
        loop {
            match video.read(&mut buf) {
                Ok(0) => {
                    let _ = tx.send(Ok(Vec::new()));
                    break;
                }
                Ok(n) => {
                    if tx.send(Ok(buf[..n].to_vec())).is_err() {
                        break;
                    }
                }
                Err(e) => {
                    let _ = tx.send(Err(e));
                    break;
                }
            }
        }
    });
    rx
}

/// Pids of the encoder and capture helpers, so a signal handler can stop them before it removes the
/// monitor they are reading (removing a monitor under a running capture can crash the compositor).
static CHILDREN: Mutex<Vec<u32>> = Mutex::new(Vec::new());

fn track(c: &std::process::Child) {
    CHILDREN.lock().unwrap_or_else(|e| e.into_inner()).push(c.id());
}

fn untrack(c: &std::process::Child) {
    CHILDREN.lock().unwrap_or_else(|e| e.into_inner()).retain(|&p| p != c.id());
}

/// Called from the termination handler: stop every helper still running.
#[cfg(target_os = "linux")]
fn kill_tracked_children() {
    let pids = std::mem::take(&mut *CHILDREN.lock().unwrap_or_else(|e| e.into_inner()));
    for p in &pids {
        // SAFETY: plain signal to a child we started.
        unsafe { libc::kill(*p as i32, libc::SIGKILL) };
    }
    if !pids.is_empty() {
        thread::sleep(Duration::from_millis(300)); // let the compositor see the capture go away
    }
}

fn too_many(max: usize) -> String {
    if max == 1 {
        "another client is already connected".into()
    } else {
        format!("already serving the maximum of {max} clients")
    }
}

fn kill_all(ff: &mut std::process::Child, recorder: &mut Option<std::process::Child>) {
    untrack(ff);
    if let Some(r) = recorder.as_ref() {
        untrack(r);
    }
    let _ = ff.kill();
    let _ = ff.wait();
    if let Some(r) = recorder.as_mut() {
        let _ = r.kill();
        let _ = r.wait();
    }
}

fn serve(
    mut stream: TcpStream,
    a: &Args,
    id: &Keypair,
    trust_path: &Path,
    shared: &Shared,
    pending: PendingGuard<'_>,
) -> std::io::Result<()> {
    stream.set_nodelay(true)?;
    // Never block forever on a peer that stops reading or never speaks.
    stream.set_read_timeout(Some(step_timeout()))?;
    stream.set_write_timeout(Some(step_timeout()))?;
    let session = handshake(&mut stream, id, false)?;
    if shared.sessions.load(Ordering::SeqCst) >= a.max_clients {
        return Err(std::io::Error::other(too_many(a.max_clients)));
    }
    let peer = stream.peer_addr().map(|p| p.to_string()).unwrap_or_default();
    // The trust list is re-read per connection; only one pairing question exists at a time.
    let mut trust = TrustStore::load(trust_path.to_path_buf())?;
    let trusted_before = trust.contains(&session.remote_public);
    let prompt_guard = if trusted_before || a.trust_all {
        None
    } else {
        Some(shared.prompt.try_lock().map_err(|_| std::io::Error::other("another pairing is already in progress"))?)
    };
    if !authorize(&session, &mut trust, &format!("client at {peer}"), a.trust_all)? {
        return Err(std::io::Error::other("pairing declined"));
    }
    // After a first-time pairing the other person may still be reading the code and answering.
    // A client that is itself pairing with this host may take as long as a pairing question.
    let hello_wait =
        if prompt_guard.is_some() || (!a.trust_all && trusted_before) { PAIRING_TIMEOUT } else { step_timeout() };
    drop(prompt_guard);
    let raw = stream.try_clone()?; // for shutdown and timeouts
    let mut reader = session.reader(stream.try_clone()?);
    let mut stream = session.writer(stream);
    reader.get_ref().set_read_timeout(Some(hello_wait))?;
    let hello = read_message_limited(&mut reader, MAX_CONTROL_FRAME)?;
    let Message::Hello { version, width, height, refresh_hz, name } = hello else {
        return Err(std::io::Error::other("first message was not Hello"));
    };
    if version != PROTOCOL_VERSION {
        return Err(std::io::Error::other(format!("protocol version {version} unsupported")));
    }
    if !video_size_ok(width, height) {
        return Err(std::io::Error::other(format!("client asked for an unsupported size {width}x{height}")));
    }
    if let Some((sw, sh)) = a.size {
        if !video_size_ok(sw, sh) {
            return Err(std::io::Error::other(format!("--size {sw}x{sh} is out of range")));
        }
    }
    let (rw, rh) = a.size.unwrap_or((width, height));
    let fps = refresh_hz.clamp(1, 120);

    // Take a slot and create the monitor under the lifecycle lock, so sessions that start or end at
    // the same moment cannot remove each other's monitors. The slot is declared before the display so
    // it is released after it: the last session to leave then clears every fake display.
    let life = shared.lifecycle.lock().unwrap_or_else(|e| e.into_inner());
    if shared.sessions.load(Ordering::SeqCst) >= a.max_clients {
        return Err(std::io::Error::other(too_many(a.max_clients)));
    }
    shared.sessions.fetch_add(1, Ordering::SeqCst);
    let _slot = SessionSlot(shared);
    // Keep the display alive for the whole session; dropping it unplugs a virtual monitor.
    let acquired = display::acquire(a.source == Source::Virtual, a.require_virtual, a.origin, rw, rh, fps);
    drop(life);
    // The connection is a served session now; it no longer takes up a handshake slot.
    drop(pending);
    let display = acquired?;
    let target = display.target;
    let (w, h) = target.size;
    let origin = target.origin;
    eprintln!("client \"{name}\" {width}x{height}@{refresh_hz}: streaming {w}x{h}@{fps} at {origin:?}");

    write_message(&mut stream, &Message::Config { width: w, height: h, fps })?;
    // Publish the session for the web UI; the guard clears it however this function ends.
    let status = status::session_started(
        status::SessionInfo {
            client: name.clone(),
            peer: peer.clone(),
            width: w,
            height: h,
            fps,
            virtual_display: display.is_virtual(),
            backend: if a.source == Source::Test { "test".into() } else { display.backend().into() },
            encoder: a.chosen.name().into(),
        },
        raw.try_clone().ok(),
    );
    let status_id = status.id;
    // From here on the client talks every couple of seconds; silence means it is gone.
    reader.get_ref().set_read_timeout(Some(idle_timeout()))?;

    // A hardware encoder that passed the start-up trial can still fail later (session limits, GPU
    // reset). If that happens before any video, retry once in software and remember it.
    let raw_input = raw.try_clone()?;
    let mut enc = if shared.software_only.load(Ordering::SeqCst) { Encoder::Software } else { a.chosen };
    let (mut ff, mut recorder) = launch(a, &display, enc, fps)?;
    let video = ff.stdout.take().unwrap();

    // Writes from two threads (video + pong) must not interleave.
    let writer = Arc::new(Mutex::new(stream));
    let w2 = writer.clone();
    let portal = display.portal.clone();
    let input = thread::spawn(move || {
        // Whatever ends the input (disconnect, silence, even a panic), stop streaming to this peer too.
        let _stop = ShutdownOnDrop(raw_input);
        let mut inj = inject::Injector::new(target, portal);
        loop {
            match read_message_limited(&mut reader, MAX_CONTROL_FRAME) {
                Ok(Message::Ping(v)) => {
                    if write_message(&mut *w2.lock().unwrap(), &Message::Pong(v)).is_err() {
                        break;
                    }
                }
                Ok(m @ (Message::Pointer { .. } | Message::Key { .. })) => inj.handle(&m),
                Ok(_) => {}
                Err(_) => break,
            }
        }
    });

    let start = Instant::now();
    let mut chunks = spawn_reader(video);
    let mut sent_any = false;
    // Set when a hardware encoder died before any video; kept only if software then works, so a
    // capture failure (not the encoder's fault) cannot downgrade every later session.
    let mut downgrade_pending = false;
    let result = loop {
        // Wake up regularly: when the client is gone (its input thread ended) the session must end
        // and give the monitor back even if the encoder has nothing more to say.
        let chunk = match chunks.recv_timeout(Duration::from_millis(250)) {
            Ok(c) => c,
            Err(mpsc::RecvTimeoutError::Timeout) => {
                if status::disconnect_requested(status_id) {
                    let _ = write_message(
                        &mut *writer.lock().unwrap(),
                        &Message::Bye { reason: "ended from the web UI".into() },
                    );
                    break Err(std::io::Error::other("disconnected from the web UI"));
                }
                if input.is_finished() {
                    break Err(std::io::Error::other("client disconnected"));
                }
                continue;
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => Ok(Vec::new()),
        };
        if status::disconnect_requested(status_id) {
            let _ =
                write_message(&mut *writer.lock().unwrap(), &Message::Bye { reason: "ended from the web UI".into() });
            break Err(std::io::Error::other("disconnected from the web UI"));
        }
        let data = match chunk {
            Ok(d) if d.is_empty() && !sent_any && enc != Encoder::Software => {
                eprintln!("encoder {} failed at start; falling back to software encoding", enc.name());
                downgrade_pending = true;
                kill_all(&mut ff, &mut recorder);
                enc = Encoder::Software;
                match launch(a, &display, enc, fps) {
                    Ok((f, r)) => {
                        ff = f;
                        recorder = r;
                        chunks = spawn_reader(ff.stdout.take().unwrap());
                        continue;
                    }
                    Err(e) => break Err(e),
                }
            }
            Ok(d) if d.is_empty() => break Err(std::io::Error::other("ffmpeg exited")),
            Ok(d) => d,
            Err(e) => break Err(e),
        };
        let msg = Message::Video { pts_us: start.elapsed().as_micros() as u64, key: false, data };
        if let Err(e) = write_message(&mut *writer.lock().unwrap(), &msg) {
            break Err(e);
        }
        sent_any = true;
        if downgrade_pending {
            downgrade_pending = false;
            shared.software_only.store(true, Ordering::SeqCst);
        }
    };
    kill_all(&mut ff, &mut recorder);
    let _ = raw.shutdown(std::net::Shutdown::Both);
    let _ = input.join();
    result
}

fn main() {
    #[cfg(windows)]
    vdisplay::set_dpi_aware();
    let mut args = match parse_args() {
        Ok(a) => a,
        Err(e) => {
            eprintln!("error: {e}");
            std::process::exit(2);
        }
    };
    if args.remove_fake {
        #[cfg(target_os = "linux")]
        {
            let r = fake_displays::remove_all("requested");
            if r.is_empty() {
                println!("no fake displays found");
            }
            for n in &r.removed {
                println!("removed {n}");
            }
            for n in &r.failed {
                println!("could not remove {n}");
            }
            std::process::exit(if r.failed.is_empty() { 0 } else { 1 });
        }
        #[cfg(not(target_os = "linux"))]
        {
            eprintln!("--remove-fake-displays is only available on Linux");
            std::process::exit(2);
        }
    }
    match dd_proto::identity::key_commands(
        "host",
        "trusted_clients.txt",
        args.show_key,
        args.trust_key.as_deref(),
        args.list_trusted,
        args.forget.as_deref(),
    ) {
        Ok(true) => return,
        Ok(false) => {}
        Err(e) => {
            eprintln!("error: {e}");
            std::process::exit(1);
        }
    }
    // Dropping the session does not run when the process is signalled; switch the extra X output
    // off explicitly so Ctrl+C or `systemctl stop` does not leave it enabled.
    #[cfg(target_os = "linux")]
    if let Err(e) = ctrlc::set_handler(|| {
        kill_tracked_children();
        linux_display::cleanup_active();
        wayland_display::cleanup_active();
        fake_displays::remove_all("shutdown");
        status::shutdown();
        std::process::exit(130);
    }) {
        eprintln!("warning: cannot install the Ctrl+C handler: {e}");
    }
    #[cfg(windows)]
    if let Some(w) = vdisplay::scaling_warning() {
        eprintln!("{w}");
    }
    args.chosen = encoder::choose(&args.ffmpeg, args.encoder);
    eprintln!("encoder: {}", args.chosen.name());
    let listener = match TcpListener::bind(&args.bind) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("cannot listen on {}: {e}", args.bind);
            std::process::exit(1);
        }
    };
    let dir = config_dir("host").unwrap_or_else(|e| {
        eprintln!("cannot open config directory: {e}");
        std::process::exit(1);
    });
    let id = load_or_create_identity(&dir).unwrap_or_else(|e| {
        eprintln!("cannot load identity: {e}");
        std::process::exit(1);
    });
    // Fail early if the trust list cannot be read; connections re-read it themselves.
    let trust_path = dir.join("trusted_clients.txt");
    if let Err(e) = TrustStore::load(trust_path.clone()) {
        eprintln!("cannot read trusted clients: {e}");
        std::process::exit(1);
    }
    // A monitor left over from a host that was killed or crashed would otherwise stay forever.
    #[cfg(target_os = "linux")]
    {
        active_record::init(&dir);
        for msg in active_record::sweep_stale() {
            eprintln!("{msg}");
        }
        fake_displays::remove_all("startup");
    }
    eprintln!("dd-host listening on {} (config: {})", args.bind, dir.display());
    status::init(
        &dir,
        &args.bind,
        args.chosen.name(),
        match args.source {
            Source::Test => "test",
            Source::Desktop => "desktop",
            Source::Virtual => "virtual",
        },
    );
    let args = Arc::new(args);
    let id = Arc::new(id);
    let shared = Arc::new(Shared {
        sessions: AtomicUsize::new(0),
        lifecycle: Mutex::new(()),
        prompt: Mutex::new(()),
        pending: AtomicUsize::new(0),
        software_only: AtomicBool::new(false),
    });
    // Each connection runs on its own thread, so a silent or slow peer cannot block the others.
    // At most `--max-clients` of them become active sessions.
    for conn in listener.incoming() {
        match conn {
            Ok(s) => {
                let peer = s.peer_addr().map(|p| p.to_string()).unwrap_or_default();
                if shared.pending.fetch_add(1, Ordering::SeqCst) >= MAX_PENDING {
                    shared.pending.fetch_sub(1, Ordering::SeqCst);
                    eprintln!("connection from {peer} refused: too many unauthenticated connections");
                    continue;
                }
                eprintln!("connection from {peer}");
                let (args, id, shared, trust_path) = (args.clone(), id.clone(), shared.clone(), trust_path.clone());
                thread::spawn(move || {
                    let pending = PendingGuard(&shared.pending);
                    if let Err(e) = serve(s, &args, &id, &trust_path, &shared, pending) {
                        eprintln!("session with {peer} ended: {e}");
                    }
                });
            }
            Err(e) => eprintln!("accept failed: {e}"),
        }
    }
}
