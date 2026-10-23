package wifilan

import (
	"bytes"
	"testing"
)

func TestNegotiationKeepsVideoStatsOnlyWhenBothPeersOfferIt(t *testing.T) {
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
	host.Features, tablet.Features = FeatureLowLatency|FeatureVideoStats, FeatureLowLatency|FeatureVideoStats
	both, err := NegotiateSession(host, tablet)
	if err != nil || both.Features != FeatureLowLatency|FeatureVideoStats {
		t.Fatalf("features %#x err %v", both.Features, err)
	}
	tablet.Features = FeatureLowLatency
	old, err := NegotiateSession(host, tablet)
	if err != nil || old.Features != FeatureLowLatency {
		t.Fatalf("an older tablet must not receive stats: %#x %v", old.Features, err)
	}
}

func TestVideoStatsRecordRoundTripsAndRejectsOtherRecords(t *testing.T) {
	body := []byte(`{"type":"stats","framesReceived":60}`)
	raw, err := EncodeVideoStats(body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeVideoStats(raw)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("got %q %v", got, err)
	}
	for _, bad := range [][]byte{
		{8, 0, 0, 1},                        // empty JSON
		append([]byte{7, 0, 0, 1}, body...), // another subtype
		append([]byte{8, 0, 0, 2}, body...), // unknown schema
		append([]byte{8, 1, 0, 1}, body...), // reserved byte set
		append([]byte{8, 0, 0, 1}, []byte("{\"type\":\"stats\"}\n")...),
	} {
		if _, err := DecodeVideoStats(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := EncodeVideoStats(make([]byte, MaxControlSize)); err == nil {
		t.Fatal("oversized stats encoded")
	}
}
