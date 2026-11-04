//! Chooses what the host captures: a virtual monitor created for the session when possible,
//! otherwise an existing screen (mirroring).

use crate::inject::Target;
use std::io;

/// The captured area, plus whatever must stay alive to keep it (dropping it removes the
/// virtual monitor again).
/// Wayland portal session, which also carries input (an empty type elsewhere).
#[cfg(target_os = "linux")]
pub type PortalHandle = Option<std::sync::Arc<crate::portal::Portal>>;
#[cfg(not(target_os = "linux"))]
pub type PortalHandle = Option<()>;

pub struct Display {
    pub target: Target,
    /// Wayland desktops without a compositor-specific backend: the screen comes from this session.
    pub portal: PortalHandle,
    /// Wayland only: the output to capture with `wf-recorder`.
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    pub wayland_output: Option<String>,
    _guard: Guard,
}

#[allow(dead_code)] // each variant is only constructed on its own OS
enum Guard {
    None,
    #[cfg(windows)]
    Windows(crate::vdisplay::VirtualDisplay),
    #[cfg(target_os = "linux")]
    X11(crate::linux_display::VirtualOutput),
    #[cfg(target_os = "linux")]
    Wayland(Option<crate::wayland_display::VirtualOutput>),
    /// A virtual monitor requested through the portal (the portal owns it; nothing to undo here).
    #[cfg(target_os = "linux")]
    PortalVirtual,
}

impl Display {
    /// Short name of how this screen was obtained, for status reports.
    pub fn backend(&self) -> &'static str {
        match &self._guard {
            #[cfg(windows)]
            Guard::Windows(_) => "windows-virtual",
            #[cfg(target_os = "linux")]
            Guard::X11(_) => "x11-virtual",
            #[cfg(target_os = "linux")]
            Guard::Wayland(Some(_)) => "wayland-virtual",
            _ if self.portal_in_use() => "wayland-portal",
            _ if self.wayland_output.is_some() => "wayland-mirror",
            _ => "mirror",
        }
    }

    /// Does this screen come from the Wayland portal?
    pub fn portal_in_use(&self) -> bool {
        self.portal.is_some()
    }

    /// True when the host created a monitor for this session (as opposed to mirroring one).
    pub fn is_virtual(&self) -> bool {
        !matches!(self._guard, Guard::None)
    }
}

fn even(n: u32) -> u32 {
    (n & !1).max(2) // yuv420p needs even dimensions
}

#[cfg(windows)]
fn create_virtual(w: u32, h: u32, fps: u32) -> Result<Display, String> {
    let vd = crate::vdisplay::VirtualDisplay::create(w, h, fps)?;
    let (x, y, vw, vh) = vd.rect;
    Ok(Display {
        target: Target { origin: (x, y), size: (even(vw), even(vh)), desktop: (0, 0) },
        wayland_output: None,
        portal: None,
        _guard: Guard::Windows(vd),
    })
}

#[cfg(target_os = "linux")]
fn create_virtual(w: u32, h: u32, fps: u32) -> Result<Display, String> {
    if crate::wayland_display::active()
        && crate::wayland_display::compositor() == crate::wayland_display::Compositor::Other
    {
        return portal_display(true, (w, h));
    }
    if crate::wayland_display::active() {
        let vo = crate::wayland_display::VirtualOutput::create(w, h, fps)?;
        return wayland_display_for(vo.out.clone(), Some(vo));
    }
    let vo = crate::linux_display::VirtualOutput::create(w, h, fps)?;
    let (x, y, vw, vh) = vo.rect;
    let desktop = vo.desktop;
    Ok(Display {
        target: Target { origin: (x, y), size: (even(vw), even(vh)), desktop },
        wayland_output: None,
        portal: None,
        _guard: Guard::X11(vo),
    })
}

/// Target for a Wayland output, in coordinates relative to the layout's top-left corner (the
/// input devices span exactly that box).
#[cfg(target_os = "linux")]
fn wayland_display_for(
    out: crate::wayland_display::Out,
    vo: Option<crate::wayland_display::VirtualOutput>,
) -> Result<Display, String> {
    use crate::wayland_display as wl;
    let (bx, by, bw, bh) = wl::bounds(&wl::outputs()?);
    Ok(Display {
        target: Target { origin: (out.x - bx, out.y - by), size: (even(out.w), even(out.h)), desktop: (bw, bh) },
        wayland_output: Some(out.name),
        portal: None,
        _guard: Guard::Wayland(vo),
    })
}

