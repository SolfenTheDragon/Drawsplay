//! xdg-desktop-portal client for Wayland sessions without a compositor-specific backend (GNOME,
//! KDE, ...). One RemoteDesktop session carries both the screen (a PipeWire stream) and the input
//! (keyboard, pointer, touch); if the portal has no RemoteDesktop, a ScreenCast-only session is used
//! and input falls back to `uinput`.
//!
//! The first start shows the desktop's own "share your screen" dialog; the user has to accept it.
//! Capture itself is done by `gst-launch-1.0 pipewiresrc` (see [`Portal::spawn_capture`]).

use std::collections::HashMap;
use std::os::fd::{AsRawFd, OwnedFd};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::mpsc;
use std::time::Duration;
use zbus::blocking::{Connection, MessageIterator};
use zbus::message::Type;
use zbus::zvariant::{ObjectPath, OwnedObjectPath, OwnedValue, Value};
use zbus::MatchRule;

const DEST: &str = "org.freedesktop.portal.Desktop";
const PATH: &str = "/org/freedesktop/portal/desktop";
const RD: &str = "org.freedesktop.portal.RemoteDesktop";
const SC: &str = "org.freedesktop.portal.ScreenCast";
/// How long we wait for the person to answer the portal's dialog.
const DIALOG_TIMEOUT: Duration = Duration::from_secs(180);

type Options<'a> = HashMap<&'a str, Value<'a>>;
type Results = HashMap<String, OwnedValue>;

pub struct Portal {
    conn: Connection,
    session: OwnedObjectPath,
    /// PipeWire node of the shared screen.
    pub node: u32,
    /// Size of the stream when the portal reported it.
    pub size: Option<(u32, u32)>,
    /// True when the session also carries input.
    pub has_input: bool,
    pw_fd: OwnedFd,
    reported_error: std::sync::atomic::AtomicBool,
}

static TOKEN: AtomicU32 = AtomicU32::new(0);

fn token(kind: &str) -> String {
    format!("dd_{kind}_{}_{}", std::process::id(), TOKEN.fetch_add(1, Ordering::Relaxed))
}

fn err<E: std::fmt::Display>(what: &str) -> impl FnOnce(E) -> String + '_ {
    move |e| format!("{what}: {e}")
}

/// Call a portal method that answers through a `Request` object and wait for its `Response`.
fn request(
    conn: &Connection,
    handle_token: &str,
    call: impl FnOnce() -> zbus::Result<zbus::Message>,
) -> Result<Results, String> {
    let sender = conn.unique_name().ok_or("no D-Bus name")?.trim_start_matches(':').replace('.', "_");
    let handle = format!("/org/freedesktop/portal/desktop/request/{sender}/{handle_token}");
    // Listen before calling, or a fast answer could be missed.
    let rule = MatchRule::builder()
        .msg_type(Type::Signal)
        .interface("org.freedesktop.portal.Request")
        .map_err(err("match rule"))?
        .member("Response")
        .map_err(err("match rule"))?
        .path(handle.as_str())
        .map_err(err("match rule"))?
        .build();
    let it = MessageIterator::for_match_rule(rule, conn, Some(8)).map_err(err("subscribe"))?;
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let _ = tx.send(it.into_iter().next());
    });
    call().map_err(err("portal call failed"))?;
    let msg = rx
        .recv_timeout(DIALOG_TIMEOUT)
        .map_err(|_| "no answer from the portal (dialog left open?)".to_string())?
        .ok_or("portal closed the connection")?
        .map_err(err("portal answer"))?;
    let (code, results): (u32, Results) = msg.body().deserialize().map_err(err("portal answer"))?;
    match code {
        0 => Ok(results),
        1 => Err("the screen-sharing request was declined".into()),
        _ => Err("the screen-sharing request was cancelled".into()),
    }
}

fn opts(handle_token: &str) -> Options<'_> {
    let mut o = Options::new();
    o.insert("handle_token", Value::from(handle_token));
    o
}

fn available_source_types(conn: &Connection) -> u32 {
    conn.call_method(Some(DEST), PATH, Some("org.freedesktop.DBus.Properties"), "Get", &(SC, "AvailableSourceTypes"))
        .ok()
        .and_then(|m| m.body().deserialize::<OwnedValue>().ok())
        .and_then(|v| match &*v {
            Value::Value(inner) => u32::try_from(&**inner).ok(),
            other => u32::try_from(other).ok(),
        })
        .unwrap_or(1)
}

