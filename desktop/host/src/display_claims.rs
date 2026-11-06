//! Shared display claims, so dd-host and the Android DrawSplay host (Go) on the same machine never
//! remove each other's virtual displays. Both record what they hold in
//! `$XDG_RUNTIME_DIR/drawsplay/display-claims.json` (or `/tmp/drawsplay-<uid>/`), guarded by `flock`
//! on `display-claims.lock` next to it, and leave alone anything another live process has claimed.
//! The format is shared with `host/display_claims_linux.go`:
//!
//! `{"claims":[{"owner":"desktop-host","pid":123,"comm":"dd-host","kind":"output","name":"HDMI-1"}]}`
//!
//! `kind` is "output" (a RandR or Wayland output in use) or "connector" (a spare HDMI connector forced
//! on; the name is its RandR output name). A claim counts only while `/proc/<pid>/comm` still equals
//! `comm`, so a crashed owner never blocks cleanup.

use serde_json::{json, Value};
use std::io::Write;
use std::os::fd::AsRawFd;
use std::path::PathBuf;

const OWNER: &str = "desktop-host";
pub const OUTPUT: &str = "output";

fn dir() -> PathBuf {
    // Tests never touch the real runtime folder.
    #[cfg(test)]
    return std::env::temp_dir().join(format!("dd-claims-test-{}", std::process::id()));
    #[cfg(not(test))]
    match std::env::var_os("XDG_RUNTIME_DIR").filter(|d| !d.is_empty()) {
        Some(d) => PathBuf::from(d).join("drawsplay"),
        // SAFETY: getuid has no preconditions.
        None => std::env::temp_dir().join(format!("drawsplay-{}", unsafe { libc::getuid() })),
    }
}

fn comm(pid: u32) -> String {
    std::fs::read_to_string(format!("/proc/{pid}/comm")).map(|s| s.trim().to_string()).unwrap_or_default()
}

#[derive(Clone, Debug, PartialEq)]
struct Claim {
    owner: String,
    pid: u32,
    comm: String,
    kind: String,
    name: String,
}

impl Claim {
    fn live(&self) -> bool {
        self.pid > 0 && !self.comm.is_empty() && comm(self.pid) == self.comm
    }
    fn parse(v: &Value) -> Option<Claim> {
        Some(Claim {
            owner: v.get("owner")?.as_str()?.into(),
            pid: u32::try_from(v.get("pid")?.as_u64()?).ok()?,
            comm: v.get("comm")?.as_str()?.into(),
            kind: v.get("kind")?.as_str()?.into(),
            name: v.get("name")?.as_str()?.into(),
        })
    }
    fn to_json(&self) -> Value {
        json!({"owner": self.owner, "pid": self.pid, "comm": self.comm, "kind": self.kind, "name": self.name})
    }
}

/// Run `f` on the live claims under the lock; when it returns true the list is written back.
fn with_claims(f: impl FnOnce(&mut Vec<Claim>) -> bool) -> std::io::Result<()> {
    let dir = dir();
    std::fs::create_dir_all(&dir)?;
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700));
    }
    let lock =
        std::fs::OpenOptions::new().create(true).truncate(false).write(true).open(dir.join("display-claims.lock"))?;
    // SAFETY: flock on a descriptor we own; released when `lock` closes.
    if unsafe { libc::flock(lock.as_raw_fd(), libc::LOCK_EX) } != 0 {
        return Err(std::io::Error::last_os_error());
    }
    let path = dir.join("display-claims.json");
    let mut claims: Vec<Claim> = std::fs::read_to_string(&path)
        .ok()
        .and_then(|t| serde_json::from_str::<Value>(&t).ok())
        .and_then(|v| v.get("claims")?.as_array().map(|a| a.iter().filter_map(Claim::parse).collect()))
        .unwrap_or_default();
    claims.retain(Claim::live);
    if f(&mut claims) {
        let doc = json!({"claims": claims.iter().map(Claim::to_json).collect::<Vec<_>>()});
        let tmp = path.with_extension("json.tmp");
        std::fs::File::create(&tmp)?.write_all(doc.to_string().as_bytes())?;
        std::fs::rename(&tmp, &path)?;
    }
    Ok(())
}

/// Record that this process holds `name`.
pub fn claim(kind: &str, name: &str) {
    if name.is_empty() {
        return;
    }
    let me = std::process::id();
    let c = Claim { owner: OWNER.into(), pid: me, comm: comm(me), kind: kind.into(), name: name.into() };
    let r = with_claims(|all| {
        if all.iter().any(|x| x.pid == me && x.kind == kind && x.name == name) {
            return false;
        }
        all.push(c);
        true
    });
    if let Err(e) = r {
        eprintln!("display claims: cannot record {kind} {name}: {e}");
    }
}

/// Drop this process's claim on `name` (any kind).
pub fn release(name: &str) {
    let me = std::process::id();
    let _ = with_claims(|all| {
        let before = all.len();
        all.retain(|x| !(x.pid == me && x.name == name));
        all.len() != before
    });
}

/// Does another live process (the Android host, or another dd-host) hold `name`?
pub fn claimed_by_other(name: &str) -> bool {
    let me = std::process::id();
    let mut found = false;
    let _ = with_claims(|all| {
        found = all.iter().any(|x| x.name == name && x.pid != me);
        false
    });
    found
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn own_claims_do_not_block_and_dead_owners_are_dropped() {
        let d = dir();
        claim(OUTPUT, "test-HDMI-1");
        assert!(!claimed_by_other("test-HDMI-1"));
        // A live other process (our parent, the test runner) and a dead one (wrong comm).
        let parent = std::os::unix::process::parent_id();
        with_claims(|all| {
            all.push(Claim {
                owner: "android-host".into(),
                pid: parent,
                comm: comm(parent),
                kind: "connector".into(),
                name: "test-Virtual-1-1".into(),
            });
            all.push(Claim {
                owner: "android-host".into(),
                pid: parent,
                comm: "gone".into(),
                kind: "output".into(),
                name: "test-HDMI-2".into(),
            });
            true
        })
        .unwrap();
        assert!(claimed_by_other("test-Virtual-1-1"));
        assert!(!claimed_by_other("test-HDMI-2"), "a claim whose process is gone must not block cleanup");
        release("test-HDMI-1");
        let text = std::fs::read_to_string(d.join("display-claims.json")).unwrap();
        assert!(!text.contains("test-HDMI-1") && !text.contains("test-HDMI-2"), "{text}");
    }

    /// The file format shared with the Go host (testdata/protocols/display-claims.json).
    #[test]
    fn reads_and_writes_the_shared_golden_file() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../testdata/protocols/display-claims.json");
        let doc: Value = serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
        let claims: Vec<Claim> = doc["claims"].as_array().unwrap().iter().filter_map(Claim::parse).collect();
        assert_eq!(
            claims,
            vec![
                Claim {
                    owner: "android-host".into(),
                    pid: 1234,
                    comm: "drawsplay-host".into(),
                    kind: "connector".into(),
                    name: "HDMI-A-1".into()
                },
                Claim {
                    owner: "desktop-host".into(),
                    pid: 5678,
                    comm: "dd-host".into(),
                    kind: OUTPUT.into(),
                    name: "Virtual-1-1".into()
                },
            ]
        );
        for (c, v) in claims.iter().zip(doc["claims"].as_array().unwrap()) {
            assert_eq!(&c.to_json(), v);
        }
    }
}
