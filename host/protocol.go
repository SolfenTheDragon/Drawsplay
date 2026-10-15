package main

import (
	"strings"
	"sync"
)

// Wire formats for both host channels.
//
// # Input channel (tablet <-> host, port 27183)
//
// Primary mode is mux-v1 binary framing (see usbmux.go) carrying UTF-8 JSON
// payloads on stream ids; legacy newline-delimited JSON is still accepted for
// backward compatibility. Every packet carries a "type" field; the packet
// struct below is the union of all fields across packet types (unknown fields
// decode as zero values, keeping additions backward compatible). See
// FUNCTIONALITY.md §3 for authoritative examples.
//
// Upstream (client -> host) types: "hello", "modePrefs", "input", and "key"
// (physical keyboard events, M5).
// Downstream (host -> client) types: "hostInfo", "displayMode", "clearDisplayMode",
// "sessionPolicy", and "cursor".
//
// # Video channel (host -> tablet, port 27184)
//
// Downstream: binary framing, big-endian:
//
//	[1 byte packet type][4 byte payload length][payload]
//
// Packet types:
//
//	0 (config) — JSON: {"codec":"h264","encoder":"h264_nvenc","fps":60,
//	             "bitrate":"8000k"}. Sent at session start and again after
//	             every adaptive-bitrate pipeline restart, always before the
//	             bitstream that follows.
//	1 (nal)    — one H.264 Annex-B NAL unit, start code included. The
//	             decoder feeds these directly to MediaCodec.
//
// Upstream (same socket, tablet -> host): newline-delimited JSON decode
// statistics (clientStatsMsg below), one report every ~2 s. These drive the
// host's adaptive bitrate and dead-peer detection (see feedback.go).
//
// NAL units (not raw byte chunks) are the framing unit so the client can
// hand the decoder complete parameter sets and pictures without scanning
// for start codes itself.

// Channel ports. The default input and video ports are localhost-only and
// reach Android through `adb reverse` over USB. Optional Wi-Fi
// transports use independently configured TCP listeners; the web UI remains
// local-only.
const (
	inputPort = 27183
	videoPort = 27184
	// webuiPort serves the localhost settings UI (webui.go).
	webuiPort = 27180
)

// Video channel packet types.
const (
	videoPacketConfig = 0
	videoPacketNAL    = 1
)

// packet is the union of all input-channel packet fields.
type packet struct {
	Type        string  `json:"type"`
	TypeShort   string  `json:"t"`
	Device      string  `json:"device"`
	Android     string  `json:"android"` // legacy Android OS field
	App         string  `json:"app"`     // legacy app version field
	ClientOS    string  `json:"clientOS,omitempty"`
	ClientOSS   string  `json:"os,omitempty"`
	ClientType  string  `json:"clientType,omitempty"`
	ClientTypeS string  `json:"ct,omitempty"`
	ClientVer   string  `json:"clientVersion,omitempty"`
	ClientVerS  string  `json:"cv,omitempty"`
	Action      string  `json:"action"`
	ActionShort string  `json:"a"`
	X           float32 `json:"x"`
	Y           float32 `json:"y"`
	Pressure    float32 `json:"pressure"`
	PressureS   float32 `json:"p"`
	Tilt        float32 `json:"tilt"`
	TiltS       float32 `json:"ti"`
	Orientation float32 `json:"orientation"`
	OrientS     float32 `json:"o"`
	Buttons     int     `json:"buttons"`
	ButtonsS    int     `json:"b"`
	ToolType    string  `json:"toolType"`
	ToolTypeS   string  `json:"tt"`
	PointerID   int     `json:"pointerId"`
	PointerIDS  int     `json:"id"`
	PointerCnt  int     `json:"pointerCount"`
	PointerCntS int     `json:"pc"`
	Width       int     `json:"width"`
	WidthS      int     `json:"w"`
	Height      int     `json:"height"`
	HeightS     int     `json:"h"`
	Timestamp   int64   `json:"timestamp"`
	TimestampS  int64   `json:"ts"`

	// "hello" packets: the tablet panel's native resolution and the user's
	// preferred host-display mode (0 = no preference stored). The host sets
	// the virtual display to the preference — or the native size by
	// default — when the session display is created.
	ScreenWidth  int `json:"screenWidth"`
	ScreenHeight int `json:"screenHeight"`
	PrefWidth    int `json:"prefWidth"`
	PrefHeight   int `json:"prefHeight"`
	PrefFPS      int `json:"prefFps"`
	// HostPrefs (hello): the client keeps display preferences per host and
	// will answer the host's "hostInfo" with a "modePrefs" packet (which
	// carries PrefWidth/PrefHeight/PrefFPS).
	HostPrefs   bool     `json:"hostPrefs"`
	VideoCodecs []string `json:"videoCodecs"`

	// "key" packets (M5): Action is "down"/"up", Keycode is the Android
	// KeyEvent keycode, Meta the Android meta-state bits, Chars the
	// character(s) the key produces (Unicode fallback for unmapped keys).
	Keycode   int    `json:"keycode"`
	KeycodeS  int    `json:"k"`
	Meta      int    `json:"meta"`
	MetaS     int    `json:"m"`
	Chars     string `json:"chars"`
	CharsS    string `json:"c"`
	AuthToken string `json:"authToken,omitempty"`
}

