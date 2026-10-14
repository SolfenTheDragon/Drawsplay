package main

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type webUIAPI struct {
	store     *settingsStore
	csrfToken string
	stop      func()
}

type settingsView struct {
	settings
	WifiAuthTokenConfigured    bool `json:"wifiAuthTokenConfigured"`
	USBLinkAuthTokenConfigured bool `json:"usbLinkAuthTokenConfigured"`
}

func redactedSettingsView(s settings) settingsView {
	return settingsView{
		settings:                   redactSettingsSecrets(s),
		WifiAuthTokenConfigured:    strings.TrimSpace(s.WifiAuthToken) != "",
		USBLinkAuthTokenConfigured: strings.TrimSpace(s.USBLinkAuthToken) != "",
	}
}

func redactSettingsSecrets(s settings) settings {
	s.WifiAuthToken = ""
	s.USBLinkAuthToken = ""
	return s
}

// newWebUIHandler builds the web UI routes (separate from runWebUI so tests
// can drive the handler with httptest). stop is invoked, shortly after the
// response is written, by POST /api/stop.
func newWebUIHandler(store *settingsStore, stop func()) http.Handler {
	mux := http.NewServeMux()
	csrfToken := newCSRFToken()

	static, err := fs.Sub(webuiFS, "webui")
	if err != nil {
		panic(err) // embedded tree is fixed at build time
	}
	mux.Handle("/", http.FileServer(http.FS(static)))

	api := webUIAPI{store: store, csrfToken: csrfToken, stop: stop}
	api.register(mux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func (api webUIAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("/api/csrf", getOnly(api.handleCSRF))
	mux.HandleFunc("/api/settings", api.handleSettings)
	mux.HandleFunc("/api/status", getOnly(api.handleStatus))
	mux.HandleFunc("/api/setup-check", getOnly(api.handleSetupCheck))
	mux.HandleFunc("/api/setup/remediate", securePost(api.csrfToken, api.handleSetupRemediate))
	mux.HandleFunc("/api/diagnostics", getOnly(api.handleDiagnostics))
	mux.HandleFunc("/api/calibration", api.handleCalibration)
	mux.HandleFunc("/api/logs", getOnly(api.handleLogs))
	mux.HandleFunc("/api/autostart", securePost(api.csrfToken, api.handleAutostart))
	mux.HandleFunc("/api/stop", securePost(api.csrfToken, api.handleStop))
	mux.HandleFunc("/api/tablet/clear-display-mode", securePost(api.csrfToken, api.handleClearDisplayMode))
	mux.HandleFunc("/api/vdd/custom-resolution/setup", securePost(api.csrfToken, api.handleVDDCustomResolutionSetup))
	mux.HandleFunc("/api/session/input-lock", securePost(api.csrfToken, api.handleSessionInputLock))
	mux.HandleFunc("/api/session/disconnect", securePost(api.csrfToken, api.handleSessionDisconnect))
	mux.HandleFunc("/api/session/reconnect-video", securePost(api.csrfToken, api.handleSessionReconnectVideo))
}

func (api webUIAPI) handleCSRF(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"token": api.csrfToken})
}

func (api webUIAPI) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, redactedSettingsView(api.store.get()))
	case http.MethodPut, http.MethodPost:
		if !requireCSRF(w, r, api.csrfToken) {
			return
		}
		// Decode over the current settings so older clients that omit a
		// newly added field preserve its value instead of zeroing it.
		old := api.store.get()
		s := old
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&s); err != nil {
			httpError(w, http.StatusBadRequest, "invalid JSON: %v", err)
			return
		}
		if err := api.store.update(s); err != nil {
			httpError(w, http.StatusUnprocessableEntity, "%v", err)
			return
		}
		restarted := restartActiveVideoSession()
		inputRestarted := false
		if old.LocalCursor != s.LocalCursor ||
			old.TouchEnabled != s.TouchEnabled ||
			old.PenEnabled != s.PenEnabled ||
			old.KeepAliveOnBackground != s.KeepAliveOnBackground ||
			old.VideoScaling != s.VideoScaling {
			inputRestarted = restartActiveInputSessions()
		}
		runBackgroundPreflight(api.store.get())
		log.Printf("Settings updated via web UI: fps=%d bitrate=%s %dx%d localCursor=%v touch=%v pen=%v keyboard=%v keepAliveOnBackground=%v videoScaling=%s preferredADBSerial=%q requirePreferredADB=%v autoLaunchAndroidApp=%v zeroCopyCapture=%v temporalLayering=%v allowToolAutoInstall=%v noDriverInstall=%v (videoRestarted=%v inputRestarted=%v)",
			s.FPS, s.Bitrate, s.Width, s.Height, s.LocalCursor, s.TouchEnabled, s.PenEnabled,
			s.KeyboardEnabled, s.KeepAliveOnBackground, s.VideoScaling, s.PreferredADBSerial, s.RequirePreferredADB, s.AutoLaunchAndroidApp, s.ZeroCopyCapture, s.TemporalLayering, s.AllowToolAutoInstall,
			s.NoDriverInstall, restarted, inputRestarted)
		writeJSON(w, map[string]any{
			"ok":             true,
			"restarted":      restarted,
			"inputRestarted": inputRestarted,
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (api webUIAPI) handleStatus(w http.ResponseWriter, _ *http.Request) {
	enabled, autostartErr := autostartEnabled()
	status := map[string]any{
		"video":                   currentVideoStatus(),
		"clientVideoStats":        currentClientVideoStats(),
		"adb":                     currentADBStatus(),
		"warnings":                currentOperationalWarnings(),
		"metrics":                 currentOperationalMetrics(),
		"setup":                   currentSetupChecklist(),
		"autostart":               enabled,
		"autostartImplementation": autostartImplementation(),
		"hostOS":                  runtime.GOOS,
		"featureFlags":            map[string]bool{"wifi": wifiFeatureEnabled()},
		"activeClient":            activeClientIdentity(),
		"activeInputDevice":       activeInputDevice(),
		"inputLocked":             sessionInputLocked(),
	}
	if autostartErr != nil {
		status["autostartError"] = autostartErr.Error()
	}
	writeJSON(w, status)
}

func (api webUIAPI) handleSetupCheck(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, currentSetupChecklist())
}

func (api webUIAPI) handleSetupRemediate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	cfg := api.store.get()
	switch strings.TrimSpace(req.Action) {
	case "", "restart-adb":
		if err := ensureADBAvailable(cfg.ADB); err != nil {
			httpError(w, http.StatusUnprocessableEntity, "adb restart failed: %v", err)
			return
		}
		runBackgroundPreflight(cfg)
		if x11SessionActive() {
			refreshX11VkmsWarning()
		}
		video := currentVideoStatus()
		videoRestarted := false
		if shouldRestartX11AfterSetupRepair(x11SessionActive(), video, x11VirtualDisplayReady()) {
			videoRestarted = restartActiveVideoSession()
		}
		writeJSON(w, map[string]any{
			"ok":             true,
			"action":         "restart-adb",
			"videoRestarted": videoRestarted,
			"setup":          currentSetupChecklist(),
		})
	default:
		httpError(w, http.StatusUnprocessableEntity, "unknown setup action: %s", req.Action)
	}
}

