//! H.264 encoder selection: a working GPU encoder when there is one, libx264 otherwise.
//!
//! "Working" is checked by actually encoding a tiny test clip, since ffmpeg lists encoders
//! that need hardware the machine does not have.

use std::process::{Command, Stdio};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Encoder {
    Software,
    Nvenc,
    Qsv,
    Amf,
    Vaapi,
}

impl Encoder {
    pub fn name(self) -> &'static str {
        match self {
            Encoder::Software => "libx264",
            Encoder::Nvenc => "h264_nvenc",
            Encoder::Qsv => "h264_qsv",
            Encoder::Amf => "h264_amf",
            Encoder::Vaapi => "h264_vaapi",
        }
    }

    pub fn parse(s: &str) -> Option<Encoder> {
        Some(match s {
            "software" | "x264" | "libx264" => Encoder::Software,
            "nvenc" | "h264_nvenc" => Encoder::Nvenc,
            "qsv" | "h264_qsv" => Encoder::Qsv,
            "amf" | "h264_amf" => Encoder::Amf,
            "vaapi" | "h264_vaapi" => Encoder::Vaapi,
            _ => return None,
        })
    }

    /// Arguments placed before `-i` (hardware device setup).
    pub fn input_args(self) -> Vec<String> {
        match self {
            Encoder::Vaapi => vec!["-vaapi_device".into(), vaapi_device()],
            _ => vec![],
        }
    }

    /// Video filter appended to the chain, if the encoder needs frames uploaded first.
    pub fn filter(self) -> Option<&'static str> {
        match self {
            Encoder::Vaapi => Some("format=nv12,hwupload"),
            _ => None,
        }
    }

    /// Pixel format to request from the filter graph/encoder.
    pub fn pix_fmt(self) -> Option<&'static str> {
        match self {
            Encoder::Vaapi => None, // set by the hwupload filter
            Encoder::Nvenc | Encoder::Qsv | Encoder::Amf => Some("nv12"),
            Encoder::Software => Some("yuv420p"),
        }
    }

    /// Low-latency encoder options. `gop` is the keyframe interval in frames.
    pub fn codec_args(self, kbps: u32, gop: u32) -> Vec<String> {
        let rate = |extra: &[&str]| -> Vec<String> {
            let mut v: Vec<String> = extra.iter().map(|s| s.to_string()).collect();
            v.extend(["-b:v".into(), format!("{kbps}k"), "-maxrate".into(), format!("{kbps}k")]);
            // A 125 ms buffer (bitrate / 8). Two frames' worth starved keyframes at high frame rates,
            // so every keyframe came out blocky; the Go host measured and fixed the same problem.
            v.extend(["-bufsize".into(), format!("{}k", vbv_kbits(kbps))]);
            v.extend(["-g".into(), gop.to_string(), "-bf".into(), "0".into()]);
            v
        };
        let mut a = vec!["-c:v".to_string(), self.name().to_string()];
        a.extend(match self {
            Encoder::Software => rate(&["-preset", "ultrafast", "-tune", "zerolatency"]),
            Encoder::Nvenc => rate(&["-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-rc", "cbr"]),
            Encoder::Qsv => rate(&["-preset", "veryfast", "-look_ahead", "0"]),
            Encoder::Amf => rate(&["-usage", "ultralowlatency", "-quality", "speed", "-rc", "cbr"]),
            // VBR under the -maxrate/-bufsize cap. In CBR Intel's iHD driver pads every frame up to the
            // bitrate: a static desktop cost ~49 KB per frame at 24 Mbit/s instead of ~1.5 KB (measured
            // on the Go host), wasting the link for nothing.
            Encoder::Vaapi => rate(&["-rc_mode", "VBR"]),
        });
        a
    }
}

/// Video buffer size in kbit for `kbps`: 125 ms of data, at least 100 kbit.
fn vbv_kbits(kbps: u32) -> u32 {
    (kbps / 8).max(100)
}

fn vaapi_device() -> String {
    std::env::var("DD_VAAPI_DEVICE").unwrap_or_else(|_| "/dev/dri/renderD128".into())
}

