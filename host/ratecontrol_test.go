package main

import (
	"testing"
	"time"
)

// testClock is a manually advanced clock for rate-controller tests.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestRC(top string) (*rateController, *testClock) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	rc := newRateController(top)
	rc.now = clock.now
	rc.cleanSince = clock.t
	return rc, clock
}

func TestRateLadderConstruction(t *testing.T) {
	rc := newRateController("8M")
	if rc.ladder[0] != 8000 {
		t.Fatalf("top rung = %d, want 8000", rc.ladder[0])
	}
	for i := 1; i < len(rc.ladder); i++ {
		if rc.ladder[i] >= rc.ladder[i-1] {
			t.Errorf("ladder not descending at %d: %v", i, rc.ladder)
		}
	}
	if last := rc.ladder[len(rc.ladder)-1]; last < minLadderKbps {
		t.Errorf("ladder floor %d below minimum %d", last, minLadderKbps)
	}
	if rc.current() != "8000k" {
		t.Errorf("current() = %s, want 8000k", rc.current())
	}
}

func TestRateLadderTinyBitrate(t *testing.T) {
	rc := newRateController("100k")
	if len(rc.ladder) != 1 || rc.ladder[0] != minLadderKbps {
		t.Errorf("tiny bitrate ladder = %v, want single floor rung", rc.ladder)
	}
	if rc.onCongestion() {
		t.Error("onCongestion at the floor should not request a restart")
	}
}

func TestRateLadderCappedAtLinkBudget(t *testing.T) {
	rc := newRateController("200M")
	if rc.ladder[0] != maxLadderKbps {
		t.Errorf("top rung = %d, want cap %d", rc.ladder[0], maxLadderKbps)
	}
	if got := scaledBitrateKbps("24M", 3840, 2160); got != maxLadderKbps {
		t.Errorf("scaledBitrateKbps 4K = %d, want cap %d", got, maxLadderKbps)
	}
}

func TestStepDownAndCooldown(t *testing.T) {
	rc, clock := newTestRC("8M")
	if !rc.onCongestion() {
		t.Fatal("first congestion should step down")
	}
	if rc.idx != 1 {
		t.Fatalf("idx = %d, want 1", rc.idx)
	}
	// Within cooldown: no further step.
	clock.advance(1 * time.Second)
	if rc.onCongestion() {
		t.Error("congestion within cooldown should not step again")
	}
	// After cooldown: steps again.
	clock.advance(stepDownCooldown)
	if !rc.onCongestion() {
		t.Error("congestion after cooldown should step down")
	}
}

func TestStepUpRequiresCleanInterval(t *testing.T) {
	rc, clock := newTestRC("8M")
	rc.onCongestion() // idx 1
	if rc.shouldStepUp() {
		t.Error("immediate step up not allowed")
	}
	clock.advance(stepUpAfter + time.Second)
	if !rc.shouldStepUp() {
		t.Error("step up expected after clean interval")
	}
	if rc.idx != 0 {
		t.Errorf("idx = %d, want 0", rc.idx)
	}
	if rc.shouldStepUp() {
		t.Error("no step up above the top rung")
	}
}

func TestQuickRelapsePenalty(t *testing.T) {
	rc, clock := newTestRC("8M")
	rc.onCongestion() // idx 1
	clock.advance(stepUpAfter + time.Second)
	if !rc.shouldStepUp() { // probe idx 0
		t.Fatal("expected step up")
	}
	clock.advance(stepDownCooldown + time.Second) // beyond down-cooldown, within relapse window? no —
	// quickRelapse (15s) > stepDownCooldown (5s): still a quick relapse.
	if !rc.onCongestion() {
		t.Fatal("relapse congestion should step down")
	}
	// Clean interval passes, but the penalty blocks the re-probe.
	clock.advance(stepUpAfter + time.Second)
	if rc.shouldStepUp() {
		t.Error("step up during relapse penalty should be blocked")
	}
	clock.advance(stepUpBackoff)
	if !rc.shouldStepUp() {
		t.Error("step up after penalty expiry should be allowed")
	}
}

// queueStep simulates one queue-only congestion event after the given time,
// with the stream having sent kbps worth of traffic during that time.
func queueStep(rc *rateController, clock *testClock, bytes *int64, gap time.Duration, sentKbps int) bool {
	clock.advance(gap)
	*bytes += int64(float64(sentKbps) * 1000 / 8 * gap.Seconds())
	return rc.onQueueCongestion(*bytes)
}

func TestQueueCongestionFutileStepsMarkDecoderBound(t *testing.T) {
	rc, clock := newTestRC("8M")
	var bytes int64
	if !queueStep(rc, clock, &bytes, 2*time.Second, 100) || rc.idx != 1 {
		t.Fatalf("first step: idx=%d", rc.idx)
	}
	if !queueStep(rc, clock, &bytes, 5*time.Second, 100) || rc.idx != 2 {
		t.Fatalf("second step: idx=%d", rc.idx)
	}
	if !queueStep(rc, clock, &bytes, 5*time.Second, 100) {
		t.Fatal("restore should restart")
	}
	if !rc.decoderBound() || rc.idx != 0 {
		t.Fatalf("bound=%v idx=%d, want bound at rung 0", rc.decoderBound(), rc.idx)
	}
	if queueStep(rc, clock, &bytes, 5*time.Second, 100) {
		t.Fatal("queue spikes must be ignored once decoder-bound")
	}
	clock.advance(2 * stepUpAfter)
	if rc.shouldStepUp() {
		t.Fatal("no step-ups while decoder-bound")
	}
}

func TestQueueCongestionBusyLinkIsNotDecoderBound(t *testing.T) {
	rc, clock := newTestRC("8M")
	var bytes int64
	for i := 0; i < 4; i++ {
		queueStep(rc, clock, &bytes, 5*time.Second, rc.ladder[rc.idx])
	}
	if rc.decoderBound() {
		t.Fatal("a stream filling its rung is link-bound")
	}
	if rc.idx < 3 {
		t.Fatalf("idx=%d, want continued step-downs", rc.idx)
	}
}

func TestQueueCongestionRecurrenceWindowResetsSeries(t *testing.T) {
	rc, clock := newTestRC("8M")
	var bytes int64
	queueStep(rc, clock, &bytes, 2*time.Second, 100)
	queueStep(rc, clock, &bytes, 5*time.Second, 100)
	queueStep(rc, clock, &bytes, queueRecurrence+time.Second, 100)
	if rc.decoderBound() {
		t.Fatal("isolated congestion after a quiet period must not mark decoder-bound")
	}
}
