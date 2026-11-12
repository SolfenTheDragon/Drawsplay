//! One client connection: handshake, pairing, then the streaming session, with the limits that keep
//! strangers from tying the host up.

use crate::args::{Args, Source};
use crate::capture::{kill_all, launch, spawn_reader, GDI_ONLY};
use crate::encoder::Encoder;
#[cfg(target_os = "linux")]
use crate::fake_displays;
use crate::{access, display, inject, pairing, ratecontrol, status};
use dd_proto::identity::{authorize_with, TrustStore};
use dd_proto::secure::{handshake, Keypair};
use dd_proto::{read_message_limited, video_size_ok, write_message, Message, MAX_CONTROL_FRAME, PROTOCOL_VERSION};
use std::io::Read;
use std::net::TcpStream;
use std::path::Path;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

/// How long a connected client may stay silent before the session is considered dead. The client
/// pings every 2 s, so a healthy one never gets near this.
pub(crate) fn idle_timeout() -> Duration {
    env_ms("DD_IDLE_TIMEOUT_MS", 10_000)
}

/// Defaults can be shortened through the environment so the integration tests run in seconds.
pub(crate) fn env_ms(name: &str, default_ms: u64) -> Duration {
    let ms = std::env::var(name).ok().and_then(|v| v.parse().ok()).filter(|&v| v > 0).unwrap_or(default_ms);
    Duration::from_millis(ms)
}
/// Time an unauthenticated or just-paired peer gets for each protocol step.
pub(crate) fn step_timeout() -> Duration {
    env_ms("DD_STEP_TIMEOUT_MS", 15_000)
}
/// Time a stranger gets to finish the cryptographic handshake. It is three small messages, so a healthy
/// peer needs a round trip or two; the shorter the limit, the less a flood of silent connections can
/// keep the few handshake slots busy.
pub(crate) fn handshake_timeout() -> Duration {
    step_timeout().min(Duration::from_secs(6))
}
/// How long to wait for the other person to confirm the pairing code on a first connection.
pub(crate) const PAIRING_TIMEOUT: Duration = Duration::from_secs(300);
/// How long the pairing question stays on this terminal before it is withdrawn.
pub(crate) const PROMPT_TIMEOUT: Duration = Duration::from_secs(120);

/// After a pairing is declined, abandoned or timed out, the same address gets no new question for this long.
pub(crate) fn pair_cooldown() -> Duration {
    env_ms("DD_PAIR_COOLDOWN_MS", 30_000)
}

/// How often a running session checks that its client is still on the trusted list, so removing a
/// device (`--forget`, or editing the file) ends its session within seconds instead of at its next connection.
pub(crate) const TRUST_RECHECK: Duration = Duration::from_secs(2);

/// Has `key` been taken off the trusted list? An unreadable list counts as "not revoked": a momentary
/// read error must not drop a working session.
pub(crate) fn trust_revoked(path: &Path, key: &dd_proto::secure::PublicKey) -> bool {
    TrustStore::load(path.to_path_buf()).is_ok_and(|t| !t.contains(key))
}

/// Tell a client that was taken off the trusted list why the session ends (it will not reconnect).
pub(crate) fn revoke(writer: &Mutex<dd_proto::secure::SecureWriter<TcpStream>>) -> std::io::Error {
    let reason = "this computer was removed from the host's trusted list";
    let _ =
        write_message(&mut *writer.lock().unwrap_or_else(|e| e.into_inner()), &Message::Bye { reason: reason.into() });
    std::io::Error::other("the client was removed from the trusted list")
}

/// Most bytes read ahead of the session while the pairing question is open.
pub(crate) const MAX_READ_AHEAD: usize = 64 * 1024;

