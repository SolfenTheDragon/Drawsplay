package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type pausedSettingsBody struct {
	io.Reader
	started chan struct{}
	resume  <-chan struct{}
	once    sync.Once
}

func (b *pausedSettingsBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.resume
	})
	return b.Reader.Read(p)
}

func TestSettingsConcurrentPatchesPreserveChanges(t *testing.T) {
	for _, resolutionChange := range []bool{false, true} {
		t.Run(fmt.Sprintf("resolution=%v", resolutionChange), func(t *testing.T) {
			store := newTestStore(t)
			api := webUIAPI{store: store, csrfToken: "test"}
			resume := make(chan struct{})
			var resumeOnce sync.Once
			unblock := func() { resumeOnce.Do(func() { close(resume) }) }
			defer unblock()
			body := &pausedSettingsBody{
				Reader:  strings.NewReader(`{"keyboardEnabled":false}`),
				started: make(chan struct{}),
				resume:  resume,
			}
			req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
			req.Header.Set("X-DrawSplay-CSRF", "test")
			res := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				api.handleSettings(res, req)
			}()
			select {
			case <-body.started:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not begin reading its body")
			}
			if resolutionChange {
				if err := store.updateResolution(2560, 1440); err != nil {
					t.Fatal(err)
				}
			} else {
				req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"touchEnabled":false}`))
				req.Header.Set("X-DrawSplay-CSRF", "test")
				res := httptest.NewRecorder()
				api.handleSettings(res, req)
				if res.Code != http.StatusOK {
					t.Fatalf("concurrent update failed: %d %s", res.Code, res.Body)
				}
			}
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not finish")
			}
			if res.Code != http.StatusOK {
				t.Fatalf("delayed update failed: %d %s", res.Code, res.Body)
			}
			got := store.get()
			if got.KeyboardEnabled || (!resolutionChange && got.TouchEnabled) ||
				(resolutionChange && (got.Width != 2560 || got.Height != 1440)) {
				t.Fatalf("concurrent change lost: %+v", got)
			}
			if saved := loadSettings(); saved != got {
				t.Fatalf("disk and memory disagree: saved=%+v memory=%+v", saved, got)
			}
		})
	}
}

func TestSettingsApplyOnlyAffectedSessions(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantVideo bool
		wantInput bool
	}{
		{"unchanged", `{}`, false, false},
		{"update preference", `{"updateCheck":false}`, false, false},
		{"keyboard policy is read live", `{"keyboardEnabled":false}`, false, false},
		{"pen policy", `{"penPrediction":false}`, false, true},
		{"touch input", `{"touchEnabled":false}`, false, true},
		{"scaling policy", `{"videoScaling":"fit"}`, false, true},
		{"bitrate", `{"bitrate":"12M"}`, true, false},
		{"frame rate", `{"fps":60}`, true, false},
		{"resolution", `{"width":1280,"height":720}`, true, false},
		{"native capture", `{"nativeMediaHelper":"off"}`, true, false},
		{"capture gating", `{"idleCaptureGate":true}`, true, false},
		{"temporal layers", `{"temporalLayering":true}`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			setActiveVideoSession(cancel, nil)
			defer func() { setActiveVideoSession(nil, nil); cancel() }()
			input, peer := net.Pipe()
			defer input.Close()
			defer peer.Close()
			registerInputConnection(987654, input)
			defer unregisterInputConnection(987654)
			srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
			defer srv.Close()
			res := protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", tt.body, testCSRFToken(t, srv.URL))
			defer res.Body.Close()
			var body struct {
				Restarted      bool     `json:"restarted"`
				InputRestarted bool     `json:"inputRestarted"`
				Settings       settings `json:"settings"`
			}
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusOK || body.Restarted != tt.wantVideo || body.InputRestarted != tt.wantInput {
				t.Fatalf("status=%d video=%v input=%v; want video=%v input=%v", res.StatusCode, body.Restarted, body.InputRestarted, tt.wantVideo, tt.wantInput)
			}
			if (ctx.Err() != nil) != tt.wantVideo {
				t.Fatalf("video cancellation = %v, want %v", ctx.Err(), tt.wantVideo)
			}
			if body.Settings != store.get() {
				t.Fatal("response does not contain the saved settings")
			}
		})
	}
}

// newTestStore builds a store whose saves go to a temp config dir.
func newTestStore(t *testing.T) *settingsStore {
	t.Helper()
	t.Setenv("APPDATA", t.TempDir())         // Windows os.UserConfigDir
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // Linux os.UserConfigDir
	return newSettingsStore(defaultSettings())
}

func testCSRFToken(t *testing.T, serverURL string) string {
	t.Helper()
	res, err := http.Get(serverURL + "/api/csrf")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Token == "" {
		t.Fatal("empty CSRF token")
	}
	return body.Token
}

func protectedRequest(t *testing.T, method, serverURL, path, body, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, serverURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-DrawSplay-CSRF", token)
	req.Header.Set("Origin", serverURL)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSettingsAPIRoundTrip(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	// GET returns the defaults.
	res, err := http.Get(srv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", res.StatusCode)
	}
	if bytes.Contains(raw, []byte("wifiAuthToken")) || bytes.Contains(raw, []byte(`"wifiEnabled"`)) {
		t.Fatalf("retired token Wi-Fi settings still exposed: %s", raw)
	}

	// PUT applies a valid change.
	body := `{"adb":"adb","preferredADBSerial":"tablet-123","requirePreferredADB":true,"ffmpeg":"ffmpeg","fps":30,"bitrate":"12M","width":2560,"height":1600,"touchEnabled":false,"penEnabled":true,"keyboardEnabled":false,"keepAliveOnBackground":true,"penPrediction":false,"videoScaling":"fit","autoLaunchAndroidApp":true,"zeroCopyCapture":false,"temporalLayering":true,"allowToolAutoInstall":false,"noDriverInstall":true}`
	res = protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", body, token)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", res.StatusCode)
	}
	got := store.get()
	if got.FPS != 30 || got.Bitrate != "12M" || got.Width != 2560 || got.Height != 1600 ||
		got.PreferredADBSerial != "tablet-123" || !got.RequirePreferredADB ||
		got.TouchEnabled || !got.PenEnabled || got.KeyboardEnabled ||
		!got.KeepAliveOnBackground || got.PenPrediction || got.VideoScaling != videoScalingFit || !got.AutoLaunchAndroidApp || got.ZeroCopyCapture || !got.TemporalLayering ||
		got.AllowToolAutoInstall || !got.NoDriverInstall {
		t.Errorf("store after PUT = %+v", got)
	}

	// Saved settings survive a reload from disk.
	if reloaded := loadSettings(); reloaded != got {
		t.Errorf("loadSettings() = %+v, want %+v", reloaded, got)
	}
}

func TestSettingsAPIPreservesWiFiFieldsWhenFeatureDisabled(t *testing.T) {
	t.Setenv(wifiFeatureFlagEnv, "0")
	store := newTestStore(t)
	initial := defaultSettings()
	initial.PairedWiFiEnabled = true
	initial.PairedWiFiInterface = "wlan0"
	if err := store.update(initial); err != nil {
		t.Fatalf("store.update(initial): %v", err)
	}

	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	// Simulate a non-Wi-Fi settings save payload from UI while Wi-Fi feature is hidden.
	body := `{"adb":"adb","ffmpeg":"ffmpeg","fps":75,"bitrate":"16M","width":1920,"height":1080,"touchEnabled":true,"penEnabled":true,"keyboardEnabled":true,"keepAliveOnBackground":false,"autoLaunchAndroidApp":false,"zeroCopyCapture":true,"temporalLayering":false,"allowToolAutoInstall":true,"noDriverInstall":false}`
	res := protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", body, token)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", res.StatusCode)
	}

	got := store.get()
	if got.PairedWiFiInterface != "wlan0" {
		t.Fatalf("wifi interface was unexpectedly changed: %+v", got)
	}
	if got.PairedWiFiEnabled {
		t.Fatalf("pairedWifiEnabled should be forced off while feature is disabled")
	}
}

func TestLegacyWiFiSettingsAreDroppedFromConfig(t *testing.T) {
	newTestStore(t)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"ffmpeg":"ffmpeg","fps":60,"bitrate":"24M","width":1920,"height":1080,"wifiEnabled":true,"wifiAuthToken":"old-secret","wifiTLS":true}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(filepath.Dir(path), "wifi_tls_key.pem")
	if err := os.WriteFile(cert, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeLegacyWiFiFiles()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("old-secret")) || bytes.Contains(data, []byte("wifiAuthToken")) {
		t.Fatalf("legacy token left in config: %s", data)
	}
	if _, err := os.Stat(cert); !os.IsNotExist(err) {
		t.Fatalf("legacy TLS key not removed: %v", err)
	}
	if got := loadSettings(); got.FPS != 60 {
		t.Fatalf("other settings lost: %+v", got)
	}
}

func TestStatusAPIExposesWarningsAndMetrics(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"video", "adb", "warnings", "metrics", "autostart", "autostartImplementation", "clientVideoStats", "activeClient"} {
		if _, ok := body[field]; !ok {
			t.Errorf("status response missing %q", field)
		}
	}
	var implementation string
	if err := json.Unmarshal(body["autostartImplementation"], &implementation); err != nil || implementation == "" {
		t.Errorf("autostartImplementation = %q, decode error = %v", implementation, err)
	}
	for _, field := range []string{"setup", "inputLocked"} {
		if _, ok := body[field]; !ok {
			t.Errorf("status response missing %q", field)
		}
	}
}

func TestAutostartImplementationNamesLinuxXDGAutostart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux autostart implementation is Linux-only")
	}
	if got, want := autostartImplementation(), "Linux desktop session: XDG autostart"; got != want {
		t.Errorf("autostartImplementation() = %q, want %q", got, want)
	}
}

func TestLogsAPIExposesBufferedConsoleOutput(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()

	logLine := "webui-log-test-line"
	log.Print(logLine)

	res, err := http.Get(srv.URL + "/api/logs?since=0")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/logs status = %d", res.StatusCode)
	}
	var body struct {
		Lines []string `json:"lines"`
		Next  int      `json:"next"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Next <= 0 {
		t.Fatalf("expected next cursor > 0, got %d", body.Next)
	}
	found := false
	for _, line := range body.Lines {
		if strings.Contains(line, logLine) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected log line %q in logs response", logLine)
	}
}

func TestDiagnosticsAPIIncludesSnapshot(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/diagnostics status = %d", res.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"generatedAt", "runtime", "settings", "video", "clientVideoStats", "activeClient", "adb", "warnings", "metrics", "setup", "logLines"} {
		if _, ok := body[field]; !ok {
			t.Errorf("diagnostics response missing %q", field)
		}
	}
}

func TestSessionInputLockAPI(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	res := protectedRequest(t, http.MethodPost, srv.URL, "/api/session/input-lock", `{"locked":true}`, token)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/session/input-lock status = %d", res.StatusCode)
	}
	if !sessionInputLocked() {
		t.Fatal("expected session input lock to be enabled")
	}
	setSessionInputLocked(false)
}

