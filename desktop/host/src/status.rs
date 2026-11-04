//! Live state of this host, published for other programs (the DrawSplay web UI) as `status.json`
//! in the host's config folder, plus a tiny control channel back: a `disconnect-request` file in the
//! same folder ends the active session.
//!
//! The file is rewritten about every 2 seconds while the host runs, so a reader treats one older than
//! a few seconds as "not running". Writes are atomic (temporary file, then rename).

use dd_proto::identity::{to_hex, TrustStore};
use serde_json::{json, Value};
use std::net::{Shutdown, TcpStream};
use std::path::{Path, PathBuf};
use std::sync::{Mutex, OnceLock};
use std::time::Instant;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

pub const STATUS_FILE: &str = "status.json";
pub const DISCONNECT_FILE: &str = "disconnect-request";
const BEAT: Duration = Duration::from_secs(2);
const POLL: Duration = Duration::from_millis(500);

#[derive(Clone, Debug)]
pub struct SessionInfo {
    pub client: String,
    pub peer: String,
    pub width: u32,
    pub height: u32,
    pub fps: u32,
    pub virtual_display: bool,
    pub backend: String,
    pub encoder: String,
}

struct Active {
    id: u64,
    info: SessionInfo,
    since_ms: u64,
    /// Handle used to end the session on request.
    stream: Option<TcpStream>,
    /// Set when the web UI asked to end this session; the session loop says goodbye and closes.
    disconnect_since: Option<Instant>,
}

struct State {
    dir: PathBuf,
    listen: String,
    version: &'static str,
    default_encoder: String,
    source: String,
    sessions: Vec<Active>,
    next_id: u64,
}

static STATE: OnceLock<Mutex<State>> = OnceLock::new();

fn now_ms() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0)
}

/// Start publishing. `dir` is the host's config folder.
pub fn init(dir: &Path, listen: &str, encoder: &str, source: &str) {
    let st = State {
        dir: dir.to_path_buf(),
        listen: listen.to_string(),
        version: env!("CARGO_PKG_VERSION"),
        default_encoder: encoder.to_string(),
        source: source.to_string(),
        sessions: Vec::new(),
        next_id: 1,
    };
    if STATE.set(Mutex::new(st)).is_err() {
        return;
    }
    publish();
    std::thread::spawn(|| {
        let mut since_write = Duration::ZERO;
        loop {
            std::thread::sleep(POLL);
            since_write += POLL;
            check_disconnect_request();
            enforce_disconnect();
            if since_write >= BEAT {
                publish();
                since_write = Duration::ZERO;
            }
        }
    });
}

/// Build the document. Separate from the file I/O so it can be tested.
fn document(st: &State, trusted: Vec<Value>) -> Value {
    let one = |a: &Active| {
        json!({
            "client": a.info.client,
            "peer": a.info.peer,
            "width": a.info.width,
            "height": a.info.height,
            "fps": a.info.fps,
            "virtualDisplay": a.info.virtual_display,
            "backend": a.info.backend,
            "encoder": a.info.encoder,
            "sinceUnixMs": a.since_ms,
        })
    };
    // "session" (the oldest) stays for readers that know only one; "sessions" lists them all.
    let session = st.sessions.first().map(one);
    let sessions: Vec<Value> = st.sessions.iter().map(one).collect();
    json!({
        "schema": 1,
        "pid": std::process::id(),
        "updatedUnixMs": now_ms(),
        "version": st.version,
        "listen": st.listen,
        "encoder": st.default_encoder,
        "source": st.source,
        "os": std::env::consts::OS,
        "arch": std::env::consts::ARCH,
        "session": session,
        "sessions": sessions,
        "trusted": trusted,
    })
}

fn trusted_list(dir: &Path) -> Vec<Value> {
    TrustStore::load(dir.join("trusted_clients.txt"))
        .and_then(|s| s.entries())
        .map(|e| e.into_iter().map(|(k, label)| json!({"key": to_hex(&k)[..12], "label": label})).collect())
        .unwrap_or_default()
}

fn write_atomic(dir: &Path, doc: &Value) {
    // One writer at a time: they share the temporary file name.
    static WRITE: Mutex<()> = Mutex::new(());
    let _w = WRITE.lock().unwrap_or_else(|e| e.into_inner());
    let tmp = dir.join("status.json.tmp");
    if std::fs::write(&tmp, doc.to_string()).is_ok() {
        let _ = std::fs::rename(&tmp, dir.join(STATUS_FILE));
    }
}

fn publish() {
    let Some(m) = STATE.get() else { return };
    let (dir, doc) = {
        let st = m.lock().unwrap_or_else(|e| e.into_inner());
        (st.dir.clone(), document(&st, trusted_list(&st.dir)))
    };
    write_atomic(&dir, &doc);
}

