package wifilan

import (
	"bytes"
	"testing"
)

func TestSessionOfferAndAttachWireBindTheSameGenerationAndTicket(t *testing.T) {
	raw := make([]byte, 56)
	copy(raw, []byte{1, 0, 0, 1})
	for i := 4; i < 20; i++ {
		raw[i] = 3
	}
	for i := 20; i < 52; i++ {
		raw[i] = 5
	}
	copy(raw[52:], []byte{0, 0, 19, 136}) // 5000ms, never an absolute peer clock.
	offer, err := DecodeSessionOffer(raw, 99)
	if err != nil || offer.Epoch != 99 || offer.ID[0] != 3 || offer.Ticket[0] != 5 {
		t.Fatalf("valid offer rejected: %v", err)
	}
	var encoded [56]byte
	if err := offer.Encode(encoded[:]); err != nil || !bytes.Equal(encoded[:], raw) {
		t.Fatalf("offer wire format changed: %v", err)
	}
	var attach [52]byte
	if err := offer.EncodeAttach(attach[:]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(attach[:4], []byte{2, 0, 0, 1}) || !bytes.Equal(attach[4:], raw[4:52]) {
		t.Fatal("video attachment omitted or changed association credentials")
	}
	attached, err := DecodeVideoAttach(attach[:], 99)
	if err != nil || attached != offer {
		t.Fatalf("attachment did not preserve its session generation: %v", err)
	}
	for _, mode := range []string{"length", "kind", "reserved", "schema", "id", "ticket", "ttl", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			bad := append([]byte{}, raw...)
			epoch := uint64(99)
			switch mode {
			case "length":
				bad = bad[:55]
			case "kind":
				bad[0] = 2
			case "reserved":
				bad[1] = 1
			case "schema":
				bad[3] = 2
			case "id":
				clear(bad[4:20])
			case "ticket":
				clear(bad[20:52])
			case "ttl":
				bad[55]++
			case "epoch":
				epoch = 0
			}
			if _, err := DecodeSessionOffer(bad, epoch); err == nil {
				t.Fatal("invalid video association offer accepted")
			}
		})
	}
	if _, err := DecodeVideoAttach(raw, 99); err == nil {
		t.Fatal("control offer accepted as a video attach record")
	}
}
