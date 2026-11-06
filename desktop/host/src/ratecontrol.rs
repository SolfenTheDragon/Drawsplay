//! Bitrate adaptation for one session, after the Go host's `ratecontrol.go`.
//!
//! The encoder runs in an ffmpeg process that cannot change its bitrate while it runs, so a change
//! restarts it at another rung of a ladder (each rung about 60% of the one above). Every restart
//! costs a short hiccup and a keyframe, so changes are damped:
//!
//! - Step down on congestion (a video write blocked for longer than [`SLOW_WRITE`]), but only when
//!   it is confirmed by a second slow write within [`CONFIRM_WINDOW`], not within [`SETTLE`] of the
//!   last (re)start (that hiccup is the restart itself), and at most once per [`STEP_DOWN_COOLDOWN`].
//! - Step up after [`STEP_UP_AFTER`] without congestion. Congestion within [`QUICK_RELAPSE`] of a
//!   step up blames it: that rung is not tried again for [`STEP_UP_BACKOFF`].
//!
//! The socket's send buffer is kept small (see `send_buffer_bytes`), so a link slower than the
//! stream shows up as blocked writes within a fraction of a second instead of seconds of video
//! queued in the kernel.

use std::time::{Duration, Instant};

pub const SLOW_WRITE: Duration = Duration::from_millis(300);
const CONFIRM_WINDOW: Duration = Duration::from_secs(2);
const SETTLE: Duration = Duration::from_secs(3);
const STEP_DOWN_COOLDOWN: Duration = Duration::from_millis(1500);
const STEP_UP_AFTER: Duration = Duration::from_secs(30);
const QUICK_RELAPSE: Duration = Duration::from_secs(15);
const STEP_UP_BACKOFF: Duration = Duration::from_secs(300);
/// Below this the picture is useless; the ladder also never drops below an eighth of its top.
const FLOOR_KBPS: u32 = 500;

pub struct RateController {
    ladder: Vec<u32>,
    idx: usize,
    started: Instant,
    last_change: Option<Instant>,
    last_step_up: Option<Instant>,
    hold_until: Option<Instant>,
    clean_since: Instant,
    last_slow: Option<Instant>,
}

impl RateController {
    pub fn new(top_kbps: u32, now: Instant) -> RateController {
        let top = top_kbps.max(FLOOR_KBPS);
        let floor = (top / 8).max(FLOOR_KBPS);
        let mut ladder = vec![top];
        let mut r = top;
        while r * 3 / 5 >= floor {
            r = r * 3 / 5;
            ladder.push(r);
        }
        RateController {
            ladder,
            idx: 0,
            started: now,
            last_change: None,
            last_step_up: None,
            hold_until: None,
            clean_since: now,
            last_slow: None,
        }
    }

    pub fn kbps(&self) -> u32 {
        self.ladder[self.idx]
    }

    /// The encoder was (re)started at `now`.
    pub fn restarted(&mut self, now: Instant) {
        self.started = now;
        self.last_slow = None;
    }

    /// A video write took `took`. Returns true when the caller should restart at [`Self::kbps`].
    pub fn on_write(&mut self, took: Duration, now: Instant) -> bool {
        if took < SLOW_WRITE {
            return false;
        }
        self.clean_since = now;
        if now.duration_since(self.started) < SETTLE {
            return false;
        }
        let confirmed = self.last_slow.is_some_and(|t| now.duration_since(t) <= CONFIRM_WINDOW);
        self.last_slow = Some(now);
        if !confirmed {
            return false;
        }
        if self.last_step_up.is_some_and(|t| now.duration_since(t) < QUICK_RELAPSE) {
            self.hold_until = Some(now + STEP_UP_BACKOFF);
        }
        if self.idx + 1 >= self.ladder.len()
            || self.last_change.is_some_and(|t| now.duration_since(t) < STEP_DOWN_COOLDOWN)
        {
            return false;
        }
        self.idx += 1;
        self.last_change = Some(now);
        self.last_slow = None;
        true
    }

