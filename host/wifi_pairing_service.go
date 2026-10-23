package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"drawsplay/host/internal/wifilan"
)

const wifiPairingTimeout = 120 * time.Second

// pairingConn is the secured enrollment channel to the tablet.
type pairingConn interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}

type wifiPairingDeps struct {
	iface    func() string
	trustDir func() (string, error)
	// pause releases the paired runtime's trust-store lock; resume restores it.
	pause, resume func()
	browse        func(ctx context.Context, iface string, found func(wifilan.Candidate)) error
	authenticate  func(ctx context.Context, iface string, c wifilan.Candidate, store *wifilan.TrustStore, inv *wifilan.PairingInvitation) (pairingConn, [32]byte, error)
	timeout       time.Duration
}

// wifiPairingState is the JSON shown in the web UI host overview.
type wifiPairingState struct {
	// Phase is idle, waiting (code shown), approval, done or failed.
	Phase       string `json:"phase"`
	Code        string `json:"code,omitempty"`
	ExpiresAtMs int64  `json:"expiresAtMs,omitempty"`
	Tablet      string `json:"tablet,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
}

type wifiPairingService struct {
	deps wifiPairingDeps

	mu       sync.Mutex
	state    wifiPairingState
	running  bool
	cancel   context.CancelFunc
	decision chan bool
}

func newWiFiPairingService(deps wifiPairingDeps) *wifiPairingService {
	if deps.timeout <= 0 {
		deps.timeout = wifiPairingTimeout
	}
	return &wifiPairingService{deps: deps, state: wifiPairingState{Phase: "idle"}}
}

func (s *wifiPairingService) Snapshot() wifiPairingState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *wifiPairingService) set(state wifiPairingState) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

// Start begins one pairing attempt. replace permits replacing an already
// paired tablet and must come from an explicit user request.
func (s *wifiPairingService) Start(replace bool) error {
	iface := strings.TrimSpace(s.deps.iface())
	if iface == "" {
		return errors.New("choose a Wi-Fi network interface first")
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("a pairing attempt is already in progress")
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.deps.timeout)
	s.running = true
	s.cancel = cancel
	s.decision = make(chan bool, 1)
	decision := s.decision
	s.state = wifiPairingState{Phase: "waiting"}
	s.mu.Unlock()
	go func() {
		err := s.run(ctx, iface, replace, decision)
		cancel()
		s.mu.Lock()
		s.running = false
		cancelled := errors.Is(err, context.Canceled)
		switch {
		case err == nil:
		case cancelled:
			s.state = wifiPairingState{Phase: "idle"}
		default:
			log.Printf("Wi-Fi pairing failed: %v", err)
			s.state = wifiPairingState{Phase: "failed", Error: err.Error()}
		}
		s.mu.Unlock()
		if s.deps.resume != nil {
			s.deps.resume()
		}
	}()
	return nil
}

func (s *wifiPairingService) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *wifiPairingService) decide(approve bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Phase != "approval" {
		return errors.New("no tablet is waiting for approval")
	}
	select {
	case s.decision <- approve:
		return nil
	default:
		return errors.New("a decision was already submitted")
	}
}

func (s *wifiPairingService) Approve() error { return s.decide(true) }
func (s *wifiPairingService) Deny() error    { return s.decide(false) }

func (s *wifiPairingService) run(ctx context.Context, iface string, replace bool, decision <-chan bool) (result error) {
	dir, err := s.deps.trustDir()
	if err != nil {
		return err
	}
	if s.deps.pause != nil {
		s.deps.pause()
	}
	store, err := wifilan.OpenTrustStore(dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	if _, paired := store.Peer(); paired && !replace {
		return errors.New("a tablet is already paired; choose Pair another tablet to replace it")
	}
	invitation, err := wifilan.NewPairingInvitation(time.Now())
	if err != nil {
		return err
	}
	defer invitation.Close()
	deadline, _ := ctx.Deadline()
	s.set(wifiPairingState{Phase: "waiting", Code: invitation.Code(), ExpiresAtMs: deadline.UnixMilli()})

	candidate, err := s.findTablet(ctx, iface)
	if err != nil {
		return err
	}
	secured, pin, err := s.deps.authenticate(ctx, iface, candidate, store, invitation)
	if err != nil {
		return fmt.Errorf("pairing code was not accepted by the tablet: %w", err)
	}
	defer secured.Close()
	s.set(wifiPairingState{Phase: "approval", Tablet: candidate.Name, Fingerprint: fmt.Sprintf("%x", pin[:6]), ExpiresAtMs: deadline.UnixMilli()})

	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("pairing timed out before approval")
		}
		return ctx.Err()
	case approved := <-decision:
		if !approved {
			return errors.New("pairing denied on this computer; no trust was changed")
		}
	}
	if err := secured.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := secured.Write([]byte{1}); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(secured, ack[:]); err != nil || ack[0] != 1 {
		return fmt.Errorf("tablet did not confirm saved trust: %w", errors.Join(err, wifilan.ErrPairing))
	}
	if err := store.Pair(pin, candidate.Name); err != nil {
		return fmt.Errorf("tablet saved this host but host trust could not be saved; pair again: %w", err)
	}
	if _, err := secured.Write([]byte{2}); err != nil {
		return fmt.Errorf("host trust saved, but the tablet missed the final confirmation: %w", err)
	}
	s.set(wifiPairingState{Phase: "done", Tablet: candidate.Name})
	return nil
}

// findTablet returns the first tablet advertising an open Wi-Fi connect panel.
func (s *wifiPairingService) findTablet(ctx context.Context, iface string) (wifilan.Candidate, error) {
	browseCtx, stop := context.WithCancel(ctx)
	defer stop()
	found := make(chan wifilan.Candidate, 1)
	go s.deps.browse(browseCtx, iface, func(c wifilan.Candidate) {
		if !c.Pairing || len(c.Addresses) == 0 {
			return
		}
		select {
		case found <- c:
		default:
		}
	})
	select {
	case c := <-found:
		return c, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return wifilan.Candidate{}, errors.New("no tablet found: open Wi-Fi connect on the tablet and check both are on the same network")
		}
		return wifilan.Candidate{}, ctx.Err()
	}
}

func wifiPairingTrustDir() (string, error) {
	base, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, wifiTrustDirName), nil
}

// wifiPairedIface is the explicit LAN interface: the saved setting, else the
// DRAWSPLAY_WIFI_INTERFACE environment variable.
func wifiPairedIface(store *settingsStore) string {
	if store != nil {
		if v := strings.TrimSpace(store.get().PairedWifiInterface); v != "" {
			return v
		}
	}
	return strings.TrimSpace(os.Getenv(wifiPairedInterfaceEnv))
}

func newHostWiFiPairingService(store *settingsStore) *wifiPairingService {
	return newWiFiPairingService(wifiPairingDeps{
		iface:    func() string { return wifiPairedIface(store) },
		trustDir: wifiPairingTrustDir,
		pause:    pairedWiFiRuntime.pause,
		resume:   func() { pairedWiFiRuntime.reconcile() },
		browse: func(ctx context.Context, iface string, found func(wifilan.Candidate)) error {
			return wifilan.Browse(ctx, iface, found, func(err error) { throttledLog("wifi-pair-reject", "Wi-Fi pairing ignored an advertisement: %v", err) })
		},
		authenticate: func(ctx context.Context, iface string, c wifilan.Candidate, trust *wifilan.TrustStore, inv *wifilan.PairingInvitation) (pairingConn, [32]byte, error) {
			endpoint := net.JoinHostPort(c.Addresses[0].String(), fmt.Sprint(c.InputPort))
			raw, err := wifilan.DialSelectedLAN(ctx, iface, endpoint)
			if err != nil {
				return nil, [32]byte{}, err
			}
			var secured *tls.Conn
			var pin [32]byte
			secured, pin, err = wifilan.AuthenticateEnrollmentHost(ctx, raw, trust.Identity(), inv)
			if err != nil {
				return nil, [32]byte{}, err
			}
			return secured, pin, nil
		},
	})
}
