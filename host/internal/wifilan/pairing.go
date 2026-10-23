package wifilan

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/1Password/srp"
)

var ErrPairing = errors.New("Wi-Fi pairing failed or expired; reopen pairing locally and verify the code")

const pairingPublicSize = 384

type PairingChallenge struct {
	Salt   [16]byte
	Nonce  [32]byte
	Public [pairingPublicSize]byte
}

// PairingServer is single-owner, single-attempt enrollment state, not a session
// authenticator. A successful proof still requires explicit local confirmation.
type PairingServer struct {
	state     *srp.SRP
	challenge PairingChallenge
	host      [32]byte
	tablet    [32]byte
	expires   time.Time
	used      bool
	confirmed bool
}

var pairingGroupOnce sync.Once

func pairingGroup() *srp.Group {
	group := srp.KnownGroups[srp.RFC5054Group3072]
	// Upstream caches k in the shared group. Initialize before concurrent use.
	pairingGroupOnce.Do(func() { group.LittleK(srp.Hash.Sha256Name) })
	return group
}

func NewPairingServer(code string, hostSPKI, tabletSPKI [32]byte, now time.Time) (*PairingServer, PairingChallenge, error) {
	if len(code) != 8 || hostSPKI == [32]byte{} || tabletSPKI == [32]byte{} || now.IsZero() {
		return nil, PairingChallenge{}, ErrPairing
	}
	for _, digit := range []byte(code) {
		if digit < '0' || digit > '9' {
			return nil, PairingChallenge{}, ErrPairing
		}
	}
	server := &PairingServer{host: hostSPKI, tablet: tabletSPKI, expires: now.Add(120 * time.Second)}
	if _, err := rand.Read(server.challenge.Salt[:]); err != nil {
		return nil, PairingChallenge{}, err
	}
	if _, err := rand.Read(server.challenge.Nonce[:]); err != nil {
		return nil, PairingChallenge{}, err
	}
	inner := sha256.Sum256([]byte("DrawSplay-WiFi-v2:" + code))
	passwordHash := sha256.New()
	passwordHash.Write(server.challenge.Salt[:])
	passwordHash.Write(inner[:])
	x := new(big.Int).SetBytes(passwordHash.Sum(nil))
	client := srp.NewClientStd(pairingGroup(), x)
	x.SetInt64(0)
	if client == nil {
		return nil, PairingChallenge{}, ErrPairing
	}
	verifier, err := client.Verifier()
	if err != nil {
		return nil, PairingChallenge{}, err
	}
	server.state = srp.NewServerStd(pairingGroup(), verifier)
	if server.state == nil {
		return nil, PairingChallenge{}, ErrPairing
	}
	server.state.EphemeralPublic().FillBytes(server.challenge.Public[:])
	return server, server.challenge, nil
}

// Confirm verifies the tablet proof first; sending a host proof before this
// would permit an active peer to test code guesses offline.
func (s *PairingServer) Confirm(public, proof []byte, now time.Time) ([]byte, error) {
	if s.used {
		return nil, ErrPairing
	}
	s.used = true
	defer func() { s.state = nil }()
	if now.IsZero() || !now.Before(s.expires) || len(public) != pairingPublicSize || len(proof) != sha256.Size {
		return nil, ErrPairing
	}
	value := new(big.Int).SetBytes(public)
	if value.Cmp(big.NewInt(1)) <= 0 || value.Cmp(pairingGroup().N()) >= 0 {
		return nil, ErrPairing
	}
	if err := s.state.SetOthersPublic(value); err != nil {
		return nil, ErrPairing
	}
	key, err := s.state.Key()
	if err != nil {
		return nil, ErrPairing
	}
	defer clear(key)
	transcript := s.transcript(public)
	if !hmac.Equal(pairingProof(key, "tablet confirms\x00", transcript), proof) {
		return nil, ErrPairing
	}
	s.confirmed = true
	return pairingProof(key, "host confirms\x00", transcript), nil
}

func (s *PairingServer) Close() {
	s.used = true
	s.state = nil
}

// PairingInvitation is single-owner and must be opened by a local UI action.
// Network peers cannot renew it or reset the global five-attempt allowance.
type PairingInvitation struct {
	code    string
	started time.Time
	expires time.Time
	active  *PairingServer
	tries   int
	closed  bool
}

func NewPairingInvitation(now time.Time) (*PairingInvitation, error) {
	if now.IsZero() {
		return nil, ErrPairing
	}
	number, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return nil, err
	}
	return &PairingInvitation{
		code: fmt.Sprintf("%08d", number), started: now, expires: now.Add(120 * time.Second),
	}, nil
}

func (i *PairingInvitation) Code() string { return i.code }

func (i *PairingInvitation) Begin(host, tablet [32]byte, now time.Time) (*PairingServer, PairingChallenge, error) {
	if i.closed || i.tries >= 5 || now.Before(i.started) || !now.Before(i.expires) ||
		(i.active != nil && (!i.active.used || i.active.confirmed)) {
		return nil, PairingChallenge{}, ErrPairing
	}
	i.tries++
	server, challenge, err := NewPairingServer(i.code, host, tablet, now)
	if err != nil {
		return nil, PairingChallenge{}, err
	}
	server.expires = i.expires
	i.active = server
	return server, challenge, nil
}

func (i *PairingInvitation) Close() {
	i.closed = true
	i.code = ""
	if i.active != nil {
		i.active.Close()
	}
}

func (s *PairingServer) transcript(public []byte) []byte {
	const domain = "DrawSplay numeric pairing\x00"
	data := make([]byte, len(domain)+4+32+32+32+16+2*pairingPublicSize)
	offset := copy(data, domain)
	binary.BigEndian.PutUint32(data[offset:], 2)
	offset += 4
	for _, part := range [][]byte{s.host[:], s.tablet[:], s.challenge.Nonce[:], s.challenge.Salt[:], public, s.challenge.Public[:]} {
		offset += copy(data[offset:], part)
	}
	return data
}

func pairingProof(key []byte, role string, transcript []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(role))
	mac.Write(transcript)
	return mac.Sum(nil)
}
