package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	// GET returns the defaults.
	res, err := http.Get(srv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	var getBody struct {
		WifiAuthToken              string `json:"wifiAuthToken"`
		USBLinkAuthToken           string `json:"usbLinkAuthToken"`
		WifiAuthTokenConfigured    bool   `json:"wifiAuthTokenConfigured"`
		USBLinkAuthTokenConfigured bool   `json:"usbLinkAuthTokenConfigured"`
	}
	if err := json.NewDecoder(res.Body).Decode(&getBody); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", res.StatusCode)
	}
	if getBody.WifiAuthToken != "" || getBody.USBLinkAuthToken != "" ||
		getBody.WifiAuthTokenConfigured || getBody.USBLinkAuthTokenConfigured {
		t.Fatalf("expected redacted default tokens, got %+v", getBody)
	}

	// PUT applies a valid change.
	body := `{"adb":"adb","preferredADBSerial":"tablet-123","requirePreferredADB":true,"ffmpeg":"ffmpeg","fps":30,"bitrate":"12M","width":2560,"height":1600,"localCursor":false,"touchEnabled":false,"penEnabled":true,"keyboardEnabled":false,"keepAliveOnBackground":true,"autoLaunchAndroidApp":true,"zeroCopyCapture":false,"temporalLayering":true,"allowToolAutoInstall":false,"noDriverInstall":true}`
	res = protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", body, token)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", res.StatusCode)
	}
	got := store.get()
	if got.FPS != 30 || got.Bitrate != "12M" || got.Width != 2560 || got.Height != 1600 ||
		got.PreferredADBSerial != "tablet-123" || !got.RequirePreferredADB ||
		got.LocalCursor || got.TouchEnabled || !got.PenEnabled || got.KeyboardEnabled ||
		!got.KeepAliveOnBackground || !got.AutoLaunchAndroidApp || got.ZeroCopyCapture || !got.TemporalLayering ||
		got.AllowToolAutoInstall || !got.NoDriverInstall {
		t.Errorf("store after PUT = %+v", got)
	}

	// Saved settings survive a reload from disk.
	if reloaded := loadSettings(); reloaded != got {
		t.Errorf("loadSettings() = %+v, want %+v", reloaded, got)
	}
}

func TestSettingsAPIPreservesWiFiFieldsWhenFeatureDisabled(t *testing.T) {
	t.Setenv(wifiFeatureFlagEnv, "")
	store := newTestStore(t)
	initial := defaultSettings()
	initial.WifiEnabled = true
	initial.WifiBindAddress = "10.0.0.5"
	initial.WifiInputPort = 40001
	initial.WifiVideoPort = 40002
	initial.WifiServiceName = "SavedService"
	initial.WifiTLS = true
	initial.WifiAuthToken = "saved-token"
	initial.WifiMaxBitrate = "9M"
	if err := store.update(initial); err != nil {
		t.Fatalf("store.update(initial): %v", err)
	}

	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	// Simulate a non-Wi-Fi settings save payload from UI while Wi-Fi feature is hidden.
	body := `{"adb":"adb","ffmpeg":"ffmpeg","fps":75,"bitrate":"16M","width":1920,"height":1080,"localCursor":true,"touchEnabled":true,"penEnabled":true,"keyboardEnabled":true,"keepAliveOnBackground":false,"autoLaunchAndroidApp":false,"zeroCopyCapture":true,"temporalLayering":false,"allowToolAutoInstall":true,"noDriverInstall":false}`
	res := protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", body, token)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", res.StatusCode)
	}

	got := store.get()
	if got.WifiAuthToken != "saved-token" ||
		got.WifiBindAddress != "10.0.0.5" ||
		got.WifiInputPort != 40001 ||
		got.WifiVideoPort != 40002 ||
		got.WifiServiceName != "SavedService" ||
		got.WifiMaxBitrate != "9M" ||
		!got.WifiTLS {
		t.Fatalf("wifi settings were unexpectedly changed: %+v", got)
	}
	if got.WifiEnabled {
		t.Fatalf("wifiEnabled should be forced off while feature is disabled")
	}
}

