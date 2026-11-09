package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeDesktopStatus(t *testing.T, dir string, updated time.Time, withSession bool) {
	t.Helper()
	session := "null"
	if withSession {
		session = `{"client":"laptop","peer":"10.0.0.2:5000","width":1920,"height":1080,"fps":60,
			"virtualDisplay":true,"backend":"x11-virtual","encoder":"h264_vaapi","sinceUnixMs":1}`
	}
	doc := fmt.Sprintf(`{"schema":1,"pid":42,"updatedUnixMs":%d,"version":"0.1.0","listen":"0.0.0.0:47800",
		"encoder":"libx264","source":"virtual","os":"linux","arch":"x86_64","session":%s,
		"trusted":[{"key":"abcdef012345","label":"tablet"}]}`, updated.UnixMilli(), session)
	if err := os.WriteFile(filepath.Join(dir, desktopStatusFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadDesktopHostStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	if st := readDesktopHostStatus(dir, now); st.Running || st.Trusted == nil {
		t.Fatalf("missing file must mean not running with an empty (non-nil) trust list: %+v", st)
	}
	writeDesktopStatus(t, dir, now.Add(-time.Second), true)
	st := readDesktopHostStatus(dir, now)
	if !st.Running || st.Session == nil || st.Session.Client != "laptop" || st.Session.FPS != 60 ||
		st.Listen != "0.0.0.0:47800" || len(st.Trusted) != 1 || st.Trusted[0].Label != "tablet" {
		t.Fatalf("fresh status misread: %+v", st)
	}
	writeDesktopStatus(t, dir, now.Add(-time.Minute), true)
	if readDesktopHostStatus(dir, now).Running {
		t.Fatal("a stale status file (dd-host died) must read as not running")
	}
	writeDesktopStatus(t, dir, now.Add(time.Hour), true)
	if readDesktopHostStatus(dir, now).Running {
		t.Fatal("a timestamp far in the future must not count as live")
	}
	if err := os.WriteFile(filepath.Join(dir, desktopStatusFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readDesktopHostStatus(dir, now).Running {
		t.Fatal("malformed status must read as not running")
	}
	writeDesktopStatus(t, dir, now, false)
	if st := readDesktopHostStatus(dir, now); !st.Running || st.Session != nil {
		t.Fatalf("idle dd-host: running without a session expected: %+v", st)
	}
}

func TestConnectionKind(t *testing.T) {
	idle := desktopHostStatus{Running: true}
	active := desktopHostStatus{Running: true, Session: &desktopSession{Client: "x"}}
	staleWithSession := desktopHostStatus{Running: false, Session: &desktopSession{Client: "x"}}
	cases := []struct {
		android bool
		pc      desktopHostStatus
		want    string
	}{
		{false, desktopHostStatus{}, connectionNone},
		{false, idle, connectionNone},
		{true, idle, connectionAndroid},
		{false, active, connectionPC},
		{true, active, connectionBoth},
		{false, staleWithSession, connectionNone},
	}
	for i, c := range cases {
		if got := connectionKind(c.android, c.pc); got != c.want {
			t.Errorf("case %d: connectionKind = %q, want %q", i, got, c.want)
		}
	}
}

func TestStatusAPIReportsPCClient(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(desktopConfigEnv, dir)
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}, func() {}))
	defer srv.Close()

	get := func() (kind string, desktop map[string]any) {
		res, err := http.Get(srv.URL + "/api/status")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		d, _ := body["desktopHost"].(map[string]any)
		return fmt.Sprint(body["connectionKind"]), d
	}

	if kind, d := get(); kind != connectionNone || d["running"] != false {
		t.Fatalf("no dd-host: kind=%q desktop=%v", kind, d)
	}
	writeDesktopStatus(t, dir, time.Now(), true)
	kind, d := get()
	if kind != connectionPC || d["running"] != true {
		t.Fatalf("PC client connected: kind=%q desktop=%v", kind, d)
	}
	if s, _ := d["session"].(map[string]any); s["client"] != "laptop" || s["backend"] != "x11-virtual" {
		t.Fatalf("session details missing: %v", d["session"])
	}
}

func TestDesktopDisconnectEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(desktopConfigEnv, dir)
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	// Without the CSRF token the request is refused.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/desktop/disconnect", nil)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode == http.StatusOK {
		t.Fatalf("unprotected disconnect must be refused (err=%v)", err)
	}

	// No PC session: conflict, and no request file is left behind.
	res := protectedRequest(t, http.MethodPost, srv.URL, "/api/desktop/disconnect", "", token)
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("no session: status %d, want 409", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, desktopDisconnectReq)); err == nil {
		t.Fatal("request file written although no session is active")
	}

	// Active session: the request file appears for dd-host to pick up.
	writeDesktopStatus(t, dir, time.Now(), true)
	res = protectedRequest(t, http.MethodPost, srv.URL, "/api/desktop/disconnect", "", token)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("active session: status %d, want 200", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, desktopDisconnectReq)); err != nil {
		t.Fatalf("request file missing: %v", err)
	}
}

