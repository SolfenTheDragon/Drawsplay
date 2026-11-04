package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}))
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