/// Is the peer still connected? A closed connection reads as end of file, but only after anything the
/// peer sent before closing has been read, so whatever arrives is kept in `ahead` and handed back to the
/// session by [`Replay`].
pub(crate) fn peer_alive(s: &TcpStream, ahead: &mut Vec<u8>) -> bool {
    let _ = s.set_read_timeout(Some(Duration::from_millis(20)));
    let mut tmp = [0u8; 1024];
    let mut stream = s;
    let alive = match stream.read(&mut tmp) {
        Ok(0) => false,
        Ok(n) if ahead.len() + n <= MAX_READ_AHEAD => {
            ahead.extend_from_slice(&tmp[..n]);
            true
        }
        Ok(_) => false, // a peer that talks this much before it is allowed to is not worth keeping
        Err(e) => matches!(e.kind(), std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut),
    };
    let _ = s.set_read_timeout(Some(step_timeout()));
    alive
}

/// The socket, preceded by bytes that were read early (see [`peer_alive`]).
pub(crate) struct Replay {
    head: Vec<u8>,
    pos: usize,
    inner: TcpStream,
}

impl Replay {
    fn set_read_timeout(&self, d: Option<Duration>) -> std::io::Result<()> {
        self.inner.set_read_timeout(d)
    }
}

impl Read for Replay {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        if self.pos < self.head.len() {
            let n = buf.len().min(self.head.len() - self.pos);
            buf[..n].copy_from_slice(&self.head[self.pos..self.pos + n]);
            self.pos += n;
            return Ok(n);
        }
        self.inner.read(buf)
    }
}

/// Connections allowed to sit in handshake/pairing at the same time.
pub(crate) const MAX_PENDING: usize = 8;
/// ...and how many of those one address may hold, so a single peer cannot lock everyone else out.
pub(crate) const MAX_PENDING_PER_IP: usize = 3;

/// State shared by the connection threads.
pub(crate) struct Shared {
    /// Clients being served right now (at most `--max-clients`).
    pub(crate) sessions: AtomicUsize,
    /// Held while a session starts (count + monitor creation) and while the last one cleans up, so a
    /// new session's monitor is never swept away by the one that is leaving.
    pub(crate) lifecycle: Mutex<()>,
    /// Held while the pairing question is on screen, so strangers cannot queue up prompts.
    pub(crate) prompt: Mutex<()>,
    /// Connections still in handshake or pairing; bounds the threads strangers can occupy. A served
    /// session stops counting once it holds a session slot.
    pub(crate) pending: AtomicUsize,
    /// Handshake/pairing connections per peer (see `MAX_PENDING_PER_IP` and `access::peer_group`).
    pub(crate) per_ip: Mutex<std::collections::HashMap<std::net::IpAddr, usize>>,
    /// Peers whose pairing was declined or abandoned, and until when they get no new question.
    pub(crate) pair_blocked: Mutex<pairing::Cooldowns>,
    /// Set once a hardware encoder has failed in a session; later sessions go straight to software.
    pub(crate) software_only: AtomicBool,
}

/// One served client. Dropping it frees the slot; when it was the last session, every fake display
/// still around (whoever made it) is removed, so nothing is left behind on the desktop.
pub(crate) struct SessionSlot<'a>(&'a Shared, bool);
impl Drop for SessionSlot<'_> {
    fn drop(&mut self) {
        self.0.sessions.fetch_sub(1, Ordering::SeqCst);
        let _life = self.0.lifecycle.lock().unwrap_or_else(|e| e.into_inner());
        #[cfg(target_os = "linux")]
        if self.1 {
            display::layout_changed();
        }
        if self.1 && self.0.sessions.load(Ordering::SeqCst) == 0 {
            #[cfg(target_os = "linux")]
            fake_displays::remove_all("last session ended");
        }
    }
}

/// Closes the connection when dropped, including while unwinding from a panic.
pub(crate) struct ShutdownOnDrop(TcpStream);
impl Drop for ShutdownOnDrop {
    fn drop(&mut self) {
        let _ = self.0.shutdown(std::net::Shutdown::Both);
    }
}

/// One connection that has not become a served session yet. Dropping it frees its slots.
pub(crate) struct PendingGuard<'a> {
    pub(crate) shared: &'a Shared,
    pub(crate) ip: std::net::IpAddr,
}
impl Drop for PendingGuard<'_> {
    fn drop(&mut self) {
        self.shared.pending.fetch_sub(1, Ordering::SeqCst);
        release_ip(self.shared, self.ip);
    }
}

