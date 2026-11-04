//! Removes every fake display, whoever made it. The machine should be left with only its real
//! monitors when a session ends, when the host shuts down, at start-up, and on request
//! (`dd-host --remove-fake-displays`).
//!
//! A display is fake when it has no real monitor behind it:
//! - X11: a spare HDMI/DP connector forced "connected" (status `connected` but an empty EDID), or an
//!   active output named `Virtual*`/`HEADLESS*` (vkms and similar);
//! - Wayland (sway, Hyprland): headless outputs, keeping the first one if nothing else is left.
//!
//! Safety rules: the primary output and real monitors are never touched, and the last remaining
//! output is never removed. If another DrawSplay host is streaming right now (the Android host's web
//! UI says so), nothing is removed.

use crate::{linux_display, wayland_display};
use std::path::{Path, PathBuf};

#[derive(Debug, Default, PartialEq)]
pub struct Report {
    pub removed: Vec<String>,
    pub failed: Vec<String>,
}

impl Report {
    pub fn is_empty(&self) -> bool {
        self.removed.is_empty() && self.failed.is_empty()
    }
}

/// A spare external connector that reports "connected" without a monitor: (RandR name, sysfs dir).
fn forced_connectors(sysfs: &Path) -> Vec<(String, PathBuf)> {
    let Ok(rd) = std::fs::read_dir(sysfs) else { return Vec::new() };
    let mut out = Vec::new();
    for e in rd.flatten() {
        let file = e.file_name().to_string_lossy().into_owned();
        // card0-HDMI-A-1 -> HDMI-1, card0-DP-2 -> DP-2; internal panels and virtual cards never match.
        let Some((_, conn)) = file.split_once('-') else { continue };
        let name = if let Some(n) = conn.strip_prefix("HDMI-A-") {
            format!("HDMI-{n}")
        } else if conn.starts_with("DP-") {
            conn.to_string()
        } else {
            continue;
        };
        let dir = e.path();
        let on_pci =
            std::fs::canonicalize(dir.join("device")).map(|d| d.to_string_lossy().contains("/pci")).unwrap_or(false);
        let connected = std::fs::read_to_string(dir.join("status")).map(|s| s.trim() == "connected").unwrap_or(false);
        let no_edid = std::fs::read(dir.join("edid")).map(|b| b.is_empty()).unwrap_or(true);
        if on_pci && connected && no_edid {
            out.push((name, dir));
        }
    }
    out
}

/// Hand a forced connector back to the kernel's own detection (the status file is group-writable
/// with the DrawSplay udev rule; otherwise try passwordless sudo).
fn release_connector(dir: &Path) -> Result<(), String> {
    let status = dir.join("status");
    if std::fs::write(&status, "detect").is_ok() {
        return Ok(());
    }
    let ok = std::process::Command::new("sudo")
        .args(["-n", "sh", "-c", "echo detect > \"$1\"", "sh"])
        .arg(&status)
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false);
    if ok {
        Ok(())
    } else {
        Err(format!("cannot write 'detect' to {} (run: echo detect | sudo tee {})", status.display(), status.display()))
    }
}

fn is_fake_name(name: &str) -> bool {
    let n = name.to_ascii_lowercase();
    n.starts_with("virtual") || n.starts_with("headless")
}

/// Decide which X11 outputs are fake. Pure so it can be tested. `forced` are RandR names.
pub fn x11_fakes<'a>(q: &'a linux_display::Query, forced: &[String]) -> Vec<&'a linux_display::Output> {
    let fake = |o: &linux_display::Output| {
        !o.primary && (forced.contains(&o.name) || (is_fake_name(&o.name) && o.geometry.is_some()))
    };
    let real_active = q.outputs.iter().filter(|o| o.geometry.is_some() && !fake(o)).count();
    if real_active == 0 {
        return Vec::new(); // never blank the last screen
    }
    q.outputs.iter().filter(|o| fake(o)).collect()
}

/// Decide which Wayland outputs to remove: every headless output, but never the last one left.
pub fn wayland_fakes(outs: &[wayland_display::Out]) -> Vec<&wayland_display::Out> {
    let headless: Vec<_> = outs.iter().filter(|o| o.name.to_ascii_uppercase().starts_with("HEADLESS")).collect();
    let real = outs.len() - headless.len();
    if real > 0 {
        headless
    } else {
        headless.into_iter().skip(1).collect() // keep the first so something is left to show
    }
}

/// Is another DrawSplay host (the Android one) streaming right now? Then its display is in use.
fn other_host_streaming() -> bool {
    use std::io::{Read, Write};
    let Ok(mut s) = std::net::TcpStream::connect_timeout(
        &"127.0.0.1:27180".parse().unwrap(),
        std::time::Duration::from_millis(300),
    ) else {
        return false;
    };
    let _ = s.set_read_timeout(Some(std::time::Duration::from_millis(800)));
    if s.write_all(b"GET /api/status HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n").is_err() {
        return false;
    }
    let mut body = String::new();
    let _ = s.read_to_string(&mut body);
    body.replace(' ', "").contains("\"streaming\":true")
}

