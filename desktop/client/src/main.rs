//! dd-client: connects to a dd-host, shows its video in a window (or fullscreen) and sends
//! mouse, keyboard, touch and pen input back.

mod keys;
mod session;

use dd_proto::{Message, PointerPhase, PointerSource, DEFAULT_PORT};
use session::{FrameSlot, Settings, UiEvent};
use std::collections::HashSet;
use std::num::NonZeroU32;
use std::rc::Rc;
use std::sync::mpsc::{channel, Sender};
use std::sync::Arc;
use std::time::{Duration, Instant};
use winit::application::ApplicationHandler;
use winit::dpi::{PhysicalPosition, PhysicalSize};
use winit::event::{ElementState, Force, MouseButton, MouseScrollDelta, TouchPhase, WindowEvent};
use winit::event_loop::{ActiveEventLoop, ControlFlow, EventLoop};
use winit::keyboard::PhysicalKey;
use winit::window::{Fullscreen, Window, WindowId};

struct Args {
    host: String,
    width: u32,
    height: u32,
    fps: u32,
    ffmpeg: String,
    fullscreen: bool,
    trust_all: bool,
    hwdec: bool,
    fit: bool,
    size_given: bool,
    show_key: bool,
    trust_key: Option<String>,
    list_trusted: bool,
    forget: Option<String>,
}

fn parse_args() -> Result<Args, String> {
    let mut a = Args {
        host: String::new(),
        width: 1280,
        height: 720,
        fps: 60,
        ffmpeg: "ffmpeg".into(),
        fullscreen: false,
        trust_all: false,
        hwdec: true,
        fit: false,
        size_given: false,
        show_key: false,
        trust_key: None,
        list_trusted: false,
        forget: None,
    };
    let mut it = std::env::args().skip(1);
    while let Some(k) = it.next() {
        match k.as_str() {
            "--size" => {
                let v = it.next().ok_or("--size WxH")?;
                let (w, h) = v.split_once('x').ok_or("--size WxH")?;
                a.width = w.parse().map_err(|e| format!("{e}"))?;
                a.height = h.parse().map_err(|e| format!("{e}"))?;
                if !dd_proto::video_size_ok(a.width, a.height) {
                    return Err("--size out of range".into());
                }
                a.size_given = true;
            }
            "--fps" => a.fps = it.next().ok_or("--fps N")?.parse().map_err(|e| format!("{e}"))?,
            "--ffmpeg" => a.ffmpeg = it.next().ok_or("--ffmpeg PATH")?,
            "--fullscreen" => a.fullscreen = true,
            "--trust-all" => a.trust_all = true,
            "--no-hwdec" => a.hwdec = false,
            "--fit" => a.fit = true,
            "--show-key" => a.show_key = true,
            "--list-trusted" => a.list_trusted = true,
            "--forget" => a.forget = Some(it.next().ok_or("--forget KEY_PREFIX")?),
            "--trust" => a.trust_key = Some(it.next().ok_or("--trust HOST_KEY")?),
            "-h" | "--help" => {
                println!(
                    "dd-client HOST[:PORT] [--size WxH] [--fps N] [--fullscreen] [--no-hwdec] [--fit]\n          [--trust-all] [--ffmpeg PATH]\n\n--fullscreen uses this screen's native size unless --size is given.\n--fit scales the picture to fill the window (keeps the aspect ratio); otherwise it is shown 1:1.\n--trust-all skips the pairing-code check (testing only).\nCtrl+Alt+Shift+Q closes the client."
                );
                std::process::exit(0);
            }
            o if !o.starts_with('-') && a.host.is_empty() => a.host = o.to_string(),
            o => return Err(format!("unknown argument {o}")),
        }
    }
    if a.host.is_empty() && !a.show_key && a.trust_key.is_none() && !a.list_trusted && a.forget.is_none() {
        return Err("missing HOST (try --help)".into());
    }
    a.host = with_default_port(&a.host);
    Ok(a)
}

