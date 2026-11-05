//! Loopback integration tests: a real `dd-host --source test` process against a scripted client
//! speaking the real protocol. Timeouts are shortened through DD_*_TIMEOUT_MS so the suite takes
//! seconds. Tests that need video skip themselves when `ffmpeg` is not installed.

use dd_proto::secure::{generate_keypair, handshake, SecureReader, SecureWriter};
use dd_proto::{read_message, write_message, Message, PROTOCOL_VERSION};
use std::io::Write;
use std::net::{TcpListener, TcpStream};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

fn have_ffmpeg() -> bool {
    Command::new("ffmpeg")
        .arg("-version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|s| s.success())
}

/// Port choice and host start-up are serialised: a probed port is free only until someone else probes it.
static START: std::sync::Mutex<()> = std::sync::Mutex::new(());

struct Host {
    child: Child,
    port: u16,
    _config: std::path::PathBuf,
}

impl Host {
    /// `trust_all`: skip the pairing question. Otherwise the question is answered from `stdin_text`.
    fn start(trust_all: bool, stdin_text: &str, extra_env: &[(&str, &str)]) -> Host {
        Host::start_with(trust_all, stdin_text, extra_env, &[])
    }

    fn start_with(trust_all: bool, stdin_text: &str, extra_env: &[(&str, &str)], extra_args: &[&str]) -> Host {
        let _start = START.lock().unwrap_or_else(|e| e.into_inner());
        let port = TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().port();
        let config = std::env::temp_dir().join(format!("dd-loopback-{}-{port}", std::process::id()));
        let mut cmd = Command::new(env!("CARGO_BIN_EXE_dd-host"));
        cmd.args(["--bind", &format!("127.0.0.1:{port}"), "--source", "test", "--encoder", "software"]);
        cmd.args(extra_args);
        if trust_all {
            cmd.arg("--trust-all");
        }
        cmd.env("XDG_CONFIG_HOME", &config).env("APPDATA", &config);
        for (k, v) in extra_env {
            cmd.env(k, v);
        }
        cmd.stdin(Stdio::piped()).stdout(Stdio::null()).stderr(Stdio::null());
        let mut child = cmd.spawn().expect("start dd-host");
        child.stdin.as_mut().unwrap().write_all(stdin_text.as_bytes()).unwrap();
        // Wait until it accepts connections (a bare connect that closes is harmless to the host).
        let deadline = Instant::now() + Duration::from_secs(10);
        while TcpStream::connect(("127.0.0.1", port)).is_err() {
            assert!(Instant::now() < deadline, "dd-host never started listening");
            std::thread::sleep(Duration::from_millis(50));
        }
        Host { child, port, _config: config }
    }
}

impl Drop for Host {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = std::fs::remove_dir_all(&self._config);
    }
}

type Reader = SecureReader<TcpStream>;
type Writer = SecureWriter<TcpStream>;

/// Connect, handshake and send `Hello`; returns the encrypted halves.
fn client(port: u16, version: u16, w: u32, h: u32) -> (Reader, Writer) {
    let mut s = TcpStream::connect(("127.0.0.1", port)).unwrap();
    s.set_read_timeout(Some(Duration::from_secs(20))).unwrap();
    let id = generate_keypair().unwrap();
    let sess = handshake(&mut s, &id, true).expect("handshake");
    let reader = sess.reader(s.try_clone().unwrap());
    let mut writer = sess.writer(s);
    write_message(&mut writer, &Message::Hello { version, width: w, height: h, refresh_hz: 30, name: "test".into() })
        .unwrap();
    (reader, writer)
}

fn expect_config(r: &mut Reader, w: u32, h: u32) {
    match read_message(r).expect("Config") {
        Message::Config { width, height, .. } => assert_eq!((width, height), (w, h)),
        other => panic!("expected Config, got {other:?}"),
    }
}

#[test]
fn streams_video_after_hello() {
    if !have_ffmpeg() {
        return;
    }
    let host = Host::start(true, "", &[]);
    let (mut r, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut r, 320, 240);
    let mut bytes = 0;
    while bytes < 1000 {
        if let Message::Video { data, .. } = read_message(&mut r).expect("video") {
            bytes += data.len();
        }
    }
}

