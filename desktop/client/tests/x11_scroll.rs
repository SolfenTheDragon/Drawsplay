//! Run on an isolated X server:
//! `xvfb-run -a env DD_TEST_X11_SCROLL=1 cargo test -p dd-client --test x11_scroll -- --ignored`

#![cfg(target_os = "linux")]

use dd_proto::secure::{generate_keypair, handshake, SecureReader, SecureWriter};
use dd_proto::{read_message, write_message, Message, PointerPhase, PointerSource};
use std::io::{BufRead, BufReader};
use std::net::{TcpListener, TcpStream};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::DirBuilderExt;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

struct Client {
    child: Child,
    config: PathBuf,
}

impl Drop for Client {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = std::fs::remove_dir_all(&self.config);
    }
}

fn xdotool(args: &[&str]) -> String {
    let output = Command::new("xdotool").args(args).output().expect("start xdotool");
    assert!(output.status.success(), "xdotool failed: {}", String::from_utf8_lossy(&output.stderr));
    String::from_utf8(output.stdout).expect("xdotool output")
}

fn wheel_until_marker(
    reader: &mut SecureReader<TcpStream>,
    writer: &mut SecureWriter<TcpStream>,
    args: &[&str],
) -> Vec<(f32, f32)> {
    let mut commands = args.to_vec();
    commands.extend(["key", "38"]); // X11 keycode for A, after all wheel events.
    xdotool(&commands);

    let deadline = Instant::now() + Duration::from_secs(10);
    let mut wheels = Vec::new();
    while Instant::now() < deadline {
        match read_message(reader).expect("client input") {
            Message::Pointer { source: PointerSource::Mouse, phase: PointerPhase::Wheel, wheel_x, wheel_y, .. } => {
                wheels.push((wheel_x, wheel_y));
            }
            Message::Key { code: 0x04, down: false } => return wheels,
            Message::Ping(value) => write_message(writer, &Message::Pong(value)).unwrap(),
            _ => {}
        }
    }
    panic!("client did not forward the marker key");
}