/// `host`, `1.2.3.4`, `::1` and `[::1]` get the default port; anything that already has one is kept.
fn with_default_port(h: &str) -> String {
    use std::net::{IpAddr, SocketAddr};
    if h.is_empty() || h.parse::<SocketAddr>().is_ok() {
        return h.to_string();
    }
    if let Ok(ip) = h.parse::<IpAddr>() {
        return SocketAddr::new(ip, DEFAULT_PORT).to_string();
    }
    if h.starts_with('[') && h.ends_with(']') || !h.contains(':') {
        return format!("{h}:{DEFAULT_PORT}");
    }
    h.to_string() // name:port
}

fn main() {
    let args = match parse_args() {
        Ok(a) => a,
        Err(e) => {
            eprintln!("error: {e}");
            std::process::exit(2);
        }
    };
    match dd_proto::identity::key_commands(
        "client",
        "trusted_hosts.txt",
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
    let event_loop = EventLoop::<UiEvent>::with_user_event().build().expect("cannot create event loop");
    event_loop.set_control_flow(ControlFlow::Wait);
    let mut app = App::new(args, event_loop.create_proxy());
    if let Err(e) = event_loop.run_app(&mut app) {
        eprintln!("error: {e}");
        std::process::exit(1);
    }
    std::process::exit(app.exit_code);
}

struct Gfx {
    window: Rc<Window>,
    surface: softbuffer::Surface<Rc<Window>, Rc<Window>>,
}

struct App {
    args: Args,
    proxy: winit::event_loop::EventLoopProxy<UiEvent>,
    gfx: Option<Gfx>,
    slot: Arc<FrameSlot>,
    tx: Option<Sender<Message>>,
    /// Video size once connected.
    video: Option<(u32, u32)>,
    last: Vec<u32>,
    need_clear: bool,
    exit_code: i32,
    // input state
    buttons: u8,
    pos: PhysicalPosition<f64>,
    pressed: HashSet<u16>,
    /// Touch/pen contacts that went down on the video and have not lifted yet.
    touches: HashSet<u64>,
    /// Last normalized position of each held touch, so a forced lift happens where the finger was.
    touch_pos: std::collections::HashMap<u64, (f32, f32)>,
    // stats
    shown: u32,
    stats_at: Instant,
    rtt_ms: f32,
}

impl App {
    fn new(args: Args, proxy: winit::event_loop::EventLoopProxy<UiEvent>) -> App {
        App {
            args,
            proxy,
            gfx: None,
            slot: Arc::new(FrameSlot::default()),
            tx: None,
            video: None,
            last: Vec::new(),
            need_clear: true,
            exit_code: 0,
            buttons: 0,
            pos: PhysicalPosition::new(0.0, 0.0),
            pressed: HashSet::new(),
            touches: HashSet::new(),
            touch_pos: std::collections::HashMap::new(),
            shown: 0,
            stats_at: Instant::now(),
            rtt_ms: 0.0,
        }
    }

    fn send(&self, m: Message) {
        if let Some(tx) = &self.tx {
            let _ = tx.send(m);
        }
    }

    /// Where the video sits inside the window: (x, y, w, h), centered; scaled only with `--fit`.
    fn video_rect(&self) -> Option<(i32, i32, u32, u32)> {
        let (vw, vh) = self.video?;
        let size = self.gfx.as_ref()?.window.inner_size();
        let (dw, dh) = if self.args.fit { fit_size((vw, vh), (size.width, size.height)) } else { (vw, vh) };
        Some(((size.width as i32 - dw as i32) / 2, (size.height as i32 - dh as i32) / 2, dw, dh))
    }

    /// Is the window position over the video itself (not the letterbox bars)?
    fn over_video(&self, p: PhysicalPosition<f64>) -> bool {
        self.video_rect().is_some_and(|(x, y, w, h)| {
            p.x >= x as f64 && p.y >= y as f64 && p.x < (x + w as i32) as f64 && p.y < (y + h as i32) as f64
        })
    }

    /// Window position -> 0..1 over the video (clamped to its edges).
    fn normalize(&self, p: PhysicalPosition<f64>) -> Option<(f32, f32)> {
        let (x, y, w, h) = self.video_rect()?;
        let nx = ((p.x - x as f64) / (w.max(2) - 1) as f64).clamp(0.0, 1.0) as f32;
        let ny = ((p.y - y as f64) / (h.max(2) - 1) as f64).clamp(0.0, 1.0) as f32;
        Some((nx, ny))
    }

    fn pointer(
        &self,
        id: u8,
        source: PointerSource,
        phase: PointerPhase,
        p: PhysicalPosition<f64>,
        pressure: f32,
        buttons: u8,
    ) {
        if let Some((x, y)) = self.normalize(p) {
            self.send(Message::Pointer {
                id,
                source,
                phase,
                x,
                y,
                pressure,
                buttons,
                tilt_x: 0,
                tilt_y: 0,
                wheel_x: 0.0,
                wheel_y: 0.0,
            });
        }
    }

    fn release_everything(&mut self) {
        for code in self.pressed.drain().collect::<Vec<_>>() {
            self.send(Message::Key { code, down: false });
        }
        for id in self.touches.drain().collect::<Vec<_>>() {
            // Lift where the finger last was: some hosts (Windows) act on the position of the Up.
            let (x, y) = self.touch_pos.remove(&id).unwrap_or((0.0, 0.0));
            self.send(Message::Pointer {
                id: (id & 0xFF) as u8,
                source: PointerSource::Touch,
                phase: PointerPhase::Up,
                x,
                y,
                pressure: 0.0,
                buttons: 0,
                tilt_x: 0,
                tilt_y: 0,
                wheel_x: 0.0,
                wheel_y: 0.0,
            });
        }
        if self.buttons != 0 {
            let p = self.pos;
            self.buttons = 0;
            self.pointer(0, PointerSource::Mouse, PointerPhase::Up, p, 0.0, 0);
        }
    }

    fn draw(&mut self) {
        let Some((vx, vy, vw, vh)) = self.video_rect() else { return };
        if let Some(f) = self.slot.take() {
            let old = std::mem::replace(&mut self.last, f);
            if !old.is_empty() {
                self.slot.recycle(old);
            }
            self.shown += 1;
        }
        let Some(g) = self.gfx.as_mut() else { return };
        let size = g.window.inner_size();
        let (Some(nw), Some(nh)) = (NonZeroU32::new(size.width), NonZeroU32::new(size.height)) else { return };
        if g.surface.resize(nw, nh).is_err() {
            return;
        }
        let Ok(mut buf) = g.surface.buffer_mut() else { return };
        let (ww, wh) = (size.width as i32, size.height as i32);
        if self.need_clear {
            buf.fill(0xFF00_0000);
            self.need_clear = false;
        } else if vx > 0 || vy > 0 || vx + vw as i32 != ww || vy + vh as i32 != wh {
            // Letterboxed: repaint only the bars, not the picture area.
            for y in 0..wh {
                let row = (y as usize) * ww as usize;
                if y < vy || y >= vy + vh as i32 {
                    buf[row..row + ww as usize].fill(0xFF00_0000);
                } else {
                    let (l, r) = (vx.clamp(0, ww) as usize, (vx + vw as i32).clamp(0, ww) as usize);
                    buf[row..row + l].fill(0xFF00_0000);
                    buf[row + r..row + ww as usize].fill(0xFF00_0000);
                }
            }
        }
        if let Some((sw, sh)) = self.video.filter(|&(sw, sh)| self.last.len() == (sw * sh) as usize) {
            blit(&self.last, (sw, sh), &mut buf, (size.width, size.height), (vx, vy, vw, vh));
        }
        let _ = buf.present();

        if self.stats_at.elapsed() >= Duration::from_secs(5) {
            eprintln!(
                "{:.1} fps shown, rtt {:.1} ms",
                self.shown as f32 / self.stats_at.elapsed().as_secs_f32(),
                self.rtt_ms
            );
            self.shown = 0;
            self.stats_at = Instant::now();
        }
    }
}

/// Largest size with the video's aspect ratio that fits the window.
fn fit_size((vw, vh): (u32, u32), (ww, wh): (u32, u32)) -> (u32, u32) {
    if vw == 0 || vh == 0 || ww == 0 || wh == 0 {
        return (vw, vh);
    }
    if (ww as u64) * (vh as u64) <= (wh as u64) * (vw as u64) {
        (ww, ((vh as u64 * ww as u64) / vw as u64).max(1) as u32)
    } else {
        (((vw as u64 * wh as u64) / vh as u64).max(1) as u32, wh)
    }
}

/// Copy `src` into the window buffer at `dst_rect` (x, y, w, h), clipped to the window. When the
/// rectangle is not the source size the picture is scaled (nearest neighbour).
fn blit(
    src: &[u32],
    (sw, sh): (u32, u32),
    dst: &mut [u32],
    (ww, wh): (u32, u32),
    (dx, dy, dw, dh): (i32, i32, u32, u32),
) {
    let (x0, x1) = (dx.max(0), (dx + dw as i32).min(ww as i32));
    if x1 <= x0 {
        return;
    }
    let xmap: Option<Vec<u32>> =
        (dw != sw).then(|| (x0..x1).map(|x| ((x - dx) as u64 * sw as u64 / dw as u64) as u32).collect());
    for y in dy.max(0)..(dy + dh as i32).min(wh as i32) {
        let sy = if dh == sh { (y - dy) as u32 } else { ((y - dy) as u64 * sh as u64 / dh as u64) as u32 };
        let srow = &src[(sy * sw) as usize..((sy + 1) * sw) as usize];
        let d = (y as usize) * ww as usize;
        let out = &mut dst[d + x0 as usize..d + x1 as usize];
        match &xmap {
            None => out.copy_from_slice(&srow[(x0 - dx) as usize..(x1 - dx) as usize]),
            Some(m) => out.iter_mut().zip(m).for_each(|(o, &sx)| *o = srow[sx as usize]),
        }
    }
}

impl ApplicationHandler<UiEvent> for App {
    fn resumed(&mut self, el: &ActiveEventLoop) {
        if self.gfx.is_some() {
            return;
        }
        let mut attrs = Window::default_attributes().with_title("DrawSplay Desktop");
        if self.args.size_given || !self.args.fullscreen {
            attrs = attrs.with_inner_size(PhysicalSize::new(self.args.width, self.args.height));
        }
        if self.args.fullscreen {
            attrs = attrs.with_fullscreen(Some(Fullscreen::Borderless(None)));
        }
        let window = Rc::new(el.create_window(attrs).expect("cannot create window"));
        let ctx = softbuffer::Context::new(window.clone()).expect("cannot create graphics context");
        let surface = softbuffer::Surface::new(&ctx, window.clone()).expect("cannot create surface");

        // Ask for the size we are actually going to show.
        let (width, height) = if self.args.fullscreen && !self.args.size_given {
            let s = window.current_monitor().map(|m| m.size()).unwrap_or(window.inner_size());
            (s.width, s.height)
        } else {
            (self.args.width, self.args.height)
        };
        let refresh = window
            .current_monitor()
            .and_then(|m| m.refresh_rate_millihertz())
            .map(|m| (m + 500) / 1000)
            .filter(|_| self.args.fullscreen)
            .unwrap_or(self.args.fps);
        self.gfx = Some(Gfx { window, surface });

        let (tx, rx) = channel();
        self.tx = Some(tx);
        let settings = Settings {
            host: self.args.host.clone(),
            width,
            height,
            fps: refresh,
            ffmpeg: self.args.ffmpeg.clone(),
            trust_all: self.args.trust_all,
            hwdec: self.args.hwdec,
        };
        let slot = self.slot.clone();
        let proxy = self.proxy.clone();
        std::thread::spawn(move || {
            session::run(settings, rx, slot, move |ev| {
                let _ = proxy.send_event(ev);
            })
        });
    }

    fn user_event(&mut self, el: &ActiveEventLoop, ev: UiEvent) {
        match ev {
            UiEvent::Connected { width, height, fps } => {
                eprintln!("connected: {width}x{height}@{fps}");
                self.video = Some((width, height));
                self.need_clear = true;
            }
            UiEvent::Frame => {
                if let Some(g) = &self.gfx {
                    g.window.request_redraw();
                }
            }
            UiEvent::Rtt(ms) => self.rtt_ms = ms,
            UiEvent::Closed(reason) => {
                eprintln!("{reason}");
                if reason != "connection closed" {
                    self.exit_code = 1;
                }
                el.exit();
            }
        }
    }

    fn window_event(&mut self, el: &ActiveEventLoop, _: WindowId, event: WindowEvent) {
        match event {
            WindowEvent::CloseRequested => {
                self.tx = None; // ends the session thread
                el.exit();
            }
            WindowEvent::RedrawRequested => self.draw(),
            WindowEvent::Resized(_) => {
                self.need_clear = true;
                if let Some(g) = &self.gfx {
                    g.window.request_redraw();
                }
            }
            WindowEvent::Focused(false) => self.release_everything(),
            WindowEvent::CursorMoved { position, .. } => {
                self.pos = position;
                let (phase, pressure) =
                    if self.buttons != 0 { (PointerPhase::Move, 1.0) } else { (PointerPhase::Hover, 0.0) };
                self.pointer(0, PointerSource::Mouse, phase, position, pressure, self.buttons);
            }
            WindowEvent::MouseInput { state, button, .. } => {
                let bit = match button {
                    MouseButton::Left => 1,
                    MouseButton::Right => 2,
                    MouseButton::Middle => 4,
                    _ => return,
                };
                let down = state == ElementState::Pressed;
                if down && !self.over_video(self.pos) {
                    return; // a click on the letterbox bars is not a click on the video edge
                }
                if !down && self.buttons & bit == 0 {
                    return; // matching press was ignored
                }
                if down {
                    self.buttons |= bit;
                } else {
                    self.buttons &= !bit;
                }
                let phase = if down { PointerPhase::Down } else { PointerPhase::Up };
                let pressure = if self.buttons != 0 { 1.0 } else { 0.0 };
                self.pointer(0, PointerSource::Mouse, phase, self.pos, pressure, self.buttons);
            }
            WindowEvent::MouseWheel { delta, .. } => {
                let (dx, dy) = match delta {
                    MouseScrollDelta::LineDelta(x, y) => (x, y),
                    MouseScrollDelta::PixelDelta(p) => (p.x as f32 / 40.0, p.y as f32 / 40.0),
                };
                if let Some((x, y)) = self.normalize(self.pos) {
                    self.send(Message::Pointer {
                        id: 0,
                        source: PointerSource::Mouse,
                        phase: PointerPhase::Wheel,
                        x,
                        y,
                        pressure: 0.0,
                        buttons: self.buttons,
                        tilt_x: 0,
                        tilt_y: 0,
                        wheel_x: dx,
                        wheel_y: dy,
                    });
                }
            }
            WindowEvent::Touch(t) => {
                let phase = match t.phase {
                    TouchPhase::Started => PointerPhase::Down,
                    TouchPhase::Moved => PointerPhase::Move,
                    TouchPhase::Ended | TouchPhase::Cancelled => PointerPhase::Up,
                };
                // A pen reports an altitude angle; fingers do not.
                let (source, pressure, tilt) = match t.force {
                    Some(Force::Calibrated { force, max_possible_force, altitude_angle }) => {
                        let p = if max_possible_force > 0.0 { (force / max_possible_force) as f32 } else { 1.0 };
                        match altitude_angle {
                            // 90 degrees = upright; report the lean away from upright.
                            Some(alt) => (PointerSource::Pen, p, (90.0 - alt.to_degrees()).clamp(0.0, 90.0) as i8),
                            None => (PointerSource::Touch, p, 0),
                        }
                    }
                    Some(Force::Normalized(p)) => (PointerSource::Touch, p as f32, 0),
                    None => (PointerSource::Touch, if phase == PointerPhase::Up { 0.0 } else { 1.0 }, 0),
                };
                match phase {
                    PointerPhase::Down if !self.over_video(t.location) => return,
                    PointerPhase::Down => {
                        self.touches.insert(t.id);
                    }
                    PointerPhase::Up => {
                        if !self.touches.remove(&t.id) {
                            return;
                        }
                    }
                    _ => {
                        if !self.touches.contains(&t.id) {
                            return;
                        }
                    }
                }
                if let Some((x, y)) = self.normalize(t.location) {
                    if phase == PointerPhase::Up {
                        self.touch_pos.remove(&t.id);
                    } else {
                        self.touch_pos.insert(t.id, (x, y));
                    }
                    self.send(Message::Pointer {
                        id: (t.id & 0xFF) as u8,
                        source,
                        phase,
                        x,
                        y,
                        pressure: pressure.clamp(0.0, 1.0),
                        buttons: if source == PointerSource::Pen { 1 } else { 0 },
                        tilt_x: tilt,
                        tilt_y: 0,
                        wheel_x: 0.0,
                        wheel_y: 0.0,
                    });
                }
            }
            WindowEvent::KeyboardInput { event, .. } => {
                if let PhysicalKey::Code(code) = event.physical_key {
                    if let Some(hid) = keys::hid(code) {
                        let down = event.state == ElementState::Pressed;
                        if down {
                            self.pressed.insert(hid);
                        } else {
                            self.pressed.remove(&hid);
                        }
                        // Local escape hatch so a fullscreen client can always be left.
                        if down
                            && code == winit::keyboard::KeyCode::KeyQ
                            && [0xE0u16, 0xE4].iter().any(|k| self.pressed.contains(k))
                            && [0xE2u16, 0xE6].iter().any(|k| self.pressed.contains(k))
                            && [0xE1u16, 0xE5].iter().any(|k| self.pressed.contains(k))
                        {
                            self.release_everything();
                            self.tx = None;
                            el.exit();
                            return;
                        }
                        self.send(Message::Key { code: hid, down });
                    }
                }
            }
            _ => {}
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn fit_keeps_aspect_and_fills_one_axis() {
        assert_eq!(super::fit_size((1280, 720), (1920, 1080)), (1920, 1080));
        assert_eq!(super::fit_size((1280, 720), (1920, 1200)), (1920, 1080)); // pillar/letterbox
        assert_eq!(super::fit_size((1280, 720), (1000, 1000)), (1000, 562));
        assert_eq!(super::fit_size((720, 1280), (1920, 1080)), (607, 1080));
        assert_eq!(super::fit_size((640, 480), (640, 480)), (640, 480));
    }

    #[test]
    fn blit_copies_scales_and_clips() {
        // 2x2 source scaled to 4x4 inside a 6x4 window at x=1.
        let src = [1u32, 2, 3, 4];
        let mut win = vec![0u32; 6 * 4];
        super::blit(&src, (2, 2), &mut win, (6, 4), (1, 0, 4, 4));
        assert_eq!(&win[0..6], &[0, 1, 1, 2, 2, 0]);
        assert_eq!(&win[3 * 6..4 * 6], &[0, 3, 3, 4, 4, 0]);
        // 1:1 with part of it outside the window on the left.
        let mut win = vec![0u32; 2 * 2];
        super::blit(&src, (2, 2), &mut win, (2, 2), (-1, 0, 2, 2));
        assert_eq!(win, vec![2, 0, 4, 0]);
    }

    use super::*;

    #[test]
    fn default_port_is_added_where_missing() {
        assert_eq!(with_default_port("pc"), "pc:47800");
        assert_eq!(with_default_port("pc:1234"), "pc:1234");
        assert_eq!(with_default_port("10.0.0.5"), "10.0.0.5:47800");
        assert_eq!(with_default_port("10.0.0.5:9"), "10.0.0.5:9");
        assert_eq!(with_default_port("::1"), "[::1]:47800");
        assert_eq!(with_default_port("[::1]"), "[::1]:47800");
        assert_eq!(with_default_port("[::1]:9"), "[::1]:9");
    }
}