// normalizeAliases folds compact packet keys into the canonical fields so
// both wire formats decode the same way on the host.
func (p *packet) normalizeAliases() {
	if p.Type == "" {
		p.Type = p.TypeShort
	}
	if p.Action == "" {
		p.Action = p.ActionShort
	}
	if p.Pressure == 0 {
		p.Pressure = p.PressureS
	}
	if p.Tilt == 0 {
		p.Tilt = p.TiltS
	}
	if p.Orientation == 0 {
		p.Orientation = p.OrientS
	}
	if p.Buttons == 0 {
		p.Buttons = p.ButtonsS
	}
	if p.ToolType == "" {
		p.ToolType = p.ToolTypeS
	}
	if p.PointerID == 0 {
		p.PointerID = p.PointerIDS
	}
	if p.PointerCnt == 0 {
		p.PointerCnt = p.PointerCntS
	}
	if p.Width == 0 {
		p.Width = p.WidthS
	}
	if p.Height == 0 {
		p.Height = p.HeightS
	}
	if p.Timestamp == 0 {
		p.Timestamp = p.TimestampS
	}
	if p.Keycode == 0 {
		p.Keycode = p.KeycodeS
	}
	if p.Meta == 0 {
		p.Meta = p.MetaS
	}
	if p.Chars == "" {
		p.Chars = p.CharsS
	}
	if p.ClientOS == "" {
		p.ClientOS = p.ClientOSS
	}
	if p.ClientType == "" {
		p.ClientType = p.ClientTypeS
	}
	if p.ClientVer == "" {
		p.ClientVer = p.ClientVerS
	}
}

// hostDisplayModeMsg is a downstream input-channel packet announcing that
// the session display's resolution changed (the user adjusted it in the OS
// display settings). The tablet persists it as its preferred mode so the
// choice survives reconnects and host restarts.
type hostDisplayModeMsg struct {
	Type   string `json:"type"` // always "displayMode"
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// FPS is the new capture rate; 0 means unchanged (omitted).
	FPS int `json:"fps,omitempty"`
}

// hostInfoMsg identifies this host to a tablet that sent hello with
// hostPrefs, so it can look up the display mode it remembered for this host.
type hostInfoMsg struct {
	Type     string `json:"type"` // always "hostInfo"
	HostID   string `json:"hostId"`
	HostName string `json:"hostName,omitempty"`
}

// hostClearDisplayModeMsg asks the tablet to clear its stored preferred
// display mode so future hellos report no resolution preference.
type hostClearDisplayModeMsg struct {
	Type string `json:"type"` // always "clearDisplayMode"
}

// hostSessionPolicyMsg tells Android whether to keep the transport alive when
// the app is backgrounded (for monitor persistence across app switches).
type hostSessionPolicyMsg struct {
	Type                  string `json:"type"` // always "sessionPolicy"
	KeepAliveOnBackground bool   `json:"keepAliveOnBackground"`
	VideoScaling          string `json:"videoScaling,omitempty"`
	WifiEnabled           bool   `json:"wifiEnabled"`
	WifiAuthToken         string `json:"wifiAuthToken,omitempty"`
	WifiTLSRequired       bool   `json:"wifiTlsRequired"`
	WifiTLSFingerprint    string `json:"wifiTlsFingerprint,omitempty"`
}

