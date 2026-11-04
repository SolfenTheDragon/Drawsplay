//! Developer tool: connects to a dd-host like a client and replays scripted pen, touch and
//! modifier-key input, so host injection can be tested without a touchscreen.
//!
//!   cargo run -p dd-client --example send_input -- HOST:PORT
//!
//! Uses the real pairing handshake and trusts the host unconditionally (testing only).

use dd_proto::identity::{config_dir, load_or_create_identity};
use dd_proto::secure::handshake;
use dd_proto::{read_message, write_message, Message, PointerPhase, PointerSource, PROTOCOL_VERSION};
use std::net::TcpStream;
use std::thread::sleep;
use std::time::Duration;

#[allow(clippy::too_many_arguments)]
fn pointer(
    id: u8,
    source: PointerSource,
    phase: PointerPhase,
    x: f32,
    y: f32,
    pressure: f32,
    buttons: u8,
    tilt: i8,
) -> Message {
    Message::Pointer { id, source, phase, x, y, pressure, buttons, tilt_x: tilt, tilt_y: 0, wheel_x: 0.0, wheel_y: 0.0 }
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let host = std::env::args().nth(1).ok_or("usage: send_input HOST:PORT")?;
    let mut s = TcpStream::connect(&host)?;
    s.set_nodelay(true)?;
    let id = load_or_create_identity(&config_dir("client")?)?;
    let sess = handshake(&mut s, &id, true)?;
    let mut r = sess.reader(s.try_clone()?);
    let mut w = sess.writer(s);
    write_message(
        &mut w,
        &Message::Hello {
            version: PROTOCOL_VERSION,
            width: 1280,
            height: 800,
            refresh_hz: 30,
            name: "send_input".into(),
        },
    )?;
    match read_message(&mut r)? {
        Message::Config { width, height, .. } => println!("host streams {width}x{height}"),
        other => return Err(format!("unexpected {other:?}").into()),
    }
    // Keep reading so the host's video writes never block.
    std::thread::spawn(move || while read_message(&mut r).is_ok() {});
    // Time for the tester to attach monitors to the devices the host just created.
    let delay = std::env::var("SEND_INPUT_DELAY_MS").ok().and_then(|v| v.parse().ok()).unwrap_or(1500);
    sleep(Duration::from_millis(delay));

    println!("pen stroke");
    let steps = 20;
    for i in (0..=steps).chain(0..=steps) {
        let t = i as f32 / steps as f32;
        let phase = match i {
            0 => PointerPhase::Down,
            n if n == steps => PointerPhase::Up,
            _ => PointerPhase::Move,
        };
        write_message(
            &mut w,
            &pointer(
                1,
                PointerSource::Pen,
                phase,
                0.3 + 0.4 * t,
                0.5 + 0.1 * (t * std::f32::consts::TAU).sin(),
                t,
                1,
                (t * 40.0) as i8,
            ),
        )?;
        sleep(Duration::from_millis(20));
    }
    println!("two-finger touch");
    for (id, x) in [(1u8, 0.4f32), (2, 0.6)] {
        write_message(&mut w, &pointer(id, PointerSource::Touch, PointerPhase::Down, x, 0.5, 1.0, 0, 0))?;
    }
    for i in 1..=10 {
        let d = i as f32 * 0.01;
        write_message(&mut w, &pointer(1, PointerSource::Touch, PointerPhase::Move, 0.4 - d, 0.5, 1.0, 0, 0))?;
        write_message(&mut w, &pointer(2, PointerSource::Touch, PointerPhase::Move, 0.6 + d, 0.5, 1.0, 0, 0))?;
        sleep(Duration::from_millis(20));
    }
    for id in [1u8, 2] {
        write_message(&mut w, &pointer(id, PointerSource::Touch, PointerPhase::Up, 0.5, 0.5, 0.0, 0, 0))?;
    }
    println!("shift key");
    write_message(&mut w, &Message::Key { code: 0xE1, down: true })?;
    sleep(Duration::from_millis(100));
    write_message(&mut w, &Message::Key { code: 0xE1, down: false })?;
    sleep(Duration::from_millis(500));
    println!("done");
    Ok(())
}
