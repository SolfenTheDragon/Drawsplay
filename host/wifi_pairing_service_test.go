package main

import (
	"context"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"drawsplay/host/internal/wifilan"
)

type pairingTestEnv struct {
	svc            *wifiPairingService
	dir            string
	paused, resume atomic.Int32
	foundTablet    chan struct{}
	tabletConn     net.Conn
	tabletWrites   chan []byte
}

func newPairingTestEnv(t *testing.T) *pairingTestEnv {
	t.Helper()
	env := &pairingTestEnv{dir: filepath.Join(t.TempDir(), "trust"), foundTablet: make(chan struct{}), tabletWrites: make(chan []byte, 4)}
	env.svc = newWiFiPairingService(wifiPairingDeps{
		iface:    func() string { return "test0" },
		trustDir: func() (string, error) { return env.dir, nil },
		pause:    func() { env.paused.Add(1) },
		resume:   func() { env.resume.Add(1) },
		timeout:  5 * time.Second,
		browse: func(ctx context.Context, _ string, found func(wifilan.Candidate)) error {
			select {
			case <-env.foundTablet:
				found(wifilan.Candidate{Name: "Tab", Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.9")}, InputPort: 4000, VideoPort: 4001, Pairing: true})
			case <-ctx.Done():
			}
			<-ctx.Done()
			return ctx.Err()
		},
		authenticate: func(_ context.Context, _ string, _ wifilan.Candidate, _ *wifilan.TrustStore, _ *wifilan.PairingInvitation) (pairingConn, [32]byte, error) {
			host, tablet := net.Pipe()
			env.tabletConn = tablet
			go func() {
				buf := make([]byte, 1)
				if _, err := io.ReadFull(tablet, buf); err != nil {
					return
				}
				env.tabletWrites <- []byte{buf[0]}
				tablet.Write([]byte{1})
				if _, err := io.ReadFull(tablet, buf); err != nil {
					return
				}
				env.tabletWrites <- []byte{buf[0]}
			}()
			return host, [32]byte{7, 7, 7}, nil
		},
	})
	return env
}

func waitPhase(t *testing.T, svc *wifiPairingService, phase string) wifiPairingState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := svc.Snapshot(); s.Phase == phase && (phase != "waiting" || s.Code != "") {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("phase %q not reached; state=%+v", phase, svc.Snapshot())
	return wifiPairingState{}
}

func TestWiFiPairingShowsCodeThenApprovesAndSavesTrust(t *testing.T) {
	env := newPairingTestEnv(t)
	if err := env.svc.Start(false); err != nil {
		t.Fatal(err)
	}
	waiting := waitPhase(t, env.svc, "waiting")
	if !regexp.MustCompile(`^\d{8}$`).MatchString(waiting.Code) || waiting.ExpiresAtMs == 0 {
		t.Fatalf("waiting state lacks an 8-digit code and expiry: %+v", waiting)
	}
	close(env.foundTablet)
	approval := waitPhase(t, env.svc, "approval")
	if approval.Tablet != "Tab" || approval.Fingerprint == "" || approval.Code != "" {
		t.Fatalf("approval state wrong (code must be hidden once used): %+v", approval)
	}
	if err := env.svc.Approve(); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, env.svc, "done")
	if got := <-env.tabletWrites; got[0] != 1 {
		t.Fatalf("first byte to tablet = %d", got[0])
	}
	if got := <-env.tabletWrites; got[0] != 2 {
		t.Fatalf("final byte to tablet = %d", got[0])
	}
	if env.paused.Load() != 1 || env.resume.Load() != 1 {
		t.Fatalf("runtime pause/resume = %d/%d", env.paused.Load(), env.resume.Load())
	}
	store, err := wifilan.OpenTrustStore(env.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, paired := store.Peer(); !paired {
		t.Fatal("trust not saved after approval")
	}
}

func TestWiFiPairingDenyChangesNoTrust(t *testing.T) {
	env := newPairingTestEnv(t)
	if err := env.svc.Start(false); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, env.svc, "waiting")
	close(env.foundTablet)
	waitPhase(t, env.svc, "approval")
	if err := env.svc.Deny(); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, env.svc, "failed")
	deadline := time.Now().Add(time.Second)
	for env.resume.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	store, err := wifilan.OpenTrustStore(env.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, paired := store.Peer(); paired {
		t.Fatal("denied pairing saved trust")
	}
}

func TestWiFiPairingRefusesToReplaceWithoutExplicitRequest(t *testing.T) {
	env := newPairingTestEnv(t)
	store, err := wifilan.OpenTrustStore(env.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Pair([32]byte{1}, "old"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := env.svc.Start(false); err != nil {
		t.Fatal(err)
	}
	s := waitPhase(t, env.svc, "failed")
	if s.Code != "" || s.Error == "" {
		t.Fatalf("expected failure without a code: %+v", s)
	}
}

func TestWiFiPairingRejectsConcurrentStartAndApproveOutOfPhase(t *testing.T) {
	env := newPairingTestEnv(t)
	if err := env.svc.Approve(); err == nil {
		t.Fatal("approve with nothing pending must fail")
	}
	if err := env.svc.Start(false); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, env.svc, "waiting")
	if err := env.svc.Start(false); err == nil {
		t.Fatal("second start must be rejected while pairing")
	}
	env.svc.Cancel()
	waitPhase(t, env.svc, "idle")
}