/// Remove every fake display. Idempotent and quiet when there is nothing to do.
pub fn remove_all(reason: &str) -> Report {
    let mut report = Report::default();
    if other_host_streaming() {
        eprintln!("not removing fake displays ({reason}): another DrawSplay host is streaming");
        return report;
    }
    if wayland_display::active() {
        remove_wayland(&mut report);
    } else if std::env::var_os("DISPLAY").is_some() {
        remove_x11(&mut report);
    }
    if !report.is_empty() {
        eprintln!("fake displays ({reason}): removed {:?}, failed {:?}", report.removed, report.failed);
    }
    report
}

fn remove_x11(report: &mut Report) {
    let forced = forced_connectors(Path::new("/sys/class/drm"));
    let names: Vec<String> = forced.iter().map(|(n, _)| n.clone()).collect();
    let Ok(q) = linux_display::query() else { return };
    for o in x11_fakes(&q, &names) {
        let active = o.geometry.is_some();
        if active && !linux_display::release_output(&o.name, "") {
            report.failed.push(o.name.clone());
            continue;
        }
        if let Some((_, dir)) = forced.iter().find(|(n, _)| *n == o.name) {
            if let Err(e) = release_connector(dir) {
                eprintln!("{e}");
                if !active {
                    report.failed.push(o.name.clone());
                    continue;
                }
            }
        }
        report.removed.push(o.name.clone());
    }
}

fn remove_wayland(report: &mut Report) {
    let Ok(outs) = wayland_display::outputs() else { return };
    for o in wayland_fakes(&outs) {
        if wayland_display::release_named(wayland_display::kind_of_session(), &o.name) {
            report.removed.push(o.name.clone());
        } else {
            report.failed.push(o.name.clone());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use linux_display::{parse_query, Query};

    fn q(text: &str) -> Query {
        parse_query(text)
    }

    const LIVE: &str = "Screen 0: minimum 320 x 200, current 2944 x 1200, maximum 16384 x 16384
eDP-1 connected primary 1920x1200+0+0 (normal left inverted right x axis y axis) 302mm x 189mm
   1920x1200     60.00 +  60.00*
DP-1 disconnected (normal left inverted right x axis y axis)
HDMI-1 connected 1024x768+1920+0 (normal left inverted right x axis y axis) 0mm x 0mm
   1024x768      60.00*
Virtual-1-1 connected (normal left inverted right x axis y axis)
";

    #[test]
    fn forced_hdmi_is_fake_real_panel_and_inactive_vkms_are_kept() {
        let query = q(LIVE);
        let names: Vec<_> = x11_fakes(&query, &["HDMI-1".to_string()]).iter().map(|o| o.name.clone()).collect();
        assert_eq!(names, ["HDMI-1"]);
        // Without the forced-connector evidence a connected HDMI is a real monitor and stays.
        assert!(x11_fakes(&query, &[]).is_empty());
    }

    #[test]
    fn active_virtual_outputs_are_fake_but_the_last_screen_is_never_removed() {
        let two = q("Screen 0: minimum 8 x 8, current 100 x 100, maximum 100 x 100
eDP-1 connected primary 100x100+0+0 (normal)
VIRTUAL1 disconnected 100x100+100+0 (normal)
");
        assert_eq!(x11_fakes(&two, &[]).len(), 1);
        let only_fake = q("Screen 0: minimum 8 x 8, current 100 x 100, maximum 100 x 100
HDMI-1 connected 100x100+0+0 (normal)
");
        assert!(x11_fakes(&only_fake, &["HDMI-1".to_string()]).is_empty(), "must keep at least one real screen");
        // The primary is never touched even if it looks fake.
        let primary = q("Screen 0: minimum 8 x 8, current 100 x 100, maximum 100 x 100
Virtual-1 connected primary 100x100+0+0 (normal)
");
        assert!(x11_fakes(&primary, &[]).is_empty());
    }

    #[test]
    fn wayland_keeps_real_outputs_and_the_first_headless_when_alone() {
        let o = |n: &str| wayland_display::Out { name: n.into(), w: 1, h: 1, x: 0, y: 0 };
        let mixed = [o("eDP-1"), o("HEADLESS-2"), o("HEADLESS-3")];
        assert_eq!(wayland_fakes(&mixed).len(), 2);
        let only = [o("HEADLESS-1"), o("HEADLESS-2")];
        let r: Vec<_> = wayland_fakes(&only).iter().map(|x| x.name.clone()).collect();
        assert_eq!(r, ["HEADLESS-2"]);
    }

    #[test]
    fn forced_connector_detection_reads_sysfs_layout() {
        let d = std::env::temp_dir().join(format!("dd-fake-{}", std::process::id()));
        let mk = |name: &str, status: &str, edid: &[u8]| {
            let p = d.join(name);
            std::fs::create_dir_all(&p).unwrap();
            std::fs::write(p.join("status"), status).unwrap();
            std::fs::write(p.join("edid"), edid).unwrap();
        };
        mk("card0-HDMI-A-1", "connected\n", b"");
        mk("card0-HDMI-A-2", "connected\n", &[1, 2, 3]);
        mk("card0-DP-1", "disconnected\n", b"");
        mk("card0-eDP-1", "connected\n", b"");
        // The temp dir is not under /pci, so nothing qualifies: the PCI check is what keeps vkms out.
        assert!(forced_connectors(&d).is_empty());
        let _ = std::fs::remove_dir_all(&d);
    }
}
