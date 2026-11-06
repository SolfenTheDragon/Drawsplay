//! dd-host: captures a screen (or a test pattern), encodes H.264 with ffmpeg and
//! streams it to DrawSplay Desktop clients, while injecting their input.
//!
//! - `main.rs`: start-up, the accept loop and the per-address connection limits.
//! - `args.rs`: the command line. `session.rs`: one client, from handshake and pairing to the end of
//!   its stream. `capture.rs`: starting and stopping ffmpeg and the capture helpers.
//! - `display.rs` picks the screen to stream (virtual or mirrored) from the platform modules:
//!   `vdisplay.rs` (Windows driver), `linux_display.rs` (X11), `wayland_display.rs`, `portal.rs`.
//! - `encoder.rs` and `ratecontrol.rs`: encoder choice and bitrate adaptation; `inject/`: input.
//! - Shared with the Android host (keep in step, see CONTRIBUTING.md): `display_claims.rs`,
//!   `fake_displays.rs`, `status.rs` and `idd_protocol.rs`.

mod access;
#[cfg(target_os = "linux")]
mod active_record;
mod args;
mod capture;
mod display;
#[cfg(target_os = "linux")]
mod display_claims;
mod encoder;
#[cfg(target_os = "linux")]
mod fake_displays;
mod idd_protocol;
mod inject;
#[cfg(target_os = "linux")]
mod linux_display;
mod pairing;
#[cfg(target_os = "linux")]
mod portal;
mod ratecontrol;
mod session;
mod status;
#[cfg(windows)]
mod vdisplay;
#[cfg(target_os = "linux")]
mod wayland_display;

use args::{parse_args, Source};
#[cfg(target_os = "linux")]
use capture::kill_tracked_children;
use dd_proto::identity::{config_dir, load_or_create_identity, TrustStore};
use session::{release_ip, serve, PendingGuard, Shared, MAX_PENDING, MAX_PENDING_PER_IP};
use std::net::TcpListener;
use std::path::Path;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

