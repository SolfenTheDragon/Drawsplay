package main

import (
	"strings"
)

// Wire formats for both host channels.
//
// # Input channel (tablet <-> host, port 27183)
//
// mux-v1 binary framing (see usbmux.go) carrying one UTF-8 JSON packet per
// frame. Control packets (hello, modePrefs) name their type in "type"; the
// frequent input and key packets use compact keys, "t" for the type. The
// packet struct below is the union of all fields across packet types (unknown
// fields decode as zero values, keeping additions backward compatible). See
// FUNCTIONALITY.md §3 for authoritative examples.
//
// Upstream (client -> host) types: "hello", "modePrefs", "input", and "key"
// (physical keyboard events, M5).
// Downstream (host -> client) types: "hostInfo", "displayMode", "clearDisplayMode",
// and "sessionPolicy".
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
	// videoPacketTile (2), videoPacketMode (3) and videoPacketFrameMeta (4)
	// are only sent to clients that advertised the matching videoFeatures
	// entry in their hello (see native_media.go).
	videoPacketTile      = 2
	videoPacketMode      = 3
	videoPacketFrameMeta = 4
	// videoPacketCursor (5) carries the host cursor for the tablet to draw
	// itself (clientFeatureCursor); the payload is the helper's cursor frame
	// (native/drawsplay-media-win/main.cpp, kKindCursor).
	videoPacketCursor = 5
)

// clientFeatureCursor: the client draws the host cursor from
// videoPacketCursor, so the Windows helper may take it out of the frames.
const clientFeatureCursor = "cursor1"

// packet is the union of all input-channel packet fields.
type packet struct {
	Type        string  `json:"type"`
	TypeShort   string  `json:"t"`
	Device      string  `json:"device"`
	Android     string  `json:"android"` // Android OS release, for logs
	App         string  `json:"app"`     // app version
	ClientOS    string  `json:"clientOS,omitempty"`
	ClientType  string  `json:"clientType,omitempty"`
	ClientVer   string  `json:"clientVersion,omitempty"`
	Action      string  `json:"a"`
	X           float32 `json:"x"`
	Y           float32 `json:"y"`
	Pressure    float32 `json:"p"`
	Tilt        float32 `json:"ti"`
	Orientation float32 `json:"o"`
	Buttons     int     `json:"b"`
	ToolType    string  `json:"tt"`
	PointerID   int     `json:"id"`
	PointerCnt  int     `json:"pc"`
	Width       int     `json:"w"`
	Height      int     `json:"h"`
	Timestamp   int64   `json:"ts"`

	// "hello" packets: the tablet panel's native resolution and the user's
	// preferred host-display mode (0 = no preference stored). The host sets
	// the virtual display to the preference — or the native size by
	// default — when the session display is created.
	ScreenWidth  int `json:"screenWidth"`
	ScreenHeight int `json:"screenHeight"`
	PrefWidth    int `json:"prefWidth"`
	PrefHeight   int `json:"prefHeight"`
	PrefFPS      int `json:"prefFps"`
	// ScreenRefreshRates (hello): refresh rates (Hz, rounded) the tablet
	// panel supports at its native resolution. Optional; older clients omit it.
	ScreenRefreshRates []int `json:"screenRefreshRates"`
	// RecommendedWidth/Height/Reason (hello): optional maximum resolution the
	// client suggests for its specs; shown in the web UI, never enforced.
	RecommendedWidth  int    `json:"recommendedWidth"`
	RecommendedHeight int    `json:"recommendedHeight"`
	RecommendedReason string `json:"recommendedReason"`
	// HostPrefs (hello): the client keeps display preferences per host and
	// will answer the host's "hostInfo" with a "modePrefs" packet (which
	// carries PrefWidth/PrefHeight/PrefFPS).
	HostPrefs   bool     `json:"hostPrefs"`
	VideoCodecs []string `json:"videoCodecs"`
	// VideoFeatures (hello): optional video-channel extensions the client
	// understands ("tile1", "meta1").
	VideoFeatures []string `json:"videoFeatures"`
	// InputSeq (input): per-session monotonically increasing sequence the
	// host echoes back in frame metadata to measure input-to-photon latency.
	InputSeq uint32 `json:"sq"`

	// "key" packets (M5): Action is "down"/"up", Keycode is the Android
	// KeyEvent keycode, Meta the Android meta-state bits, Chars the
	// character(s) the key produces (Unicode fallback for unmapped keys).
	Keycode int    `json:"k"`
	Meta    int    `json:"m"`
	Chars   string `json:"c"`
}

// kind is the packet's type: "type" on control packets, "t" on input and
// key packets.
func (p *packet) kind() string {
	if p.Type != "" {
		return p.Type
	}
	return p.TypeShort
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
	PenPrediction         bool   `json:"penPrediction"`
	PenInkPreview         bool   `json:"penInkPreview"`
	VideoScaling          string `json:"videoScaling,omitempty"`
}

// videoConfigMsg is the JSON payload of a config packet on the video channel.
type videoConfigMsg struct {
	Codec      string   `json:"codec"`
	HostCodecs []string `json:"hostCodecs,omitempty"`
	Encoder    string   `json:"encoder"`
	FPS        int      `json:"fps"`
	Bitrate    string   `json:"bitrate"`
	Pacing     string   `json:"pacing,omitempty"`
	// CaptureMode is "damage" when frames are produced on screen damage
	// rather than a fixed tick, so the client should present immediately.
	CaptureMode string `json:"captureMode,omitempty"`
}

// negotiateVideoCodec selects the first host-supported codec (in the host's
// order of preference) that the client offered; H.264 when the client offered
// none or none matches.
func negotiateVideoCodec(supported, offered []string) string {
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
	Type string `json:"type"` // "stats"; also "keyframe" and "ack" (see feedback.go)
	// Frames is the cumulative picture count of an "ack" message.
	Frames         int64   `json:"frames,omitempty"`
	IntervalMs     int64   `json:"intervalMs"`
	FramesReceived int64   `json:"framesReceived"`
	FramesRendered int64   `json:"framesRendered"`
	AvgDecodeMs    float64 `json:"avgDecodeMs"`
	QueuedFrames   int     `json:"queuedFrames"`
	AvgPresentMs   float64 `json:"avgPresentMs,omitempty"`
	// EchoMs is the tablet's recent median input-to-frame echo latency
	// (input sent until a frame carrying its sequence arrived); 0 = unknown.
	EchoMs          float64 `json:"echoMs,omitempty"`
	FrameDrops      int64   `json:"frameDrops,omitempty"`
	RefreshHz       float64 `json:"refreshHz,omitempty"`
	RenderMode      string  `json:"renderMode,omitempty"`
	PacingGuardrail bool    `json:"pacingGuardrail,omitempty"`
	// StaleFrames counts frames the client decoded but never showed, or
	// dropped before decoding, because newer ones were already queued: it
	// was catching up after a stall rather than keeping pace.
	StaleFrames int64 `json:"staleFrames,omitempty"`
	// AppForeground is omitted by legacy clients; Windows uses it to adjust
	// the capture process scheduling priority for background keep-alive.
	AppForeground *bool `json:"appForeground,omitempty"`
	// Paired Wi-Fi tablets sample their own association (RSSI dBm, link
	// rate, channel frequency) for the web UI's live signal indicator.
	WiFiRssiDbm      int `json:"wifiRssiDbm,omitempty"`
	WiFiLinkMbps     int `json:"wifiLinkMbps,omitempty"`
	WiFiFrequencyMHz int `json:"wifiFrequencyMhz,omitempty"`
}
