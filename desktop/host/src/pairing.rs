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
