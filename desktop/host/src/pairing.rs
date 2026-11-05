//! The pairing question on the host's terminal.
//!
//! Anyone who can reach the port can start a pairing, so the question must not let a stranger tie up
//! the terminal: it has a time limit, it notices when the other computer has already gone (and then
//! trusts nothing), and answers typed before the question appeared are not taken for answers to it.

use std::io::{self, BufRead, IsTerminal};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError};
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

type Line = (String, Instant);

/// One thread reads the terminal for the life of the process; a question abandoned by timeout cannot
/// leave a reader behind to swallow the next answer.
fn lines() -> &'static Mutex<Receiver<Line>> {
    static LINES: OnceLock<Mutex<Receiver<Line>>> = OnceLock::new();
    LINES.get_or_init(|| {
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let stdin = io::stdin();
            for line in stdin.lock().lines() {
                match line {
                    Ok(l) => {
                        if tx.send((l, Instant::now())).is_err() {
                            break;
                        }
                    }
                    Err(_) => break,
                }
            }
            // Dropping the sender tells a waiting question there is no terminal (or it was closed).
        });
        Mutex::new(rx)
    })
}

fn gone() -> io::Error {
    io::Error::other("the other computer disconnected before the question was answered")
}

/// Ask whether `sas` matches the other screen. `alive` says whether the peer is still connected. Without a
/// terminal (nothing to read) the answer is no.
pub fn ask(desc: &str, sas: &str, timeout: Duration, mut alive: impl FnMut() -> bool) -> io::Result<bool> {
    let rx = lines().lock().unwrap_or_else(|e| e.into_inner());
    let terminal = io::stdin().is_terminal();
    let asked = Instant::now();
    eprintln!("\nNew device wants to pair: {desc}");
    eprintln!("Pairing code: {sas}");
    eprint!("Does the other screen show the same code? Accept? [y/N] ");
    loop {
        match rx.recv_timeout(Duration::from_millis(500)) {
            Ok((line, at)) => {
                if terminal && at < asked {
                    continue; // typed before this question was on screen
                }
                let yes = matches!(line.trim().to_ascii_lowercase().as_str(), "y" | "yes");
                if yes && !alive() {
                    eprintln!();
                    return Err(gone());
                }
                return Ok(yes);
            }
            Err(RecvTimeoutError::Timeout) => {
                if !alive() {
                    eprintln!("\n(the other computer disconnected)");
                    return Err(gone());
                }
                if asked.elapsed() >= timeout {
                    eprintln!("\n(no answer; pairing cancelled)");
                    return Err(io::Error::other("nobody answered the pairing question in time"));
                }
            }
            Err(RecvTimeoutError::Disconnected) => return Ok(false),
        }
    }
}

/// Longest a peer can be shut out of the pairing question, however often it has been refused.
const MAX_COOLDOWN: Duration = Duration::from_secs(600);
/// Refusals older than this are forgotten, so an honest slip does not count against a peer for ever.
const STRIKE_MEMORY: Duration = Duration::from_secs(3600);
/// Most peers remembered; beyond this the one whose block ends soonest is forgotten.
const MAX_TRACKED: usize = 1024;

/// Peers (see `access::peer_group`) that were refused, abandoned or timed out a pairing question, and
/// until when they get no new one. Every further refusal doubles the wait (up to ten minutes), so a
/// peer that keeps knocking asks for the operator's attention a few times an hour, not twice a minute.
#[derive(Default)]
pub struct Cooldowns {
    /// Peer -> (blocked until, refusals in a row, time of the last one).
    map: std::collections::HashMap<std::net::IpAddr, (Instant, u32, Instant)>,
}

impl Cooldowns {
    pub fn blocked(&self, peer: std::net::IpAddr, now: Instant) -> bool {
        self.map.get(&peer).is_some_and(|&(until, _, _)| until > now)
    }

    /// Record a refusal; `base` is the wait after the first one.
    pub fn failed(&mut self, peer: std::net::IpAddr, now: Instant, base: Duration) {
        self.map.retain(|_, &mut (_, _, last)| now.saturating_duration_since(last) < STRIKE_MEMORY);
        if self.map.len() >= MAX_TRACKED && !self.map.contains_key(&peer) {
            if let Some(oldest) = self.map.iter().min_by_key(|(_, v)| v.0).map(|(k, _)| *k) {
                self.map.remove(&oldest);
            }
        }
        let strikes = self.map.get(&peer).map_or(0, |&(_, s, _)| s).saturating_add(1);
        let wait = base.saturating_mul(1u32 << (strikes - 1).min(16)).min(MAX_COOLDOWN.max(base));
        self.map.insert(peer, (now + wait, strikes, now));
    }

    /// A pairing was accepted: the peer starts afresh.
    pub fn succeeded(&mut self, peer: std::net::IpAddr) {
        self.map.remove(&peer);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn each_refusal_doubles_the_wait_up_to_a_cap_and_success_resets_it() {
        let peer: std::net::IpAddr = "192.168.1.9".parse().unwrap();
        let other: std::net::IpAddr = "192.168.1.10".parse().unwrap();
        let base = Duration::from_secs(30);
        let t0 = Instant::now();
        let mut c = Cooldowns::default();
        assert!(!c.blocked(peer, t0));
        c.failed(peer, t0, base);
        assert!(c.blocked(peer, t0 + Duration::from_secs(29)) && !c.blocked(peer, t0 + Duration::from_secs(31)));
        assert!(!c.blocked(other, t0), "another peer is unaffected");
        let t1 = t0 + Duration::from_secs(31);
        c.failed(peer, t1, base);
        assert!(c.blocked(peer, t1 + Duration::from_secs(59)) && !c.blocked(peer, t1 + Duration::from_secs(61)));
        let mut t = t1;
        for _ in 0..12 {
            t += Duration::from_secs(700);
            c.failed(peer, t, base);
        }
        assert!(c.blocked(peer, t + Duration::from_secs(599)), "capped, but still blocked");
        assert!(!c.blocked(peer, t + Duration::from_secs(601)), "never longer than ten minutes");
        c.succeeded(peer);
        assert!(!c.blocked(peer, t));
        c.failed(peer, t, base);
        assert!(!c.blocked(peer, t + Duration::from_secs(31)), "starts from the base wait again");
    }

    #[test]
    fn old_refusals_are_forgotten() {
        let peer: std::net::IpAddr = "10.0.0.7".parse().unwrap();
        let base = Duration::from_secs(30);
        let t0 = Instant::now();
        let mut c = Cooldowns::default();
        for i in 0..5 {
            c.failed(peer, t0 + Duration::from_secs(i), base);
        }
        let later = t0 + STRIKE_MEMORY + Duration::from_secs(60);
        c.failed(peer, later, base);
        assert!(!c.blocked(peer, later + Duration::from_secs(31)), "an hour on, one slip costs the base wait");
    }

    #[test]
    fn the_table_stays_bounded() {
        let base = Duration::from_secs(30);
        let t0 = Instant::now();
        let mut c = Cooldowns::default();
        for i in 0..(MAX_TRACKED as u32 + 50) {
            c.failed(std::net::IpAddr::from([10, 0, (i >> 8) as u8, i as u8]), t0, base);
        }
        assert!(c.map.len() <= MAX_TRACKED);
    }
}
