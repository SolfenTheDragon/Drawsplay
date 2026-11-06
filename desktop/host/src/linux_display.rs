//! Linux (X11) virtual display through RandR: lights up a spare output as an extra monitor.
//!
//! Candidates, in order: outputs named `VIRTUAL*` (Intel/modesetting virtual CRTCs, the dummy
//! driver), then any output that is currently disconnected. Many GPU drivers refuse to enable
//! a disconnected output without a monitor attached; in that case [`VirtualOutput::create`]
//! returns an error and the host mirrors the screen instead. Workarounds are an HDMI dummy plug,
//! the `vkms` kernel module, or forcing a connector with the `video=` kernel parameter.
//!
//! Wayland sessions are not supported yet.

use std::process::Command;

#[derive(Debug, PartialEq)]
pub struct Output {
    pub name: String,
    pub connected: bool,
    /// Marked `primary` by xrandr.
    pub primary: bool,
    /// (width, height, x, y) when the output is currently active.
    pub geometry: Option<(u32, u32, i32, i32)>,
}

#[derive(Debug, PartialEq)]
pub struct Query {
    pub outputs: Vec<Output>,
    /// Current size of the whole X screen.
    pub screen: (u32, u32),
}

/// Parse `xrandr --query`.
pub fn parse_query(text: &str) -> Query {
    let mut outputs = Vec::new();
    let mut screen = (0, 0);
    for line in text.lines() {
        if line.starts_with("Screen ") {
            if let Some(rest) = line.split("current ").nth(1) {
                let mut it = rest.split([',', 'x']).map(|p| p.trim().parse::<u32>());
                if let (Some(Ok(w)), Some(Ok(h))) = (it.next(), it.next()) {
                    screen = (w, h);
                }
            }
            continue;
        }
        if line.starts_with(' ') || line.starts_with('\t') {
            continue; // mode list
        }
        let mut words = line.split_whitespace();
        let (Some(name), Some(state)) = (words.next(), words.next()) else { continue };
        if state != "connected" && state != "disconnected" {
            continue;
        }
        // "... [primary] 1920x1080+0+0 (normal ..." : the first WxH+X+Y token is the active geometry.
        let geometry = line.split_whitespace().skip(2).find_map(parse_geometry);
        let primary = line.split_whitespace().skip(2).take(2).any(|w| w == "primary");
        outputs.push(Output { name: name.to_string(), connected: state == "connected", primary, geometry });
    }
    Query { outputs, screen }
}

fn parse_geometry(tok: &str) -> Option<(u32, u32, i32, i32)> {
    let (size, pos) = tok.split_once('+').map(|(s, p)| (s, format!("+{p}")))?;
    let (w, h) = size.split_once('x')?;
    let (w, h) = (w.parse().ok()?, h.parse().ok()?);
    // pos is "+X+Y" (a negative offset shows as "+-5" in some versions, "-5" in others).
    let nums: Vec<i32> =
        pos.replace("+-", "-").split(['+']).filter(|s| !s.is_empty()).filter_map(|s| s.parse().ok()).collect();
    if nums.len() == 2 {
        Some((w, h, nums[0], nums[1]))
    } else {
        None
    }
}

/// Outputs that could become the virtual monitor, best first: `VIRTUAL*` outputs, then any other
/// unused disconnected port. The caller tries them in turn, since drivers refuse some of them.
pub fn candidate_outputs(q: &Query) -> Vec<&Output> {
    let unused = |o: &&Output| o.geometry.is_none();
    let is_virtual = |o: &&Output| o.name.to_ascii_uppercase().starts_with("VIRTUAL");
    let mut v: Vec<&Output> = q.outputs.iter().filter(unused).filter(is_virtual).collect();
    v.extend(q.outputs.iter().filter(unused).filter(|o| !is_virtual(o) && !o.connected));
    v
}

/// A CVT reduced-blanking (v1) mode: everything `xrandr --newmode` needs.
#[derive(Debug, PartialEq)]
pub struct Modeline {
    pub name: String,
    pub clock_mhz: f64,
    pub h: [u32; 4], // display, sync start, sync end, total
    pub v: [u32; 4],
}