/// Screen (and input) from the xdg-desktop-portal: GNOME, KDE and other compositors without a
/// compositor-specific backend. A virtual monitor has a size chosen by the compositor.
#[cfg(target_os = "linux")]
fn portal_display(virtual_monitor: bool, requested: (u32, u32)) -> Result<Display, String> {
    let p = crate::portal::Portal::start(virtual_monitor)?;
    let (w, h) = p.size.unwrap_or(requested);
    Ok(Display {
        target: Target { origin: (0, 0), size: (even(w), even(h)), desktop: (w, h) },
        wayland_output: None,
        portal: Some(std::sync::Arc::new(p)),
        _guard: if virtual_monitor { Guard::PortalVirtual } else { Guard::None },
    })
}

/// Mirror the primary output of a Wayland session.
#[cfg(target_os = "linux")]
fn wayland_mirror() -> Result<Display, String> {
    if crate::wayland_display::compositor() == crate::wayland_display::Compositor::Other {
        return portal_display(false, (1920, 1080));
    }
    let outs = crate::wayland_display::outputs()?;
    let out = crate::wayland_display::primary(&outs).ok_or("no active Wayland output")?.clone();
    wayland_display_for(out, None)
}

#[cfg(not(any(windows, target_os = "linux")))]
fn create_virtual(_: u32, _: u32, _: u32) -> Result<Display, String> {
    Err("virtual displays are not supported on this operating system".into())
}

/// Size of the primary screen, for mirroring.
fn primary_size() -> Option<(u32, u32)> {
    #[cfg(windows)]
    {
        Some(crate::vdisplay::primary_size())
    }
    #[cfg(target_os = "linux")]
    {
        crate::linux_display::screen_size()
    }
    #[cfg(not(any(windows, target_os = "linux")))]
    {
        None
    }
}

#[cfg(not(target_os = "linux"))]
type MirrorArea = ((u32, u32), (i32, i32), (u32, u32));
#[cfg(target_os = "linux")]
use crate::linux_display::MirrorArea;

/// What to capture when mirroring: size, origin and whole-desktop size (zero when unknown).
/// On Linux this is the primary monitor, not the whole X screen.
fn mirror_area() -> Option<MirrorArea> {
    #[cfg(target_os = "linux")]
    {
        crate::linux_display::primary_area()
    }
    #[cfg(not(target_os = "linux"))]
    {
        primary_size().map(|s| (s, (0, 0), (0, 0)))
    }
}

/// `want_virtual`: try to create a virtual monitor first. `origin` is the manual capture origin
/// used when mirroring a region. With `require_virtual`, failure is an error instead of a mirror.
pub fn acquire(
    want_virtual: bool,
    require_virtual: bool,
    origin: (i32, i32),
    w: u32,
    h: u32,
    fps: u32,
) -> io::Result<Display> {
    if want_virtual {
        match create_virtual(w, h, fps) {
            Ok(d) => return Ok(d),
            Err(e) if require_virtual => return Err(io::Error::other(e)),
            Err(e) => eprintln!("WARNING: no virtual display ({e}); mirroring the primary screen instead"),
        }
        #[cfg(target_os = "linux")]
        if crate::wayland_display::active() {
            return wayland_mirror().map_err(io::Error::other);
        }
        let (size, origin, desktop) = mirror_area().unwrap_or(((w, h), (0, 0), (0, 0)));
        return Ok(Display {
            target: Target { origin, size: (even(size.0), even(size.1)), desktop },
            wayland_output: None,
            portal: None,
            _guard: Guard::None,
        });
    }
    #[cfg(target_os = "linux")]
    if crate::wayland_display::active() {
        return wayland_mirror().map_err(io::Error::other); // no fixed regions on Wayland
    }
    // Capturing a fixed region: keep it inside the screen where we can tell.
    let (mut cw, mut ch) = (w, h);
    if let Some((sw, sh)) = primary_size() {
        cw = cw.min(sw.saturating_sub(origin.0.max(0) as u32).max(2));
        ch = ch.min(sh.saturating_sub(origin.1.max(0) as u32).max(2));
    }
    Ok(Display {
        target: Target { origin, size: (even(cw), even(ch)), desktop: (0, 0) },
        wayland_output: None,
        portal: None,
        _guard: Guard::None,
    })
}
