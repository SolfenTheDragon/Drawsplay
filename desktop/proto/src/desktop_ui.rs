//! Small native dialogs, with a bounded terminal fallback for headless installations.

use std::io::{self, BufRead, IsTerminal, Write};
use std::process::{Child, Command, Output, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Mutex, OnceLock};
use std::time::{Duration, Instant};

enum Kind<'a> {
    Entry,
    Choice(&'a [&'a str]),
}

static TERMINAL: AtomicBool = AtomicBool::new(false);

pub fn use_terminal(terminal: bool) {
    TERMINAL.store(terminal, Ordering::Relaxed);
    if terminal {
        let _ = lines();
    }
}

pub fn graphical_available() -> bool {
    if TERMINAL.load(Ordering::Relaxed) {
        return false;
    }
    if cfg!(windows) {
        return true;
    }
    (std::env::var_os("DISPLAY").is_some() || std::env::var_os("WAYLAND_DISPLAY").is_some())
        && Command::new("zenity")
            .arg("--version")
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .is_ok_and(|s| s.success())
}

fn spawn(title: &str, text: &str, kind: &Kind<'_>) -> io::Result<Child> {
    if TERMINAL.load(Ordering::Relaxed) {
        return Err(io::Error::other("terminal dialogs requested"));
    }
    #[cfg(windows)]
    let mut command = {
        use std::os::windows::process::CommandExt;
        let mut c = Command::new("powershell.exe");
        c.args(["-NoProfile", "-STA", "-Command", r#"
$ErrorActionPreference = 'Stop'
try {
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$f = New-Object System.Windows.Forms.Form
$f.Text = $env:DD_DIALOG_TITLE; $f.Width = 760; $f.Height = 520
$f.StartPosition = 'CenterScreen'; $f.TopMost = $true
$label = New-Object System.Windows.Forms.TextBox
$label.Multiline = $true; $label.ReadOnly = $true; $label.ScrollBars = 'Vertical'
$label.Text = $env:DD_DIALOG_TEXT.Replace("`n", "`r`n"); $label.SetBounds(16,16,710,310)
$label.AccessibleName = $env:DD_DIALOG_TITLE; $label.TabStop = $true
$f.Controls.Add($label)
$entry = New-Object System.Windows.Forms.TextBox
$entry.SetBounds(16,338,710,30); $f.Controls.Add($entry)
$items = $env:DD_DIALOG_CHOICES -split "`n"
if ($env:DD_DIALOG_KIND -eq 'choice') {
  $entry.Visible = $false
  for ($i=0; $i -lt $items.Length; $i++) {
    $b = New-Object System.Windows.Forms.Button
    $b.Text = $items[$i]; $b.Tag = $items[$i]
    $b.SetBounds((16 + ($i % 3)*236),(338 + [int][Math]::Floor($i/3)*40),228,34)
    $b.Add_Click({ $script:answer = $this.Tag; $f.Close() })
    $f.Controls.Add($b)
  }
} else {
  $b = New-Object System.Windows.Forms.Button
  $b.Text = 'OK'; $b.SetBounds(480,388,110,34)
  $b.Add_Click({ $script:answer = $entry.Text; $f.Close() })
  $f.Controls.Add($b); $f.AcceptButton = $b
  $entry.TabIndex = 0
}
$cancel = New-Object System.Windows.Forms.Button
$cancel.Text = 'Cancel'; $cancel.SetBounds(608,428,110,34)
$cancel.Add_Click({ $f.Close() }); $f.Controls.Add($cancel); $f.CancelButton = $cancel
$script:answer = $null
[void]$f.ShowDialog()
if ($null -ne $script:answer) { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding; [Console]::Write($script:answer); exit 0 }
exit 1
} catch { [Console]::Error.WriteLine($_.Exception.Message); exit 2 }
"#]);
        c.env("DD_DIALOG_TITLE", title).env("DD_DIALOG_TEXT", text);
        match kind {
            Kind::Entry => {
                c.env("DD_DIALOG_KIND", "entry");
            }
            Kind::Choice(items) => {
                c.env("DD_DIALOG_KIND", "choice").env("DD_DIALOG_CHOICES", items.join("\n"));
            }
        }
        c.creation_flags(0x0800_0000);
        c
    };
    #[cfg(not(windows))]
    let mut command = {
        if std::env::var_os("DISPLAY").is_none() && std::env::var_os("WAYLAND_DISPLAY").is_none() {
            return Err(io::Error::other("no graphical display"));
        }
        let mut c = Command::new("zenity");
        c.args(["--title", title, "--text", text, "--no-markup", "--width=760", "--height=450"]);
        match kind {
            Kind::Entry => {
                c.arg("--entry");
            }
            Kind::Choice(items) => {
                c.args(["--list", "--column=Action", "--hide-header"]);
                c.args(*items);
            }
        }
        c
    };
    command.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn()
}

type Lines = mpsc::Receiver<(String, Instant)>;
fn lines() -> &'static Mutex<Lines> {
    static LINES: OnceLock<Mutex<Lines>> = OnceLock::new();
    LINES.get_or_init(|| {
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            for line in io::stdin().lock().lines().map_while(Result::ok) {
                if tx.send((line, Instant::now())).is_err() {
                    break;
                }
            }
        });
        Mutex::new(rx)
    })
}

