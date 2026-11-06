package main

import "testing"

// bindTestIdentity binds a session that only carries an identity.
func bindTestIdentity(id uint64, ident clientIdentity) {
	bindClientSession(id, clientSession{Identity: ident})
}

func TestHelloBecomesOneNormalizedClientSession(t *testing.T) {
	c := newClientSession(packet{
		Device: "  SM-X920 ", Android: "14", ScreenWidth: 2960, ScreenHeight: 1848,
		VideoFeatures: []string{" Cursor1 ", "", "ack1"},
	})
	bindClientSession(77, c)
	defer unbindClientSession(77)
	got, ok := clientSessionByID(77)
	if !ok || got.Device != "SM-X920" || got.Identity.Platform != "android" || got.Identity.Type != "tablet" {
		t.Fatalf("session = %+v", got)
	}
	if !got.hasFeature(clientFeatureCursor) || !got.requestsKeyframes() || got.hasFeature("") {
		t.Fatalf("features = %v", got.Features)
	}
	if activeInputDevice() != "SM-X920" {
		t.Fatalf("active device = %q", activeInputDevice())
	}
	unbindClientSession(77)
	if activeInputDevice() != "" || activeClientIdentity().Platform != "" {
		t.Fatal("active session survived its connection")
	}
}

func TestEmptyHelloGetsPlaceholders(t *testing.T) {
	bindClientSession(78, newClientSession(packet{}))
	defer unbindClientSession(78)
	got, _ := clientSessionByID(78)
	if got.Device != unknownClientDevice || got.Identity.Platform != "unknown" || got.Identity.Type != "client" {
		t.Fatalf("session = %+v", got)
	}
}