func TestReadDesktopHostStatusSessions(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	doc := fmt.Sprintf(`{"updatedUnixMs":%d,"session":{"client":"a","fps":60},"sessions":[{"client":"a","fps":60},{"client":"b","fps":30}]}`, now.UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, desktopStatusFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readDesktopHostStatus(dir, now)
	if len(st.Sessions) != 2 || st.Sessions[1].Client != "b" || st.Session == nil || st.Session.Client != "a" {
		t.Fatalf("two sessions misread: %+v", st)
	}
	// An older dd-host publishes only "session"; it still shows up in the list.
	old := fmt.Sprintf(`{"updatedUnixMs":%d,"session":{"client":"solo"}}`, now.UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, desktopStatusFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := readDesktopHostStatus(dir, now); len(st.Sessions) != 1 || st.Sessions[0].Client != "solo" {
		t.Fatalf("legacy single session not promoted: %+v", st)
	}
	if st := readDesktopHostStatus(dir, now.Add(time.Hour)); st.Sessions == nil || len(st.Sessions) != 0 {
		t.Fatalf("not running must give an empty non-nil list: %+v", st)
	}
}

// fakeDesktopControl serves dd-host's control protocol from a test listener.
func fakeDesktopControl(t *testing.T, dir string, answer func(cmd string) string) func() []string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	token := strings.Repeat("ab", 16)
	ctl := fmt.Sprintf(`{"port":%d,"token":"%s"}`, ln.Addr().(*net.TCPAddr).Port, token)
	if err := os.WriteFile(filepath.Join(dir, desktopControlFile), []byte(ctl), 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		mu   sync.Mutex
		seen []string
	)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			tok, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
			if tok != token {
				fmt.Fprintln(c, `{"error":"bad token"}`)
			} else {
				mu.Lock()
				seen = append(seen, cmd)
				mu.Unlock()
				fmt.Fprintln(c, answer(cmd))
			}
			c.Close()
		}
	}()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func TestDesktopControlEndpointIsPreferred(t *testing.T) {
	dir := t.TempDir()
	// A stale status file that alone would read as "not running".
	if err := os.WriteFile(filepath.Join(dir, desktopStatusFile), []byte(`{"updatedUnixMs":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := fakeDesktopControl(t, dir, func(cmd string) string {
		if cmd == "status" {
			return `{"version":"9.9","session":{"client":"laptop","width":1920,"height":1080,"fps":60},"sessions":[]}`
		}
		return `{"ok":true,"sessions":1}`
	})
	st := readDesktopHostStatus(dir, time.Now())
	if !st.Running || st.Session == nil || st.Session.Client != "laptop" || len(st.Sessions) != 1 {
		t.Fatalf("live status not used: %+v", st)
	}
	if err := requestDesktopDisconnect(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, desktopDisconnectReq)); !os.IsNotExist(err) {
		t.Fatal("the request file must not be needed when the control endpoint answers")
	}
	if got := seen(); len(got) != 3 || got[2] != "disconnect" {
		t.Fatalf("commands = %v", got)
	}
}