pub fn cvt_reduced(width: u32, height: u32, hz: u32) -> Modeline {
    const RB_MIN_V_BLANK_US: f64 = 460.0;
    const RB_V_FPORCH: u32 = 3;
    const MIN_V_BPORCH: u32 = 6;
    const RB_H_BLANK: u32 = 160;
    const RB_H_SYNC: u32 = 32;
    const CLOCK_STEP: f64 = 0.25;

    let w = (width / 8) * 8;
    let (wl, hl) = (w as u64, height as u64);
    // Vertical sync width depends on the aspect ratio (CVT table).
    let vsync = if wl * 3 == hl * 4 {
        4
    } else if wl * 9 == hl * 16 {
        5
    } else if wl * 10 == hl * 16 {
        6
    } else if wl * 4 == hl * 5 || wl * 9 == hl * 15 {
        7
    } else {
        10
    };
    let hz = hz.max(1) as f64;
    let h_period = ((1_000_000.0 / hz) - RB_MIN_V_BLANK_US) / height as f64;
    let vbi_lines = (RB_MIN_V_BLANK_US / h_period).floor() as u32 + 1;
    let min_vbi = RB_V_FPORCH + vsync + MIN_V_BPORCH;
    let act_vbi = vbi_lines.max(min_vbi);
    let v_total = act_vbi + height;
    let h_total = w + RB_H_BLANK;
    let clock = ((hz * v_total as f64 * h_total as f64 / 1_000_000.0) / CLOCK_STEP).floor() * CLOCK_STEP;

    // Reduced blanking splits the 160 px as 48 front porch, 32 sync, 80 back porch.
    let h_sync_start = w + 48;
    Modeline {
        name: format!("dd_{width}x{height}_{}", hz as u32),
        clock_mhz: clock,
        h: [w, h_sync_start, h_sync_start + RB_H_SYNC, h_total],
        v: [height, height + RB_V_FPORCH, height + RB_V_FPORCH + vsync, v_total],
    }
}

impl Modeline {
    fn xrandr_args(&self) -> Vec<String> {
        let mut a = vec!["--newmode".to_string(), self.name.clone(), format!("{:.2}", self.clock_mhz)];
        a.extend(self.h.iter().map(|n| n.to_string()));
        a.extend(self.v.iter().map(|n| n.to_string()));
        a.push("+HSync".into());
        a.push("-VSync".into());
        a
    }
}

fn xrandr(args: &[String]) -> Result<String, String> {
    let out = Command::new("xrandr").args(args).output().map_err(|e| format!("cannot run xrandr: {e}"))?;
    if out.status.success() {
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    } else {
        Err(String::from_utf8_lossy(&out.stderr).trim().to_string())
    }
}

pub fn query() -> Result<Query, String> {
    Ok(parse_query(&xrandr(&["--query".to_string()])?))
}

/// Size of the current X screen (for mirroring and for scaling input). `None` without X11.
pub fn screen_size() -> Option<(u32, u32)> {
    query().ok().map(|q| q.screen).filter(|s| s.0 > 0 && s.1 > 0)
}

/// (size, origin, whole-screen size) of the area to mirror.
pub type MirrorArea = ((u32, u32), (i32, i32), (u32, u32));

/// The monitor to mirror: its (width, height), its (x, y) on the X screen, and the whole screen
/// size. The output marked primary, else the first active one.
pub fn primary_area() -> Option<MirrorArea> {
    let q = query().ok()?;
    pick_primary(&q).map(|(w, h, x, y)| ((w, h), (x, y), q.screen))
}

fn pick_primary(q: &Query) -> Option<(u32, u32, i32, i32)> {
    let active = |o: &&Output| o.connected && o.geometry.is_some();
    q.outputs.iter().filter(active).find(|o| o.primary).or_else(|| q.outputs.iter().find(active))?.geometry
}

pub struct VirtualOutput {
    /// (x, y, width, height) of the output on the X screen.
    pub rect: (i32, i32, u32, u32),
    pub desktop: (u32, u32),
    /// RandR output and mode this session created; released when it is dropped.
    name: String,
    mode: String,
}

