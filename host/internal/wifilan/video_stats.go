package wifilan

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Hello feature bits. Negotiation keeps only bits both peers offer.
const (
	FeatureLowLatency uint32 = 1 << 0
	// FeatureVideoStats: after attach the tablet sends decode-statistics
	// records (control subtype 8) upstream on the video socket.
	FeatureVideoStats uint32 = 1 << 1

	knownFeatures = FeatureLowLatency | FeatureVideoStats
)

var ErrVideoStats = errors.New("invalid Wi-Fi video statistics record; reconnect video")

var videoStatsPrefix = [4]byte{8, 0, 0, 1}

// KeyframeRequest is control subtype 9, schema 1, with no body: the tablet
// discarded queued pictures and waits for an IDR to resume decoding.
var KeyframeRequest = [4]byte{9, 0, 0, 1}

// EncodeVideoStats wraps one JSON statistics object (the USB clientStatsMsg
// schema, single line) as control subtype 8, schema 1.
func EncodeVideoStats(body []byte) ([]byte, error) {
	if !validVideoStats(body) {
		return nil, ErrVideoStats
	}
	return append(videoStatsPrefix[:], body...), nil
}

// DecodeVideoStats returns the JSON object of a subtype 8 record; it aliases raw.
func DecodeVideoStats(raw []byte) ([]byte, error) {
	if len(raw) < 4 || [4]byte(raw[:4]) != videoStatsPrefix || !validVideoStats(raw[4:]) {
		return nil, ErrVideoStats
	}
	return raw[4:], nil
}

func validVideoStats(body []byte) bool {
	return len(body) > 0 && len(body) <= MaxControlSize-4 && body[0] == '{' &&
		!bytes.ContainsAny(body, "\r\n") && json.Valid(body)
}