fn wait_child(mut child: Child, timeout: Duration, mut alive: impl FnMut() -> bool) -> io::Result<Output> {
    use std::io::Read;
    let started = Instant::now();
    let mut stdout = child.stdout.take().unwrap();
    let mut stderr = child.stderr.take().unwrap();
    let read = |stream: &mut dyn Read| {
        let mut bytes = Vec::new();
        let _ = stream.read_to_end(&mut bytes);
        bytes
    };
    let output = std::thread::spawn(move || read(&mut stdout));
    let errors = std::thread::spawn(move || read(&mut stderr));
    let status = loop {
        if started.elapsed() >= timeout || !alive() {
            let _ = child.kill();
            let _ = child.wait();
            let _ = output.join();
            let _ = errors.join();
            return Err(io::Error::new(io::ErrorKind::Interrupted, "dialog expired or peer disconnected"));
        }
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) => std::thread::sleep(Duration::from_millis(100)),
            Err(error) => {
                let _ = child.kill();
                let _ = child.wait();
                let _ = output.join();
                let _ = errors.join();
                return Err(error);
            }
        }
    };
    let output =
        Output { status, stdout: output.join().unwrap_or_default(), stderr: errors.join().unwrap_or_default() };
    if started.elapsed() >= timeout || !alive() {
        return Err(io::Error::new(io::ErrorKind::Interrupted, "dialog expired or peer disconnected"));
    }
    Ok(output)
}

fn dialog(
    title: &str,
    text: &str,
    kind: Kind<'_>,
    timeout: Duration,
    mut alive: impl FnMut() -> bool,
) -> io::Result<Option<String>> {
    let started = Instant::now();
    if let Ok(child) = spawn(title, text, &kind) {
        let output = wait_child(child, timeout, &mut alive)?;
        if output.status.success() {
            return Ok(Some(String::from_utf8_lossy(&output.stdout).trim().to_string()));
        }
        let errors = String::from_utf8_lossy(&output.stderr);
        let no_display = errors.contains("cannot open display") || errors.contains("Unable to init server");
        if output.status.code() == Some(1) && !no_display {
            return Ok(None); // A cancelled native dialog is never retried in the terminal.
        }
        eprintln!("Native dialog unavailable: {errors}");
    }
    let terminal = io::stdin().is_terminal();
    let rx = lines().lock().unwrap_or_else(|e| e.into_inner());
    let asked = Instant::now();
    eprintln!("{title}\n{text}");
    if let Kind::Choice(items) = &kind {
        for (i, item) in items.iter().enumerate() {
            eprintln!("{}: {item}", i + 1);
        }
    }
    eprint!("Answer (blank cancels): ");
    let _ = io::stderr().flush();
    loop {
        if started.elapsed() >= timeout || !alive() {
            return Err(io::Error::other("dialog expired or peer disconnected"));
        }
        match rx.recv_timeout(Duration::from_millis(100)) {
            Ok((answer, at)) if !terminal || at >= asked => {
                if started.elapsed() >= timeout || !alive() {
                    return Err(io::Error::other("dialog expired or peer disconnected"));
                }
                let answer = answer.trim();
                if let Kind::Choice(items) = &kind {
                    if title == "DrawSplay: verify pairing"
                        && matches!(answer.to_ascii_lowercase().as_str(), "y" | "yes")
                    {
                        return Ok(items.last().map(|s| s.to_string()));
                    }
                    if let Some(item) =
                        answer.parse::<usize>().ok().and_then(|i| i.checked_sub(1)).and_then(|i| items.get(i))
                    {
                        return Ok(Some(item.to_string()));
                    }
                }
                return Ok((!answer.is_empty()).then(|| answer.into()));
            }
            Ok(_) | Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => return Ok(None),
        }
    }
}

pub fn entry(title: &str, text: &str) -> io::Result<Option<String>> {
    dialog(title, text, Kind::Entry, Duration::from_secs(3600), || true)
}

pub fn choose(title: &str, text: &str, choices: &[&str]) -> io::Result<Option<String>> {
    dialog(title, text, Kind::Choice(choices), Duration::from_secs(3600), || true)
}

