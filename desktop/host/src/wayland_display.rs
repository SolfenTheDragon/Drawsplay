//! Wayland hosts: a virtual output made through the compositor, and screen capture with
//! `wf-recorder` (wlroots compositors: sway, Hyprland, river, labwc...).
//!
//! Virtual outputs: sway (`swaymsg create_output`, only on its headless/nested backends) and
//! Hyprland (`hyprctl output create headless`). GNOME and KDE need the xdg-desktop-portal
//! ScreenCast API and are not supported yet; there the host reports why and mirrors nothing.

use std::process::{Child, Command, Stdio};

#[derive(Debug, Clone, PartialEq)]
pub struct Out {
    pub name: String,
    pub w: u32,
    pub h: u32,
    pub x: i32,
    pub y: i32,
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Compositor {
    Sway,
    Hyprland,
    Other,
}

/// True in a Wayland session (the host then must not use the X11 paths, which only reach
/// XWayland).
pub fn active() -> bool {
    std::env::var_os("WAYLAND_DISPLAY").is_some()
        && std::env::var("XDG_SESSION_TYPE").map(|t| t != "x11" && t != "tty").unwrap_or(true)
}

pub fn compositor() -> Compositor {
    if std::env::var_os("SWAYSOCK").is_some() {
        Compositor::Sway
    } else if std::env::var_os("HYPRLAND_INSTANCE_SIGNATURE").is_some() {
        Compositor::Hyprland
    } else {
        Compositor::Other
    }
}

fn run(cmd: &str, args: &[&str]) -> Result<String, String> {
    let out = Command::new(cmd).args(args).output().map_err(|e| format!("cannot run {cmd}: {e}"))?;
    if !out.status.success() {
        return Err(format!("{cmd} {}: {}", args.join(" "), String::from_utf8_lossy(&out.stderr).trim()));
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

/// Parse `swaymsg -t get_outputs -r`. Inactive outputs (size 0) are skipped.
pub fn parse_sway(json: &str) -> Vec<Out> {
    let Ok(serde_json::Value::Array(list)) = serde_json::from_str::<serde_json::Value>(json) else {
        return Vec::new();
    };
    list.iter()
        .filter_map(|o| {
            let r = o.get("rect")?;
            let n = |k: &str| r.get(k)?.as_i64();
            let (w, h) = (n("width")?, n("height")?);
            if w <= 0 || h <= 0 || o.get("active").and_then(|a| a.as_bool()) == Some(false) {
                return None;
            }
            Some(Out {
                name: o.get("name")?.as_str()?.to_string(),
                w: w as u32,
                h: h as u32,
                x: n("x")? as i32,
                y: n("y")? as i32,
            })
        })
        .collect()
}

/// Parse `hyprctl monitors -j`.
pub fn parse_hyprland(json: &str) -> Vec<Out> {
    let Ok(serde_json::Value::Array(list)) = serde_json::from_str::<serde_json::Value>(json) else {
        return Vec::new();
    };
    list.iter()
        .filter_map(|o| {
            let n = |k: &str| o.get(k)?.as_i64();
            Some(Out {
                name: o.get("name")?.as_str()?.to_string(),
                w: n("width")? as u32,
                h: n("height")? as u32,
                x: n("x")? as i32,
                y: n("y")? as i32,
            })
        })
        .collect()
}

pub fn outputs() -> Result<Vec<Out>, String> {
    match compositor() {
        Compositor::Sway => Ok(parse_sway(&run("swaymsg", &["-t", "get_outputs", "-r"])?)),
        Compositor::Hyprland => Ok(parse_hyprland(&run("hyprctl", &["monitors", "-j"])?)),
        Compositor::Other => Err(unsupported()),
    }
}

fn unsupported() -> String {
    "this Wayland compositor is not supported yet (sway and Hyprland are; GNOME and KDE need the \
     ScreenCast portal)"
        .into()
}

/// Bounding box of the layout: (min x, min y, width, height).
pub fn bounds(outs: &[Out]) -> (i32, i32, u32, u32) {
    let min_x = outs.iter().map(|o| o.x).min().unwrap_or(0);
    let min_y = outs.iter().map(|o| o.y).min().unwrap_or(0);
    let max_x = outs.iter().map(|o| o.x + o.w as i32).max().unwrap_or(0);
    let max_y = outs.iter().map(|o| o.y + o.h as i32).max().unwrap_or(0);
    (min_x, min_y, (max_x - min_x).max(0) as u32, (max_y - min_y).max(0) as u32)
}

/// The output to mirror: the first one (sway and Hyprland list the focused/primary one first
/// only by accident, so take the one at the layout origin when there is one).
pub fn primary(outs: &[Out]) -> Option<&Out> {
    let (bx, by, _, _) = bounds(outs);
    outs.iter().find(|o| (o.x, o.y) == (bx, by)).or(outs.first())
}

/// The headless output this process created, so a signal handler can remove it.
static ACTIVE: std::sync::Mutex<Option<(Compositor, String)>> = std::sync::Mutex::new(None);

/// "sway", "hyprland" or "other" for the running session.
pub fn kind_of_session() -> &'static str {
    kind_name(compositor())
}

fn kind_name(c: Compositor) -> &'static str {
    match c {
        Compositor::Sway => "sway",
        Compositor::Hyprland => "hyprland",
        Compositor::Other => "other",
    }
}

fn outputs_without(name: &str) -> bool {
    outputs().map(|o| !o.iter().any(|x| x.name == name)).unwrap_or(false)
}

fn remove_output(comp: Compositor, name: &str) -> bool {
    for _ in 0..4 {
        let _ = match comp {
            Compositor::Sway => run("swaymsg", &["output", name, "unplug"]),
            Compositor::Hyprland => run("hyprctl", &["output", "remove", name]),
            Compositor::Other => return true,
        };
        if outputs_without(name) {
            return true;
        }
        std::thread::sleep(std::time::Duration::from_millis(250));
    }
    false
}

/// Remove an output left by a previous host (`kind` as stored in the active record). Only possible
/// from a session of the same compositor.
pub fn release_named(kind: &str, name: &str) -> bool {
    let comp = match kind {
        "sway" => Compositor::Sway,
        "hyprland" => Compositor::Hyprland,
        _ => return true,
    };
    if compositor() != comp {
        return false; // not in that compositor's session: leave the record for later
    }
    remove_output(comp, name)
}

/// Remove the output this process created, if any. Idempotent; used by Drop and the signal handler.
pub fn cleanup_active() {
    let taken = ACTIVE.lock().ok().and_then(|mut a| a.take());
    if let Some((comp, name)) = taken {
        if remove_output(comp, &name) {
            crate::active_record::clear();
        } else {
            eprintln!("WARNING: could not remove virtual output {name}; it will be removed at the next start");
        }
    }
}

/// A virtual output that is removed again when dropped.
pub struct VirtualOutput {
    pub out: Out,
}

impl VirtualOutput {
    pub fn create(w: u32, h: u32, hz: u32) -> Result<VirtualOutput, String> {
        let comp = compositor();
        let before = outputs()?;
        let (bx, _, bw, _) = bounds(&before);
        let right = bx + bw as i32;
        let known = |name: &str, list: &[Out]| list.iter().any(|o| o.name == name);
        let name = match comp {
            Compositor::Sway => {
                run("swaymsg", &["create_output"])
                    .map_err(|e| format!("{e} (sway can only create outputs on its headless or nested backends)"))?;
                // The new output is inactive until configured, so look at all of them.
                let all = run("swaymsg", &["-t", "get_outputs", "-r"])?;
                let v: serde_json::Value = serde_json::from_str(&all).map_err(|e| e.to_string())?;
                v.as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|o| o.get("name")?.as_str().map(str::to_string))
                    .find(|n| n.starts_with("HEADLESS") && !known(n, &before))
                    .ok_or("sway did not report the new output")?
            }
            Compositor::Hyprland => {
                run("hyprctl", &["output", "create", "headless"])?;
                outputs()?
                    .into_iter()
                    .map(|o| o.name)
                    .find(|n| n.starts_with("HEADLESS") && !known(n, &before))
                    .ok_or("Hyprland did not report the new output")?
            }
            Compositor::Other => return Err(unsupported()),
        };
        // Record the new output before configuring it: from here on a crash is recoverable.
        crate::active_record::write(kind_name(comp), &name, "");
        *ACTIVE.lock().unwrap() = Some((comp, name.clone()));
        let mut me = VirtualOutput { out: Out { name: name.clone(), w, h, x: right, y: 0 } };
        let configured = match comp {
            Compositor::Sway => run(
                "swaymsg",
                &["output", &name, "mode", &format!("{w}x{h}@{hz}Hz"), "position", &right.to_string(), "0", "enable"],
            ),
            _ => run("hyprctl", &["keyword", "monitor", &format!("{name},{w}x{h}@{hz},{right}x0,1")]),
        };
        configured?; // on error `me` drops here and removes the output again
                     // Read back what the compositor actually did.
        if let Some(o) = outputs()?.into_iter().find(|o| o.name == name) {
            me.out = o;
        }
        Ok(me)
    }
}