/// Hold an exclusive lock on `host.lock` in `dir` for the life of the process.
fn lock_config_dir(dir: &Path) -> Result<std::fs::File, String> {
    let path = dir.join("host.lock");
    let busy = || format!("another dd-host is already running with the config folder {}", dir.display());
    #[cfg(target_os = "linux")]
    {
        use std::os::fd::AsRawFd;
        let f = std::fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .write(true)
            .open(&path)
            .map_err(|e| format!("cannot open {}: {e}", path.display()))?;
        // SAFETY: flock on a descriptor we own; released when the file closes at exit.
        if unsafe { libc::flock(f.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } != 0 {
            return Err(busy());
        }
        Ok(f)
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        // No sharing: a second process gets a sharing violation.
        std::fs::OpenOptions::new().create(true).truncate(false).write(true).share_mode(0).open(&path).map_err(|e| {
            if e.raw_os_error() == Some(32) {
                busy()
            } else {
                format!("cannot open {}: {e}", path.display())
            }
        })
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        let _ = busy;
        std::fs::OpenOptions::new().create(true).truncate(false).write(true).open(&path).map_err(|e| e.to_string())
    }
}

/// Say, at most every 30 seconds, that connections from outside the allowed networks were dropped.
fn log_refused(ip: &str) {
    static LAST: Mutex<Option<(Instant, u32)>> = Mutex::new(None);
    let mut last = LAST.lock().unwrap_or_else(|e| e.into_inner());
    let (since, count) = last.get_or_insert((Instant::now() - Duration::from_secs(60), 0));
    *count += 1;
    if since.elapsed() >= Duration::from_secs(30) {
        eprintln!("refused {count} connection(s) from outside the allowed networks (latest: {ip}); see --allow");
        *last = Some((Instant::now(), 0));
    }
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
    // Without the pairing question anyone who can reach the port controls this computer.
    if args.trust_all
        && !args.bind.parse::<std::net::SocketAddr>().is_ok_and(|a| a.ip().is_loopback())
        && std::env::var("DD_INSECURE_TRUST_ALL").as_deref() != Ok("1")
    {
        eprintln!(
            "error: --trust-all is only allowed with a loopback --bind (for example 127.0.0.1:47800): without the \
             pairing check anyone on the network could type on this computer"
        );
        std::process::exit(2);
    }
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
    // One dd-host per config folder: two would overwrite each other's status.json and trust state.
    let _instance = lock_config_dir(&dir).unwrap_or_else(|e| {
        eprintln!("error: {e}");
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
    if args.allow_any {
        eprintln!("WARNING: --allow-any is set: connections from every address are served");
    } else if args.allow.is_empty() {
        eprintln!("serving private networks only (loopback, LAN, link-local, 100.64.0.0/10); --allow NET adds more");
    } else {
        eprintln!("serving private networks plus {} extra network(s) given with --allow", args.allow.len());
    }
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
    let allow = access::Allow::new(args.allow_any, &args.allow);
    let args = Arc::new(args);
    let id = Arc::new(id);
    let shared = Arc::new(Shared {
        sessions: AtomicUsize::new(0),
        lifecycle: Mutex::new(()),
        prompt: Mutex::new(()),
        pending: AtomicUsize::new(0),
        per_ip: Mutex::new(Default::default()),
        pair_blocked: Mutex::new(Default::default()),
        software_only: AtomicBool::new(false),
    });
    // Each connection runs on its own thread, so a silent or slow peer cannot block the others.
    // At most `--max-clients` of them become active sessions.
    for conn in listener.incoming() {
        match conn {
            Ok(s) => {
                let peer = s.peer_addr().map(|p| p.to_string()).unwrap_or_default();
                if let Ok(addr) = s.peer_addr() {
                    if !allow.permits(addr.ip()) {
                        log_refused(&addr.ip().to_string());
                        continue;
                    }
                }
                let ip =
                    s.peer_addr().map(|a| access::peer_group(a.ip())).unwrap_or(std::net::IpAddr::from([0, 0, 0, 0]));
                {
                    let mut m = shared.per_ip.lock().unwrap_or_else(|e| e.into_inner());
                    let held = m.entry(ip).or_insert(0);
                    if *held >= MAX_PENDING_PER_IP {
                        drop(m);
                        eprintln!("connection from {peer} refused: {ip} already has {MAX_PENDING_PER_IP} unfinished connections");
                        continue;
                    }
                    *held += 1;
                }
                if shared.pending.fetch_add(1, Ordering::SeqCst) >= MAX_PENDING {
                    shared.pending.fetch_sub(1, Ordering::SeqCst);
                    release_ip(&shared, ip);
                    eprintln!("connection from {peer} refused: too many unauthenticated connections");
                    continue;
                }
                eprintln!("connection from {peer}");
                let (args, id, shared, trust_path) = (args.clone(), id.clone(), shared.clone(), trust_path.clone());
                thread::spawn(move || {
                    let pending = PendingGuard { shared: &shared, ip };
                    if let Err(e) = serve(s, &args, &id, &trust_path, &shared, pending) {
                        eprintln!("session with {peer} ended: {e}");
                    }
                });
            }
            Err(e) => {
                // Persistent failures (out of file descriptors, say) would otherwise spin and flood the log.
                eprintln!("accept failed: {e}");
                thread::sleep(Duration::from_millis(200));
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(any(target_os = "linux", windows))]
    #[test]
    fn a_second_host_on_the_same_config_folder_is_refused() {
        let d = std::env::temp_dir().join(format!("dd-lock-test-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        let first = lock_config_dir(&d).expect("first lock");
        let err = lock_config_dir(&d).expect_err("second lock must fail");
        assert!(err.contains("already running"), "{err}");
        drop(first);
        assert!(lock_config_dir(&d).is_ok(), "the lock is free again once the first host is gone");
        let _ = std::fs::remove_dir_all(&d);
    }
}