#[test]
fn max_clients_two_serves_two_and_refuses_a_third() {
    if !have_ffmpeg() {
        return;
    }
    let host = Host::start_with(true, "", &[], &["--max-clients", "2"]);
    let (mut a, _wa) = client(host.port, PROTOCOL_VERSION, 320, 240);
    let (mut b, _wb) = client(host.port, PROTOCOL_VERSION, 640, 360);
    expect_config(&mut a, 320, 240);
    expect_config(&mut b, 640, 360);
    // Both receive video at the same time.
    for r in [&mut a, &mut b] {
        let mut bytes = 0;
        while bytes < 1000 {
            if let Message::Video { data, .. } = read_message(r).expect("video") {
                bytes += data.len();
            }
        }
    }
    let (mut third, _wc) = client(host.port, PROTOCOL_VERSION, 320, 240);
    assert!(read_message(&mut third).is_err(), "a third client must be refused");
    // Dropping one frees a slot for a new client while the other keeps streaming.
    drop((a, _wa));
    std::thread::sleep(Duration::from_millis(1500));
    let (mut c, _wc2) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut c, 320, 240);
    assert!(matches!(read_message(&mut b), Ok(Message::Video { .. } | Message::Ping(_))));
}

#[test]
fn max_clients_is_validated() {
    for bad in ["0", "9", "x"] {
        let out = Command::new(env!("CARGO_BIN_EXE_dd-host"))
            .args(["--max-clients", bad])
            .stdin(Stdio::null())
            .output()
            .unwrap();
        assert_eq!(out.status.code(), Some(2), "--max-clients {bad} must be rejected");
    }
}

/// Without the pairing question anyone who can reach the port controls the computer, so --trust-all
/// is refused unless the host listens on loopback only.
#[test]
fn trust_all_is_refused_on_a_public_bind() {
    for bind in ["0.0.0.0:0", "192.168.1.5:47800", "somehost:47800"] {
        let out = Command::new(env!("CARGO_BIN_EXE_dd-host"))
            .args(["--trust-all", "--bind", bind])
            .stdin(Stdio::null())
            .output()
            .unwrap();
        assert_eq!(out.status.code(), Some(2), "--trust-all with --bind {bind} must be refused");
        assert!(String::from_utf8_lossy(&out.stderr).contains("loopback"));
    }
}

#[test]
fn allow_options_are_validated() {
    for bad in [&["--allow", "nonsense"][..], &["--allow", "10.0.0.0/40"], &["--allow"]] {
        let out = Command::new(env!("CARGO_BIN_EXE_dd-host")).args(bad).stdin(Stdio::null()).output().unwrap();
        assert_eq!(out.status.code(), Some(2), "{bad:?} must be rejected");
    }
}

#[test]
fn second_client_is_refused_while_one_is_active() {
    if !have_ffmpeg() {
        return;
    }
    let host = Host::start(true, "", &[]);
    let (mut first, _w1) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut first, 320, 240);
    let (mut second, _w2) = client(host.port, PROTOCOL_VERSION, 320, 240);
    assert!(read_message(&mut second).is_err(), "second client must not get a Config");
    // The first one is unaffected.
    assert!(read_message(&mut first).is_ok());
}

#[test]
fn bad_hellos_are_rejected() {
    let host = Host::start(true, "", &[]);
    for (version, w, h) in
        [(PROTOCOL_VERSION, 20000, 240), (PROTOCOL_VERSION, 1, 240), (PROTOCOL_VERSION + 1, 320, 240)]
    {
        let (mut r, _w) = client(host.port, version, w, h);
        assert!(read_message(&mut r).is_err(), "Hello {version} {w}x{h} must be rejected");
    }
}

#[test]
fn silent_stranger_neither_blocks_others_nor_lives_forever() {
    if !have_ffmpeg() {
        return;
    }
    let host = Host::start(true, "", &[("DD_STEP_TIMEOUT_MS", "800")]);
    let mut stranger = TcpStream::connect(("127.0.0.1", host.port)).unwrap();
    // A real client gets through while the stranger sits there.
    let (mut r, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut r, 320, 240);
    // The stranger is dropped after the step timeout: reading ends (EOF or reset).
    stranger.set_read_timeout(Some(Duration::from_secs(10))).unwrap();
    let started = Instant::now();
    let mut buf = [0u8; 16];
    let n = std::io::Read::read(&mut stranger, &mut buf);
    assert!(matches!(n, Ok(0) | Err(_)) && started.elapsed() < Duration::from_secs(8));
}

#[test]
fn a_client_that_goes_quiet_is_dropped() {
    if !have_ffmpeg() {
        return;
    }
    let host = Host::start(true, "", &[("DD_IDLE_TIMEOUT_MS", "1500")]);
    let (mut r, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut r, 320, 240);
    // Never send a Ping: the host must hang up on its own.
    let started = Instant::now();
    while read_message(&mut r).is_ok() {
        assert!(started.elapsed() < Duration::from_secs(10), "host kept streaming to a silent client");
    }
}

