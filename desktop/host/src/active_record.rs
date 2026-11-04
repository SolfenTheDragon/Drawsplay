//! Crash safety for virtual monitors that outlive a process (X11 RandR outputs, sway and Hyprland
//! headless outputs). The host records what it created in `active-display.json` in its config folder
//! (a list: every session owns one monitor) and drops an entry once its monitor is verifiably gone. An entry still present at the next
//! start means the previous host was killed or crashed: [`sweep_stale`] removes that monitor, unless
//! the process that wrote the record is still a running `dd-host`.
//!
//! (The Windows driver removes its monitors by itself when the control handle closes, and portal
//! sessions end with the D-Bus connection, so neither needs a record.)

use serde_json::{json, Value};
use std::path::PathBuf;
use std::sync::OnceLock;

const FILE: &str = "active-display.json";
static DIR: OnceLock<PathBuf> = OnceLock::new();

#[derive(Debug, PartialEq, Clone)]
pub struct Record {
    /// "x11", "sway" or "hyprland".
    pub kind: String,
    pub output: String,
    /// RandR mode name created for the output (X11 only).
    pub mode: String,
    pub pid: u32,
}

pub fn init(dir: &std::path::Path) {
    let _ = DIR.set(dir.to_path_buf());
}

fn path() -> Option<PathBuf> {
    DIR.get().map(|d| d.join(FILE))
}

fn entry(kind: &str, output: &str, mode: &str, pid: u32) -> Value {
    json!({"kind": kind, "output": output, "mode": mode, "pid": pid})
}

fn save(records: &[Record]) {
    let Some(p) = path() else { return };
    if records.is_empty() {
        let _ = std::fs::remove_file(p);
        return;
    }
    let doc = Value::Array(records.iter().map(|r| entry(&r.kind, &r.output, &r.mode, r.pid)).collect());
    let tmp = p.with_extension("tmp");
    if std::fs::write(&tmp, doc.to_string()).is_ok() {
        let _ = std::fs::rename(&tmp, &p);
    }
}

/// Serialises read-modify-write of the file between session threads.
static LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

/// Add a monitor to the record (several sessions can each own one).
pub fn write(kind: &str, output: &str, mode: &str) {
    let _g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let mut all = read();
    all.retain(|r| !(r.kind == kind && r.output == output));
    all.push(Record { kind: kind.into(), output: output.into(), mode: mode.into(), pid: std::process::id() });
    save(&all);
}

/// Forget one monitor once it is verifiably gone.
pub fn clear(output: &str) {
    let _g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let mut all = read();
    all.retain(|r| r.output != output);
    save(&all);
}

fn parse_record(v: &Value) -> Option<Record> {
    Some(Record {
        kind: v.get("kind")?.as_str()?.to_string(),
        output: v.get("output")?.as_str()?.to_string(),
        mode: v.get("mode").and_then(|m| m.as_str()).unwrap_or("").to_string(),
        pid: v.get("pid")?.as_u64()? as u32,
    })
}

/// Every recorded monitor. Accepts the older single-object file as well as the current list.
pub fn read() -> Vec<Record> {
    let Some(text) = path().and_then(|p| std::fs::read_to_string(p).ok()) else { return Vec::new() };
    match serde_json::from_str::<Value>(&text) {
        Ok(Value::Array(a)) => a.iter().filter_map(parse_record).collect(),
        Ok(v) => parse_record(&v).into_iter().collect(),
        Err(_) => Vec::new(),
    }
}

/// Is `pid` a running dd-host (not a recycled pid of something else)?
pub fn is_running_host(pid: u32) -> bool {
    std::fs::read(format!("/proc/{pid}/cmdline"))
        .map(|c| {
            let s = String::from_utf8_lossy(&c);
            let exe = s.split('\0').next().unwrap_or("");
            exe.ends_with("dd-host") || exe.contains("dd_host") || exe.contains("loopback")
        })
        .unwrap_or(false)
}

/// Remove the monitors a previous, dead host left behind. Returns one description per monitor.
pub fn sweep_stale() -> Vec<String> {
    let mut notes = Vec::new();
    for r in read() {
        if r.pid != std::process::id() && is_running_host(r.pid) {
            continue; // another live host owns it
        }
        let released = match r.kind.as_str() {
            "x11" => crate::linux_display::release_output(&r.output, &r.mode),
            "sway" | "hyprland" => crate::wayland_display::release_named(&r.kind, &r.output),
            _ => true,
        };
        if released {
            clear(&r.output);
            notes.push(format!(
                "removed the {} monitor {} left behind by a previous host (pid {})",
                r.kind, r.output, r.pid
            ));
        } else {
            notes.push(format!(
                "could not remove the {} monitor {} left behind by pid {}; will retry at next start",
                r.kind, r.output, r.pid
            ));
        }
    }
    notes
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn record_roundtrip_and_pid_check() {
        let d = std::env::temp_dir().join(format!("dd-record-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        init(&d);
        write("x11", "VIRTUAL1", "dd1280x720");
        write("x11", "HDMI-1", "dd1920x1080");
        let r = read();
        assert_eq!(r.len(), 2);
        assert_eq!(
            (r[0].kind.as_str(), r[0].output.as_str(), r[0].mode.as_str(), r[0].pid),
            ("x11", "VIRTUAL1", "dd1280x720", std::process::id())
        );
        clear("VIRTUAL1");
        assert_eq!(read().len(), 1);
        clear("HDMI-1");
        assert!(read().is_empty());
        assert!(path().is_some_and(|p| !p.exists()));
        // The older single-object file still reads.
        std::fs::write(path().unwrap(), r#"{"kind":"sway","output":"HEADLESS-2","mode":"","pid":7}"#).unwrap();
        assert_eq!(read()[0].output, "HEADLESS-2");
        clear("HEADLESS-2");
        assert!(!is_running_host(4_000_000)); // no such pid
        let _ = std::fs::remove_dir_all(&d);
    }
}
