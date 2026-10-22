package wifilan

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	SessionRoleTablet byte = 1
	SessionRoleHost   byte = 2
	SessionHelloSize       = 96
)

var (
	ErrSessionHello  = errors.New("incompatible Wi-Fi capabilities, identity or memory limits; update both endpoints or lower the requested mode")
	ErrVideoAttach   = errors.New("Wi-Fi video attachment is stale, expired, reused or belongs to another identity; reconnect control first")
	ErrSessionActive = errors.New("a Wi-Fi control session is still active; close it before reconnecting")
)

// SessionHello is exchanged only after paired mutual TLS. Dimensions/FPS are
// independent maxima, not a promise that their product fits the codec level.
type SessionHello struct {
	MinMajor, MaxMajor          uint16
	Role                        byte
	CodecMask                   uint16
	MaxWidth, MaxHeight, MaxFPS uint16
	H264Level                   uint16
	MaxAccessUnit, QueueBytes   uint32
	QueueSlots, StatsVersion    uint16
	Features                    uint32
	SPKI, Nonce                 [32]byte
}

func (h SessionHello) validate() error {
	level := h.H264Level == 31 || h.H264Level == 32 || h.H264Level == 40 ||
		h.H264Level == 41 || h.H264Level == 42 || h.H264Level == 50 ||
		h.H264Level == 51 || h.H264Level == 52
	if h.MinMajor == 0 || h.MinMajor > 2 || h.MaxMajor < 2 || h.MinMajor > h.MaxMajor ||
		(h.Role != SessionRoleTablet && h.Role != SessionRoleHost) || h.CodecMask&1 == 0 ||
		h.MaxWidth < 320 || h.MaxWidth > 8192 || h.MaxHeight < 320 || h.MaxHeight > 8192 ||
		h.MaxFPS == 0 || h.MaxFPS > 120 || !level || h.MaxAccessUnit < 64*1024 || h.MaxAccessUnit > MaxAccessUnit ||
		h.QueueSlots < 2 || h.QueueSlots > 4 || h.QueueBytes > 8*1024*1024 ||
		uint64(h.MaxAccessUnit)*uint64(h.QueueSlots) > uint64(h.QueueBytes) ||
		h.StatsVersion != 1 || h.SPKI == [32]byte{} || h.Nonce == [32]byte{} {
		return ErrSessionHello
	}
	return nil
}

func (h SessionHello) Encode(destination []byte) error {
	if len(destination) != SessionHelloSize {
		return ErrSessionHello
	}
	if err := h.validate(); err != nil {
		return err
	}
	clear(destination)
	binary.BigEndian.PutUint16(destination[0:], h.MinMajor)
	binary.BigEndian.PutUint16(destination[2:], h.MaxMajor)
	destination[4] = h.Role
	binary.BigEndian.PutUint16(destination[6:], h.CodecMask)
	binary.BigEndian.PutUint16(destination[8:], h.MaxWidth)
	binary.BigEndian.PutUint16(destination[10:], h.MaxHeight)
	binary.BigEndian.PutUint16(destination[12:], h.MaxFPS)
	binary.BigEndian.PutUint16(destination[14:], h.H264Level)
	binary.BigEndian.PutUint32(destination[16:], h.MaxAccessUnit)
	binary.BigEndian.PutUint32(destination[20:], h.QueueBytes)
	binary.BigEndian.PutUint16(destination[24:], h.QueueSlots)
	binary.BigEndian.PutUint16(destination[26:], h.StatsVersion)
	binary.BigEndian.PutUint32(destination[28:], h.Features)
	copy(destination[32:64], h.SPKI[:])
	copy(destination[64:], h.Nonce[:])
	return nil
}

func DecodeSessionHello(raw []byte, role byte, authenticatedSPKI [32]byte) (SessionHello, error) {
	if len(raw) != SessionHelloSize || raw[5] != 0 || (role != SessionRoleTablet && role != SessionRoleHost) {
		return SessionHello{}, ErrSessionHello
	}
	h := SessionHello{
		MinMajor: binary.BigEndian.Uint16(raw), MaxMajor: binary.BigEndian.Uint16(raw[2:]),
		Role: raw[4], CodecMask: binary.BigEndian.Uint16(raw[6:]),
		MaxWidth: binary.BigEndian.Uint16(raw[8:]), MaxHeight: binary.BigEndian.Uint16(raw[10:]),
		MaxFPS: binary.BigEndian.Uint16(raw[12:]), H264Level: binary.BigEndian.Uint16(raw[14:]),
		MaxAccessUnit: binary.BigEndian.Uint32(raw[16:]), QueueBytes: binary.BigEndian.Uint32(raw[20:]),
		QueueSlots: binary.BigEndian.Uint16(raw[24:]), StatsVersion: binary.BigEndian.Uint16(raw[26:]),
		Features: binary.BigEndian.Uint32(raw[28:]),
		SPKI:     [32]byte(raw[32:64]), Nonce: [32]byte(raw[64:]),
	}
	if err := h.validate(); err != nil {
		return SessionHello{}, err
	}
	if h.Role != role || h.SPKI != authenticatedSPKI {
		return SessionHello{}, ErrSessionHello
	}
	return h, nil
}