impl Portal {
    /// Ask the desktop for a screen. `virtual_monitor`: request a new virtual monitor instead of an
    /// existing one (fails when the portal does not offer that).
    pub fn start(virtual_monitor: bool) -> Result<Portal, String> {
        let conn = Connection::session().map_err(err("no D-Bus session bus"))?;
        Portal::start_on(conn, virtual_monitor)
    }

    pub fn start_on(conn: Connection, virtual_monitor: bool) -> Result<Portal, String> {
        let available = available_source_types(&conn);
        let source = if virtual_monitor {
            if available & 4 == 0 {
                return Err("this desktop's portal cannot create virtual monitors".into());
            }
            4u32
        } else {
            1u32
        };
        match Portal::open(&conn, source, true) {
            Err(e) if e.starts_with("portal call failed") => {
                eprintln!("portal: RemoteDesktop unavailable ({e}); screen only, input through /dev/uinput");
                Portal::open(&conn, source, false)
            }
            other => other,
        }
    }

    fn open(conn: &Connection, source: u32, with_input: bool) -> Result<Portal, String> {
        let iface = if with_input { RD } else { SC };
        let tok = token("req");
        let sess_tok = token("sess");
        let mut o = opts(&tok);
        o.insert("session_handle_token", Value::from(sess_tok.as_str()));
        let r = request(conn, &tok, || conn.call_method(Some(DEST), PATH, Some(iface), "CreateSession", &(o,)))?;
        let session = r
            .get("session_handle")
            .and_then(|v| <String>::try_from(v.try_clone().ok()?).ok())
            .ok_or("portal returned no session")?;
        let session = OwnedObjectPath::try_from(session).map_err(err("session path"))?;
        // From here on, close the session if anything fails.
        let close = |conn: &Connection, s: &OwnedObjectPath| {
            let _ = conn.call_method(Some(DEST), s.as_str(), Some("org.freedesktop.portal.Session"), "Close", &());
        };
        let rest = (|| -> Result<(u32, Option<(u32, u32)>), String> {
            let sp = ObjectPath::try_from(session.as_str()).map_err(err("session path"))?;
            if with_input {
                let t = token("dev");
                let mut o = opts(&t);
                o.insert("types", Value::from(7u32)); // keyboard | pointer | touchscreen
                request(conn, &t, || conn.call_method(Some(DEST), PATH, Some(RD), "SelectDevices", &(&sp, o)))?;
            }
            let t = token("src");
            let mut o = opts(&t);
            o.insert("types", Value::from(source));
            o.insert("multiple", Value::from(false));
            o.insert("cursor_mode", Value::from(1u32)); // hidden: the client draws its own
            request(conn, &t, || conn.call_method(Some(DEST), PATH, Some(SC), "SelectSources", &(&sp, o)))?;
            let t = token("start");
            let o = opts(&t);
            let r = request(conn, &t, || conn.call_method(Some(DEST), PATH, Some(iface), "Start", &(&sp, "", o)))?;
            let streams = r.get("streams").ok_or("portal returned no stream")?;
            let streams: Vec<(u32, HashMap<String, OwnedValue>)> =
                streams.try_clone().map_err(err("streams"))?.try_into().map_err(err("streams"))?;
            let (node, props) = streams.into_iter().next().ok_or("portal returned an empty stream list")?;
            let size = props
                .get("size")
                .and_then(|v| <(i32, i32)>::try_from(v.try_clone().ok()?).ok())
                .filter(|s| s.0 > 0 && s.1 > 0)
                .map(|s| (s.0 as u32, s.1 as u32));
            Ok((node, size))
        })();
        let (node, size) = match rest {
            Ok(v) => v,
            Err(e) => {
                close(conn, &session);
                return Err(e);
            }
        };
        let sp = ObjectPath::try_from(session.as_str()).map_err(err("session path"))?;
        let fd = conn
            .call_method(Some(DEST), PATH, Some(SC), "OpenPipeWireRemote", &(&sp, Options::new()))
            .map_err(err("OpenPipeWireRemote"))
            .and_then(|m| m.body().deserialize::<zbus::zvariant::OwnedFd>().map_err(err("OpenPipeWireRemote reply")));
        let fd = match fd {
            Ok(fd) => OwnedFd::from(fd),
            Err(e) => {
                close(conn, &session);
                return Err(e);
            }
        };
        Ok(Portal {
            conn: conn.clone(),
            session,
            node,
            size,
            has_input: with_input,
            pw_fd: fd,
            reported_error: Default::default(),
        })
    }

