package main

import (
	"strings"
	"sync"
)

// clientSession is what one tablet told the host in its hello, for as long as
// that input connection lasts: its device name (the calibration key), its
// identity (platform, screen, remembered mode) and the video codecs and
// extensions it supports.
//
// The registry below keeps one per input connection; the most recent hello is
// the active one. A video session copies the active one when it starts
// (videoOptions.Client) and reads only that copy, so a reconnecting input
// channel cannot change a running stream.
type clientSession struct {
	Device   string
	Identity clientIdentity
	// Codecs the client decodes, lowercase ("h264", "hevc"), in its order.
	Codecs []string
	// Features are the video-channel extensions it supports (protocol.go).
	Features map[string]bool
}

func (c clientSession) hasFeature(name string) bool { return c.Features[name] }

// requestsKeyframes reports whether the tablet asks for a keyframe whenever
// it drops pictures (clients with frame acks also do that). Native helpers
// then send keyframes only on request instead of every two seconds: on a
// lossless link (USB, TCP) a periodic keyframe only adds a periodic burst,
// several frames in size, to the link and the latency.
func (c clientSession) requestsKeyframes() bool { return c.hasFeature(clientFeatureFrameAck) }

// newClientSession reads a hello. Every field is normalized here, once.
func newClientSession(msg packet) clientSession {
	platform := strings.ToLower(strings.TrimSpace(msg.ClientOS))
	if platform == "" && strings.TrimSpace(msg.Android) != "" {
		platform = "android"
	}
	clientType := strings.ToLower(strings.TrimSpace(msg.ClientType))
	if clientType == "" {
		clientType = "client"
		if platform == "android" {
			clientType = "tablet"
		}
	}
	version := strings.TrimSpace(msg.ClientVer)
	if version == "" {
		version = strings.TrimSpace(msg.App)
	}
	return clientSession{
		Device: msg.Device,
		Identity: clientIdentity{
			Platform:          platform,
			Type:              clientType,
			Version:           version,
			NativeWidth:       msg.ScreenWidth,
			NativeHeight:      msg.ScreenHeight,
			RefreshRates:      msg.ScreenRefreshRates,
			RecommendedWidth:  msg.RecommendedWidth,
			RecommendedHeight: msg.RecommendedHeight,
			RecommendedReason: msg.RecommendedReason,
			PrefWidth:         msg.PrefWidth,
			PrefHeight:        msg.PrefHeight,
			PrefFPS:           msg.PrefFPS,
			PrefsPending:      msg.HostPrefs,
		},
		Codecs:   normalizeClientCodecs(msg.VideoCodecs),
		Features: normalizeClientFeatures(msg.VideoFeatures),
	}
}

// normalized fills in placeholders and drops implausible values.
func (c clientSession) normalized() clientSession {
	c.Device = strings.TrimSpace(c.Device)
	if c.Device == "" {
		c.Device = unknownClientDevice
	}
	c.Identity = c.Identity.normalized()
	return c
}

func normalizeClientCodecs(codecs []string) []string {
	var out []string
	for _, c := range codecs {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "h265" {
			c = "hevc"
		}
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func normalizeClientFeatures(features []string) map[string]bool {
	set := make(map[string]bool, len(features))
	for _, f := range features {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			set[f] = true
		}
	}
	return set
}

var clientSessions = struct {
	mu   sync.RWMutex
	byID map[uint64]clientSession
	// active is the session that sent the latest hello; zero once no input
	// connection is left.
	active clientSession
}{byID: make(map[uint64]clientSession)}

// bindClientSession records the hello of input connection id and makes it
// the active session.
func bindClientSession(id uint64, c clientSession) {
	c = c.normalized()
	clientSessions.mu.Lock()
	defer clientSessions.mu.Unlock()
	clientSessions.byID[id] = c
	clientSessions.active = c
}

// unbindClientSession forgets input connection id when it closes.
func unbindClientSession(id uint64) {
	clientSessions.mu.Lock()
	defer clientSessions.mu.Unlock()
	delete(clientSessions.byID, id)
	if len(clientSessions.byID) == 0 {
		clientSessions.active = clientSession{}
	}
}

// updateClientSessions changes the identity of session id, or of every
// session when id is 0, keeping the active copy in step.
func updateClientSessions(id uint64, change func(*clientIdentity)) {
	clientSessions.mu.Lock()
	defer clientSessions.mu.Unlock()
	for sid, c := range clientSessions.byID {
		if id != 0 && sid != id {
			continue
		}
		change(&c.Identity)
		clientSessions.byID[sid] = c
		if id != 0 {
			clientSessions.active = c
		}
	}
	if id == 0 && len(clientSessions.byID) > 0 {
		change(&clientSessions.active.Identity)
	}
}

func clientSessionByID(id uint64) (clientSession, bool) {
	clientSessions.mu.RLock()
	defer clientSessions.mu.RUnlock()
	c, ok := clientSessions.byID[id]
	return c, ok
}

func activeClientSession() clientSession {
	clientSessions.mu.RLock()
	defer clientSessions.mu.RUnlock()
	return clientSessions.active
}

// activeClientIdentity is the active session's identity (zero when none).
func activeClientIdentity() clientIdentity { return activeClientSession().Identity }

// activeInputDevice is the active session's device name ("" when none).
func activeInputDevice() string { return activeClientSession().Device }
