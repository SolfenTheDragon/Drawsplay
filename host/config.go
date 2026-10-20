package main

// Persisted host settings (web UI backend storage).
//
// Settings live in a JSON file under the OS user config directory
// (%APPDATA%\DrawSplay\config.json on Windows,
// ~/.config/DrawSplay/config.json on Linux). Precedence at startup:
//
//	explicit CLI flags  >  config file  >  built-in defaults
//
// so a manually launched `host -bitrate 4M` always wins for that run, while
// the autostarted background instance follows whatever the web UI saved.
//
// settingsStore is the process-wide source of truth after startup: the web
// UI mutates it (persisting each change) and every new video session takes
// a snapshot from it, so saved changes apply live (the active session is
// restarted separately, see video.go restartActiveVideoSession).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// settings is the user-tunable host configuration.
type settings struct {
	ADB                  string `json:"adb"`
	PreferredADBSerial   string `json:"preferredADBSerial,omitempty"`
	RequirePreferredADB  bool   `json:"requirePreferredADB"`
	AllowToolAutoInstall bool   `json:"allowToolAutoInstall"`
	AutoLaunchAndroidApp bool   `json:"autoLaunchAndroidApp"`
	ZeroCopyCapture      bool   `json:"zeroCopyCapture"`
	TemporalLayering     bool   `json:"temporalLayering"`
	FFmpeg               string `json:"ffmpeg"`
	FPS                  int    `json:"fps"`
	Bitrate              string `json:"bitrate"`
	Width                int    `json:"width"`
	Height               int    `json:"height"`
	TouchEnabled         bool   `json:"touchEnabled"`
	PenEnabled           bool   `json:"penEnabled"`
	// TouchGestures makes Linux fingers a real multi-touch screen (pinch,
	// two-finger scroll) instead of a pointer, where a one-finger drag selects.
	TouchGestures         bool `json:"touchGestures"`
	KeyboardEnabled       bool `json:"keyboardEnabled"`
	KeepAliveOnBackground bool `json:"keepAliveOnBackground"`
	// PenPrediction makes the tablet extrapolate stylus hover motion a few
	// milliseconds ahead so the host cursor tracks the pen with less lag.
	PenPrediction bool `json:"penPrediction"`
	// IdleCaptureGate pauses the Linux X11 capture process while the screen is
	// static (no damage, no pointer movement) and resumes it on the first
	// change, cutting idle CPU, bitrate and tablet decode work. Off by
	// default; turning it off restores continuous capture.
	IdleCaptureGate bool `json:"idleCaptureGate"`
	// X11HighRateCapture lets Linux X11 capture run above 60 fps (up to the
	// tablet's rate) when VAAPI can sustain it, sampling the 60 Hz desktop
	// twice per frame to cut capture-phase latency at about 2x encoder CPU.
	// Off clamps X11 capture to 60 fps.
	X11HighRateCapture bool `json:"x11HighRateCapture"`
	// NativeMediaHelper selects the optional native capture/encode helper
	// ("auto", "on", "off"; empty is auto). It only ever adds a candidate in
	// front of the ffmpeg ones, which remain the fallback.
	NativeMediaHelper string `json:"nativeMediaHelper"`
	// VideoScaling is how the tablet lays the host picture out on its panel
	// (videoScalingFill, videoScalingFit or videoScalingOriginal).
	VideoScaling    string `json:"videoScaling"`
	NoDriverInstall bool   `json:"noDriverInstall"`
	WifiEnabled     bool   `json:"wifiEnabled"`
	WifiBindAddress string `json:"wifiBindAddress"`
	WifiInputPort   int    `json:"wifiInputPort"`
	WifiVideoPort   int    `json:"wifiVideoPort"`
	WifiServiceName string `json:"wifiServiceName"`
	WifiTLS         bool   `json:"wifiTLS"`
	WifiAuthToken   string `json:"wifiAuthToken,omitempty"`
	WifiMaxBitrate  string `json:"wifiMaxBitrate"`
	AOAEnabled      bool   `json:"aoaEnabled"`
	// ADBTunnelEnabled turns on the legacy adb-reverse transport. It needs
	// Developer options and USB debugging on the tablet, so it is opt-in.
	ADBTunnelEnabled bool `json:"adbTunnelEnabled"`
	// IdleDisplayTimeoutSec removes the virtual display once no tablet has
	// been connected for this many seconds (0 disables the check).
	IdleDisplayTimeoutSec int `json:"idleDisplayTimeoutSec"`
}

