//! Wire protocol shared by the host and the client.
//!
//! Every message is one frame: `u32` big-endian length of (type + payload),
//! one type byte, then the payload. All integers are big-endian.
//! Frames are bounded by [`MAX_FRAME`] so a bad peer cannot force a huge allocation.

use std::io::{self, Read, Write};

pub mod identity;
pub mod secure;

pub const PROTOCOL_VERSION: u16 = 1;
pub const DEFAULT_PORT: u16 = 47800;
/// Largest accepted frame (type byte + payload): 16 MiB.
pub const MAX_FRAME: usize = 16 * 1024 * 1024;
const MAX_NAME: usize = 256;

/// Where a pointer event came from.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum PointerSource {
    Mouse = 0,
    Touch = 1,
    Pen = 2,
}

/// Phase of a pointer event.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum PointerPhase {
    Move = 0,
    Down = 1,
    Up = 2,
    Hover = 3,
    Wheel = 4,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Message {
    /// client -> host: first message; describes the client's display.
    Hello {
        version: u16,
        width: u32,
        height: u32,
        refresh_hz: u32,
        name: String,
    },
    /// host -> client: the video stream's actual geometry (may differ from the request).
    Config {
        width: u32,
        height: u32,
        fps: u32,
    },
    /// host -> client: one chunk of Annex-B H.264.
    Video {
        pts_us: u64,
        key: bool,
        data: Vec<u8>,
    },
    /// client -> host: mouse, touch or pen. x/y are normalized 0..1 over the video area.
    Pointer {
        id: u8,
        source: PointerSource,
        phase: PointerPhase,
        x: f32,
        y: f32,
        /// 0..1 (mouse buttons use 1.0 while pressed).
        pressure: f32,
        /// Bitmask: 1 = primary/left, 2 = secondary/right, 4 = middle, 8 = pen barrel, 16 = eraser.
        buttons: u8,
        tilt_x: i8,
        tilt_y: i8,
        /// Wheel deltas (only meaningful for `PointerPhase::Wheel`).
        wheel_x: f32,
        wheel_y: f32,
    },
    /// client -> host: keyboard key. `code` is a USB HID usage (page 0x07).
    Key {
        code: u16,
        down: bool,
    },
    /// Either direction: liveness / RTT probe. Peer echoes `Pong` with the same value.
    Ping(u64),
    Pong(u64),
    /// host -> client: the host ended the session on purpose (for example from the web UI); the client
    /// should not reconnect. Older clients see an unknown message and just close.
    Bye {
        reason: String,
    },
}

const T_HELLO: u8 = 1;
const T_CONFIG: u8 = 2;
const T_VIDEO: u8 = 3;
const T_POINTER: u8 = 4;
const T_KEY: u8 = 5;
const T_PING: u8 = 6;
const T_PONG: u8 = 7;
const T_BYE: u8 = 8;

fn bad(msg: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg.to_string())
}

struct Cursor<'a> {
    buf: &'a [u8],
}

impl<'a> Cursor<'a> {
    fn take(&mut self, n: usize) -> io::Result<&'a [u8]> {
        if self.buf.len() < n {
            return Err(bad("truncated message"));
        }
        let (a, b) = self.buf.split_at(n);
        self.buf = b;
        Ok(a)
    }
    fn u8(&mut self) -> io::Result<u8> {
        Ok(self.take(1)?[0])
    }
    fn u16(&mut self) -> io::Result<u16> {
        Ok(u16::from_be_bytes(self.take(2)?.try_into().unwrap()))
    }
    fn u32(&mut self) -> io::Result<u32> {
        Ok(u32::from_be_bytes(self.take(4)?.try_into().unwrap()))
    }
    fn u64(&mut self) -> io::Result<u64> {
        Ok(u64::from_be_bytes(self.take(8)?.try_into().unwrap()))
    }
    fn f32(&mut self) -> io::Result<f32> {
        Ok(f32::from_bits(self.u32()?))
    }
    fn rest(&mut self) -> &'a [u8] {
        std::mem::take(&mut self.buf)
    }
}