impl Drop for VirtualOutput {
    fn drop(&mut self) {
        cleanup_active();
    }
}

/// Start `wf-recorder` writing raw frames of one output to its stdout (a `nut` stream that the
/// encoder ffmpeg reads from stdin).
pub fn spawn_capture(output: &str, fps: u32) -> Result<Child, String> {
    Command::new("wf-recorder")
        .args(["-o", output, "-y", "-D", "--no-dmabuf", "-c", "rawvideo", "-x", "bgr0", "-r", &fps.to_string()])
        .args(["-m", "nut", "-f", "/dev/stdout"])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| format!("cannot start wf-recorder ({e}); install it to capture on Wayland"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_sway_outputs() {
        let j = r#"[{"name":"eDP-1","active":true,"rect":{"x":0,"y":0,"width":1920,"height":1080}},
                    {"name":"HEADLESS-2","active":false,"rect":{"x":0,"y":0,"width":0,"height":0}},
                    {"name":"HDMI-A-1","active":true,"rect":{"x":1920,"y":-100,"width":1280,"height":800}}]"#;
        let v = parse_sway(j);
        assert_eq!(v.len(), 2);
        assert_eq!(v[1], Out { name: "HDMI-A-1".into(), w: 1280, h: 800, x: 1920, y: -100 });
        assert_eq!(bounds(&v), (0, -100, 3200, 1180));
        assert_eq!(primary(&v).unwrap().name, "eDP-1");
    }

    #[test]
    fn parses_hyprland_monitors_and_survives_garbage() {
        let j = r#"[{"name":"DP-1","width":2560,"height":1440,"x":0,"y":0}]"#;
        assert_eq!(parse_hyprland(j)[0].w, 2560);
        assert!(parse_sway("not json").is_empty());
        assert!(parse_hyprland("{}").is_empty());
    }
}
