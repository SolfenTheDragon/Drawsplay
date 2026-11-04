//! Windows virtual display via the DrawSplay IddCx driver (`drivers/windows/idd`).
//!
//! The driver tears every monitor down when the control handle closes, so dropping
//! [`VirtualDisplay`] (or crashing) never leaves an orphaned monitor behind.

use std::ffi::c_void;
use std::time::{Duration, Instant};
use windows::core::{GUID, PCWSTR};
use windows::Win32::Devices::DeviceAndDriverInstallation::{
    CM_Get_Device_Interface_ListW, CM_Get_Device_Interface_List_SizeW, CM_GET_DEVICE_INTERFACE_LIST_PRESENT, CR_SUCCESS,
};
use windows::Win32::Devices::Display::{SetDisplayConfig, SDC_APPLY, SDC_TOPOLOGY_EXTEND};
use windows::Win32::Foundation::{CloseHandle, BOOL, GENERIC_READ, GENERIC_WRITE, HANDLE, LPARAM, RECT};
use windows::Win32::Graphics::Gdi::{
    ChangeDisplaySettingsExW, EnumDisplayMonitors, GetMonitorInfoW, CDS_UPDATEREGISTRY, DEVMODEW,
    DISP_CHANGE_SUCCESSFUL, DM_DISPLAYFREQUENCY, DM_PELSHEIGHT, DM_PELSWIDTH, HDC, HMONITOR, MONITORINFO,
    MONITORINFOEXW,
};
use windows::Win32::Storage::FileSystem::{
    CreateFileW, FILE_ATTRIBUTE_NORMAL, FILE_SHARE_READ, FILE_SHARE_WRITE, OPEN_EXISTING,
};
use windows::Win32::System::IO::DeviceIoControl;

// {4f73af95-3814-4206-a651-125dfbb5e8f5}
const IDD_GUID: GUID = GUID::from_u128(0x4f73af95_3814_4206_a651_125dfbb5e8f5);
// CTL_CODE(FILE_DEVICE_UNKNOWN, 0x800+n, METHOD_BUFFERED, FILE_ANY_ACCESS)
const IOCTL_ADD_MONITOR: u32 = 0x0022_2000;
const IOCTL_REMOVE_MONITOR: u32 = 0x0022_2004;

pub struct VirtualDisplay {
    handle: HANDLE,
    monitor_id: u32,
    /// Desktop rectangle of the virtual monitor (left, top, width, height) in physical pixels.
    pub rect: (i32, i32, u32, u32),
}

impl Drop for VirtualDisplay {
    fn drop(&mut self) {
        unsafe {
            let id = self.monitor_id.to_le_bytes();
            let mut got = 0u32;
            let _ = DeviceIoControl(
                self.handle,
                IOCTL_REMOVE_MONITOR,
                Some(id.as_ptr() as *const c_void),
                4,
                None,
                0,
                Some(&mut got),
                None,
            );
            let _ = CloseHandle(self.handle);
        }
    }
}

