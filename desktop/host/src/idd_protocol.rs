//! The DrawSplay IddCx driver's control interface (`drivers/windows/idd/Public.h`). The Go host speaks it
//! too (`host/idd_protocol.go`); both are tested against `testdata/protocols/idd-ioctl.vectors`. It is
//! plain data, so it builds and is tested on every platform; only `vdisplay.rs` (Windows) uses it.
#![cfg_attr(not(windows), allow(dead_code))]

/// Device interface GUID of the control device: {4f73af95-3814-4206-a651-125dfbb5e8f5}.
pub const INTERFACE_GUID: u128 = 0x4f73af95_3814_4206_a651_125dfbb5e8f5;

// CTL_CODE(FILE_DEVICE_UNKNOWN, 0x800+n, METHOD_BUFFERED, FILE_ANY_ACCESS)
pub const IOCTL_ADD_MONITOR: u32 = 0x0022_2000;
pub const IOCTL_REMOVE_MONITOR: u32 = 0x0022_2004;
/// dd-host never removes all monitors (the driver drops its monitors when its handle closes); kept so
/// the vectors pin every code the driver defines.
#[allow(dead_code)]
pub const IOCTL_REMOVE_ALL: u32 = 0x0022_2008;

/// The add-monitor input: width, height, refresh rate, native width and native height (0 = unknown),
/// each a little-endian u32.
pub fn add_monitor_request(w: u32, h: u32, hz: u32, native_w: u32, native_h: u32) -> [u8; 20] {
    let mut req = [0u8; 20];
    for (i, v) in [w, h, hz, native_w, native_h].into_iter().enumerate() {
        req[i * 4..i * 4 + 4].copy_from_slice(&v.to_le_bytes());
    }
    req
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn vectors() -> Vec<(String, HashMap<String, String>)> {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../testdata/protocols/idd-ioctl.vectors");
        std::fs::read_to_string(path)
            .unwrap()
            .lines()
            .map(str::trim)
            .filter(|l| !l.is_empty() && !l.starts_with('#'))
            .map(|l| {
                let mut parts = l.split_whitespace();
                let name = parts.next().unwrap().to_string();
                let fields =
                    parts.map(|kv| kv.split_once('=').unwrap()).map(|(k, v)| (k.to_string(), v.to_string())).collect();
                (name, fields)
            })
            .collect()
    }

    #[test]
    fn matches_the_shared_vectors() {
        let all = vectors();
        assert!(!all.is_empty());
        for (name, f) in all {
            let n = |k: &str| f[k].parse::<u32>().unwrap();
            match name.as_str() {
                "interface" => assert_eq!(f["guid"].replace('-', ""), format!("{INTERFACE_GUID:032x}")),
                "ioctl-add-monitor" => assert_eq!(u32::from_str_radix(&f["code"], 16).unwrap(), IOCTL_ADD_MONITOR),
                "ioctl-remove-monitor" => {
                    assert_eq!(u32::from_str_radix(&f["code"], 16).unwrap(), IOCTL_REMOVE_MONITOR)
                }
                "ioctl-remove-all" => assert_eq!(u32::from_str_radix(&f["code"], 16).unwrap(), IOCTL_REMOVE_ALL),
                _ if name.starts_with("add-monitor-request") => {
                    let req =
                        add_monitor_request(n("width"), n("height"), n("hz"), n("nativeWidth"), n("nativeHeight"));
                    let hex: String = req.iter().map(|b| format!("{b:02x}")).collect();
                    assert_eq!(hex, f["wire"], "{name}");
                }
                _ => panic!("unknown vector {name}"),
            }
        }
    }
}
