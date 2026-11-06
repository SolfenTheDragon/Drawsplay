package main

import (
	"runtime"
	"time"
)

// hostSnapshot is the state the settings page and the diagnostics download
// both show, read once from one settings snapshot so a response never mixes
// two versions. The JSON names are the web UI's API.
type hostSnapshot struct {
	Video             videoStatusInfo    `json:"video"`
	ClientVideoStats  *clientStatsMsg    `json:"clientVideoStats"`
	ADB               adbStatusInfo      `json:"adb"`
	AOA               aoaStatusInfo      `json:"aoa"`
	Warnings          []string           `json:"warnings"`
	Metrics           operationalMetrics `json:"metrics"`
	Setup             setupChecklist     `json:"setup"`
	ActiveClient      clientIdentity     `json:"activeClient"`
	ActiveInputDevice string             `json:"activeInputDevice"`
	InputLocked       bool               `json:"inputLocked"`
}

func currentHostSnapshot(cfg settings) hostSnapshot {
	client := activeClientSession()
	return hostSnapshot{
		Video:             currentVideoStatus(),
		ClientVideoStats:  currentClientVideoStats(),
		ADB:               currentADBStatus(),
		AOA:               currentAOAStatus(),
		Warnings:          currentOperationalWarnings(),
		Metrics:           currentOperationalMetrics(),
		Setup:             currentSetupChecklist(cfg),
		ActiveClient:      client.Identity,
		ActiveInputDevice: client.Device,
		InputLocked:       sessionInputLocked(),
	}
}

// hostStatus is GET /api/status: the snapshot plus what the settings page
// needs about this host and its connections.
type hostStatus struct {
	hostSnapshot
	ADBTunnelEnabled        bool              `json:"adbTunnelEnabled"`
	AOAEnabled              bool              `json:"aoaEnabled"`
	Autostart               bool              `json:"autostart"`
	AutostartError          string            `json:"autostartError,omitempty"`
	AutostartImplementation string            `json:"autostartImplementation"`
	HostOS                  string            `json:"hostOS"`
	Version                 string            `json:"version"`
	FeatureFlags            map[string]bool   `json:"featureFlags"`
	DesktopHost             desktopHostStatus `json:"desktopHost"`
	ConnectionKind          string            `json:"connectionKind"`
	ActiveTransport         string            `json:"activeTransport"`
}

func currentHostStatus(cfg settings) hostStatus {
	autostart, autostartErr := autostartEnabled()
	desktop := currentDesktopHostStatus()
	st := hostStatus{
		hostSnapshot:            currentHostSnapshot(cfg),
		ADBTunnelEnabled:        cfg.ADBTunnelEnabled,
		AOAEnabled:              cfg.AOAEnabled,
		Autostart:               autostart,
		AutostartImplementation: autostartImplementation(),
		HostOS:                  runtime.GOOS,
		Version:                 hostVersion(),
		FeatureFlags:            map[string]bool{"wifi": wifiFeatureEnabled()},
		DesktopHost:             desktop,
		ConnectionKind:          connectionKind(androidClientConnected(), desktop),
		ActiveTransport:         hostSessionOwnership.currentTransport(),
	}
	if autostartErr != nil {
		st.AutostartError = autostartErr.Error()
	}
	return st
}

// hostDiagnostics is GET /api/diagnostics: the snapshot plus the settings,
// the Go runtime and the captured log.
type hostDiagnostics struct {
	hostSnapshot
	GeneratedAt string         `json:"generatedAt"`
	Runtime     map[string]any `json:"runtime"`
	Settings    settings       `json:"settings"`
	LogCursor   int            `json:"logCursor"`
	LogLines    []string       `json:"logLines"`
}

func currentHostDiagnostics(cfg settings) hostDiagnostics {
	lines, next := currentDebugLogs(0)
	return hostDiagnostics{
		hostSnapshot: currentHostSnapshot(cfg),
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Runtime: map[string]any{
			"goVersion": runtime.Version(),
			"goOS":      runtime.GOOS,
			"goArch":    runtime.GOARCH,
			"cpus":      runtime.NumCPU(),
		},
		Settings:  cfg,
		LogCursor: next,
		LogLines:  lines,
	}
}