pub fn choose_until(
    title: &str,
    text: &str,
    choices: &[&str],
    alive: impl FnMut() -> bool,
) -> io::Result<Option<String>> {
    dialog(title, text, Kind::Choice(choices), Duration::from_secs(3600), alive)
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;

    fn child(command: &str) -> Child {
        Command::new("sh").args(["-c", command]).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn().unwrap()
    }

    #[test]
    fn abandoned_or_expired_dialog_is_reaped_without_accepting() {
        for expired in [true, false] {
            let child = child("exec sleep 30");
            let pid = child.id();
            let result = wait_child(child, if expired { Duration::ZERO } else { Duration::from_secs(60) }, || expired);
            assert_eq!(result.unwrap_err().kind(), io::ErrorKind::Interrupted);
            #[cfg(target_os = "linux")]
            assert!(!std::path::Path::new(&format!("/proc/{pid}")).exists(), "dialog must be reaped");
            #[cfg(not(target_os = "linux"))]
            let _ = pid;
        }
    }

    #[test]
    fn completed_dialog_preserves_explicit_answer_and_cancel_status() {
        let output = wait_child(child("printf 'Codes match - trust device'"), Duration::from_secs(3), || true).unwrap();
        assert!(output.status.success());
        assert_eq!(output.stdout, b"Codes match - trust device");
        let output = wait_child(child("exit 1"), Duration::from_secs(3), || true).unwrap();
        assert!(!output.status.success());
        assert!(output.stdout.is_empty());
    }
}

pub struct Notice {
    done: std::sync::Arc<AtomicBool>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl Drop for Notice {
    fn drop(&mut self) {
        self.done.store(true, Ordering::Release);
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

/// Keep the code visible when this side already trusts the peer but the peer may have forgotten us.
pub fn pairing_notice(text: String) -> Option<Notice> {
    eprintln!("{text}");
    if !graphical_available() {
        return None;
    }
    let done = std::sync::Arc::new(AtomicBool::new(false));
    let cancelled = done.clone();
    let thread = std::thread::spawn(move || {
        let _ = dialog("DrawSplay: pairing code", &text, Kind::Choice(&["Close"]), Duration::from_secs(300), || {
            !cancelled.load(Ordering::Acquire)
        });
    });
    Some(Notice { done, thread: Some(thread) })
}

pub fn approve(text: &str, timeout: Duration, alive: impl FnMut() -> bool) -> io::Result<bool> {
    Ok(dialog(
        "DrawSplay: verify pairing",
        text,
        Kind::Choice(&["Deny", "Codes match - trust device"]),
        timeout,
        alive,
    )?
    .as_deref()
        == Some("Codes match - trust device"))
}

/// This view only renames or removes existing keys; it cannot grant trust.
pub fn manage(role: &str, file: &str) -> io::Result<()> {
    let result = manage_until(role, file, std::sync::Arc::new(AtomicBool::new(false)));
    if let Err(error) = &result {
        let _ = choose("DrawSplay: trusted devices error", &error.to_string(), &["Close"]);
    }
    result
}

pub fn manage_until(role: &str, file: &str, cancelled: std::sync::Arc<AtomicBool>) -> io::Result<()> {
    use crate::identity::{config_dir, load_or_create_identity, to_hex, TrustStore};
    let dir = config_dir(role)?;
    let identity = to_hex(&load_or_create_identity(&dir)?.public);
    loop {
        let mut store = TrustStore::load(dir.join(file))?;
        let entries = store.entries()?;
        let mut text = format!("This {role}'s identity:\n{identity}\n\nTrusted devices:\n");
        for (i, (key, label)) in entries.iter().enumerate() {
            text.push_str(&format!("\n{}. {label}\n{}\n", i + 1, to_hex(key)));
        }

        let alive = || !cancelled.load(Ordering::Acquire);
        match choose_until("DrawSplay: trusted devices", &text, &["Rename", "Forget", "Done"], alive)?.as_deref() {
            Some(action @ ("Rename" | "Forget")) => {
                let Some(number) = dialog(
                    "Select device",
                    "Enter the device number from the trusted list.",
                    Kind::Entry,
                    Duration::from_secs(3600),
                    alive,
                )?
                else {
                    continue;
                };
                let Some((key, label)) =
                    number.parse::<usize>().ok().and_then(|i| i.checked_sub(1)).and_then(|i| entries.get(i))
                else {
                    continue;
                };
                if action == "Rename" {
                    if let Some(name) = dialog(
                        "Name device",
                        &format!("Friendly name for {label}:\n{}", to_hex(key)),
                        Kind::Entry,
                        Duration::from_secs(3600),
                        alive,
                    )? {
                        store.rename(key, &name)?;
                    }
                } else if choose_until(
                    "Forget device",
                    &format!("Remove trust for {label}?\n{}", to_hex(key)),
                    &["Cancel", "Forget"],
                    alive,
                )?
                .as_deref()
                    == Some("Forget")
                {
                    store.forget(&to_hex(key))?;
                }
            }
            _ => return Ok(()),
        }
    }
}
