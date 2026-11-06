//! Starting the encoder (ffmpeg) and the capture helpers that feed it, and stopping them.

use crate::args::{Args, Source};
use crate::display;
use crate::encoder::Encoder;
#[cfg(windows)]
use crate::vdisplay;
#[cfg(target_os = "linux")]
use crate::wayland_display;
use std::io::Read;
use std::process::{Command, Stdio};
use std::sync::atomic::AtomicBool;
use std::sync::{mpsc, Mutex};
use std::thread;
#[cfg(target_os = "linux")]
use std::time::Duration;

/// How the encoder's input arrives when a helper process (not ffmpeg itself) captures the screen.
#[derive(Clone, Copy, PartialEq)]
pub(crate) enum Piped {
    No,
    /// `wf-recorder`'s nut stream.
    Nut,
    /// Raw BGRx frames of the target size (`gst-launch-1.0`, portal capture).
    Raw,
}

/// A DXGI output: adapter and output index (as `ddagrab` counts them) and its desktop rectangle.
#[derive(Clone, Copy, Debug, PartialEq)]
#[cfg_attr(not(windows), allow(dead_code))]
pub struct DxgiOutput {
    pub adapter: u32,
    pub output: u32,
    pub rect: (i32, i32, u32, u32),
}

/// Set once `ddagrab` has failed and `gdigrab` then worked: later sessions skip `ddagrab`.
#[cfg_attr(not(windows), allow(dead_code))]
pub(crate) static GDI_ONLY: AtomicBool = AtomicBool::new(false);