impl Message {
    /// Serialize into a complete frame (length prefix included).
    pub fn encode(&self) -> Vec<u8> {
        let mut b = vec![0, 0, 0, 0];
        match self {
            Message::Hello { version, width, height, refresh_hz, name } => {
                b.push(T_HELLO);
                b.extend(version.to_be_bytes());
                b.extend(width.to_be_bytes());
                b.extend(height.to_be_bytes());
                b.extend(refresh_hz.to_be_bytes());
                let n = name.as_bytes();
                let n = &n[..n.len().min(MAX_NAME)];
                b.extend((n.len() as u16).to_be_bytes());
                b.extend(n);
            }
            Message::Config { width, height, fps } => {
                b.push(T_CONFIG);
                b.extend(width.to_be_bytes());
                b.extend(height.to_be_bytes());
                b.extend(fps.to_be_bytes());
            }
            Message::Video { pts_us, key, data } => {
                b.push(T_VIDEO);
                b.extend(pts_us.to_be_bytes());
                b.push(*key as u8);
                b.extend(data);
            }
            Message::Pointer { id, source, phase, x, y, pressure, buttons, tilt_x, tilt_y, wheel_x, wheel_y } => {
                b.push(T_POINTER);
                b.push(*id);
                b.push(*source as u8);
                b.push(*phase as u8);
                for f in [x, y, pressure] {
                    b.extend(f.to_bits().to_be_bytes());
                }
                b.push(*buttons);
                b.push(*tilt_x as u8);
                b.push(*tilt_y as u8);
                for f in [wheel_x, wheel_y] {
                    b.extend(f.to_bits().to_be_bytes());
                }
            }
            Message::Key { code, down } => {
                b.push(T_KEY);
                b.extend(code.to_be_bytes());
                b.push(*down as u8);
            }
            Message::Ping(v) => {
                b.push(T_PING);
                b.extend(v.to_be_bytes());
            }
            Message::Pong(v) => {
                b.push(T_PONG);
                b.extend(v.to_be_bytes());
            }
            Message::Bye { reason } => {
                b.push(T_BYE);
                let r = reason.as_bytes();
                let r = &r[..r.len().min(MAX_NAME)];
                b.extend((r.len() as u16).to_be_bytes());
                b.extend(r);
            }
        }
        let len = (b.len() - 4) as u32;
        b[..4].copy_from_slice(&len.to_be_bytes());
        b
    }

    /// Parse the body of a frame (type byte + payload, no length prefix).
    pub fn decode(body: &[u8]) -> io::Result<Message> {
        let (&ty, rest) = body.split_first().ok_or_else(|| bad("empty frame"))?;
        let mut c = Cursor { buf: rest };
        let m = match ty {
            T_HELLO => {
                let version = c.u16()?;
                let width = c.u32()?;
                let height = c.u32()?;
                let refresh_hz = c.u32()?;
                let n = c.u16()? as usize;
                if n > MAX_NAME {
                    return Err(bad("name too long"));
                }
                let name = String::from_utf8_lossy(c.take(n)?).into_owned();
                Message::Hello { version, width, height, refresh_hz, name }
            }
            T_CONFIG => Message::Config { width: c.u32()?, height: c.u32()?, fps: c.u32()? },
            T_VIDEO => {
                let pts_us = c.u64()?;
                let key = c.u8()? != 0;
                Message::Video { pts_us, key, data: c.rest().to_vec() }
            }
            T_POINTER => {
                let id = c.u8()?;
                let source = match c.u8()? {
                    0 => PointerSource::Mouse,
                    1 => PointerSource::Touch,
                    2 => PointerSource::Pen,
                    _ => return Err(bad("bad pointer source")),
                };
                let phase = match c.u8()? {
                    0 => PointerPhase::Move,
                    1 => PointerPhase::Down,
                    2 => PointerPhase::Up,
                    3 => PointerPhase::Hover,
                    4 => PointerPhase::Wheel,
                    _ => return Err(bad("bad pointer phase")),
                };
                Message::Pointer {
                    id,
                    source,
                    phase,
                    x: c.f32()?,
                    y: c.f32()?,
                    pressure: c.f32()?,
                    buttons: c.u8()?,
                    tilt_x: c.u8()? as i8,
                    tilt_y: c.u8()? as i8,
                    wheel_x: c.f32()?,
                    wheel_y: c.f32()?,
                }
            }
            T_KEY => Message::Key { code: c.u16()?, down: c.u8()? != 0 },
            T_PING => Message::Ping(c.u64()?),
            T_PONG => Message::Pong(c.u64()?),
            T_BYE => {
                let n = c.u16()? as usize;
                if n > MAX_NAME {
                    return Err(bad("reason too long"));
                }
                Message::Bye { reason: String::from_utf8_lossy(c.take(n)?).into_owned() }
            }
            _ => return Err(bad("unknown message type")),
        };
        Ok(m)
    }
}

