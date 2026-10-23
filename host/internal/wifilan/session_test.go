package wifilan

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func sessionHelloFixture() []byte {
	// Literal major-2 tablet hello: H264 baseline, 1920x1200@60, level4.2,
	// 1MiB AU, 4MiB queue, 3 slots, stats1, low-latency feature.
	raw := make([]byte, 96)
	copy(raw, []byte{
		0, 2, 0, 2, 1, 0, 0, 1,
		7, 128, 4, 176, 0, 60, 0, 42,
		0, 16, 0, 0, 0, 64, 0, 0,
		0, 3, 0, 1, 0, 0, 0, 1,
	})
	for i := 32; i < 64; i++ {
		raw[i] = 7
	}
	for i := 64; i < 96; i++ {
		raw[i] = 9
	}
	return raw
}

func TestSessionHelloValidatesAuthenticatedRoleIdentityAndResourceLimits(t *testing.T) {
	pin := [32]byte{}
	for i := range pin {
		pin[i] = 7
	}
	valid := sessionHelloFixture()
	hello, err := DecodeSessionHello(valid, SessionRoleTablet, pin)
	if err != nil || hello.MaxWidth != 1920 || hello.MaxHeight != 1200 || hello.MaxFPS != 60 || hello.QueueSlots != 3 {
		t.Fatalf("valid tablet hello rejected: %+v %v", hello, err)
	}
	var encoded [96]byte
	if err := hello.Encode(encoded[:]); err != nil || !bytes.Equal(encoded[:], valid) {
		t.Fatalf("hello wire shape changed: %x %v", encoded, err)
	}
	for _, mode := range []string{"truncated", "oversized", "old-version", "new-version", "role", "reserved", "identity", "nonce", "codec", "dimensions", "fps", "au", "queue", "slots", "stats", "level"} {
		t.Run(mode, func(t *testing.T) {
			raw := append([]byte{}, valid...)
			switch mode {
			case "truncated":
				raw = raw[:95]
			case "oversized":
				raw = append(raw, 0)
			case "old-version":
				raw[3] = 1
			case "new-version":
				raw[1] = 3
			case "role":
				raw[4] = 2
			case "reserved":
				raw[5] = 1
			case "identity":
				raw[32] ^= 1
			case "nonce":
				clear(raw[64:])
			case "codec":
				raw[7] = 0
			case "dimensions":
				raw[8], raw[9] = 0, 0
			case "fps":
				raw[13] = 0
			case "au":
				binary.BigEndian.PutUint32(raw[16:], 1024*1024+1)
			case "queue":
				binary.BigEndian.PutUint32(raw[20:], 1024*1024)
			case "slots":
				raw[25] = 255
			case "stats":
				raw[27] = 2
			case "level":
				raw[15] = 255
			}
			if _, err := DecodeSessionHello(raw, SessionRoleTablet, pin); err == nil {
				t.Fatal("invalid, unbound or unsupported capabilities accepted")
			}
		})
	}
}

func TestSessionNegotiationNeverExceedsEitherPeersAllocationBudget(t *testing.T) {
	pin := [32]byte{}
	for i := range pin {
		pin[i] = 7
	}
	tablet, err := DecodeSessionHello(sessionHelloFixture(), SessionRoleTablet, pin)
	if err != nil {
		t.Fatal(err)
	}
	host := tablet
	host.Role = SessionRoleHost
	host.MaxWidth, host.MaxHeight, host.MaxFPS = 1280, 720, 30
	host.MaxAccessUnit, host.QueueBytes, host.QueueSlots = 256*1024, 512*1024, 2
	host.Features = 0
	selected, err := NegotiateSession(host, tablet)
	if err != nil {
		t.Fatal(err)
	}
	if selected.MaxWidth != 1280 || selected.MaxHeight != 720 || selected.MaxFPS != 30 ||
		selected.MaxAccessUnit != 256*1024 || selected.QueueSlots != 2 || selected.QueueBytes != 512*1024 || selected.Features != 0 {
		t.Fatalf("negotiation exceeded one peer's resource bounds: %+v", selected)
	}
	if _, err := NegotiateSession(tablet, host); err == nil {
		t.Fatal("reversed channel roles negotiated")
	}
}