#[test]
fn pairing_accept_and_decline_follow_the_answer() {
    if !have_ffmpeg() {
        return;
    }
    // Declined: the connection closes without a Config.
    let host = Host::start(false, "n\n", &[]);
    let (mut r, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    assert!(read_message(&mut r).is_err());
    drop(host);
    // Accepted: streaming starts.
    let host = Host::start(false, "y\n", &[]);
    let (mut r, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut r, 320, 240);
}

/// A hardware encoder that passes the start-up trial but dies in the real session must not end the
/// session: the host retries in software.
#[cfg(unix)]
#[test]
fn failing_hardware_encoder_falls_back_to_software() {
    use std::os::unix::fs::PermissionsExt;
    if !have_ffmpeg() {
        return;
    }
    let dir = std::env::temp_dir().join(format!("dd-fakeffmpeg-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let script = dir.join("ffmpeg");
    std::fs::write(
        &script,
        "#!/bin/sh\ncase \"$*\" in\n  *h264_vaapi*256x256*) exit 0 ;;\n  *h264_vaapi*) exit 1 ;;\nesac\nexec ffmpeg \"$@\"\n",
    )
    .unwrap();
    std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();
    // Later --encoder/--ffmpeg flags override the defaults given first.
    let host = Host::start_with(true, "", &[], &["--encoder", "vaapi", "--ffmpeg", script.to_str().unwrap()]);
    let (mut r, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut r, 320, 240);
    let mut bytes = 0;
    while bytes < 1000 {
        if let Message::Video { data, .. } = read_message(&mut r).expect("video after fallback") {
            bytes += data.len();
        }
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// If the encoder goes silent and the client then leaves, the session must still end (giving the
/// virtual monitor back) instead of waiting for output that never comes. Visible from outside as the
/// host accepting a new client again.
#[cfg(unix)]
#[test]
fn session_ends_when_client_leaves_even_if_the_encoder_is_silent() {
    use std::os::unix::fs::PermissionsExt;
    let dir = std::env::temp_dir().join(format!("dd-silentffmpeg-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let script = dir.join("ffmpeg");
    std::fs::write(&script, "#!/bin/sh\nexec sleep 600\n").unwrap();
    std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();
    let host = Host::start_with(true, "", &[], &["--ffmpeg", script.to_str().unwrap()]);
    let (mut first, first_w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut first, 320, 240);
    drop((first, first_w)); // the client leaves; the encoder never produced a byte
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let (mut again, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
        if matches!(read_message(&mut again), Ok(Message::Config { .. })) {
            break;
        }
        assert!(Instant::now() < deadline, "host still thinks the old session is active");
        std::thread::sleep(Duration::from_millis(300));
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// The web UI asks dd-host to end the session by dropping a `disconnect-request` file next to
/// `status.json`. The client must get a `Bye` (so it does not reconnect), and the host keeps running.
#[cfg(unix)]
#[test]
fn web_ui_disconnect_request_ends_the_session_with_bye() {
    if !have_ffmpeg() {
        return;
    }
    let host = Host::start(true, "", &[]);
    let dir = host._config.join("drawsplay-desktop").join("host");
    let (mut r, _w) = client(host.port, PROTOCOL_VERSION, 320, 240);
    expect_config(&mut r, 320, 240);
    // status.json shows the session while it lasts.
    let deadline = Instant::now() + Duration::from_secs(8);
    loop {
        let text = std::fs::read_to_string(dir.join("status.json")).unwrap_or_default();
        if text.contains("\"client\":\"test\"") {
            break;
        }
        assert!(Instant::now() < deadline, "status.json never showed the session: {text}");
        std::thread::sleep(Duration::from_millis(200));
    }
    std::fs::write(dir.join("disconnect-request"), "disconnect\n").unwrap();
    let deadline = Instant::now() + Duration::from_secs(8);
    loop {
        match read_message(&mut r) {
            Ok(Message::Bye { .. }) => break,
            Ok(_) => assert!(Instant::now() < deadline, "no Bye after the disconnect request"),
            Err(e) => panic!("connection ended without a Bye: {e}"),
        }
    }
    // The request file is consumed, the host still serves new clients, and status.json is idle again.
    assert!(!dir.join("disconnect-request").exists());
    let deadline = Instant::now() + Duration::from_secs(8);
    loop {
        let (mut again, _w2) = client(host.port, PROTOCOL_VERSION, 320, 240);
        if matches!(read_message(&mut again), Ok(Message::Config { .. })) {
            break;
        }
        assert!(Instant::now() < deadline, "host did not accept a new client after the disconnect");
        std::thread::sleep(Duration::from_millis(300));
    }
}