/// Write one message and flush.
pub fn write_message<W: Write>(w: &mut W, m: &Message) -> io::Result<()> {
    w.write_all(&m.encode())?;
    w.flush()
}

/// Read one message. Returns `UnexpectedEof` when the peer closes cleanly.
pub fn read_message<R: Read>(r: &mut R) -> io::Result<Message> {
    let mut len = [0u8; 4];
    r.read_exact(&mut len)?;
    let len = u32::from_be_bytes(len) as usize;
    if len == 0 || len > MAX_FRAME {
        return Err(bad("frame length out of range"));
    }
    let mut body = vec![0u8; len];
    r.read_exact(&mut body)?;
    Message::decode(&body)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn roundtrip(m: Message) {
        let bytes = m.encode();
        let got = read_message(&mut &bytes[..]).unwrap();
        assert_eq!(got, m);
    }

    #[test]
    fn all_messages_roundtrip() {
        roundtrip(Message::Hello {
            version: PROTOCOL_VERSION,
            width: 2560,
            height: 1600,
            refresh_hz: 120,
            name: "laptop".into(),
        });
        roundtrip(Message::Config { width: 1920, height: 1080, fps: 60 });
        roundtrip(Message::Bye { reason: "ended from the web UI".into() });
        roundtrip(Message::Video { pts_us: 123456789, key: true, data: vec![0, 0, 0, 1, 0x65, 9] });
        roundtrip(Message::Pointer {
            id: 3,
            source: PointerSource::Pen,
            phase: PointerPhase::Move,
            x: 0.25,
            y: 0.75,
            pressure: 0.5,
            buttons: 9,
            tilt_x: -30,
            tilt_y: 45,
            wheel_x: 0.0,
            wheel_y: -1.5,
        });
        roundtrip(Message::Key { code: 0x04, down: true });
        roundtrip(Message::Ping(7));
        roundtrip(Message::Pong(7));
    }

    #[test]
    fn rejects_oversized_and_truncated() {
        let mut b = ((MAX_FRAME + 1) as u32).to_be_bytes().to_vec();
        b.push(T_PING);
        assert!(read_message(&mut &b[..]).is_err());
        // Pointer frame cut short.
        let mut full = Message::Pointer {
            id: 0,
            source: PointerSource::Mouse,
            phase: PointerPhase::Down,
            x: 0.0,
            y: 0.0,
            pressure: 1.0,
            buttons: 1,
            tilt_x: 0,
            tilt_y: 0,
            wheel_x: 0.0,
            wheel_y: 0.0,
        }
        .encode();
        full.truncate(full.len() - 3);
        let len = (full.len() - 4) as u32;
        full[..4].copy_from_slice(&len.to_be_bytes());
        assert!(read_message(&mut &full[..]).is_err());
    }

    #[test]
    fn rejects_unknown_type_and_bad_enum() {
        let b = [0, 0, 0, 1, 99];
        assert!(read_message(&mut &b[..]).is_err());
        let b = [0, 0, 0, 3, T_POINTER, 0, 9];
        assert!(read_message(&mut &b[..]).is_err());
    }
}