fn check_disconnect_request() {
    let Some(m) = STATE.get() else { return };
    let st = m.lock().unwrap_or_else(|e| e.into_inner());
    let req = st.dir.join(DISCONNECT_FILE);
    if !req.exists() {
        return;
    }
    let _ = std::fs::remove_file(&req);
    drop(st);
    if let Some(m) = STATE.get() {
        let mut st = m.lock().unwrap_or_else(|e| e.into_inner());
        if !st.sessions.is_empty() {
            eprintln!("disconnect requested from the web UI (ends all sessions)");
        }
        for a in st.sessions.iter_mut() {
            a.disconnect_since.get_or_insert_with(Instant::now);
        }
    }
}

/// Backstop: if the session loop has not reacted to a disconnect request within a few seconds (it is
/// stuck writing to a dead peer, say), cut the connection from here.
fn enforce_disconnect() {
    let Some(m) = STATE.get() else { return };
    let st = m.lock().unwrap_or_else(|e| e.into_inner());
    for a in &st.sessions {
        if a.disconnect_since.is_some_and(|t| t.elapsed() > Duration::from_secs(3)) {
            if let Some(s) = &a.stream {
                let _ = s.shutdown(Shutdown::Both);
            }
        }
    }
}

/// Has the web UI asked to end this session? (Polled by the session loop.)
pub fn disconnect_requested(id: u64) -> bool {
    STATE
        .get()
        .map(|m| {
            m.lock()
                .unwrap_or_else(|e| e.into_inner())
                .sessions
                .iter()
                .any(|a| a.id == id && a.disconnect_since.is_some())
        })
        .unwrap_or(false)
}

/// Marks a session as active until dropped.
pub struct SessionGuard {
    pub id: u64,
}

pub fn session_started(info: SessionInfo, stream: Option<TcpStream>) -> SessionGuard {
    let mut id = 0;
    if let Some(m) = STATE.get() {
        let mut st = m.lock().unwrap_or_else(|e| e.into_inner());
        id = st.next_id;
        st.next_id += 1;
        st.sessions.push(Active { id, info, since_ms: now_ms(), stream, disconnect_since: None });
    }
    publish();
    SessionGuard { id }
}

impl Drop for SessionGuard {
    fn drop(&mut self) {
        if let Some(m) = STATE.get() {
            m.lock().unwrap_or_else(|e| e.into_inner()).sessions.retain(|a| a.id != self.id);
        }
        publish();
    }
}

/// Remove the file on a clean exit so readers see "not running" at once.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
pub fn shutdown() {
    if let Some(m) = STATE.get() {
        let dir = m.lock().unwrap_or_else(|e| e.into_inner()).dir.clone();
        let _ = std::fs::remove_file(dir.join(STATUS_FILE));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn state(dir: &Path) -> State {
        State {
            dir: dir.to_path_buf(),
            listen: "0.0.0.0:47800".into(),
            version: "9.9.9",
            default_encoder: "libx264".into(),
            source: "virtual".into(),
            sessions: Vec::new(),
            next_id: 1,
        }
    }

    #[test]
    fn idle_and_active_documents() {
        let d = std::env::temp_dir().join(format!("dd-status-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        let mut st = state(&d);
        let idle = document(&st, vec![]);
        assert_eq!(idle["schema"], 1);
        assert!(idle["session"].is_null());
        assert_eq!(idle["sessions"].as_array().unwrap().len(), 0);
        assert_eq!(idle["listen"], "0.0.0.0:47800");
        st.sessions.push(Active {
            id: 1,
            info: SessionInfo {
                client: "laptop".into(),
                peer: "10.0.0.2:5000".into(),
                width: 1920,
                height: 1080,
                fps: 60,
                virtual_display: true,
                backend: "x11-virtual".into(),
                encoder: "h264_vaapi".into(),
            },
            since_ms: 123,
            stream: None,
            disconnect_since: None,
        });
        let active = document(&st, vec![json!({"key": "ab", "label": "x"})]);
        assert_eq!(active["session"]["client"], "laptop");
        assert_eq!(active["session"]["fps"], 60);
        assert_eq!(active["sessions"].as_array().unwrap().len(), 1);
        assert_eq!(active["trusted"][0]["label"], "x");
        write_atomic(&d, &active);
        let back: Value = serde_json::from_str(&std::fs::read_to_string(d.join(STATUS_FILE)).unwrap()).unwrap();
        assert_eq!(back["version"], "9.9.9");
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn trusted_list_hides_all_but_a_key_prefix() {
        let d = std::env::temp_dir().join(format!("dd-status-tl-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        let mut t = TrustStore::load(d.join("trusted_clients.txt")).unwrap();
        t.add([0xAB; 32], "tablet").unwrap();
        let l = trusted_list(&d);
        assert_eq!(l.len(), 1);
        assert_eq!(l[0]["key"].as_str().unwrap().len(), 12);
        assert_eq!(l[0]["label"], "tablet");
        let _ = std::fs::remove_dir_all(&d);
    }
}
