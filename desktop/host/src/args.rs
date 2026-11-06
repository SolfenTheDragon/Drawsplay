//! dd-host's command line.

use crate::access;
use crate::encoder::Encoder;
use dd_proto::{video_size_ok, DEFAULT_PORT};

/// Upper bound for `--max-clients` (the Windows virtual display driver has eight monitor slots).
pub(crate) const MAX_CLIENTS: usize = 8;

pub(crate) struct Args {
    pub(crate) bind: String,
    pub(crate) source: Source,
    pub(crate) bitrate_kbps: u32,
    pub(crate) ffmpeg: String,
    /// Top-left of the captured screen region in desktop coordinates, used to map
    /// the client's normalized pointer to absolute coordinates.
    pub(crate) origin: (i32, i32),
    pub(crate) size: Option<(u32, u32)>,
    /// Accept unknown clients without the pairing-code check. Testing only.
    pub(crate) trust_all: bool,
    /// Fail instead of mirroring when no virtual display can be created.
    pub(crate) require_virtual: bool,
    /// Requested encoder (`None` = automatic) and the one actually chosen at startup.
    pub(crate) encoder: Option<Encoder>,
    pub(crate) chosen: Encoder,
    pub(crate) show_key: bool,
    pub(crate) list_trusted: bool,
    pub(crate) forget: Option<String>,
    pub(crate) remove_fake: bool,
    pub(crate) trust_key: Option<String>,
    /// Clients served at the same time (each gets its own virtual monitor).
    pub(crate) max_clients: usize,
    /// Networks served in addition to the private ones, and whether every address is served.
    pub(crate) allow: Vec<access::Net>,
    pub(crate) allow_any: bool,
}

#[derive(Clone, Copy, PartialEq)]
pub(crate) enum Source {
    Test,
    Desktop,
    /// Create a virtual monitor (Windows: DrawSplay IDD driver; Linux/X11: a spare RandR output).
    Virtual,
}

pub(crate) fn parse_args() -> Result<Args, String> {
    let mut a = Args {
        bind: format!("0.0.0.0:{DEFAULT_PORT}"),
        source: Source::Virtual,
        bitrate_kbps: 12_000,
        ffmpeg: "ffmpeg".into(),
        origin: (0, 0),
        size: None,
        trust_all: false,
        require_virtual: false,
        encoder: None,
        chosen: Encoder::Software,
        show_key: false,
        list_trusted: false,
        forget: None,
        remove_fake: false,
        trust_key: None,
        max_clients: 1,
        allow: Vec::new(),
        allow_any: false,
    };
    let mut it = std::env::args().skip(1);
    while let Some(k) = it.next() {
        let mut val = |name: &str| it.next().ok_or(format!("{name} needs a value"));
        match k.as_str() {
            "--bind" => a.bind = val("--bind")?,
            "--source" => {
                a.source = match val("--source")?.as_str() {
                    "test" => Source::Test,
                    "desktop" => Source::Desktop,
                    "virtual" => Source::Virtual,
                    o => return Err(format!("unknown source {o} (test|desktop|virtual)")),
                }
            }
            "--bitrate-kbps" => a.bitrate_kbps = val("--bitrate-kbps")?.parse().map_err(|e| format!("{e}"))?,
            "--trust-all" => a.trust_all = true,
            "--require-virtual" => a.require_virtual = true,
            "--show-key" => a.show_key = true,
            "--list-trusted" => a.list_trusted = true,
            "--remove-fake-displays" => a.remove_fake = true,
            "--forget" => a.forget = Some(val("--forget")?),
            "--trust" => a.trust_key = Some(val("--trust")?),
            "--allow" => a.allow.push(access::Net::parse(&val("--allow")?)?),
            "--allow-any" => a.allow_any = true,
            "--max-clients" => {
                a.max_clients = val("--max-clients")?.parse().map_err(|e| format!("--max-clients: {e}"))?;
                if !(1..=MAX_CLIENTS).contains(&a.max_clients) {
                    return Err(format!("--max-clients must be between 1 and {MAX_CLIENTS}"));
                }
            }
            "--encoder" => {
                let v = val("--encoder")?;
                a.encoder =
                    if v == "auto" { None } else { Some(Encoder::parse(&v).ok_or(format!("unknown encoder {v}"))?) };
            }
            "--ffmpeg" => a.ffmpeg = val("--ffmpeg")?,
            "--origin" => {
                let v = val("--origin")?;
                let (x, y) = v.split_once(',').ok_or("--origin X,Y")?;
                a.origin = (x.parse().map_err(|e| format!("{e}"))?, y.parse().map_err(|e| format!("{e}"))?);
            }
            "--size" => {
                let v = val("--size")?;
                let (w, h) = v.split_once('x').ok_or("--size WxH")?;
                let (w, h) = (w.parse().map_err(|e| format!("{e}"))?, h.parse().map_err(|e| format!("{e}"))?);
                if !video_size_ok(w, h) {
                    return Err(format!("--size {w}x{h} is out of range"));
                }
                a.size = Some((w, h));
            }
            "-h" | "--help" => {
                println!(
                    "dd-host [--bind ADDR:PORT] [--source virtual|desktop|test] [--bitrate-kbps N]\n        [--size WxH] [--origin X,Y] [--ffmpeg PATH] [--trust-all] [--require-virtual]
        [--encoder auto|software|nvenc|qsv|amf|vaapi]
        [--show-key] [--trust CLIENT_KEY]\n        [--list-trusted] [--forget KEY_PREFIX]\n        [--max-clients N] [--remove-fake-displays]\n        [--allow NET]... [--allow-any]\n\nOnly loopback, private, link-local and 100.64.0.0/10 addresses are served; --allow adds a network\n(ADDR/LEN), --allow-any serves everyone. The stream size defaults to the client's requested size."
                );
                std::process::exit(0);
            }
            o => return Err(format!("unknown argument {o}")),
        }
    }
    Ok(a)
}