/// Windows capture input. `ddagrab` (DXGI Desktop Duplication) works in physical pixels, so it is right
/// at any display scaling, and is far cheaper than `gdigrab`, which copies through GDI and is not
/// DPI-aware (the cause of the offset/cropped capture at scaling above 100%). `gdigrab` is the fallback
/// for ffmpeg builds without `ddagrab` (before 6.0) or when duplication is refused.
#[cfg_attr(not(windows), allow(dead_code))]
pub(crate) fn windows_capture_args(
    origin: (i32, i32),
    w: u32,
    h: u32,
    fps: u32,
    dxgi: Option<DxgiOutput>,
) -> Vec<String> {
    let mut v: Vec<String> = Vec::new();
    match dxgi {
        Some(o) => {
            // ddagrab makes its own D3D11 device on adapter 0; another adapter needs an explicit one
            // (an explicit device on adapter 0 breaks duplication on some Intel drivers).
            if o.adapter != 0 {
                v.extend(["-init_hw_device".into(), format!("d3d11va:{}", o.adapter)]);
            }
            let mut f = format!("ddagrab=output_idx={}:framerate={fps}:draw_mouse=0", o.output);
            let (ox, oy) = (origin.0 - o.rect.0, origin.1 - o.rect.1);
            if (ox, oy) != (0, 0) || (w, h) != (o.rect.2, o.rect.3) {
                f += &format!(":video_size={w}x{h}:offset_x={ox}:offset_y={oy}");
            }
            f += ",hwdownload,format=bgra";
            v.extend(["-filter_complex".into(), f]);
        }
        None => {
            v.extend(["-fflags", "nobuffer", "-f", "gdigrab", "-framerate"].map(String::from));
            v.push(fps.to_string());
            v.extend(["-offset_x".into(), origin.0.to_string(), "-offset_y".into(), origin.1.to_string()]);
            v.extend(["-video_size".into(), format!("{w}x{h}"), "-draw_mouse".into(), "0".into()]);
            v.extend(["-i".into(), "desktop".into()]);
        }
    }
    v
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn ffmpeg_command(
    a: &Args,
    enc: Encoder,
    origin: (i32, i32),
    w: u32,
    h: u32,
    fps: u32,
    piped: Piped,
    gdi: bool,
    kbps: u32,
) -> Command {
    let _ = gdi;
    let piped_nut = piped == Piped::Nut;
    let mut c = Command::new(&a.ffmpeg);
    c.args(["-hide_banner", "-loglevel", "error"]);
    // An input option: the Windows ddagrab source is a filter graph, not an input.
    if !(cfg!(windows) && matches!(a.source, Source::Desktop | Source::Virtual) && piped == Piped::No) {
        c.args(["-fflags", "nobuffer"]);
    }
    c.args(enc.input_args());
    match a.source {
        Source::Test => {
            // -re paces the synthetic source at real time, like a real capture.
            c.args(["-re", "-f", "lavfi", "-i", &format!("testsrc2=size={w}x{h}:rate={fps}")]);
        }
        Source::Desktop | Source::Virtual if piped == Piped::Raw => {
            c.args(["-f", "rawvideo", "-pixel_format", "bgr0", "-video_size", &format!("{w}x{h}")]);
            c.args(["-framerate", &fps.to_string(), "-i", "pipe:0"]);
        }
        Source::Desktop | Source::Virtual if piped_nut => {
            // Wayland: wf-recorder feeds raw frames on stdin.
            c.args(["-use_wallclock_as_timestamps", "1", "-f", "nut", "-i", "pipe:0"]);
        }
        Source::Desktop | Source::Virtual => {
            #[cfg(windows)]
            {
                let dxgi = if gdi { None } else { vdisplay::dxgi_output_containing(origin.0, origin.1) };
                c.args(windows_capture_args(origin, w, h, fps, dxgi));
            }
            #[cfg(not(windows))]
            {
                let display = std::env::var("DISPLAY").unwrap_or_else(|_| ":0".into());
                c.args(["-f", "x11grab", "-framerate", &fps.to_string(), "-video_size", &format!("{w}x{h}")]);
                c.args(["-draw_mouse", "0", "-i", &format!("{display}+{},{}", origin.0, origin.1)]);
            }
        }
    }
    c.arg("-an");
    // wf-recorder's timestamps mean nothing to the encoder: stamp by arrival, then hold a steady rate.
    let rate = if piped_nut { format!("fps={fps}") } else { String::new() };
    match (enc.filter(), piped_nut) {
        (Some(f), true) => {
            c.args(["-vf", &format!("{rate},{f}")]);
        }
        (Some(f), false) => {
            c.args(["-vf", f]);
        }
        (None, true) => {
            c.args(["-vf", &rate]);
        }
        (None, false) => {}
    }
    if let Some(p) = enc.pix_fmt() {
        c.args(["-pix_fmt", p]);
    }
    c.args(enc.codec_args(kbps, fps * 2));
    c.args(["-f", "h264", "-"]);
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    c.stdout(Stdio::piped())
        .stdin(if piped != Piped::No { Stdio::piped() } else { Stdio::null() })
        .stderr(Stdio::inherit());
    c
}

/// Start the encoder (and, on Wayland, the `wf-recorder` that feeds it).
pub(crate) fn launch(
    a: &Args,
    display: &display::Display,
    enc: Encoder,
    fps: u32,
    gdi: bool,
    kbps: u32,
) -> std::io::Result<(std::process::Child, Option<std::process::Child>)> {
    let (w, h) = display.target.size;
    let mut recorder: Option<std::process::Child> = None;
    #[cfg(target_os = "linux")]
    if a.source != Source::Test {
        if let Some(out) = &display.wayland_output {
            recorder = Some(wayland_display::spawn_capture(out, fps).map_err(std::io::Error::other)?);
        }
    }
    #[cfg(target_os = "linux")]
    if a.source != Source::Test && recorder.is_none() {
        if let Some(p) = &display.portal {
            recorder = Some(p.spawn_capture(w, h, fps).map_err(std::io::Error::other)?);
        }
    }
    let piped = match (recorder.is_some(), display.portal.is_some()) {
        (false, _) => Piped::No,
        (true, true) => Piped::Raw,
        (true, false) => Piped::Nut,
    };
    let mut cmd = ffmpeg_command(a, enc, display.target.origin, w, h, fps, piped, gdi, kbps);
    if let Some(r) = recorder.as_mut() {
        cmd.stdin(Stdio::from(r.stdout.take().unwrap()));
    }
    match cmd.spawn() {
        Ok(ff) => {
            track(&ff);
            if let Some(r) = recorder.as_ref() {
                track(r);
            }
            Ok((ff, recorder))
        }
        Err(e) => {
            if let Some(r) = recorder.as_mut() {
                let _ = r.kill();
                let _ = r.wait();
            }
            Err(std::io::Error::other(format!("cannot start ffmpeg ({}): {e}", a.ffmpeg)))
        }
    }
}

/// Read the encoder's output on its own thread so the session loop can notice a vanished client even
/// when the encoder is silent. An empty chunk means end of output.
pub(crate) fn spawn_reader(mut video: std::process::ChildStdout) -> mpsc::Receiver<std::io::Result<Vec<u8>>> {
    let (tx, rx) = mpsc::sync_channel(4);
    thread::spawn(move || {
        let mut buf = vec![0u8; 64 * 1024];
        loop {
            match video.read(&mut buf) {
                Ok(0) => {
                    let _ = tx.send(Ok(Vec::new()));
                    break;
                }
                Ok(n) => {
                    if tx.send(Ok(buf[..n].to_vec())).is_err() {
                        break;
                    }
                }
                Err(e) => {
                    let _ = tx.send(Err(e));
                    break;
                }
            }
        }
    });
    rx
}

/// Pids of the encoder and capture helpers, so a signal handler can stop them before it removes the
/// monitor they are reading (removing a monitor under a running capture can crash the compositor).
pub(crate) static CHILDREN: Mutex<Vec<u32>> = Mutex::new(Vec::new());

pub(crate) fn track(c: &std::process::Child) {
    CHILDREN.lock().unwrap_or_else(|e| e.into_inner()).push(c.id());
}

pub(crate) fn untrack(c: &std::process::Child) {
    CHILDREN.lock().unwrap_or_else(|e| e.into_inner()).retain(|&p| p != c.id());
}

/// Called from the termination handler: stop every helper still running.
#[cfg(target_os = "linux")]
pub(crate) fn kill_tracked_children() {
    let pids = std::mem::take(&mut *CHILDREN.lock().unwrap_or_else(|e| e.into_inner()));
    for p in &pids {
        // SAFETY: plain signal to a child we started.
        unsafe { libc::kill(*p as i32, libc::SIGKILL) };
    }
    if !pids.is_empty() {
        thread::sleep(Duration::from_millis(300)); // let the compositor see the capture go away
    }
}

pub(crate) fn kill_all(ff: &mut std::process::Child, recorder: &mut Option<std::process::Child>) {
    untrack(ff);
    if let Some(r) = recorder.as_ref() {
        untrack(r);
    }
    let _ = ff.kill();
    let _ = ff.wait();
    if let Some(r) = recorder.as_mut() {
        let _ = r.kill();
        let _ = r.wait();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ddagrab_captures_a_whole_output_or_a_region_of_it() {
        let out = DxgiOutput { adapter: 0, output: 1, rect: (1920, 0, 2560, 1600) };
        let whole = windows_capture_args((1920, 0), 2560, 1600, 60, Some(out)).join(" ");
        assert_eq!(whole, "-filter_complex ddagrab=output_idx=1:framerate=60:draw_mouse=0,hwdownload,format=bgra");
        let part = windows_capture_args((2020, 50), 800, 600, 30, Some(DxgiOutput { adapter: 2, ..out })).join(" ");
        assert!(part.starts_with("-init_hw_device d3d11va:2 -filter_complex"), "{part}");
        assert!(part.contains(":video_size=800x600:offset_x=100:offset_y=50"), "{part}");
    }

    #[test]
    fn gdigrab_is_the_fallback() {
        let v = windows_capture_args((10, 20), 640, 480, 30, None).join(" ");
        assert!(
            v.contains("-f gdigrab") && v.contains("-offset_x 10 -offset_y 20") && v.ends_with("-i desktop"),
            "{v}"
        );
        assert!(v.starts_with("-fflags nobuffer"), "{v}");
    }
}
