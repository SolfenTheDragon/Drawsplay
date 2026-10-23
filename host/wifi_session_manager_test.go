package main

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"drawsplay/host/internal/wifilan"
)

func newTestManager() (*wifiSessionManager, *wifiConnListener, *wifiConnListener) {
	in, vid := newWiFiConnListener(), newWiFiConnListener()
	return &wifiSessionManager{inputs: in, videos: vid, retryDelay: time.Millisecond, owner: func() string { return "" }}, in, vid
}

func TestWiFiSessionManagerDisabledDoesNoWork(t *testing.T) {
	m, _, _ := newTestManager()
	var called atomic.Bool
	m.browse = func(context.Context, func(wifilan.Candidate)) error { called.Store(true); return nil }
	m.enabled = false
	m.run(context.Background())
	if called.Load() {
		t.Fatal("discovery ran while Wi-Fi was disabled")
	}
}

func TestWiFiSessionManagerPushesSessionAndReconnectsAfterClose(t *testing.T) {
	m, in, vid := newTestManager()
	m.enabled = true
	m.browse = func(ctx context.Context, found func(wifilan.Candidate)) error {
		for ctx.Err() == nil {
			found(wifilan.Candidate{Name: "tab"})
			time.Sleep(2 * time.Millisecond)
		}
		return ctx.Err()
	}
	var sessions atomic.Int32
	m.establish = func(context.Context, wifilan.Candidate) (net.Conn, net.Conn, error) {
		sessions.Add(1)
		a, _ := net.Pipe()
		b, _ := net.Pipe()
		return a, b, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go m.run(ctx)
	for i := 0; i < 2; i++ {
		ic, err := in.Accept()
		if err != nil {
			t.Fatal(err)
		}
		vc, err := vid.Accept()
		if err != nil {
			t.Fatal(err)
		}
		vc.Close()
		ic.Close() // both gone -> manager reconnects
	}
	if sessions.Load() < 2 {
		t.Fatalf("sessions=%d", sessions.Load())
	}
}

func TestWiFiSessionManagerYieldsToUSBAndSurvivesEstablishErrors(t *testing.T) {
	m, in, _ := newTestManager()
	m.enabled = true
	var owner atomic.Value
	owner.Store("usb")
	m.owner = func() string { return owner.Load().(string) }
	m.browse = func(ctx context.Context, found func(wifilan.Candidate)) error {
		for ctx.Err() == nil {
			found(wifilan.Candidate{Name: "tab"})
			time.Sleep(2 * time.Millisecond)
		}
		return ctx.Err()
	}
	var attempts atomic.Int32
	m.establish = func(context.Context, wifilan.Candidate) (net.Conn, net.Conn, error) {
		if attempts.Add(1) == 1 {
			return nil, nil, errors.New("handshake failed")
		}
		a, _ := net.Pipe()
		b, _ := net.Pipe()
		return a, b, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go m.run(ctx)
	time.Sleep(60 * time.Millisecond)
	if attempts.Load() != 0 {
		t.Fatal("dialed while USB owned the session")
	}
	owner.Store("")
	if _, err := in.Accept(); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() < 2 {
		t.Fatal("did not retry after establish failure")
	}
}