// defaultSettings mirrors the flag defaults in main.go.
func defaultSettings() settings {
	return settings{
		ADB:                   "adb",
		FFmpeg:                "ffmpeg",
		FPS:                   120,
		Bitrate:               "24M",
		Width:                 1920,
		Height:                1080,
		AllowToolAutoInstall:  true,
		AutoLaunchAndroidApp:  false,
		ZeroCopyCapture:       true,
		TemporalLayering:      false,
		TouchEnabled:          true,
		PenEnabled:            true,
		TouchGestures:         true,
		KeyboardEnabled:       true,
		KeepAliveOnBackground: false,
		PenPrediction:         true,
		X11HighRateCapture:    true,
		VideoScaling:          videoScalingFill,
		NoDriverInstall:       false,
		WifiEnabled:           false,
		WifiBindAddress:       "0.0.0.0",
		WifiInputPort:         37183,
		WifiVideoPort:         37184,
		WifiServiceName:       "DrawSplay",
		WifiTLS:               true,
		WifiAuthToken:         "",
		WifiMaxBitrate:        "12M",
		AOAEnabled:            true,
		ADBTunnelEnabled:      false,
		IdleDisplayTimeoutSec: defaultIdleDisplayTimeoutSec,
	}
}

const (
	defaultIdleDisplayTimeoutSec = 600
	maxIdleDisplayTimeoutSec     = 86400
)

// Tablet video scaling modes. Fill stretches the picture over the whole panel
// (the original behaviour), fit scales it to the panel keeping its aspect
// ratio, and original shows it pixel for pixel, centred (scaled down like fit
// only when it is larger than the panel).
const (
	videoScalingFill     = "fill"
	videoScalingFit      = "fit"
	videoScalingOriginal = "original"
)

func validVideoScaling(mode string) bool {
	switch mode {
	case videoScalingFill, videoScalingFit, videoScalingOriginal:
		return true
	}
	return false
}

// validate rejects values that would break the capture pipeline or the
// virtual display. Returned errors are user-facing (web UI form feedback).
func (s settings) validate() error {
	if s.FPS < 10 || s.FPS > 240 {
		return fmt.Errorf("fps must be between 10 and 240")
	}
	if kbps := parseBitrateKbps(s.Bitrate); kbps < minLadderKbps {
		return fmt.Errorf("bitrate must be at least %dk (ffmpeg syntax, e.g. 24M)", minLadderKbps)
	}
	if s.Width < 640 || s.Width > 7680 || s.Height < 480 || s.Height > 4320 {
		return fmt.Errorf("resolution must be between 640x480 and 7680x4320")
	}
	if s.IdleDisplayTimeoutSec < 0 || s.IdleDisplayTimeoutSec > maxIdleDisplayTimeoutSec {
		return fmt.Errorf("idle display timeout must be between 0 (disabled) and %d seconds", maxIdleDisplayTimeoutSec)
	}
	if !validVideoScaling(s.VideoScaling) {
		return fmt.Errorf("video scaling must be %q, %q or %q", videoScalingFill, videoScalingFit, videoScalingOriginal)
	}
	if err := validateConfiguredExecutable(s.ADB, "adb"); err != nil {
		return fmt.Errorf("adb path: %w", err)
	}
	if err := validateConfiguredExecutable(s.FFmpeg, "ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg path: %w", err)
	}
	if strings.IndexFunc(s.PreferredADBSerial, unicode.IsControl) >= 0 ||
		strings.ContainsAny(s.PreferredADBSerial, " \t\r\n") {
		return fmt.Errorf("preferred ADB serial must not contain whitespace or control characters")
	}
	if s.RequirePreferredADB && strings.TrimSpace(s.PreferredADBSerial) == "" {
		return fmt.Errorf("preferred ADB serial is required when strict device trust is enabled")
	}
	if wifiFeatureEnabled() {
		if strings.TrimSpace(s.WifiBindAddress) == "" {
			return fmt.Errorf("wifi bind address must not be empty")
		}
		if s.WifiInputPort < 1024 || s.WifiInputPort > 65535 {
			return fmt.Errorf("wifi input port must be between 1024 and 65535")
		}
		if s.WifiVideoPort < 1024 || s.WifiVideoPort > 65535 {
			return fmt.Errorf("wifi video port must be between 1024 and 65535")
		}
		if s.WifiInputPort == s.WifiVideoPort {
			return fmt.Errorf("wifi input and video ports must differ")
		}
		if strings.TrimSpace(s.WifiServiceName) == "" {
			return fmt.Errorf("wifi service name must not be empty")
		}
		if kbps := parseBitrateKbps(s.WifiMaxBitrate); kbps < minLadderKbps {
			return fmt.Errorf("wifi max bitrate must be at least %dk", minLadderKbps)
		}
		if strings.IndexFunc(s.WifiAuthToken, unicode.IsControl) >= 0 {
			return fmt.Errorf("wifi auth token must not contain control characters")
		}
		if s.WifiEnabled && strings.TrimSpace(s.WifiAuthToken) == "" {
			return fmt.Errorf("wifi auth token is required when Wi-Fi transport is enabled")
		}
		if s.WifiEnabled && !s.WifiTLS {
			return fmt.Errorf("wifi TLS is required when Wi-Fi transport is enabled")
		}
	}
	return nil
}