    /// Called regularly; returns true when the caller should restart at the higher [`Self::kbps`].
    pub fn maybe_step_up(&mut self, now: Instant) -> bool {
        if self.idx == 0
            || now.duration_since(self.clean_since) < STEP_UP_AFTER
            || self.hold_until.is_some_and(|t| now < t)
        {
            return false;
        }
        self.idx -= 1;
        self.last_change = Some(now);
        self.last_step_up = Some(now);
        self.clean_since = now;
        true
    }
}

/// Kernel send buffer for a stream of `kbps`: about 100 ms of video (at least 64 KiB), so a slow link
/// blocks writes quickly instead of queueing seconds of video. Keyframes larger than this just take a
/// little longer to write.
pub fn send_buffer_bytes(kbps: u32) -> usize {
    ((kbps as usize) * 1000 / 8 / 10).max(64 * 1024)
}

#[cfg(test)]
mod tests {
    use super::*;

    const SLOW: Duration = Duration::from_millis(400);

    #[test]
    fn ladder_steps_by_sixty_percent_down_to_an_eighth() {
        let rc = RateController::new(12_000, Instant::now());
        assert_eq!(rc.ladder, vec![12_000, 7_200, 4_320, 2_592, 1_555]);
        assert_eq!(RateController::new(100, Instant::now()).ladder, vec![500]);
    }

    #[test]
    fn a_single_slow_write_or_one_during_settling_does_not_step_down() {
        let t0 = Instant::now();
        let mut rc = RateController::new(12_000, t0);
        // Right after the start: the restart hiccup.
        assert!(!rc.on_write(SLOW, t0 + Duration::from_secs(1)));
        assert!(!rc.on_write(SLOW, t0 + Duration::from_secs(2)));
        // Isolated slow writes, further apart than the confirmation window.
        assert!(!rc.on_write(SLOW, t0 + Duration::from_secs(5)));
        assert!(!rc.on_write(SLOW, t0 + Duration::from_secs(8)));
        assert_eq!(rc.kbps(), 12_000);
        assert!(!rc.on_write(Duration::from_millis(50), t0 + Duration::from_secs(9)));
    }

    #[test]
    fn sustained_congestion_walks_down_with_a_cooldown() {
        let t0 = Instant::now();
        let mut rc = RateController::new(12_000, t0);
        let at = |s: f64| t0 + Duration::from_secs_f64(s);
        assert!(!rc.on_write(SLOW, at(4.0)));
        assert!(rc.on_write(SLOW, at(4.5)));
        assert_eq!(rc.kbps(), 7_200);
        rc.restarted(at(4.6));
        // Settling after the restart, then confirmed again.
        assert!(!rc.on_write(SLOW, at(5.0)));
        assert!(!rc.on_write(SLOW, at(7.7)));
        assert!(rc.on_write(SLOW, at(8.0)));
        assert_eq!(rc.kbps(), 4_320);
    }

    #[test]
    fn steps_back_up_after_a_clean_period_and_backs_off_after_a_relapse() {
        let t0 = Instant::now();
        let mut rc = RateController::new(12_000, t0);
        let at = |s: u64| t0 + Duration::from_secs(s);
        rc.on_write(SLOW, at(4));
        assert!(rc.on_write(SLOW, at(5)));
        assert!(!rc.maybe_step_up(at(20)), "not clean for long enough");
        assert!(rc.maybe_step_up(at(36)));
        assert_eq!(rc.kbps(), 12_000);
        rc.restarted(at(36));
        // Congestion again right after probing: back down, and that rung is held off.
        rc.on_write(SLOW, at(40));
        assert!(rc.on_write(SLOW, at(41)));
        assert_eq!(rc.kbps(), 7_200);
        assert!(!rc.maybe_step_up(at(80)), "the relapsed rung waits out its backoff");
        assert!(rc.maybe_step_up(at(400)));
    }

    #[test]
    fn send_buffer_holds_about_a_tenth_of_a_second() {
        assert_eq!(send_buffer_bytes(12_000), 150_000);
        assert_eq!(send_buffer_bytes(1_000), 64 * 1024);
    }
}
