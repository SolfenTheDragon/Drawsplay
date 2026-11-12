package main

// PC clients are served by the separate Rust program dd-host (the "desktop" host), not by this
// process. dd-host publishes its live state as status.json in its config folder (rewritten about every 2 s)
// and accepts a disconnect-request file next to it. This file reads that state so the web UI can tell
// an Android tablet session from a PC client session. Nothing here talks to the network.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	desktopStatusFile    = "status.json"
	desktopDisconnectReq = "disconnect-request"
	// desktopStatusStale is how old status.json may be before dd-host is considered not running.
	desktopStatusStale = 6 * time.Second
	// desktopConfigEnv overrides the dd-host config folder (tests, unusual installs).
	desktopConfigEnv = "DRAWSPLAY_DESKTOP_CONFIG"
)

type desktopSession struct {
	Client         string `json:"client"`
	Peer           string `json:"peer"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	FPS            int    `json:"fps"`
	VirtualDisplay bool   `json:"virtualDisplay"`
	Backend        string `json:"backend"`
	Encoder        string `json:"encoder"`
	SinceUnixMs    int64  `json:"sinceUnixMs"`
}

type desktopTrusted struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// desktopHostStatus is what the web UI sees about dd-host. Running is false when dd-host is absent
// or its status is stale; the other fields are then zero.
type desktopHostStatus struct {
	Running       bool            `json:"running"`
	PID           int             `json:"pid,omitempty"`
	Version       string          `json:"version,omitempty"`
	Listen        string          `json:"listen,omitempty"`
	Encoder       string          `json:"encoder,omitempty"`
	Source        string          `json:"source,omitempty"`
	OS            string          `json:"os,omitempty"`
	Arch          string          `json:"arch,omitempty"`
	UpdatedUnixMs int64           `json:"updatedUnixMs,omitempty"`
	Session       *desktopSession `json:"session"`
	// Sessions lists every connected PC client (dd-host --max-clients > 1). Session is the oldest.
	Sessions []desktopSession `json:"sessions"`
	Trusted  []desktopTrusted `json:"trusted"`
}

// desktopHostDir is dd-host's config folder: %APPDATA%\DrawSplayDesktop\host on Windows,
// $XDG_CONFIG_HOME (or ~/.config)/drawsplay-desktop/host elsewhere.
func desktopHostDir() string {
	if d := os.Getenv(desktopConfigEnv); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	name := "drawsplay-desktop"
	if runtime.GOOS == "windows" {
		name = "DrawSplayDesktop"
	}
	return filepath.Join(base, name, "host")
}

// readDesktopHostStatus parses status.json in dir. Missing, unreadable, malformed or stale files all
// mean "not running".
// desktopControlFile names dd-host's control endpoint: a loopback port and a
// secret token, readable only by this user (see desktop/host/src/status.rs).
const desktopControlFile = "control.json"

// desktopControl sends one command to a running dd-host's control endpoint
// and returns its one-line JSON answer.
func desktopControl(dir, command string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, desktopControlFile))
	if err != nil {
		return nil, err
	}
	var c struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &c); err != nil || c.Port <= 0 || c.Port > 65535 || len(c.Token) != 32 ||
		strings.ContainsAny(c.Token, " \r\n") {
		return nil, errors.New("dd-host control file is invalid")
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.Port), 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := fmt.Fprintf(conn, "%s %s\n", c.Token, command); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 1<<20)).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(line, &e) == nil && e.Error != "" {
		return nil, errors.New("dd-host: " + e.Error)
	}
	return line, nil
}

func readDesktopHostStatus(dir string, now time.Time) desktopHostStatus {
	none := desktopHostStatus{Trusted: []desktopTrusted{}, Sessions: []desktopSession{}}
	if dir == "" {
		return none
	}
	var st desktopHostStatus
	// Ask the running host first; status.json (rewritten every 2 s) serves
	// older hosts and is checked for staleness.
	if live, err := desktopControl(dir, "status"); err == nil && json.Unmarshal(live, &st) == nil {
		st.UpdatedUnixMs = now.UnixMilli()
	} else {
		data, err := os.ReadFile(filepath.Join(dir, desktopStatusFile))
		if err != nil {
			return none
		}
		st = desktopHostStatus{}
		if err := json.Unmarshal(data, &st); err != nil {
			return none
		}
		age := now.Sub(time.UnixMilli(st.UpdatedUnixMs))
		if st.UpdatedUnixMs <= 0 || age > desktopStatusStale || age < -desktopStatusStale {
			return none
		}
	}
	st.Running = true
	// Older dd-host builds publish only "session".
	if len(st.Sessions) == 0 && st.Session != nil {
		st.Sessions = []desktopSession{*st.Session}
	}
	if st.Sessions == nil {
		st.Sessions = []desktopSession{}
	}
	if st.Trusted == nil {
		st.Trusted = []desktopTrusted{}
	}
	return st
}

func currentDesktopHostStatus() desktopHostStatus {
	return readDesktopHostStatus(desktopHostDir(), time.Now())
}

// Connection kinds reported to the web UI.
const (
	connectionNone    = "none"
	connectionAndroid = "android"
	connectionPC      = "pc"
	connectionBoth    = "both"
)

// connectionKind says which kind of client is connected right now.
func connectionKind(androidConnected bool, pc desktopHostStatus) string {
	pcConnected := pc.Running && pc.Session != nil
	switch {
	case androidConnected && pcConnected:
		return connectionBoth
	case androidConnected:
		return connectionAndroid
	case pcConnected:
		return connectionPC
	}
	return connectionNone
}

// androidClientConnected reports whether a tablet session is active in this process.
func androidClientConnected() bool {
	return hostSessionOwnership.currentTransport() != "" || activeClientIdentity().Platform != ""
}

var errNoDesktopSession = errors.New("no PC client session is active")

// requestDesktopDisconnect asks dd-host to end its active session by dropping a request file; dd-host
// polls for it a couple of times a second.
func requestDesktopDisconnect(dir string, now time.Time) error {
	st := readDesktopHostStatus(dir, now)
	if !st.Running || st.Session == nil {
		return errNoDesktopSession
	}
	if _, err := desktopControl(dir, "disconnect"); err == nil {
		return nil // ends the session at once
	}
	// Older hosts poll for this file every half second.
	return os.WriteFile(filepath.Join(dir, desktopDisconnectReq), []byte("disconnect\n"), 0o600)
}