impl VirtualOutput {
    pub fn create(w: u32, h: u32, hz: u32) -> Result<VirtualOutput, String> {
        if std::env::var_os("DISPLAY").is_none() {
            return Err("no X11 display (Wayland sessions are not supported yet)".into());
        }
        let q = query()?;
        // The Android DrawSplay host (or another dd-host) may be using a spare output right now.
        let candidates: Vec<&Output> =
            candidate_outputs(&q).into_iter().filter(|o| !crate::display_claims::claimed_by_other(&o.name)).collect();
        if candidates.is_empty() {
            return Err("no spare video output to use (all outputs are in use)".into());
        }
        let ml = cvt_reduced(w, h, hz);
        // The mode may already exist from an earlier run: that is fine.
        let _ = xrandr(&ml.xrandr_args());

        let mut failures = Vec::new();
        for out in candidates {
            match Self::enable(&q, &out.name, &ml) {
                Ok(v) => return Ok(v),
                Err(e) => failures.push(e),
            }
        }
        // Nothing took the mode: drop it again (fails harmlessly if another session uses the same one).
        let _ = xrandr(&["--rmmode".into(), ml.name.clone()]);
        Err(failures.join("; "))
    }

    /// Try to light up `name` with the mode `ml`, right of the current screen.
    fn enable(q: &Query, name: &str, ml: &Modeline) -> Result<VirtualOutput, String> {
        let name = name.to_string();
        xrandr(&["--addmode".into(), name.clone(), ml.name.clone()])
            .map_err(|e| format!("{name} refused the {} mode: {e}", ml.name))?;
        let right_edge = q.screen.0;
        let r = xrandr(&[
            "--output".into(),
            name.clone(),
            "--mode".into(),
            ml.name.clone(),
            "--pos".into(),
            format!("{right_edge}x0"),
        ]);
        if let Err(e) = r {
            let _ = xrandr(&["--delmode".into(), name.clone(), ml.name.clone()]);
            return Err(format!("could not enable {name} (the GPU driver may need a monitor attached): {e}"));
        }
        let after = query()?;
        let Some(geo) = after.outputs.iter().find(|o| o.name == name).and_then(|o| o.geometry) else {
            let _ = xrandr(&["--output".into(), name.clone(), "--off".into()]);
            let _ = xrandr(&["--delmode".into(), name.clone(), ml.name.clone()]);
            return Err(format!("{name} did not become active"));
        };
        // Record it first: if this process dies from here on, the next start removes the monitor.
        crate::active_record::write("x11", &name, &ml.name);
        ACTIVE.lock().unwrap().push((name.clone(), ml.name.clone()));
        Ok(VirtualOutput { rect: (geo.2, geo.3, geo.0, geo.1), desktop: after.screen, name, mode: ml.name.clone() })
    }
}

/// The outputs currently enabled by this process (output, mode), so a signal handler can switch them off.
static ACTIVE: std::sync::Mutex<Vec<(String, String)>> = std::sync::Mutex::new(Vec::new());

/// Is `name` currently without a mode (switched off) on the X screen?
fn output_is_off(name: &str) -> Option<bool> {
    query().ok().map(|q| q.outputs.iter().find(|o| o.name == name).is_none_or(|o| o.geometry.is_none()))
}

/// Switch `output` off and drop its mode, then check it really is off, retrying a few times (the
/// X server or driver sometimes refuses the first request). Returns true once it is verifiably off.
pub fn release_output(output: &str, mode: &str) -> bool {
    for attempt in 0..4 {
        let _ = xrandr(&["--output".into(), output.to_string(), "--off".into()]);
        if !mode.is_empty() {
            let _ = xrandr(&["--delmode".into(), output.to_string(), mode.to_string()]);
            let _ = xrandr(&["--rmmode".into(), mode.to_string()]);
        }
        match output_is_off(output) {
            Some(true) => return true,
            None => return false, // cannot even query the X server
            Some(false) => {
                eprintln!("virtual output {output} is still on (attempt {}); retrying", attempt + 1);
                std::thread::sleep(std::time::Duration::from_millis(250));
            }
        }
    }
    false
}

/// Switch one of our outputs off and forget it; on failure the record stays so the next start retries.
fn release_active(output: &str, mode: &str) {
    ACTIVE.lock().unwrap_or_else(|e| e.into_inner()).retain(|(o, _)| o != output);
    if release_output(output, mode) {
        crate::active_record::clear(output);
    } else {
        eprintln!("WARNING: could not switch virtual output {output} off; it will be removed at the next start");
    }
}

