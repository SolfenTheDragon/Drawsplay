package main

import (
	"context"
	"net"
	"testing"
	"time"
)

type nopInjector struct{}

func (nopInjector) inject(*packet)    {}
func (nopInjector) injectKey(*packet) {}
func (nopInjector) cancelAll()        {}
func (nopInjector) close()            {}

func runHelloDeadlineCase(t *testing.T, first string, wantClosed bool) {
	t.Helper()
	old := initialHelloTimeout
	initialHelloTimeout = 300 * time.Millisecond
	defer func() { initialHelloTimeout = old }()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		handleConnection(ctx, 1, server, newEventLogger(16), nopInjector{}, newSettingsStore(defaultSettings()), "test-transport")
		close(done)
	}()
	// Drain whatever the host sends back (hostInfo, sessionPolicy).
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := client.Read(buf); err != nil {
				return
			}
		}
	}()
	if _, err := client.Write([]byte(first + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if !wantClosed {
			t.Fatal("a connection that sent a valid hello was closed by the hello deadline")
		}
	case <-time.After(1500 * time.Millisecond):
		if wantClosed {
			t.Fatal("a peer that never sent hello kept the connection open past the hello deadline")
		}
	}
	client.Close()
	<-done // release the session before the next test
}

func TestNonHelloPacketDoesNotLiftHelloDeadline(t *testing.T) {
	runHelloDeadlineCase(t, `{"type":"bogus"}`, true)
}

func TestModePrefsBeforeHelloDoesNotLiftHelloDeadline(t *testing.T) {
	runHelloDeadlineCase(t, `{"type":"modePrefs","prefWidth":1,"prefHeight":1}`, true)
}

func TestHelloLiftsHelloDeadline(t *testing.T) {
	runHelloDeadlineCase(t, `{"type":"hello","device":"test"}`, false)
}