pub(crate) fn release_ip(shared: &Shared, ip: std::net::IpAddr) {
    let mut m = shared.per_ip.lock().unwrap_or_else(|e| e.into_inner());
    if let Some(c) = m.get_mut(&ip) {
        *c = c.saturating_sub(1);
        if *c == 0 {
            m.remove(&ip);
        }
    }
}

/// Gives the whole handshake one deadline. A per-read timeout alone lets a peer that sends a byte
/// every few seconds hold a slot for minutes.
pub(crate) struct Deadline<'a> {
    stream: &'a TcpStream,
    until: Instant,
}

impl Deadline<'_> {
    fn left(&self) -> std::io::Result<Duration> {
        let left = self.until.saturating_duration_since(Instant::now());
        if left.is_zero() {
            return Err(std::io::Error::new(std::io::ErrorKind::TimedOut, "the handshake took too long"));
        }
        Ok(left)
    }
}

impl Read for Deadline<'_> {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        self.stream.set_read_timeout(Some(self.left()?))?;
        let mut s = self.stream;
        s.read(buf)
    }
}

impl std::io::Write for Deadline<'_> {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        self.stream.set_write_timeout(Some(self.left()?))?;
        let mut s = self.stream;
        s.write(buf)
    }
    fn flush(&mut self) -> std::io::Result<()> {
        let mut s = self.stream;
        s.flush()
    }
}

pub(crate) fn too_many(max: usize) -> String {
    if max == 1 {
        "another client is already connected".into()
    } else {
        format!("already serving the maximum of {max} clients")
    }
}