// NegotiateSession computes hard upper bounds; the capture owner must still
// choose a mode supported by the encoder, decoder and H.264 macroblock level.
func NegotiateSession(host, tablet SessionHello) (SessionHello, error) {
	if host.Role != SessionRoleHost || tablet.Role != SessionRoleTablet ||
		host.validate() != nil || tablet.validate() != nil {
		return SessionHello{}, ErrSessionHello
	}
	result := host
	result.MinMajor, result.MaxMajor = 2, 2
	result.CodecMask = 1
	result.MaxWidth, result.MaxHeight = min(host.MaxWidth, tablet.MaxWidth), min(host.MaxHeight, tablet.MaxHeight)
	result.MaxFPS, result.H264Level = min(host.MaxFPS, tablet.MaxFPS), min(host.H264Level, tablet.H264Level)
	result.MaxAccessUnit = min(host.MaxAccessUnit, tablet.MaxAccessUnit)
	result.QueueBytes, result.QueueSlots = min(host.QueueBytes, tablet.QueueBytes), min(host.QueueSlots, tablet.QueueSlots)
	result.Features = host.Features & tablet.Features & 1
	return result, result.validate()
}

type SessionOffer struct {
	ID     [16]byte
	Epoch  uint64
	Ticket [32]byte
}

// SessionBinding serializes attachment/revocation at connection time only.
// Call Close when control ends; owners must also close associated sockets.
type SessionBinding struct {
	mu      sync.Mutex
	offer   SessionOffer
	peer    [32]byte
	started time.Time
	used    bool
	closed  bool
}

func (s *SessionBinding) Offer() SessionOffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offer
}

func (s *SessionBinding) Attach(peer [32]byte, offer SessionOffer, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used || s.closed {
		return ErrVideoAttach
	}
	s.used = true
	expected := s.offer
	clear(s.offer.Ticket[:])
	if s.peer == [32]byte{} || s.started.IsZero() || now.IsZero() ||
		expected.ID == [16]byte{} || expected.Epoch == 0 || expected.Ticket == [32]byte{} ||
		now.Before(s.started) || !now.Before(s.started.Add(5*time.Second)) ||
		peer != s.peer || offer.Epoch != expected.Epoch || offer.ID != expected.ID ||
		!hmac.Equal(offer.Ticket[:], expected.Ticket[:]) {
		return ErrVideoAttach
	}
	return nil
}

func (s *SessionBinding) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed, s.used = true, true
	clear(s.offer.Ticket[:])
}

type sessionGeneration struct {
	id    [16]byte
	epoch uint64
}

// SessionRegistry is owned by the Wi-Fi connection coordinator, not USB.
// One active control session and 64 recent generations are retained.
type SessionRegistry struct {
	recent [64]sessionGeneration
	count  int
	next   int
	active *SessionBinding
}

func (r *SessionRegistry) NewSession(peer [32]byte, now time.Time) (*SessionBinding, error) {
	return r.newSession(peer, now, rand.Reader)
}

func (r *SessionRegistry) newSession(peer [32]byte, now time.Time, entropy io.Reader) (*SessionBinding, error) {
	if peer == [32]byte{} || now.IsZero() {
		return nil, ErrPairedIdentity
	}
	if r.active != nil {
		r.active.mu.Lock()
		closed := r.active.closed
		r.active.mu.Unlock()
		if !closed {
			return nil, ErrSessionActive
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		var random [56]byte
		if _, err := io.ReadFull(entropy, random[:]); err != nil {
			return nil, err
		}
		offer := SessionOffer{
			ID: [16]byte(random[:16]), Epoch: binary.BigEndian.Uint64(random[16:24]),
			Ticket: [32]byte(random[24:]),
		}
		clear(random[:])
		if offer.ID == [16]byte{} || offer.Epoch == 0 || offer.Ticket == [32]byte{} {
			continue
		}
		collision := false
		for _, recent := range r.recent[:r.count] {
			if recent.id == offer.ID || recent.epoch == offer.Epoch {
				collision = true
				break
			}
		}
		if collision {
			continue
		}
		r.recent[r.next] = sessionGeneration{id: offer.ID, epoch: offer.Epoch}
		r.next = (r.next + 1) % len(r.recent)
		r.count = min(r.count+1, len(r.recent))
		r.active = &SessionBinding{offer: offer, peer: peer, started: now}
		return r.active, nil
	}
	return nil, errors.New("could not generate a unique nonzero Wi-Fi session; no session admitted")
}
