package wifilan

import "encoding/binary"

func (s SessionOffer) valid() bool {
	return s.ID != [16]byte{} && s.Ticket != [32]byte{} && s.Epoch != 0
}

// Encode creates control subtype 1. The outer envelope carries this epoch;
// its initial sequence is 1. Tickets must never be sent outside mutual TLS.
func (s SessionOffer) Encode(destination []byte) error {
	if len(destination) != 56 || !s.valid() {
		return ErrVideoAttach
	}
	clear(destination)
	destination[0], destination[3] = 1, 1
	copy(destination[4:20], s.ID[:])
	copy(destination[20:52], s.Ticket[:])
	binary.BigEndian.PutUint32(destination[52:], 5000)
	return nil
}

func DecodeSessionOffer(raw []byte, epoch uint64) (SessionOffer, error) {
	if len(raw) != 56 || raw[0] != 1 || raw[1] != 0 || binary.BigEndian.Uint16(raw[2:]) != 1 ||
		binary.BigEndian.Uint32(raw[52:]) != 5000 {
		return SessionOffer{}, ErrVideoAttach
	}
	offer := SessionOffer{ID: [16]byte(raw[4:20]), Epoch: epoch, Ticket: [32]byte(raw[20:52])}
	if !offer.valid() {
		return SessionOffer{}, ErrVideoAttach
	}
	return offer, nil
}

// EncodeAttach is sent on the separate video mTLS channel, never on control.
// Its initial outer header is FrameAttach, the offered epoch, sequence 1.
func (s SessionOffer) EncodeAttach(destination []byte) error {
	if len(destination) != 52 || !s.valid() {
		return ErrVideoAttach
	}
	clear(destination)
	destination[0], destination[3] = 2, 1
	copy(destination[4:20], s.ID[:])
	copy(destination[20:], s.Ticket[:])
	return nil
}

func DecodeVideoAttach(raw []byte, epoch uint64) (SessionOffer, error) {
	if len(raw) != 52 || raw[0] != 2 || raw[1] != 0 || binary.BigEndian.Uint16(raw[2:]) != 1 {
		return SessionOffer{}, ErrVideoAttach
	}
	offer := SessionOffer{ID: [16]byte(raw[4:20]), Epoch: epoch, Ticket: [32]byte(raw[20:])}
	if !offer.valid() {
		return SessionOffer{}, ErrVideoAttach
	}
	return offer, nil
}