// hostCursorMsg is pushed over the input channel so Android can render the
// pointer locally at input cadence instead of relying on DXGI's intermittently
// refreshed hardware-cursor metadata. X/Y are normalized to the active
// streamed display.
type hostCursorMsg struct {
	Type    string  `json:"type"` // always "cursor"
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Visible bool    `json:"visible"`
}

// videoConfigMsg is the JSON payload of a config packet on the video channel.
type videoConfigMsg struct {
	Codec      string   `json:"codec"`
	HostCodecs []string `json:"hostCodecs,omitempty"`
	Encoder    string   `json:"encoder"`
	FPS        int      `json:"fps"`
	Bitrate    string   `json:"bitrate"`
	Pacing     string   `json:"pacing,omitempty"`
}

var (
	hostVideoCodecs = []string{"h264"}
	clientCodecsMu  sync.RWMutex
	clientCodecs    []string
	hostCodecsMu    sync.RWMutex
)

func publishClientVideoCodecs(codecs []string) {
	normalized := make([]string, 0, len(codecs))
	for _, c := range codecs {
		cc := strings.ToLower(strings.TrimSpace(c))
		if cc == "h265" {
			cc = "hevc"
		}
		if cc != "" {
			normalized = append(normalized, cc)
		}
	}
	clientCodecsMu.Lock()
	clientCodecs = append([]string(nil), normalized...)
	clientCodecsMu.Unlock()
}

func publishHostVideoCodecs(codecs []string) {
	hostCodecsMu.Lock()
	hostVideoCodecs = append([]string(nil), codecs...)
	hostCodecsMu.Unlock()
}

// negotiateVideoCodec selects the first host-preferred codec that the client
// also advertised. Legacy clients without a list remain on H.264.
func negotiateVideoCodec() string {
	clientCodecsMu.RLock()
	offered := append([]string(nil), clientCodecs...)
	clientCodecsMu.RUnlock()
	hostCodecsMu.RLock()
	supported := append([]string(nil), hostVideoCodecs...)
	hostCodecsMu.RUnlock()
	if len(offered) == 0 {
		setOperationalWarning("codec", "")
		return "h264"
	}
	for _, hostCodec := range supported {
		for _, clientCodec := range offered {
			if strings.EqualFold(hostCodec, clientCodec) {
				setOperationalWarning("codec", "")
				return hostCodec
			}
		}
	}
	setOperationalWarning("codec",
		"Client did not advertise H.264 support; the current protocol still selects H.264")
	return "h264"
}

// clientStatsMsg is one upstream decode-statistics report on the video
// channel (client → host, newline-delimited JSON; see feedback.go). All
// counters are per-interval deltas, not cumulative.
type clientStatsMsg struct {
	Type            string  `json:"type"` // always "stats"
	IntervalMs      int64   `json:"intervalMs"`
	FramesReceived  int64   `json:"framesReceived"`
	FramesRendered  int64   `json:"framesRendered"`
	AvgDecodeMs     float64 `json:"avgDecodeMs"`
	QueuedFrames    int     `json:"queuedFrames"`
	AvgPresentMs    float64 `json:"avgPresentMs,omitempty"`
	FrameDrops      int64   `json:"frameDrops,omitempty"`
	RefreshHz       float64 `json:"refreshHz,omitempty"`
	RenderMode      string  `json:"renderMode,omitempty"`
	PacingGuardrail bool    `json:"pacingGuardrail,omitempty"`
	// StaleFrames counts frames the client decoded but never showed, or
	// dropped before decoding, because newer ones were already queued: it
	// was catching up after a stall rather than keeping pace.
	StaleFrames int64 `json:"staleFrames,omitempty"`
}

// videoAuthMsg is an optional first upstream line on Wi-Fi video sessions.
type videoAuthMsg struct {
	Type      string `json:"type"` // "auth"
	AuthToken string `json:"authToken,omitempty"`
}