fn device_path() -> Result<Vec<u16>, String> {
    unsafe {
        let mut size = 0u32;
        if CM_Get_Device_Interface_List_SizeW(
            &mut size,
            &IDD_GUID,
            PCWSTR::null(),
            CM_GET_DEVICE_INTERFACE_LIST_PRESENT,
        ) != CR_SUCCESS
            || size <= 1
        {
            return Err("DrawSplay virtual display driver not found (is it installed?)".into());
        }
        let mut buf = vec![0u16; size as usize];
        if CM_Get_Device_Interface_ListW(&IDD_GUID, PCWSTR::null(), &mut buf, CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
            != CR_SUCCESS
        {
            return Err("cannot list virtual display device interfaces".into());
        }
        // First entry of a double-NUL-terminated list.
        let end = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
        buf.truncate(end);
        buf.push(0);
        Ok(buf)
    }
}

struct Mon {
    id: isize,
    rect: RECT,
    /// GDI device name, e.g. `\.\DISPLAY7`, NUL-terminated.
    device: [u16; 32],
}

unsafe extern "system" fn collect(m: HMONITOR, _: HDC, _: *mut RECT, lp: LPARAM) -> BOOL {
    let out = &mut *(lp.0 as *mut Vec<Mon>);
    let mut mi = MONITORINFOEXW::default();
    mi.monitorInfo.cbSize = std::mem::size_of::<MONITORINFOEXW>() as u32;
    if GetMonitorInfoW(m, &mut mi as *mut MONITORINFOEXW as *mut MONITORINFO).as_bool() {
        out.push(Mon { id: m.0 as isize, rect: mi.monitorInfo.rcMonitor, device: mi.szDevice });
    }
    BOOL(1)
}

fn monitors() -> Vec<Mon> {
    let mut v: Vec<Mon> = Vec::new();
    unsafe {
        let _ = EnumDisplayMonitors(None, None, Some(collect), LPARAM(&mut v as *mut _ as isize));
    }
    v
}

/// Ask Windows to switch `device` to w x h @ hz. Returns whether it accepted.
fn set_mode(device: &[u16; 32], w: u32, h: u32, hz: u32) -> bool {
    unsafe {
        let mut dm = DEVMODEW { dmSize: std::mem::size_of::<DEVMODEW>() as u16, ..Default::default() };
        dm.dmPelsWidth = w;
        dm.dmPelsHeight = h;
        dm.dmDisplayFrequency = hz;
        let apply = |dm: &DEVMODEW| {
            ChangeDisplaySettingsExW(PCWSTR(device.as_ptr()), Some(dm), None, CDS_UPDATEREGISTRY, None)
                == DISP_CHANGE_SUCCESSFUL
        };
        dm.dmFields = DM_PELSWIDTH | DM_PELSHEIGHT | DM_DISPLAYFREQUENCY;
        // The driver may not offer this refresh rate; the size matters more, so retry without it.
        apply(&dm) || {
            dm.dmFields = DM_PELSWIDTH | DM_PELSHEIGHT;
            apply(&dm)
        }
    }
}

impl VirtualDisplay {
    /// Plug a virtual monitor at `w`x`h`@`hz`, extend the desktop onto it and return where it landed.
    pub fn create(w: u32, h: u32, hz: u32) -> Result<VirtualDisplay, String> {
        let path = device_path()?;
        let handle = unsafe {
            CreateFileW(
                PCWSTR(path.as_ptr()),
                (GENERIC_READ | GENERIC_WRITE).0,
                FILE_SHARE_READ | FILE_SHARE_WRITE,
                None,
                OPEN_EXISTING,
                FILE_ATTRIBUTE_NORMAL,
                None,
            )
        }
        .map_err(|e| format!("cannot open virtual display driver: {e}"))?;

        let before: Vec<isize> = monitors().iter().map(|m| m.id).collect();

        // {Width, Height, RefreshHz, NativeWidth, NativeHeight}, little-endian u32.
        let mut req = Vec::with_capacity(20);
        for v in [w, h, hz, w, h] {
            req.extend(v.to_le_bytes());
        }
        let mut out = [0u8; 4];
        let mut got = 0u32;
        // The driver reports "not ready" until IddCx has initialised its adapter: retry briefly.
        let deadline = Instant::now() + Duration::from_secs(5);
        let id = loop {
            let r = unsafe {
                DeviceIoControl(
                    handle,
                    IOCTL_ADD_MONITOR,
                    Some(req.as_ptr() as *const c_void),
                    req.len() as u32,
                    Some(out.as_mut_ptr() as *mut c_void),
                    4,
                    Some(&mut got),
                    None,
                )
            };
            match r {
                Ok(()) if got >= 4 => break u32::from_le_bytes(out),
                Ok(()) => {
                    unsafe {
                        let _ = CloseHandle(handle);
                    }
                    return Err("driver returned a short reply".into());
                }
                Err(e) if Instant::now() < deadline => {
                    let _ = e;
                    std::thread::sleep(Duration::from_millis(100));
                }
                Err(e) => {
                    unsafe {
                        let _ = CloseHandle(handle);
                    }
                    return Err(format!("driver refused to add a monitor: {e}"));
                }
            }
        };
        let mut vd = VirtualDisplay { handle, monitor_id: id, rect: (0, 0, w, h) };

        // Windows keeps a freshly plugged monitor off the desktop until it is extended.
        unsafe {
            let _ = SetDisplayConfig(None, None, SDC_TOPOLOGY_EXTEND | SDC_APPLY);
        }

        // Find the monitor that appeared (diff against the list from before the plug).
        let deadline = Instant::now() + Duration::from_secs(8);
        while Instant::now() < deadline {
            if let Some(m) = monitors().into_iter().find(|m| !before.contains(&m.id)) {
                let size = |r: RECT| ((r.right - r.left) as u32, (r.bottom - r.top) as u32);
                let mut r = m.rect;
                // The driver may bring the monitor up in a remembered mode: switch to the requested one.
                if size(r) != (w, h) && set_mode(&m.device, w, h, hz) {
                    std::thread::sleep(Duration::from_millis(300));
                    // The monitor handle can change with the mode, so match by device name.
                    if let Some(n) = monitors().into_iter().find(|n| n.device == m.device) {
                        r = n.rect;
                    }
                }
                let (rw, rh) = size(r);
                vd.rect = (r.left, r.top, rw, rh);
                return Ok(vd);
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        Err("virtual monitor was added but never appeared on the desktop".into())
    }
}

/// Make coordinates physical pixels; otherwise scaled displays report/accept the wrong values.
pub fn set_dpi_aware() {
    use windows::Win32::UI::HiDpi::{SetProcessDpiAwarenessContext, DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2};
    unsafe {
        let _ = SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2);
    }
}

/// A warning to show when Windows display scaling is not 100%: `ffmpeg.exe` (gdigrab) is not
/// DPI-aware, so capture offsets and sizes may be interpreted in scaled units. See docs/known-issues.md.
pub fn scaling_warning() -> Option<String> {
    use windows::Win32::UI::HiDpi::GetDpiForSystem;
    let dpi = unsafe { GetDpiForSystem() };
    (dpi != 0 && dpi != 96).then(|| {
        format!(
            "WARNING: Windows display scaling is {}%. Screen capture through ffmpeg may be offset or cropped;              if the picture is wrong, set scaling to 100% (see docs/known-issues.md).",
            dpi * 100 / 96
        )
    })
}

/// Size of the primary monitor in physical pixels (used when mirroring).
pub fn primary_size() -> (u32, u32) {
    use windows::Win32::UI::WindowsAndMessaging::{GetSystemMetrics, SM_CXSCREEN, SM_CYSCREEN};
    unsafe { (GetSystemMetrics(SM_CXSCREEN).max(2) as u32, GetSystemMetrics(SM_CYSCREEN).max(2) as u32) }
}