func shouldRestartX11AfterSetupRepair(x11Session bool, video videoStatusInfo, virtualOutputReady bool) bool {
	return x11Session && video.Streaming && video.DisplayMode == "mirror" && virtualOutputReady
}

func (api webUIAPI) handleDiagnostics(w http.ResponseWriter, _ *http.Request) {
	lines, next := currentDebugLogs(0)
	writeJSON(w, map[string]any{
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"runtime": map[string]any{
			"goVersion": runtime.Version(),
			"goOS":      runtime.GOOS,
			"goArch":    runtime.GOARCH,
			"cpus":      runtime.NumCPU(),
		},
		"settings":          redactedSettingsView(api.store.get()),
		"video":             currentVideoStatus(),
		"clientVideoStats":  currentClientVideoStats(),
		"activeClient":      activeClientIdentity(),
		"adb":               currentADBStatus(),
		"warnings":          currentOperationalWarnings(),
		"metrics":           currentOperationalMetrics(),
		"setup":             currentSetupChecklist(),
		"activeInputDevice": activeInputDevice(),
		"inputLocked":       sessionInputLocked(),
		"logCursor":         next,
		"logLines":          lines,
	})
}

func (api webUIAPI) handleCalibration(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		device := strings.TrimSpace(r.URL.Query().Get("device"))
		if device == "" {
			device = activeInputDevice()
		}
		writeJSON(w, map[string]any{
			"device":      device,
			"calibration": calibrationForDevice(device),
		})
	case http.MethodPut, http.MethodPost:
		if !requireCSRF(w, r, api.csrfToken) {
			return
		}
		var req struct {
			Device      string            `json:"device"`
			Calibration deviceCalibration `json:"calibration"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid JSON: %v", err)
			return
		}
		if err := setCalibrationForDevice(req.Device, req.Calibration); err != nil {
			httpError(w, http.StatusUnprocessableEntity, "%v", err)
			return
		}
		writeJSON(w, map[string]any{
			"ok":          true,
			"device":      strings.TrimSpace(req.Device),
			"calibration": calibrationForDevice(req.Device),
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (api webUIAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	since := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		since = parsed
	}
	lines, next := currentDebugLogs(since)
	writeJSON(w, map[string]any{
		"lines": lines,
		"next":  next,
	})
}

func (api webUIAPI) handleAutostart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	var err error
	if req.Enabled {
		err = installAutostart()
	} else {
		err = uninstallAutostart()
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "autostart": req.Enabled})
}

func (api webUIAPI) handleStop(w http.ResponseWriter, _ *http.Request) {
	log.Printf("Shutdown requested via web UI")
	writeJSON(w, map[string]any{"ok": true})
	// Let the response flush before tearing the server down.
	go func() {
		time.Sleep(200 * time.Millisecond)
		api.stop()
	}()
}

func (api webUIAPI) handleClearDisplayMode(w http.ResponseWriter, _ *http.Request) {
	if !publishModePreferenceClear() {
		httpError(w, http.StatusConflict, "no connected client session")
		return
	}
	log.Printf("Requested client display-preference clear via web UI")
	writeJSON(w, map[string]any{"ok": true})
}

func (api webUIAPI) handleSessionInputLock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Locked bool `json:"locked"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	setSessionInputLocked(req.Locked)
	writeJSON(w, map[string]any{"ok": true, "inputLocked": req.Locked})
}

func (api webUIAPI) handleSessionDisconnect(w http.ResponseWriter, _ *http.Request) {
	videoRestarted := restartActiveVideoSession()
	inputRestarted := restartActiveInputSessions()
	writeJSON(w, map[string]any{
		"ok":             true,
		"videoRestarted": videoRestarted,
		"inputRestarted": inputRestarted,
	})
}

func (api webUIAPI) handleSessionReconnectVideo(w http.ResponseWriter, _ *http.Request) {
	videoRestarted := restartActiveVideoSession()
	writeJSON(w, map[string]any{
		"ok":             true,
		"videoRestarted": videoRestarted,
	})
}