func validateConfiguredExecutable(value, tool string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("contains an invalid NUL character")
	}
	if strings.EqualFold(value, tool) || strings.EqualFold(value, tool+".exe") {
		return nil
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") || strings.HasPrefix(lower, "file:") {
		return fmt.Errorf("URLs are not allowed")
	}
	if strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `//`) {
		return fmt.Errorf("UNC/network paths are not allowed")
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("use the default bare name %q or an absolute local executable path", tool)
	}
	clean := filepath.Clean(value)
	if isNetworkExecutablePath(clean) {
		return fmt.Errorf("network-backed paths are not allowed")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("executable is not accessible: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("must name a regular executable file")
	}
	if err := validateLocalExecutable(clean, info); err != nil {
		return err
	}
	return nil
}

// configDir returns the DrawSplay per-user config directory, creating it if
// needed.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "DrawSplay")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadSettings reads the config file over the defaults. A missing file is
// not an error (first run); a corrupt file is reported and ignored.
func loadSettings() settings {
	s := defaultSettings()
	path, err := configPath()
	if err != nil {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: ignoring corrupt config %s: %v\n", path, err)
		return defaultSettings()
	}
	return applyFeatureFlags(s)
}

// saveSettings writes the config file atomically (write temp + rename).
func saveSettings(s settings) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// settingsStore is the mutable, thread-safe runtime view of the settings.
type settingsStore struct {
	mu sync.RWMutex
	s  settings
}

// globalSettingsStore is a process-wide handle to the running settings
// store, set once in main() right after it's created. It exists so
// OS-specific code far from main() -- currently only the Linux extended
// displays' watchers (x11_extend_linux.go, mutter_extend_linux.go), which
// persist a resolution picked in the display settings back into the config
// -- can update settings without every intermediate caller threading a
// *settingsStore through. Safe for concurrent use: settingsStore itself is
// already mutex-guarded, and this pointer is written once before any
// reader goroutine starts.
var globalSettingsStore *settingsStore

// updateResolution persists a new width/height into the settings snapshot
// and config file, without going through the full web UI update path (no
// CSRF, no active-session restart -- the caller already owns that
// decision). Used by the Linux extended displays' watchers to make a
// resolution picked in the display settings stick across the session
// restart it triggers; otherwise the next session would set the display
// back to the old configured resolution instead of keeping the user's
// choice.
func (st *settingsStore) updateResolution(width, height int) error {
	// Push to the tablet too: it is the durable store of the preferred mode.
	publishModeChange(width, height, 0)
	s := st.get()
	s.Width = width
	s.Height = height
	if err := s.validate(); err != nil {
		return err
	}
	if err := saveSettings(s); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	st.mu.Lock()
	st.s = s
	st.mu.Unlock()
	return nil
}

func newSettingsStore(s settings) *settingsStore {
	return &settingsStore{s: s}
}

// get returns a snapshot of the current settings.
func (st *settingsStore) get() settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.s
}

// update validates, persists, and applies new settings.
func (st *settingsStore) update(s settings) error {
	s = applyFeatureFlags(s)
	if err := s.validate(); err != nil {
		return err
	}
	if err := saveSettings(s); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	st.mu.Lock()
	st.s = s
	st.mu.Unlock()
	return nil
}

// videoOptions converts a settings snapshot into the video pipeline's
// option struct.
func (s settings) videoOptions() videoOptions {
	return videoOptions{
		FFmpeg:           s.FFmpeg,
		FPS:              s.FPS,
		Bitrate:          s.Bitrate,
		Width:            s.Width,
		Height:           s.Height,
		ZeroCopyCapture:  s.ZeroCopyCapture,
		TemporalLayering: s.TemporalLayering,
		IdleGate:         s.IdleCaptureGate,
		X11HighRate:      s.X11HighRateCapture,
		NativeMedia:      s.NativeMediaHelper,
	}
}
