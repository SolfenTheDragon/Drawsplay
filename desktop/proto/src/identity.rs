//! Per-user identity key and the list of peers this machine trusts.

use crate::secure::{generate_keypair, Keypair, PublicKey};
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use zeroize::Zeroizing;

/// Directory for keys and trust lists of `role` ("host" or "client"), under e.g. `%APPDATA%\DrawSplayDesktop` or `~/.config/drawsplay-desktop`.
pub fn config_dir(role: &str) -> io::Result<PathBuf> {
    let dir = if cfg!(windows) {
        PathBuf::from(std::env::var_os("APPDATA").ok_or_else(|| io::Error::other("APPDATA is not set"))?)
            .join("DrawSplayDesktop")
    } else if let Some(x) = std::env::var_os("XDG_CONFIG_HOME").filter(|x| !x.is_empty()) {
        PathBuf::from(x).join("drawsplay-desktop")
    } else {
        PathBuf::from(std::env::var_os("HOME").ok_or_else(|| io::Error::other("HOME is not set"))?)
            .join(".config/drawsplay-desktop")
    };
    let dir = dir.join(role);
    fs::create_dir_all(&dir)?;
    // Keys, the trust list and the live status belong to this user alone.
    if let Some(parent) = dir.parent() {
        make_private(parent, 0o700);
    }
    make_private(&dir, 0o700);
    Ok(dir)
}

/// Remove access for everyone but the owner when `path` is more open than `mode` (Unix; elsewhere the
/// per-user profile folder already is private). Failure is ignored: a folder we do not own is not ours
/// to change.
fn make_private(path: &Path, mode: u32) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Ok(meta) = fs::metadata(path) {
            if meta.permissions().mode() & 0o077 != 0 {
                let _ = fs::set_permissions(path, fs::Permissions::from_mode(mode));
            }
        }
    }
    #[cfg(not(unix))]
    let _ = (path, mode);
}

/// Create (or truncate) a file that only this user can read, for the trust list.
fn open_private(path: &Path, append: bool) -> io::Result<fs::File> {
    let mut o = fs::OpenOptions::new();
    o.write(true).create(true);
    if append {
        o.append(true);
    } else {
        o.truncate(true);
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        o.mode(0o600);
    }
    o.open(path)
}

pub fn to_hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

pub fn from_hex<const N: usize>(s: &str) -> Option<[u8; N]> {
    let s = s.trim();
    if s.len() != N * 2 || !s.is_ascii() {
        return None;
    }
    let mut out = [0u8; N];
    for (i, o) in out.iter_mut().enumerate() {
        *o = u8::from_str_radix(&s[i * 2..i * 2 + 2], 16).ok()?;
    }
    Some(out)
}

/// Load `identity.key` from `dir`, creating it on first use (readable only by the user on Unix).
pub fn load_or_create_identity(dir: &Path) -> io::Result<Keypair> {
    let path = dir.join("identity.key");
    if let Ok(text) = fs::read_to_string(&path).map(Zeroizing::new) {
        if from_hex::<32>(&text).is_some() && cfg!(unix) {
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                if fs::metadata(&path).is_ok_and(|m| m.permissions().mode() & 0o077 != 0) {
                    eprintln!("warning: {} was readable by other users; restricting it to you", path.display());
                    let _ = fs::set_permissions(&path, fs::Permissions::from_mode(0o600));
                }
            }
        }
        if let Some(private) = from_hex::<32>(&text) {
            // snow cannot derive the public key from the private one, so it is stored alongside.
            let public =
                fs::read_to_string(dir.join("identity.pub")).ok().and_then(|t| from_hex::<32>(&t)).ok_or_else(
                    || io::Error::other("identity.pub missing or corrupt; delete both identity files to regenerate"),
                )?;
            return Ok(Keypair { private, public });
        }
        return Err(io::Error::other("identity.key is corrupt; delete it to generate a new identity"));
    }
    let kp = generate_keypair()?;
    // Public half first: a crash in between leaves no key file, so the next start regenerates both.
    fs::write(dir.join("identity.pub"), to_hex(&kp.public))?;
    write_private(&path, &Zeroizing::new(to_hex(&kp.private)))?;
    Ok(kp)
}