/// Switch every virtual output of this process off. Safe to call more than once; used by the
/// Ctrl+C / SIGTERM handler, because `Drop` does not run when the process is signalled.
pub fn cleanup_active() {
    let taken = std::mem::take(&mut *ACTIVE.lock().unwrap_or_else(|e| e.into_inner()));
    for (output, mode) in taken {
        release_active(&output, &mode);
    }
}

impl Drop for VirtualOutput {
    /// Only this session's output goes; other sessions keep theirs.
    fn drop(&mut self) {
        release_active(&self.name, &self.mode);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const SAMPLE: &str = "Screen 0: minimum 320 x 200, current 3200 x 1080, maximum 16384 x 16384
eDP-1 connected primary 1920x1080+0+0 (normal left inverted right x axis y axis) 344mm x 193mm
   1920x1080     60.01*+  59.97
HDMI-1 connected 1280x800+1920+0 (normal left inverted right x axis y axis) 0mm x 0mm
   1280x800      60.00*
HDMI-2 disconnected (normal left inverted right x axis y axis)
DP-1 disconnected (normal left inverted right x axis y axis)
VIRTUAL1 disconnected (normal left inverted right x axis y axis)
";

    #[test]
    fn parses_outputs_and_screen() {
        let q = parse_query(SAMPLE);
        assert_eq!(q.screen, (3200, 1080));
        assert_eq!(q.outputs.len(), 5);
        assert_eq!(
            q.outputs[0],
            Output { name: "eDP-1".into(), connected: true, primary: true, geometry: Some((1920, 1080, 0, 0)) }
        );
        assert_eq!(q.outputs[1].geometry, Some((1280, 800, 1920, 0)));
        assert!(!q.outputs[2].connected && q.outputs[2].geometry.is_none());
    }

    #[test]
    fn prefers_virtual_then_any_disconnected() {
        let q = parse_query(SAMPLE);
        let names = |q: &Query| candidate_outputs(q).iter().map(|o| o.name.clone()).collect::<Vec<_>>();
        // Every usable port is offered, best first, so a refusal moves on to the next one.
        assert_eq!(names(&q), ["VIRTUAL1", "HDMI-2", "DP-1"]);
        let q = parse_query(&SAMPLE.replace("VIRTUAL1", "DP-2"));
        assert_eq!(names(&q), ["HDMI-2", "DP-1", "DP-2"]);
        let q = parse_query("Screen 0: minimum 8 x 8, current 100 x 100, maximum 100 x 100\neDP-1 connected primary 100x100+0+0 (normal)\n");
        assert!(candidate_outputs(&q).is_empty());
    }

    #[test]
    fn mirrors_the_primary_monitor_not_the_whole_screen() {
        let q = parse_query(SAMPLE);
        assert_eq!(pick_primary(&q), Some((1920, 1080, 0, 0)));
        // Primary on the right-hand monitor.
        let moved = SAMPLE
            .replace("eDP-1 connected primary", "eDP-1 connected")
            .replace("HDMI-1 connected", "HDMI-1 connected primary");
        assert_eq!(pick_primary(&parse_query(&moved)), Some((1280, 800, 1920, 0)));
        // Nothing marked primary: first active output.
        let none = SAMPLE.replace(" primary", "");
        assert_eq!(pick_primary(&parse_query(&none)), Some((1920, 1080, 0, 0)));
    }

    #[test]
    fn negative_offsets_parse() {
        assert_eq!(parse_geometry("1920x1080+-1920+0"), Some((1920, 1080, -1920, 0)));
        assert_eq!(parse_geometry("1920x1080+0+0"), Some((1920, 1080, 0, 0)));
        assert_eq!(parse_geometry("(normal"), None);
    }

    #[test]
    fn cvt_reduced_blanking_matches_the_standard() {
        // VESA CVT-RB v1 for 1920x1080@60: clock 138.50 MHz, 2080 x 1111 total.
        let m = cvt_reduced(1920, 1080, 60);
        assert_eq!(m.clock_mhz, 138.5);
        assert_eq!(m.h, [1920, 1968, 2000, 2080]);
        assert_eq!(m.v, [1080, 1083, 1088, 1111]);
        // 1280x720@60: 64.0 MHz, 1440 x 741.
        let m = cvt_reduced(1280, 720, 60);
        assert_eq!(m.clock_mhz, 64.0);
        assert_eq!((m.h[3], m.v[3]), (1440, 741));
    }
}