    /// Start `gst-launch-1.0` reading the shared screen and writing raw BGRx frames of `w`x`h` at `fps`
    /// to its stdout (the encoder ffmpeg reads them as `rawvideo`).
    pub fn spawn_capture(&self, w: u32, h: u32, fps: u32) -> Result<Child, String> {
        spawn_gst(self.pw_fd.as_raw_fd(), self.node, w, h, fps)
    }

    fn notify(&self, method: &str, body: &(impl serde::Serialize + zbus::zvariant::DynamicType)) {
        if !self.has_input {
            return;
        }
        if let Err(e) = self.conn.call_method(Some(DEST), PATH, Some(RD), method, body) {
            if !self.reported_error.swap(true, Ordering::Relaxed) {
                eprintln!("portal input {method} failed: {e}");
            }
        }
    }

    fn sp(&self) -> ObjectPath<'_> {
        ObjectPath::try_from(self.session.as_str()).expect("validated at creation")
    }

    /// Pointer position in stream pixels.
    pub fn pointer_move(&self, x: f64, y: f64) {
        self.notify("NotifyPointerMotionAbsolute", &(self.sp(), Options::new(), self.node, x, y));
    }

    /// `code` is a Linux `BTN_*` value.
    pub fn pointer_button(&self, code: i32, down: bool) {
        self.notify("NotifyPointerButton", &(self.sp(), Options::new(), code, down as u32));
    }

    /// Whole scroll steps; positive `vertical` scrolls down, positive `horizontal` right.
    pub fn pointer_scroll(&self, horizontal: i32, vertical: i32) {
        if vertical != 0 {
            self.notify("NotifyPointerAxisDiscrete", &(self.sp(), Options::new(), 0u32, vertical));
        }
        if horizontal != 0 {
            self.notify("NotifyPointerAxisDiscrete", &(self.sp(), Options::new(), 1u32, horizontal));
        }
    }

    /// `code` is a Linux `KEY_*` value.
    pub fn key(&self, code: i32, down: bool) {
        self.notify("NotifyKeyboardKeycode", &(self.sp(), Options::new(), code, down as u32));
    }

    pub fn touch_down(&self, slot: u32, x: f64, y: f64) {
        self.notify("NotifyTouchDown", &(self.sp(), Options::new(), self.node, slot, x, y));
    }

    pub fn touch_move(&self, slot: u32, x: f64, y: f64) {
        self.notify("NotifyTouchMotion", &(self.sp(), Options::new(), self.node, slot, x, y));
    }

    pub fn touch_up(&self, slot: u32) {
        self.notify("NotifyTouchUp", &(self.sp(), Options::new(), slot));
    }
}

/// `gst-launch-1.0 pipewiresrc` on connected PipeWire socket `fd` (handed to the child as fd 3).
fn spawn_gst(fd: i32, node: u32, w: u32, h: u32, fps: u32) -> Result<Child, String> {
    use std::os::unix::process::CommandExt;
    let mut c = Command::new("gst-launch-1.0");
    c.args([
        "-q",
        "pipewiresrc",
        "fd=3",
        &format!("path={node}"),
        "do-timestamp=true",
        "!",
        "videoconvert",
        "!",
        "videoscale",
    ]);
    c.args(["!", "videorate", "!"]);
    c.arg(format!("video/x-raw,format=BGRx,width={w},height={h},framerate={fps}/1"));
    c.args(["!", "fdsink", "fd=1"]);
    c.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::inherit());
    // SAFETY: only async-signal-safe calls (dup2/fcntl) between fork and exec.
    unsafe {
        c.pre_exec(move || {
            if libc::dup2(fd, 3) < 0 {
                return Err(std::io::Error::last_os_error());
            }
            let flags = libc::fcntl(3, libc::F_GETFD);
            libc::fcntl(3, libc::F_SETFD, flags & !libc::FD_CLOEXEC);
            Ok(())
        });
    }
    c.spawn()
        .map_err(|e| format!("cannot start gst-launch-1.0 ({e}); install gstreamer1.0-tools and gstreamer1.0-pipewire"))
}