func TestSessionVideoAttachmentIsIdentityBoundExpiringAndSingleUse(t *testing.T) {
	now := time.Now()
	peer := [32]byte{7}
	for _, mode := range []string{"correct", "identity", "session", "ticket", "epoch", "expired", "before-start", "closed"} {
		t.Run(mode, func(t *testing.T) {
			var registry SessionRegistry
			binding, err := registry.NewSession(peer, now)
			if err != nil {
				t.Fatal(err)
			}
			offer := binding.Offer()
			if offer.Epoch == 0 || offer.ID == [16]byte{} || offer.Ticket == [32]byte{} {
				t.Fatal("session generation has missing identity, epoch or attachment credential")
			}
			pin, when := peer, now.Add(time.Second)
			switch mode {
			case "identity":
				pin[0] ^= 1
			case "session":
				offer.ID[0] ^= 1
			case "ticket":
				offer.Ticket[0] ^= 1
			case "epoch":
				offer.Epoch++
			case "expired":
				when = now.Add(5 * time.Second)
			case "before-start":
				when = now.Add(-time.Second)
			case "closed":
				binding.Close()
			}
			err = binding.Attach(pin, offer, when)
			if (err == nil) != (mode == "correct") {
				t.Fatalf("unexpected video attachment admission: %v", err)
			}
			if err := binding.Attach(peer, binding.Offer(), now.Add(2*time.Second)); err == nil {
				t.Fatal("video attachment ticket was reusable")
			}
		})
	}
}

func TestSessionReconnectGenerationsAreFreshAndRegistryIsBounded(t *testing.T) {
	var registry SessionRegistry
	peer := [32]byte{7}
	var previous SessionOffer
	for i := 0; i < 100; i++ {
		binding, err := registry.NewSession(peer, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		current := binding.Offer()
		if current.Epoch == previous.Epoch || current.ID == previous.ID || current.Ticket == previous.Ticket {
			t.Fatal("reconnect reused a session generation or attachment ticket")
		}
		previous = current
		binding.Close()
	}
	if registry.count != 64 {
		t.Fatal("recent generation registry is not bounded to 64 entries")
	}
	if _, err := registry.NewSession([32]byte{}, time.Now()); err == nil {
		t.Fatal("unbound identity created session credentials")
	}
}

func TestSessionUninitializedBindingCannotAuthorizeVideo(t *testing.T) {
	if err := new(SessionBinding).Attach([32]byte{}, SessionOffer{}, time.Time{}); err == nil {
		t.Fatal("uninitialized binding accepted an unauthenticated zero-credential attachment")
	}
}

func TestSessionRegistryRejectsConcurrentOwnersAndRandomCollisions(t *testing.T) {
	var registry SessionRegistry
	random := bytes.Repeat([]byte{7}, 56)
	first, err := registry.newSession([32]byte{7}, time.Now(), bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.NewSession([32]byte{8}, time.Now()); err == nil {
		t.Fatal("second active control owner was admitted")
	}
	first.Close()
	if _, err := registry.newSession([32]byte{7}, time.Now(), bytes.NewReader(bytes.Repeat(random, 4))); err == nil {
		t.Fatal("recent session identity/epoch collision admitted")
	}
	if _, err := registry.newSession([32]byte{7}, time.Now(), bytes.NewReader(make([]byte, 56*4))); err == nil {
		t.Fatal("zero credentials admitted")
	}
	if _, err := registry.newSession([32]byte{7}, time.Now(), bytes.NewReader([]byte{1})); err == nil {
		t.Fatal("entropy failure returned a success-shaped session")
	}
	if registry.count != 1 {
		t.Fatal("failed admissions changed recent session state")
	}
}