func TestSessionReconnectVideoAndSetupRemediationAPIs(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	res := protectedRequest(t, http.MethodPost, srv.URL, "/api/session/reconnect-video", `{}`, token)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/session/reconnect-video status = %d", res.StatusCode)
	}

	res = protectedRequest(t, http.MethodPost, srv.URL, "/api/setup/remediate", `{"action":"unknown"}`, token)
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /api/setup/remediate unknown status = %d", res.StatusCode)
	}
}

func TestShouldRestartX11AfterSetupRepair(t *testing.T) {
	tests := []struct {
		name        string
		x11         bool
		video       videoStatusInfo
		outputReady bool
		want        bool
	}{
		{"new virtual output after repair", true, videoStatusInfo{Streaming: true, DisplayMode: "mirror"}, true, true},
		{"no output", true, videoStatusInfo{Streaming: true, DisplayMode: "mirror"}, false, false},
		{"not currently mirroring", true, videoStatusInfo{Streaming: true, DisplayMode: "extend"}, true, false},
		{"no active stream", true, videoStatusInfo{DisplayMode: "mirror"}, true, false},
		{"not X11", false, videoStatusInfo{Streaming: true, DisplayMode: "mirror"}, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRestartX11AfterSetupRepair(tc.x11, tc.video, tc.outputReady); got != tc.want {
				t.Fatalf("shouldRestartX11AfterSetupRepair() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInputAllowedBySettings(t *testing.T) {
	s := defaultSettings()
	if !inputAllowed(s, "finger") || !inputAllowed(s, "stylus") || !inputAllowed(s, "eraser") {
		t.Fatal("default settings should allow touch and pen")
	}
	s.TouchEnabled = false
	if inputAllowed(s, "finger") {
		t.Error("finger input allowed while touch disabled")
	}
	if !inputAllowed(s, "stylus") {
		t.Error("pen input blocked while only touch disabled")
	}
	s.PenEnabled = false
	if inputAllowed(s, "stylus") || inputAllowed(s, "eraser") {
		t.Error("pen or eraser input allowed while pen disabled")
	}
	if !inputAllowed(s, "mouse") {
		t.Error("mouse input should be unaffected by touch/pen settings")
	}
}

func TestSettingsAPIRejectsInvalid(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	cases := []string{
		`{"adb":"adb","ffmpeg":"ffmpeg","fps":5,"bitrate":"12M","width":1920,"height":1080}`, // fps too low
		`{"adb":"adb","ffmpeg":"ffmpeg","fps":60,"bitrate":"1k","width":1920,"height":1080}`, // bitrate under floor
		`{"adb":"adb","ffmpeg":"ffmpeg","fps":60,"bitrate":"12M","width":100,"height":1080}`, // width too small
		`{"adb":"","ffmpeg":"ffmpeg","fps":60,"bitrate":"12M","width":1920,"height":1080}`,   // empty adb
		`{"adb":"adb","preferredADBSerial":"","requirePreferredADB":true,"ffmpeg":"ffmpeg","fps":60,"bitrate":"12M","width":1920,"height":1080}`,
		`not json`,
	}

	before := store.get()
	for _, c := range cases {
		res := protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", c, token)
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Errorf("PUT %q accepted, want rejection", c)
		}
	}
	if store.get() != before {
		t.Error("store changed after rejected updates")
	}
}

func TestCalibrationAPIRoundTrip(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Ensure in-memory calibration state does not leak between tests.
	calibrationState.mu.Lock()
	calibrationState.loaded = false
	calibrationState.values = make(map[string]deviceCalibration)
	calibrationState.mu.Unlock()

	store := newSettingsStore(defaultSettings())
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	res, err := http.Get(srv.URL + "/api/calibration?device=tablet-1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var getBody struct {
		Device      string            `json:"device"`
		Calibration deviceCalibration `json:"calibration"`
	}
	if err := json.NewDecoder(res.Body).Decode(&getBody); err != nil {
		t.Fatal(err)
	}
	if getBody.Device != "tablet-1" {
		t.Fatalf("device = %q, want tablet-1", getBody.Device)
	}
	if getBody.Calibration.ScaleX != 1 || getBody.Calibration.ScaleY != 1 {
		t.Fatalf("default calibration = %+v, want identity", getBody.Calibration)
	}

	put := `{"device":"tablet-1","calibration":{"scaleX":1.05,"scaleY":0.98,"offsetX":0.01,"offsetY":-0.02}}`
	res = protectedRequest(t, http.MethodPut, srv.URL, "/api/calibration", put, token)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/calibration status = %d", res.StatusCode)
	}

	res, err = http.Get(srv.URL + "/api/calibration?device=tablet-1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&getBody); err != nil {
		t.Fatal(err)
	}
	if getBody.Calibration.ScaleX != 1.05 || getBody.Calibration.ScaleY != 0.98 ||
		getBody.Calibration.OffsetX != 0.01 || getBody.Calibration.OffsetY != -0.02 {
		t.Fatalf("saved calibration = %+v, want requested values", getBody.Calibration)
	}
}

func TestStateChangingAPIRequiresCSRFAndLocalOrigin(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)
	body := `{"fps":30}`

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings", strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("missing-token status = %d, want 403", res.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/api/settings", strings.NewReader(body))
	req.Header.Set("X-DrawSplay-CSRF", token)
	req.Header.Set("Origin", "https://evil.example")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", res.StatusCode)
	}
}