impl Drop for Portal {
    fn drop(&mut self) {
        let _ = self.conn.call_method(
            Some(DEST),
            self.session.as_str(),
            Some("org.freedesktop.portal.Session"),
            "Close",
            &(),
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Mutex};
    use zbus::blocking::connection::Builder;
    use zbus::message::Header;

    type Log = Arc<Mutex<Vec<String>>>;

    /// Stand-in for the desktop portal: answers every request-style call with a `Response` signal.
    struct Mock {
        conn: Connection,
        log: Log,
        with_remote_desktop: bool,
        source_types: u32,
    }

    impl Mock {
        fn respond(&self, hdr: &Header<'_>, opts: &Options<'_>, results: Results) -> OwnedObjectPath {
            let sender = hdr.sender().unwrap().as_str().trim_start_matches(':').replace('.', "_");
            let tok: String = opts
                .get("handle_token")
                .and_then(|v| <String>::try_from(v.try_clone().ok()?).ok())
                .expect("handle_token");
            let path = format!("/org/freedesktop/portal/desktop/request/{sender}/{tok}");
            let (conn, dest) = (self.conn.clone(), hdr.sender().unwrap().to_owned());
            let p = path.clone();
            std::thread::spawn(move || {
                std::thread::sleep(Duration::from_millis(30));
                let _ = conn.emit_signal(
                    Some(dest),
                    p.as_str(),
                    "org.freedesktop.portal.Request",
                    "Response",
                    &(0u32, results),
                );
            });
            OwnedObjectPath::try_from(path).unwrap()
        }
    }

    fn session_results(s: &str) -> Results {
        let mut r = Results::new();
        r.insert("session_handle".into(), Value::from(s).try_to_owned().unwrap());
        r
    }

    struct MockRd(Arc<Mock>);
    #[zbus::interface(name = "org.freedesktop.portal.RemoteDesktop")]
    impl MockRd {
        fn create_session(
            &self,
            options: Options<'_>,
            #[zbus(header)] hdr: Header<'_>,
        ) -> zbus::fdo::Result<OwnedObjectPath> {
            if !self.0.with_remote_desktop {
                return Err(zbus::fdo::Error::UnknownMethod("no RemoteDesktop".into()));
            }
            self.0.log.lock().unwrap().push("RD.CreateSession".into());
            Ok(self.0.respond(&hdr, &options, session_results("/org/freedesktop/portal/desktop/session/test/s1")))
        }
        fn select_devices(
            &self,
            _s: ObjectPath<'_>,
            options: Options<'_>,
            #[zbus(header)] hdr: Header<'_>,
        ) -> OwnedObjectPath {
            let types = options.get("types").and_then(|v| u32::try_from(v.try_clone().ok()?).ok());
            self.0.log.lock().unwrap().push(format!("RD.SelectDevices types={types:?}"));
            self.0.respond(&hdr, &options, Results::new())
        }
        fn start(
            &self,
            _s: ObjectPath<'_>,
            _parent: &str,
            options: Options<'_>,
            #[zbus(header)] hdr: Header<'_>,
        ) -> OwnedObjectPath {
            self.0.log.lock().unwrap().push("RD.Start".into());
            self.0.respond(&hdr, &options, start_results())
        }
        fn notify_pointer_motion_absolute(&self, _s: ObjectPath<'_>, _o: Options<'_>, stream: u32, x: f64, y: f64) {
            self.0.log.lock().unwrap().push(format!("motion {stream} {x} {y}"));
        }
        fn notify_pointer_button(&self, _s: ObjectPath<'_>, _o: Options<'_>, button: i32, state: u32) {
            self.0.log.lock().unwrap().push(format!("button {button} {state}"));
        }
        fn notify_pointer_axis_discrete(&self, _s: ObjectPath<'_>, _o: Options<'_>, axis: u32, steps: i32) {
            self.0.log.lock().unwrap().push(format!("axis {axis} {steps}"));
        }
        fn notify_keyboard_keycode(&self, _s: ObjectPath<'_>, _o: Options<'_>, key: i32, state: u32) {
            self.0.log.lock().unwrap().push(format!("key {key} {state}"));
        }
        fn notify_touch_down(&self, _s: ObjectPath<'_>, _o: Options<'_>, stream: u32, slot: u32, x: f64, y: f64) {
            self.0.log.lock().unwrap().push(format!("touchdown {stream} {slot} {x} {y}"));
        }
        fn notify_touch_up(&self, _s: ObjectPath<'_>, _o: Options<'_>, slot: u32) {
            self.0.log.lock().unwrap().push(format!("touchup {slot}"));
        }
    }

    fn start_results() -> Results {
        let mut props: HashMap<String, OwnedValue> = HashMap::new();
        props.insert("size".into(), Value::from((1280i32, 720i32)).try_to_owned().unwrap());
        let streams: Vec<(u32, HashMap<String, OwnedValue>)> = vec![(77, props)];
        let mut r = Results::new();
        r.insert("streams".into(), Value::new(streams).try_to_owned().unwrap());
        r
    }

    struct MockSc(Arc<Mock>);
    #[zbus::interface(name = "org.freedesktop.portal.ScreenCast")]
    impl MockSc {
        #[zbus(property)]
        fn available_source_types(&self) -> u32 {
            self.0.source_types
        }
        fn create_session(&self, options: Options<'_>, #[zbus(header)] hdr: Header<'_>) -> OwnedObjectPath {
            self.0.log.lock().unwrap().push("SC.CreateSession".into());
            self.0.respond(&hdr, &options, session_results("/org/freedesktop/portal/desktop/session/test/s2"))
        }
        fn select_sources(
            &self,
            _s: ObjectPath<'_>,
            options: Options<'_>,
            #[zbus(header)] hdr: Header<'_>,
        ) -> OwnedObjectPath {
            let types = options.get("types").and_then(|v| u32::try_from(v.try_clone().ok()?).ok());
            self.0.log.lock().unwrap().push(format!("SC.SelectSources types={types:?}"));
            self.0.respond(&hdr, &options, Results::new())
        }
        fn start(
            &self,
            _s: ObjectPath<'_>,
            _parent: &str,
            options: Options<'_>,
            #[zbus(header)] hdr: Header<'_>,
        ) -> OwnedObjectPath {
            self.0.log.lock().unwrap().push("SC.Start".into());
            self.0.respond(&hdr, &options, start_results())
        }
        fn open_pipe_wire_remote(&self, _s: ObjectPath<'_>, _o: Options<'_>) -> zbus::zvariant::OwnedFd {
            self.0.log.lock().unwrap().push("SC.OpenPipeWireRemote".into());
            let (r, _w) = std::os::unix::net::UnixStream::pair().unwrap();
            OwnedFd::from(r).into()
        }
    }

    struct MockSession(Arc<Mock>);
    #[zbus::interface(name = "org.freedesktop.portal.Session")]
    impl MockSession {
        fn close(&self) {
            self.0.log.lock().unwrap().push("Session.Close".into());
        }
    }

    /// Needs a running PipeWire (the desktop's own) and gst-launch-1.0 with the pipewire plugin:
    /// publish a test video node, then capture it the way a portal session would.
    /// Run with `cargo test -p dd-host capture_from_real_pipewire -- --ignored`.
    #[test]
    #[ignore]
    fn capture_from_real_pipewire() {
        use std::io::Read;
        let mut src = Command::new("gst-launch-1.0")
            .args(["videotestsrc", "is-live=true", "!", "video/x-raw,width=800,height=450,framerate=30/1", "!"])
            .args(["pipewiresink", "stream-properties=props,media.class=Video/Source,node.name=ddcapturetest"])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("gst-launch-1.0");
        std::thread::sleep(Duration::from_secs(2));
        let dump = Command::new("pw-dump").output().expect("pw-dump");
        let json = String::from_utf8_lossy(&dump.stdout).into_owned();
        let at = json.find("ddcapturetest").expect("test node");
        let id: u32 = json[..at]
            .rsplit("\"id\":")
            .next()
            .unwrap()
            .trim()
            .split(',')
            .next()
            .unwrap()
            .trim()
            .parse()
            .unwrap_or_else(|_| {
                // "id" precedes "info"; fall back to scanning objects
                panic!("cannot find node id in pw-dump")
            });
        let sock = std::os::unix::net::UnixStream::connect(format!(
            "{}/pipewire-0",
            std::env::var("XDG_RUNTIME_DIR").unwrap()
        ))
        .unwrap();
        let mut child = spawn_gst(sock.as_raw_fd(), id, 640, 360, 30).expect("spawn");
        let mut out = child.stdout.take().unwrap();
        let mut frame = vec![0u8; 640 * 360 * 4];
        out.read_exact(&mut frame).expect("one raw BGRx frame");
        let _ = child.kill();
        let _ = child.wait();
        let _ = src.kill();
        let _ = src.wait();
        assert!(frame.iter().any(|&b| b != 0), "frame is all black");
    }

    /// Private bus + mock portal; None when `dbus-daemon` is missing.
    fn setup(with_rd: bool, source_types: u32) -> Option<(Child, Connection, Connection, Log)> {
        let mut daemon = Command::new("dbus-daemon")
            .args(["--session", "--nofork", "--print-address=1"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .ok()?;
        let mut addr = String::new();
        std::io::BufRead::read_line(&mut std::io::BufReader::new(daemon.stdout.take()?), &mut addr).ok()?;
        let addr = addr.trim().to_string();
        let log: Log = Default::default();
        let server_conn = Builder::address(addr.as_str()).ok()?.build().ok()?;
        let mock =
            Arc::new(Mock { conn: server_conn.clone(), log: log.clone(), with_remote_desktop: with_rd, source_types });
        let server = Builder::address(addr.as_str())
            .ok()?
            .name(DEST)
            .ok()?
            .serve_at(PATH, MockRd(mock.clone()))
            .ok()?
            .serve_at(PATH, MockSc(mock.clone()))
            .ok()?
            .serve_at("/org/freedesktop/portal/desktop/session/test/s1", MockSession(mock.clone()))
            .ok()?
            .serve_at("/org/freedesktop/portal/desktop/session/test/s2", MockSession(mock))
            .ok()?
            .build()
            .ok()?;
        let client = Builder::address(addr.as_str()).ok()?.build().ok()?;
        Some((daemon, server, client, log))
    }

    #[test]
    fn full_session_with_input_against_a_mock_portal() {
        let Some((mut daemon, _server, client, log)) = setup(true, 5) else { return };
        let p = Portal::start_on(client, true).expect("portal session");
        assert_eq!((p.node, p.size, p.has_input), (77, Some((1280, 720)), true));
        p.pointer_move(10.5, 20.0);
        p.pointer_button(0x110, true);
        p.pointer_scroll(0, 3);
        p.key(30, true);
        p.touch_down(2, 1.0, 2.0);
        p.touch_up(2);
        drop(p); // closes the session
        std::thread::sleep(Duration::from_millis(100));
        let l = log.lock().unwrap().clone();
        let _ = daemon.kill();
        let want = [
            "RD.CreateSession",
            "RD.SelectDevices types=Some(7)",
            "SC.SelectSources types=Some(4)",
            "RD.Start",
            "SC.OpenPipeWireRemote",
            "motion 77 10.5 20",
            "button 272 1",
            "axis 0 3",
            "key 30 1",
            "touchdown 77 2 1 2",
            "touchup 2",
            "Session.Close",
        ];
        assert_eq!(l, want, "{l:#?}");
    }

    #[test]
    fn falls_back_to_screen_only_and_rejects_missing_virtual_monitors() {
        let Some((mut daemon, _server, client, log)) = setup(false, 1) else { return };
        // Virtual monitors are not offered (types = 1): clear error, nothing started.
        assert!(Portal::start_on(client.clone(), true).err().expect("must fail").contains("virtual"));
        let p = Portal::start_on(client, false).expect("screen-only session");
        assert!(!p.has_input);
        p.pointer_move(1.0, 1.0); // ignored without input, must not panic or call the portal
        drop(p);
        std::thread::sleep(Duration::from_millis(100));
        let l = log.lock().unwrap().clone();
        let _ = daemon.kill();
        assert!(l.contains(&"SC.CreateSession".to_string()) && l.contains(&"SC.Start".to_string()), "{l:#?}");
        assert!(!l.iter().any(|e| e.starts_with("motion")));
    }
}