#[test]
#[ignore = "requires an isolated X11 display, xdotool and ffmpeg"]
fn wheel_buttons_send_one_step_per_press() {
    assert_eq!(
        std::env::var("DD_TEST_X11_SCROLL").as_deref(),
        Ok("1"),
        "run this test only on an isolated X11 display"
    );
    assert!(std::env::var_os("DISPLAY").is_some(), "DISPLAY is required");
    let display = std::env::var("DISPLAY").unwrap();
    let number = display.trim_start_matches(':').split('.').next().unwrap();
    let lock = std::fs::read_to_string(format!("/tmp/.X{number}-lock")).expect("private Xvfb lock");
    let pid: u32 = lock.trim().parse().expect("Xvfb PID");
    let command = std::fs::read(format!("/proc/{pid}/cmdline")).expect("Xvfb process");
    let executable = command.split(|b| *b == 0).next().unwrap_or_default();
    assert!(
        std::path::Path::new(std::ffi::OsStr::from_bytes(executable)).file_name() == Some(std::ffi::OsStr::new("Xvfb")),
        "DISPLAY must belong to Xvfb, never a user's desktop"
    );

    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let port = listener.local_addr().unwrap().port();
    let config = std::env::temp_dir().join(format!("dd-x11-scroll-{}-{port}", std::process::id()));
    std::fs::DirBuilder::new().mode(0o700).create(&config).expect("create isolated client config");
    let mut child = Command::new(env!("CARGO_BIN_EXE_dd-client"))
        .args([&format!("127.0.0.1:{port}"), "--size", "320x240", "--trust-all", "--no-hwdec"])
        .env("XDG_CONFIG_HOME", &config)
        .env("WINIT_UNIX_BACKEND", "x11")
        .env_remove("WAYLAND_DISPLAY")
        .env_remove("SWAYSOCK")
        .env_remove("HYPRLAND_INSTANCE_SIGNATURE")
        .env_remove("XDG_SESSION_TYPE")
        .env_remove("XDG_RUNTIME_DIR")
        .env_remove("DBUS_SESSION_BUS_ADDRESS")
        .env_remove("DRAWSPLAY_DESKTOP_CONFIG")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start dd-client");
    let stderr = child.stderr.take().unwrap();
    let mut client = Client { child, config };
    let (log_tx, log_rx) = std::sync::mpsc::channel();
    thread::spawn(move || {
        for line in BufReader::new(stderr).lines().map_while(Result::ok) {
            if log_tx.send(line).is_err() {
                break;
            }
        }
    });

    let deadline = Instant::now() + Duration::from_secs(10);
    let mut stream = loop {
        match listener.accept() {
            Ok((stream, _)) => break stream,
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock && Instant::now() < deadline => {
                if let Some(status) = client.child.try_wait().unwrap() {
                    thread::sleep(Duration::from_millis(100));
                    let logs: Vec<String> = log_rx.try_iter().collect();
                    panic!("client exited before connecting ({status}); stderr: {logs:#?}");
                }
                thread::sleep(Duration::from_millis(20));
            }
            Err(e) => panic!("client did not connect: {e}"),
        }
    };
    stream.set_read_timeout(Some(Duration::from_secs(10))).unwrap();
    stream.set_write_timeout(Some(Duration::from_secs(10))).unwrap();
    let session = handshake(&mut stream, &generate_keypair().unwrap(), false).unwrap();
    let mut reader = session.reader(stream.try_clone().unwrap());
    let mut writer = session.writer(stream);
    assert!(matches!(read_message(&mut reader).unwrap(), Message::Hello { .. }));
    write_message(&mut writer, &Message::Config { width: 320, height: 240, fps: 30 }).unwrap();
    let video = Command::new("ffmpeg")
        .args(["-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30"])
        .args(["-frames:v", "60", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency"])
        .args(["-g", "15", "-bf", "0", "-pix_fmt", "yuv420p", "-f", "h264", "-"])
        .stdin(Stdio::null())
        .output()
        .expect("encode synthetic video");
    assert!(video.status.success(), "ffmpeg failed: {}", String::from_utf8_lossy(&video.stderr));
    write_message(&mut writer, &Message::Video { pts_us: 0, key: true, data: video.stdout }).unwrap();

    let deadline = Instant::now() + Duration::from_secs(10);
    let mut logs = Vec::new();
    loop {
        match log_rx.recv_timeout(deadline.saturating_duration_since(Instant::now())) {
            Ok(line) if line == "video ready" => break,
            Ok(line) => logs.push(line),
            Err(e) => panic!("client did not become ready: {e}; {logs:?}"),
        }
    }

    let windows = xdotool(&["search", "--onlyvisible", "--name", "^DrawSplay Desktop$"]);
    let window = windows.lines().next().expect("client window");
    xdotool(&["windowfocus", window, "mousemove", "--window", window, "100", "100"]);

    assert_eq!(wheel_until_marker(&mut reader, &mut writer, &["mousedown", "4"]), [(0.0, 1.0)]);
    assert!(wheel_until_marker(&mut reader, &mut writer, &["mouseup", "4"]).is_empty());
    assert_eq!(
        wheel_until_marker(
            &mut reader,
            &mut writer,
            &["click", "--repeat", "3", "--delay", "20", "4", "click", "5", "click", "6", "click", "7"]
        ),
        [(0.0, 1.0), (0.0, 1.0), (0.0, 1.0), (0.0, -1.0), (1.0, 0.0), (-1.0, 0.0)]
    );

    write_message(&mut writer, &Message::Bye { reason: "test complete".into() }).unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    while client.child.try_wait().unwrap().is_none() && Instant::now() < deadline {
        thread::sleep(Duration::from_millis(20));
    }
}