/// Candidates worth trying on this OS, best first.
fn candidates() -> &'static [Encoder] {
    if cfg!(windows) {
        &[Encoder::Nvenc, Encoder::Qsv, Encoder::Amf]
    } else {
        &[Encoder::Nvenc, Encoder::Vaapi, Encoder::Qsv]
    }
}

/// Does this encoder actually work here? Encodes a few frames of a synthetic clip.
fn works(ffmpeg: &str, e: Encoder) -> bool {
    let mut c = Command::new(ffmpeg);
    c.args(["-hide_banner", "-loglevel", "error"]);
    c.args(e.input_args());
    c.args(["-f", "lavfi", "-i", "testsrc2=size=256x256:rate=30:duration=0.2"]);
    let mut vf = Vec::new();
    if let Some(f) = e.filter() {
        vf.push(f);
    }
    if !vf.is_empty() {
        c.args(["-vf", &vf.join(",")]);
    }
    if let Some(p) = e.pix_fmt() {
        c.args(["-pix_fmt", p]);
    }
    c.args(e.codec_args(2000, 30));
    c.args(["-frames:v", "5", "-f", "null", "-"]);
    c.stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    let Ok(mut child) = c.spawn() else { return false };
    // A hung GPU driver must not block start-up forever: give the five-frame test a fixed time.
    let deadline = std::time::Instant::now() + PROBE_TIMEOUT;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return status.success(),
            Ok(None) if std::time::Instant::now() < deadline => {
                std::thread::sleep(std::time::Duration::from_millis(50))
            }
            _ => {
                eprintln!("encoder {} test did not finish in {} s; skipping it", e.name(), PROBE_TIMEOUT.as_secs());
                let _ = child.kill();
                let _ = child.wait();
                return false;
            }
        }
    }
}

/// Longest an encoder test may take (it normally finishes in well under a second).
const PROBE_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(15);

/// Pick an encoder. `requested` of `None` means automatic.
pub fn choose(ffmpeg: &str, requested: Option<Encoder>) -> Encoder {
    match requested {
        Some(Encoder::Software) => Encoder::Software,
        Some(e) => {
            if works(ffmpeg, e) {
                e
            } else {
                eprintln!("encoder {} is not usable here; using software encoding", e.name());
                Encoder::Software
            }
        }
        None => candidates().iter().copied().find(|&e| works(ffmpeg, e)).unwrap_or(Encoder::Software),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_roundtrip() {
        for e in [Encoder::Software, Encoder::Nvenc, Encoder::Qsv, Encoder::Amf, Encoder::Vaapi] {
            assert_eq!(Encoder::parse(e.name()), Some(e));
        }
        assert_eq!(Encoder::parse("nope"), None);
    }

    #[test]
    fn low_latency_args() {
        let a = Encoder::Software.codec_args(12000, 120);
        let joined = a.join(" ");
        assert!(joined.contains("-c:v libx264"));
        assert!(joined.contains("-tune zerolatency"));
        assert!(joined.contains("-b:v 12000k"));
        assert!(joined.contains("-bf 0")); // no B-frames: they add latency
        assert!(joined.contains("-g 120"));
        // bufsize holds 125 ms of data, so keyframes are not starved
        assert!(joined.contains("-bufsize 1500k"));
        assert!(Encoder::Software.codec_args(400, 120).join(" ").contains("-bufsize 100k"));
        assert!(Encoder::Nvenc.codec_args(5000, 60).join(" ").contains("-tune ull"));
        assert!(Encoder::Amf.codec_args(5000, 60).join(" ").contains("ultralowlatency"));
        let vaapi = Encoder::Vaapi.codec_args(5000, 60).join(" ");
        assert!(vaapi.contains("-rc_mode VBR") && vaapi.contains("-maxrate 5000k"), "{vaapi}");
    }

    #[test]
    fn vaapi_uploads_frames() {
        assert_eq!(Encoder::Vaapi.filter(), Some("format=nv12,hwupload"));
        assert!(Encoder::Vaapi.input_args().contains(&"-vaapi_device".to_string()));
        assert_eq!(Encoder::Software.filter(), None);
    }
}