fn write_private(path: &Path, text: &str) -> io::Result<()> {
    #[cfg(unix)]
    {
        use std::io::Write;
        use std::os::unix::fs::OpenOptionsExt;
        let mut f = fs::OpenOptions::new().write(true).create_new(true).mode(0o600).open(path)?;
        f.write_all(text.as_bytes())
    }
    #[cfg(not(unix))]
    {
        fs::write(path, text)
    }
}

/// The label stored with a trusted key: no control characters, at most 64 characters.
fn clean_label(label: &str) -> String {
    label.chars().filter(|c| !c.is_control()).take(64).collect()
}

/// Is `desc` (how a peer is described: its address) already trusted under a different key? That is what
/// a reinstalled computer looks like, and also what someone who has taken over its address looks like.
fn trusted_with_other_key(entries: &[(PublicKey, String)], key: &PublicKey, desc: &str) -> bool {
    let desc = clean_label(desc);
    entries.iter().any(|(k, label)| *label == desc.trim() && k != key)
}

/// Trusted peer keys, stored one hex key per line (`#` starts a comment, e.g. a name).
pub struct TrustStore {
    path: PathBuf,
    keys: Vec<PublicKey>,
}

impl TrustStore {
    pub fn load(path: PathBuf) -> io::Result<TrustStore> {
        let mut keys = Vec::new();
        match fs::read_to_string(&path) {
            Ok(text) => {
                for line in text.lines() {
                    let key = line.split('#').next().unwrap_or("");
                    if let Some(k) = from_hex::<32>(key) {
                        keys.push(k);
                    }
                }
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => {}
            Err(e) => return Err(e),
        }
        Ok(TrustStore { path, keys })
    }

    pub fn contains(&self, k: &PublicKey) -> bool {
        self.keys.contains(k)
    }

    /// Every entry as (key, label) in file order; the label is the comment after the key.
    pub fn entries(&self) -> io::Result<Vec<(PublicKey, String)>> {
        let text = match fs::read_to_string(&self.path) {
            Ok(t) => t,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(e) => return Err(e),
        };
        Ok(text
            .lines()
            .filter_map(|l| {
                let (key, label) = l.split_once('#').unwrap_or((l, ""));
                Some((from_hex::<32>(key)?, label.trim().to_string()))
            })
            .collect())
    }

    /// Remove every entry whose hex key starts with `prefix` (at least 6 characters, so a typo cannot
    /// wipe the list). Returns how many were removed; comments and unrelated lines are kept.
    pub fn forget(&mut self, prefix: &str) -> io::Result<usize> {
        let prefix = prefix.trim().to_ascii_lowercase();
        if prefix.len() < 6 || !prefix.chars().all(|c| c.is_ascii_hexdigit()) {
            return Err(io::Error::other("--forget needs at least 6 hex characters of the key (see --list-trusted)"));
        }
        let text = match fs::read_to_string(&self.path) {
            Ok(t) => t,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(0),
            Err(e) => return Err(e),
        };
        let mut removed = 0;
        let mut kept = String::new();
        for line in text.lines() {
            let key = line.split('#').next().unwrap_or("").trim().to_ascii_lowercase();
            if from_hex::<32>(&key).is_some() && key.starts_with(&prefix) {
                removed += 1;
            } else {
                kept.push_str(line);
                kept.push('\n');
            }
        }
        if removed > 0 {
            let tmp = self.path.with_extension("tmp");
            {
                use std::io::Write;
                open_private(&tmp, false)?.write_all(kept.as_bytes())?;
            }
            fs::rename(&tmp, &self.path)?;
            self.keys = TrustStore::load(self.path.clone())?.keys;
        }
        Ok(removed)
    }

    pub fn add(&mut self, k: PublicKey, label: &str) -> io::Result<()> {
        if self.contains(&k) {
            return Ok(());
        }
        use std::io::Write;
        let mut f = open_private(&self.path, true)?;
        let label = clean_label(label);
        // A hand-edited file may lack a final newline; without one the entry would join the last
        // line's comment and be lost on the next load.
        let needs_newline = fs::read(&self.path).map(|b| !b.is_empty() && !b.ends_with(b"\n")).unwrap_or(false);
        if needs_newline {
            f.write_all(b"\n")?;
        }
        writeln!(f, "{} # {}", to_hex(&k), label)?;
        self.keys.push(k);
        Ok(())
    }
}

/// Handle `--show-key` / `--trust KEY` for either app. `role` is "host" or "client".
/// Returns true if a flag was handled (the caller should then exit).
pub fn key_commands(
    role: &str,
    trust_file: &str,
    show: bool,
    trust: Option<&str>,
    list: bool,
    forget: Option<&str>,
) -> io::Result<bool> {
    if !show && trust.is_none() && !list && forget.is_none() {
        return Ok(false);
    }
    let dir = config_dir(role)?;
    if list {
        let entries = TrustStore::load(dir.join(trust_file))?.entries()?;
        if entries.is_empty() {
            println!("no trusted devices");
        }
        for (k, label) in entries {
            println!("{}  {}", to_hex(&k), label);
        }
    }
    if let Some(prefix) = forget {
        let mut store = TrustStore::load(dir.join(trust_file))?;
        let n = store.forget(prefix)?;
        println!("removed {n} trusted device{}", if n == 1 { "" } else { "s" });
    }
    if show {
        let id = load_or_create_identity(&dir)?;
        println!("{}", to_hex(&id.public));
    }
    if let Some(hex) = trust {
        let key = from_hex::<32>(hex).ok_or_else(|| {
            io::Error::other("--trust needs a 64-character hex key (see --show-key on the other machine)")
        })?;
        let mut store = TrustStore::load(dir.join(trust_file))?;
        store.add(key, "added with --trust")?;
        println!("trusted {}", to_hex(&key));
    }
    Ok(true)
}

/// Decide whether to accept `session`'s peer. Known keys pass silently. Unknown keys need both
/// people to see the same code and answer yes on this machine; accepted keys are remembered.
/// `trust_all` skips the question (testing only; it defeats man-in-the-middle protection).
pub fn authorize(
    session: &crate::secure::Session,
    store: &mut TrustStore,
    peer_desc: &str,
    trust_all: bool,
) -> io::Result<bool> {
    authorize_with(session, store, peer_desc, trust_all, &mut |desc, sas| {
        eprintln!("\nNew device wants to pair: {desc}");
        eprintln!("Pairing code: {sas}");
        eprint!("Does the other screen show the same code? Accept? [y/N] ");
        let mut line = String::new();
        io::stdin().read_line(&mut line)?;
        Ok(matches!(line.trim().to_ascii_lowercase().as_str(), "y" | "yes"))
    })
}

/// Like [`authorize`], with the question put by `ask(peer_desc, code)`. A program that needs a time
/// limit, or must notice that the other side has gone, supplies its own.
pub fn authorize_with(
    session: &crate::secure::Session,
    store: &mut TrustStore,
    peer_desc: &str,
    trust_all: bool,
    ask: &mut dyn FnMut(&str, &str) -> io::Result<bool>,
) -> io::Result<bool> {
    if store.contains(&session.remote_public) {
        return Ok(true);
    }
    if !trust_all && trusted_with_other_key(&store.entries().unwrap_or_default(), &session.remote_public, peer_desc) {
        eprintln!(
            "\nWARNING: {peer_desc} was trusted before with a DIFFERENT key. That is expected if its DrawSplay \
             Desktop identity was reset or the computer was reinstalled; if not, someone else may be answering at \
             that address. Accept only if you know why the key changed."
        );
    }
    let accept = if trust_all {
        eprintln!("WARNING: trusting new peer {peer_desc} without confirmation (--trust-all)");
        true
    } else {
        ask(peer_desc, &session.sas)?
    };
    if accept {
        store.add(session.remote_public, peer_desc)?;
    }
    Ok(accept)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("dd-test-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn identity_is_stable_across_loads() {
        let d = tmp("id");
        let a = load_or_create_identity(&d).unwrap();
        let b = load_or_create_identity(&d).unwrap();
        assert_eq!(a.private, b.private);
        assert_eq!(a.public, b.public);
        let _ = fs::remove_dir_all(d);
    }

    #[test]
    fn list_and_forget() {
        let d = tmp("forget");
        let path = d.join("t.txt");
        let mut s = TrustStore::load(path.clone()).unwrap();
        s.add([0xAB; 32], "laptop").unwrap();
        s.add([0xCD; 32], "tablet").unwrap();
        let e = s.entries().unwrap();
        assert_eq!(e.len(), 2);
        assert_eq!(e[0].1, "laptop");
        assert!(s.forget("ab").is_err()); // too short to be safe
        assert!(s.forget("xyzxyzxyz").is_err());
        assert_eq!(s.forget("ababab").unwrap(), 1);
        assert!(!s.contains(&[0xAB; 32]) && s.contains(&[0xCD; 32]));
        assert_eq!(TrustStore::load(path).unwrap().entries().unwrap().len(), 1);
        assert_eq!(s.forget("ffffff").unwrap(), 0);
    }

    #[test]
    fn trust_store_persists() {
        let d = tmp("trust");
        let p = d.join("trusted.txt");
        let key = [7u8; 32];
        let mut t = TrustStore::load(p.clone()).unwrap();
        assert!(!t.contains(&key));
        t.add(key, "laptop\nevil").unwrap();
        assert!(TrustStore::load(p).unwrap().contains(&key));
        let _ = fs::remove_dir_all(d);
    }

    #[test]
    fn add_survives_a_file_without_final_newline() {
        let d = tmp("nonl");
        let p = d.join("t.txt");
        fs::write(&p, format!("{} # first", "ab".repeat(32))).unwrap();
        let mut t = TrustStore::load(p.clone()).unwrap();
        t.add([0xCD; 32], "second").unwrap();
        let again = TrustStore::load(p).unwrap();
        assert!(again.contains(&[0xAB; 32]) && again.contains(&[0xCD; 32]));
        let _ = fs::remove_dir_all(d);
    }

    #[test]
    fn a_new_key_at_a_trusted_address_is_noticed() {
        let entries =
            vec![([1u8; 32], "host at 192.168.1.5:47800".to_string()), ([2u8; 32], "client at 10.0.0.2".into())];
        assert!(trusted_with_other_key(&entries, &[9; 32], "host at 192.168.1.5:47800"));
        assert!(!trusted_with_other_key(&entries, &[1; 32], "host at 192.168.1.5:47800"), "same key: nothing to say");
        assert!(!trusted_with_other_key(&entries, &[9; 32], "host at 192.168.1.6:47800"), "a different address");
        assert!(!trusted_with_other_key(&[], &[9; 32], "host at 192.168.1.5:47800"));
        // Labels are stored cleaned, so the comparison cleans the description the same way.
        let entries = vec![([1u8; 32], "host at ab".to_string())];
        assert!(trusted_with_other_key(&entries, &[9; 32], "host at a\nb"));
    }

    #[test]
    fn hex_roundtrip_and_rejects_garbage() {
        assert_eq!(from_hex::<4>("deadbeef"), Some([0xde, 0xad, 0xbe, 0xef]));
        assert_eq!(from_hex::<4>("deadbee"), None);
        assert_eq!(from_hex::<4>("zzzzzzzz"), None);
    }

    #[cfg(unix)]
    #[test]
    fn secrets_are_private_to_the_user() {
        use std::os::unix::fs::PermissionsExt;
        let mode = |p: &Path| fs::metadata(p).unwrap().permissions().mode() & 0o777;
        let d = tmp("perms");
        // A too-open folder and key (as an older version or a careless copy would leave them).
        fs::set_permissions(&d, fs::Permissions::from_mode(0o755)).unwrap();
        make_private(&d, 0o700);
        assert_eq!(mode(&d), 0o700);
        let kp = load_or_create_identity(&d).unwrap();
        assert_eq!(mode(&d.join("identity.key")), 0o600);
        fs::set_permissions(d.join("identity.key"), fs::Permissions::from_mode(0o644)).unwrap();
        assert_eq!(load_or_create_identity(&d).unwrap().public, kp.public);
        assert_eq!(mode(&d.join("identity.key")), 0o600, "an open key file must be tightened on load");
        // The trust list is created private too, including after --forget rewrites it.
        let mut t = TrustStore::load(d.join("trusted.txt")).unwrap();
        t.add([0xAB; 32], "a").unwrap();
        t.add([0xCD; 32], "b").unwrap();
        assert_eq!(mode(&d.join("trusted.txt")), 0o600);
        t.forget("abababab").unwrap();
        assert_eq!(mode(&d.join("trusted.txt")), 0o600);
        let _ = fs::remove_dir_all(d);
    }
}