pub(crate) fn serve(
    stream: TcpStream,
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
    let session = handshake(&mut Deadline { stream: &stream, until: Instant::now() + handshake_timeout() }, id, false)?;
    stream.set_read_timeout(Some(step_timeout()))?;
    stream.set_write_timeout(Some(step_timeout()))?;
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
        // A panic during an earlier pairing poisons the lock; that question is over, so take it.
        Some(match shared.prompt.try_lock() {
            Ok(g) => g,
            Err(std::sync::TryLockError::Poisoned(p)) => p.into_inner(),
            Err(std::sync::TryLockError::WouldBlock) => {
                return Err(std::io::Error::other("another pairing is already in progress"))
            }
        })
    };
    let peer_ip = stream.peer_addr().ok().map(|p| access::peer_group(p.ip()));
    if prompt_guard.is_some() {
        if let Some(ip) = peer_ip {
            if shared.pair_blocked.lock().unwrap_or_else(|e| e.into_inner()).blocked(ip, Instant::now()) {
                return Err(std::io::Error::other(format!("{ip} was refused a moment ago; not asking again yet")));
            }
        }
    }
    let mut ahead: Vec<u8> = Vec::new();
    // The label is how the trust list names the client, and what the "trusted before with a different
    // key" warning compares: the address only, since the source port changes on every connection.
    let peer_label = stream.peer_addr().map(|p| p.ip().to_canonical().to_string()).unwrap_or_else(|_| peer.clone());
    let notice = if trusted_before && !a.trust_all {
        dd_proto::desktop_ui::pairing_notice(format!(
            "Known client at {peer_label}\nPeer identity: {}\nThis host's identity: {}\n\nPairing code: {}\nCompare on the client if it asks to pair. This window grants no new trust.",
            dd_proto::identity::to_hex(&session.remote_public),
            dd_proto::identity::to_hex(&id.public),
            session.sas,
        ))
    } else {
        None
    };
    let answer =
        authorize_with(&session, &mut trust, &format!("client at {peer_label}"), a.trust_all, &mut |desc, sas| {
            pairing::ask(
                &format!("{desc}\nThis host's identity: {}", dd_proto::identity::to_hex(&id.public)),
                sas,
                PROMPT_TIMEOUT,
                || peer_alive(&stream, &mut ahead),
            )
        });
    if let Some(ip) = peer_ip.filter(|_| prompt_guard.is_some()) {
        let mut blocked = shared.pair_blocked.lock().unwrap_or_else(|e| e.into_inner());
        if matches!(answer, Ok(true)) {
            blocked.succeeded(ip);
        } else {
            blocked.failed(ip, Instant::now(), pair_cooldown());
        }
    }
    if !answer? {
        return Err(std::io::Error::other("pairing declined"));
    }
    // After a first-time pairing the other person may still be reading the code and answering.
    // A client that is itself pairing with this host may take as long as a pairing question.
    let hello_wait =
        if prompt_guard.is_some() || (!a.trust_all && trusted_before) { PAIRING_TIMEOUT } else { step_timeout() };
    drop(prompt_guard);
    let raw = stream.try_clone()?; // for shutdown and timeouts
    let mut reader = session.reader(Replay { head: ahead, pos: 0, inner: stream.try_clone()? });
    let mut stream = session.writer(stream);
    reader.get_ref().set_read_timeout(Some(hello_wait))?;
    let hello = read_message_limited(&mut reader, MAX_CONTROL_FRAME)?;
    drop(notice);
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
    let _slot = SessionSlot(shared, a.source != Source::Test);
    // Keep the display alive for the whole session; dropping it unplugs a virtual monitor.
    let acquired = if a.source == Source::Test {
        Ok(display::synthetic(rw, rh))
    } else {
        display::acquire(a.source == Source::Virtual, a.require_virtual, a.origin, rw, rh, fps)
    };
    drop(life);
    // The connection is a served session now; it no longer takes up a handshake slot.
    drop(pending);
    let display = acquired?;
    #[cfg(target_os = "linux")]
    if a.source != Source::Test {
        display::layout_changed();
    }
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
    // Windows: ddagrab first, gdigrab once ddagrab has been seen to fail (see windows_capture_args).
    let mut gdi = GDI_ONLY.load(Ordering::SeqCst);
    let uses_ddagrab = cfg!(windows) && a.source != Source::Test;
    // Bitrate ladder (see ratecontrol.rs); the small send buffer makes a slow link show up as slow writes.
    let mut rate = ratecontrol::RateController::new(a.bitrate_kbps, Instant::now());
    set_send_buffer(&raw, ratecontrol::send_buffer_bytes(rate.kbps()));
    let (mut ff, mut recorder) = launch(a, &display, enc, fps, gdi, rate.kbps())?;
    let video = ff.stdout.take().unwrap();

    // Writes from two threads (video + pong) must not interleave.
    let writer = Arc::new(Mutex::new(stream));
    let w2 = writer.clone();
    #[allow(clippy::clone_on_copy)] // an Arc on Linux, () elsewhere
    let portal = display.portal.clone();
    #[cfg(target_os = "linux")]
    let output = display.input_output();
    let synthetic = a.source == Source::Test;
    let input = thread::spawn(move || {
        // Whatever ends the input (disconnect, silence, even a panic), stop streaming to this peer too.
        let _stop = ShutdownOnDrop(raw_input);
        let mut inj = (!synthetic).then(|| {
            #[cfg(target_os = "linux")]
            return inject::Injector::new(target, portal, output);
            #[cfg(not(target_os = "linux"))]
            inject::Injector::new(target, portal)
        });
        loop {
            match read_message_limited(&mut reader, MAX_CONTROL_FRAME) {
                Ok(Message::Ping(v)) => {
                    if write_message(&mut *w2.lock().unwrap(), &Message::Pong(v)).is_err() {
                        break;
                    }
                }
                Ok(m @ (Message::Pointer { .. } | Message::Key { .. })) => {
                    if let Some(inj) = inj.as_mut() {
                        inj.handle(&m);
                    }
                }
                Ok(_) => {}
                Err(_) => break,
            }
        }
    });

    let start = Instant::now();
    let client_key = session.remote_public;
    let mut last_trust_check = Instant::now();
    let mut chunks = spawn_reader(video);
    let mut sent_any = false;
    // Set when a hardware encoder died before any video; kept only if software then works, so a
    // capture failure (not the encoder's fault) cannot downgrade every later session.
    let mut downgrade_pending = false;
    // Likewise for ddagrab -> gdigrab: remembered only once gdigrab has produced video.
    let mut gdi_pending = false;
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
                if !a.trust_all && last_trust_check.elapsed() >= TRUST_RECHECK {
                    last_trust_check = Instant::now();
                    if trust_revoked(trust_path, &client_key) {
                        break Err(revoke(&writer));
                    }
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
        if !a.trust_all && last_trust_check.elapsed() >= TRUST_RECHECK {
            last_trust_check = Instant::now();
            if trust_revoked(trust_path, &client_key) {
                break Err(revoke(&writer));
            }
        }
        let data = match chunk {
            Ok(d) if d.is_empty() && !sent_any && uses_ddagrab && !gdi => {
                eprintln!("ddagrab capture failed at start (ffmpeg older than 6.0?); falling back to gdigrab");
                gdi_pending = true;
                gdi = true;
                kill_all(&mut ff, &mut recorder);
                match launch(a, &display, enc, fps, gdi, rate.kbps()) {
                    Ok((f, r)) => {
                        ff = f;
                        recorder = r;
                        chunks = spawn_reader(ff.stdout.take().unwrap());
                        continue;
                    }
                    Err(e) => break Err(e),
                }
            }
            Ok(d) if d.is_empty() && !sent_any && enc != Encoder::Software => {
                eprintln!("encoder {} failed at start; falling back to software encoding", enc.name());
                downgrade_pending = true;
                kill_all(&mut ff, &mut recorder);
                enc = Encoder::Software;
                match launch(a, &display, enc, fps, gdi, rate.kbps()) {
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
        let began = Instant::now();
        if let Err(e) = write_message(&mut *writer.lock().unwrap(), &msg) {
            break Err(e);
        }
        sent_any = true;
        let now = Instant::now();
        if rate.on_write(now.duration_since(began), now) || rate.maybe_step_up(now) {
            eprintln!(
                "link {}: restarting the encoder at {} kbit/s",
                if rate.kbps() < a.bitrate_kbps { "congested or recovering" } else { "clear" },
                rate.kbps()
            );
            kill_all(&mut ff, &mut recorder);
            set_send_buffer(&raw, ratecontrol::send_buffer_bytes(rate.kbps()));
            match launch(a, &display, enc, fps, gdi, rate.kbps()) {
                Ok((f, r)) => {
                    ff = f;
                    recorder = r;
                    chunks = spawn_reader(ff.stdout.take().unwrap());
                    rate.restarted(Instant::now());
                }
                Err(e) => break Err(e),
            }
        }
        if downgrade_pending {
            downgrade_pending = false;
            shared.software_only.store(true, Ordering::SeqCst);
        }
        if gdi_pending {
            gdi_pending = false;
            GDI_ONLY.store(true, Ordering::SeqCst);
        }
    };
    kill_all(&mut ff, &mut recorder);
    let _ = raw.shutdown(std::net::Shutdown::Both);
    let _ = input.join();
    result
}

/// Size the socket's kernel send buffer (best effort).
pub(crate) fn set_send_buffer(s: &TcpStream, bytes: usize) {
    let v = i32::try_from(bytes).unwrap_or(i32::MAX);
    #[cfg(target_os = "linux")]
    {
        use std::os::fd::AsRawFd;
        // SAFETY: setsockopt with a pointer to a live i32 and its size.
        unsafe {
            libc::setsockopt(
                s.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_SNDBUF,
                &v as *const i32 as *const libc::c_void,
                std::mem::size_of::<i32>() as libc::socklen_t,
            );
        }
    }
    #[cfg(windows)]
    {
        use std::os::windows::io::AsRawSocket;
        use windows::Win32::Networking::WinSock::{setsockopt, SOCKET, SOL_SOCKET, SO_SNDBUF};
        // SAFETY: setsockopt on a socket we own, with a 4-byte value.
        unsafe {
            setsockopt(SOCKET(s.as_raw_socket() as usize), SOL_SOCKET, SO_SNDBUF, Some(&v.to_ne_bytes()));
        }
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    let _ = (s, v);
}