func TestSettingsAPIRejectsUnsafeToolPaths(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	cases := []string{
		`{"adb":"https://example.test/adb.exe"}`,
		`{"adb":"relative\\adb.exe"}`,
		`{"adb":"\\\\server\\share\\adb.exe"}`,
		`{"ffmpeg":"file:///C:/tools/ffmpeg.exe"}`,
	}
	for _, body := range cases {
		res := protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", body, token)
		res.Body.Close()
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("PUT %s status = %d, want 422", body, res.StatusCode)
		}
	}
}

func TestWebUIRefusesRebindedHostNames(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	for _, path := range []string{"/api/wifi/pairing", "/api/logs", "/api/csrf", "/api/settings", "/"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = "evil.example:27180"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("%s with a foreign Host: status %d, want %d", path, res.StatusCode, http.StatusMisdirectedRequest)
		}
	}
	for _, host := range []string{"localhost:27180", "127.0.0.1:27180", "[::1]:27180", "LOCALHOST"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/csrf", nil)
		req.Host = host
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("Host %q refused: %d", host, res.StatusCode)
		}
	}
}

func TestWebUISetsContentSecurityPolicy(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}, func() {}))
	defer srv.Close()
	for _, path := range []string{"/", "/api/status"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		csp := res.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
	}
}