func TestSettingsAPIUSBLinkPayload(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
	defer srv.Close()
	token := testCSRFToken(t, srv.URL)

	body := `{"adb":"adb","ffmpeg":"ffmpeg","fps":120,"bitrate":"24M","width":1920,"height":1080,"usbLinkEnabled":true,"usbLinkBindAddress":"192.168.7.1","usbLinkInputPort":40101,"usbLinkVideoPort":40102,"usbLinkTLS":true,"usbLinkAuthToken":"usb-link-secret","usbLinkMaxBitrate":"18M"}`
	res := protectedRequest(t, http.MethodPut, srv.URL, "/api/settings", body, token)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", res.StatusCode)
	}

	got := store.get()
	if !got.USBLinkEnabled || got.USBLinkBindAddress != "192.168.7.1" ||
		got.USBLinkInputPort != 40101 || got.USBLinkVideoPort != 40102 ||
		!got.USBLinkTLS || got.USBLinkAuthToken != "usb-link-secret" ||
		got.USBLinkMaxBitrate != "18M" {
		t.Fatalf("USB Link settings after PUT = %+v", got)
	}
}

func TestSettingsAndDiagnosticsRedactAuthTokens(t *testing.T) {
	store := newTestStore(t)
	s := defaultSettings()
	s.WifiEnabled = true
	s.WifiAuthToken = "wifi-secret"
	s.USBLinkEnabled = true
	s.USBLinkAuthToken = "usb-secret"
	s.USBLinkTLS = true
	if err := store.update(s); err != nil {
		t.Fatalf("store.update: %v", err)
	}
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	var settingsBody struct {
		WifiAuthToken              string `json:"wifiAuthToken"`
		USBLinkAuthToken           string `json:"usbLinkAuthToken"`
		WifiAuthTokenConfigured    bool   `json:"wifiAuthTokenConfigured"`
		USBLinkAuthTokenConfigured bool   `json:"usbLinkAuthTokenConfigured"`
	}
	if err := json.NewDecoder(res.Body).Decode(&settingsBody); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if settingsBody.WifiAuthToken != "" || settingsBody.USBLinkAuthToken != "" {
		t.Fatalf("expected settings token redaction, got %+v", settingsBody)
	}
	if !settingsBody.WifiAuthTokenConfigured || !settingsBody.USBLinkAuthTokenConfigured {
		t.Fatalf("expected configured flags true, got %+v", settingsBody)
	}

	res, err = http.Get(srv.URL + "/api/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	var diagnosticsBody struct {
		Settings struct {
			WifiAuthToken              string `json:"wifiAuthToken"`
			USBLinkAuthToken           string `json:"usbLinkAuthToken"`
			WifiAuthTokenConfigured    bool   `json:"wifiAuthTokenConfigured"`
			USBLinkAuthTokenConfigured bool   `json:"usbLinkAuthTokenConfigured"`
		} `json:"settings"`
	}
	if err := json.NewDecoder(res.Body).Decode(&diagnosticsBody); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if diagnosticsBody.Settings.WifiAuthToken != "" || diagnosticsBody.Settings.USBLinkAuthToken != "" {
		t.Fatalf("expected diagnostics token redaction, got %+v", diagnosticsBody.Settings)
	}
	if !diagnosticsBody.Settings.WifiAuthTokenConfigured || !diagnosticsBody.Settings.USBLinkAuthTokenConfigured {
		t.Fatalf("expected diagnostics configured flags true, got %+v", diagnosticsBody.Settings)
	}
}

func TestStatusAPIExposesWarningsAndMetrics(t *testing.T) {
	store := newTestStore(t)
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	calibrationState.session = make(map[uint64]string)
	calibrationState.active = ""
	calibrationState.mu.Unlock()

	store := newSettingsStore(defaultSettings())
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
	srv := httptest.NewServer(newWebUIHandler(store, func() {}))
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
