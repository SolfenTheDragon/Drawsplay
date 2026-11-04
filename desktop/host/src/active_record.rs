//! Crash safety for virtual monitors that outlive a process (X11 RandR outputs, sway and Hyprland
//! headless outputs). The host records what it created in `active-display.json` in its config folder
//! and deletes the record once the monitor is verifiably gone. A record still present at the next
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

pub fn write(kind: &str, output: &str, mode: &str) {
    let Some(p) = path() else { return };
    let doc = json!({"kind": kind, "output": output, "mode": mode, "pid": std::process::id()});
    let tmp = p.with_extension("tmp");
    if std::fs::write(&tmp, doc.to_string()).is_ok() {
        let _ = std::fs::rename(&tmp, &p);
    }
}

pub fn clear() {
    if let Some(p) = path() {
        let _ = std::fs::remove_file(p);
    }
}

pub fn read() -> Option<Record> {
    let v: Value = serde_json::from_str(&std::fs::read_to_string(path()?).ok()?).ok()?;
    Some(Record {
        kind: v.get("kind")?.as_str()?.to_string(),
        output: v.get("output")?.as_str()?.to_string(),
        mode: v.get("mode").and_then(|m| m.as_str()).unwrap_or("").to_string(),
        pid: v.get("pid")?.as_u64()? as u32,
    })
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

/// Remove the monitor a previous, dead host left behind. Returns a description when it acted.
pub fn sweep_stale() -> Option<String> {
    let r = read()?;
    if r.pid != std::process::id() && is_running_host(r.pid) {
        return None; // another live host owns it
    }
    let released = match r.kind.as_str() {
        "x11" => crate::linux_display::release_output(&r.output, &r.mode),
        "sway" | "hyprland" => crate::wayland_display::release_named(&r.kind, &r.output),
        _ => true,
    };
    if released {
        clear();
        Some(format!("removed the {} monitor {} left behind by a previous host (pid {})", r.kind, r.output, r.pid))
    } else {
        Some(format!(
            "could not remove the {} monitor {} left behind by pid {}; will retry at next start",
            r.kind, r.output, r.pid
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn record_roundtrip_and_pid_check() {
        let d = std::env::temp_dir().join(format!("dd-record-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        init(&d);
        assert!(read().is_none() || path().is_some());
        write("x11", "VIRTUAL1", "dd1280x720");
        let r = read().expect("record");
        assert_eq!(
            (r.kind.as_str(), r.output.as_str(), r.mode.as_str(), r.pid),
            ("x11", "VIRTUAL1", "dd1280x720", std::process::id())
        );
        clear();
        assert!(read().is_none());
        assert!(!is_running_host(4_000_000)); // no such pid
        let _ = std::fs::remove_dir_all(&d);
    }
}
