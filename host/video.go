package main

// Video channel: streams the host desktop to the tablet as framed H.264.
//
// Session model: a single video session at a time. The capture/encode
// pipeline (ffmpeg, see ffmpeg.go) is started when a client connects and torn
// down when it disconnects, so no encoding cost is paid while idle.
//
// Data flow per session:
//
//	ffmpeg stdout (Annex-B byte stream)
//	  -> splitAnnexB (reassemble into complete NAL units; accessUnitTracker
//	     sends a frame's last unit as soon as the frame's last write is read)
//	  -> writeVideoPacket (type+length framing, see protocol.go)
//	  -> TCP -> adb reverse -> tablet MediaCodec

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// nalBufferMax bounds reassembly memory; a single NAL should never come
	// close to this at streaming bitrates.
	nalBufferMax = 8 * 1024 * 1024

	// encodeStartTimeout is how long an encoder gets to produce its first
	// byte before it is declared unusable and the next candidate is tried.
	encodeStartTimeout = 6 * time.Second

	// sessionSummaryInterval paces the periodic throughput/health log line.
	sessionSummaryInterval = 5 * time.Second
	// nalTailFlushInterval bounds how long a trailing Annex-B tail is held
	// without any new bytes before attempting a best-effort flush.
	// Keep this comfortably above frame cadence/jitter so we don't emit
	// partial NAL units on active streams (which corrupts decoder input).
	nalTailFlushInterval = 120 * time.Millisecond
	// pipelineReadSize is the pipeline stdout read size. Boundary proofs
	// (see preparePipeWriteBoundaries) cover only reads shorter than it, and
	// it bounds a chunk queued between the reader and the sender.
	pipelineReadSize = 64 * 1024
	// videoHandoffWaitTimeout is how long a new connection waits for an
	// existing session to tear down before this connection is rejected.
	// Teardown includes stopping the pipeline process and removing the
	// virtual display (which waits for the monitor to actually depart), so
	// this must be generous; a rejected client auto-reconnects anyway.
	videoHandoffWaitTimeout = 10 * time.Second
	videoHandoffPoll        = 50 * time.Millisecond
	videoReconnectDebounce  = 2 * time.Second
	// Only allow reconnect-triggered handoff when the active session has
	// been idle (no encoded output) for at least this long.
	videoReconnectHandoffMinSilence = 3 * time.Second
	transportVideoAuthTimeout       = 5 * time.Second
	transportVideoAuthMaxBytes      = 4 * 1024
)

// videoOptions carries the user-tunable video settings from main.
type videoOptions struct {
	FFmpeg  string
	Codec   string
	FPS     int
	Bitrate string
	// Windows-only: try NVENC zero-copy capture path before readback.
	ZeroCopyCapture bool
	// Prefer temporal layering profiles that allow runtime enhancement-drop
	// adaptation without encoder restarts when supported by the active spec.
	TemporalLayering bool
	// Requested virtual display size (extend mode).
	Width  int
	Height int
	// IdleGate pauses X11 capture while the screen is static (setting
	// idleCaptureGate; Linux x11grab only).
	IdleGate bool
	// X11HighRate allows x11grab capture above 60 fps (setting x11HighRateCapture).
	X11HighRate bool
	// NativeMedia is the nativeMediaHelper setting (auto, on, off).
	NativeMedia string
}

// runVideoListener accepts video sessions until ctx is cancelled. If the
// port is unavailable the video feature is disabled but input keeps working.
// Each session takes a fresh settings snapshot from the store, so web UI
// changes apply to the next connection (and the UI restarts the active
// session on save via restartActiveVideoSession).
func runVideoListener(ctx context.Context, store *settingsStore) {
	addr := fmt.Sprintf("127.0.0.1:%d", videoPort)
	runVideoListenerAt(ctx, store, addr, "usb", nil)
}

func runVideoListenerAt(ctx context.Context, store *settingsStore, addr string, transport string, listenerFactory func(string) (net.Listener, error)) {
	var (
		ln  net.Listener
		err error
	)
	if listenerFactory != nil {
		ln, err = listenerFactory(addr)
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		log.Printf("Video listener disabled: %v", err)
		return
	}
	defer ln.Close()
	log.Printf("Video listener on %s", addr)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			log.Printf("Video accept error: %v", err)
			continue
		}
		go handleAcceptedVideoConnection(ctx, conn, store, transport)
	}
}

func handleAcceptedVideoConnection(ctx context.Context, conn net.Conn, store *settingsStore, transport string) {
	if token, protected := transportAuthToken(store.get(), transport); protected {
		if !authorizeTransportVideo(conn, token) {
			log.Printf("%s video authentication failed for %s", transportDisplayName(transport), conn.RemoteAddr())
			_ = conn.Close()
			return
		}
	}
	if !acquireVideoSession(transport) {
		_ = conn.Close()
		return
	}
	operationalCounters.videoConnections.Add(1)
	log.Printf("Video client connected (%s)", conn.RemoteAddr())
	serveVideoSession(ctx, conn, store, transport)
}

// acquireVideoSession reserves the active session's transport, then claims
// its video channel. A same-transport reconnect requests a clean handoff;
// another transport is rejected without disturbing the current session.
func acquireVideoSession(transport string) bool {
	if !hostSessionOwnership.reserveVideo(transport) {
		owner, handoffPending := hostSessionOwnership.videoReservationInfo()
		if owner == transport && handoffPending {
			log.Printf("Coalescing %s video reconnect: a handoff is already pending", transport)
			return false
		}
		log.Printf(
			"Rejecting %s video connection: active session owned by %s",
			transport, owner,
		)
		return false
	}
	activated := false
	defer func() {
		if !activated {
			hostSessionOwnership.cancelVideoReservation(transport)
		}
	}()
	active, handoff := hostSessionOwnership.activateReservedVideo(transport)
	if active {
		activated = true
		return true
	}
	if !requestVideoReconnectHandoff() {
		return false
	}
	deadline := time.NewTimer(videoHandoffWaitTimeout)
	poll := time.NewTicker(videoHandoffPoll)
	defer deadline.Stop()
	defer poll.Stop()
	for {
		select {
		case <-handoff:
			active, handoff = hostSessionOwnership.activateReservedVideo(transport)
		case <-poll.C:
			active, handoff = hostSessionOwnership.activateReservedVideo(transport)
		case <-deadline.C:
			log.Printf("Rejecting %s video connection: session still active after handoff timeout", transport)
			return false
		}
		if active {
			activated = true
			return true
		}
	}
}

// serveVideoSession runs one accepted connection to completion. The caller
// must own the coordinator's video channel; it is released here only after
// streamVideo has fully unwound (including virtual display teardown).
func serveVideoSession(ctx context.Context, conn net.Conn, store *settingsStore, transport string) {
	defer hostSessionOwnership.releaseVideo(transport)
	defer conn.Close()
	defer clearLatestClientVideoStats()
	defer logPanic("video session")
	if tcpConn := underlyingTCPConn(conn); tcpConn != nil {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetWriteBuffer(1024 * 1024)
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	setActiveVideoSession(cancel, conn)
	defer func() {
		setActiveVideoSession(nil, nil)
		publishVideoStatus(false, "", "")
		cancel()
	}()
	// The replacement now owns the channel and has a registered cancellation
	// path. End the handoff gate before startup; reconnects during startup are
	// handled by the debounce state instead of being rejected for the entire
	// lifetime of this replacement session.
	hostSessionOwnership.finishVideoHandoff(transport)
	// The tablet's hello (native size, remembered mode) can land just after
	// its video socket connects; the display must be created from it.
	waitForClientIdentity(sessionCtx, clientHelloWait)
	opts := store.get().videoOptions()
	applyClientDisplayDefaults(&opts, activeClientIdentity())
	if ident := activeClientIdentity(); strings.EqualFold(ident.Platform, "android") && opts.TemporalLayering {
		// Temporal enhancement-layer dropping can make text look unstable on
		// Android tablet decode paths; prefer cleaner glyph edges there.
		opts.TemporalLayering = false
		log.Printf("Temporal layering disabled for Android client session to reduce text artifacting")
	}
	if cap := transportMaxBitrate(store.get(), transport); cap != "" {
		opts.Bitrate = cappedBitrate(opts.Bitrate, cap)
	}
	if err := streamVideo(sessionCtx, conn, opts); err != nil && sessionCtx.Err() == nil {
		log.Printf("Video session ended: %v", err)
	} else {
		log.Printf("Video session ended")
	}
}

// Active-session restart hook (web UI settings apply). The listener
// registers each session's cancel func; restartActiveVideoSession cancels
// it, which tears the session down like a disconnect — the tablet client
// auto-reconnects within ~1 s and picks up the new settings.
type videoRestartDisposition int

const (
	videoRestartNoSession videoRestartDisposition = iota
	videoRestartDeferred
	videoRestartAlreadyRequested
	videoRestartRequested
)

type activeVideoSessionState struct {
	mu               sync.Mutex
	cancel           context.CancelFunc
	conn             net.Conn
	starting         bool
	readyAt          time.Time
	restartRequested bool
	lastOutputAtNS   atomic.Int64
}

var activeVideoSession activeVideoSessionState

func (s *activeVideoSessionState) set(cancel context.CancelFunc, conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = cancel
	s.conn = conn
	s.starting = cancel != nil || conn != nil
	s.readyAt = time.Time{}
	s.restartRequested = false
	s.lastOutputAtNS.Store(0)
}

func (s *activeVideoSessionState) markReady(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil && s.conn == nil {
		return
	}
	s.starting = false
	s.readyAt = now
	s.lastOutputAtNS.Store(now.UnixNano())
}

func (s *activeVideoSessionState) markOutput(now time.Time) {
	if now.IsZero() {
		return
	}
	s.lastOutputAtNS.Store(now.UnixNano())
}

func (s *activeVideoSessionState) requestRestart(force bool, now time.Time) (videoRestartDisposition, context.CancelFunc, net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil && s.conn == nil {
		return videoRestartNoSession, nil, nil
	}
	if s.restartRequested {
		return videoRestartAlreadyRequested, nil, nil
	}
	if !force && (s.starting || s.readyAt.IsZero() || now.Sub(s.readyAt) < videoReconnectDebounce) {
		return videoRestartDeferred, nil, nil
	}
	if !force {
		lastOutputNS := s.lastOutputAtNS.Load()
		if lastOutputNS != 0 && now.Sub(time.Unix(0, lastOutputNS)) < videoReconnectHandoffMinSilence {
			return videoRestartDeferred, nil, nil
		}
	}
	s.restartRequested = true
	return videoRestartRequested, s.cancel, s.conn
}

func setActiveVideoSession(cancel context.CancelFunc, conn net.Conn) {
	activeVideoSession.set(cancel, conn)
}

func markActiveVideoSessionReady() {
	activeVideoSession.markReady(time.Now())
}

func markActiveVideoSessionOutput() {
	activeVideoSession.markOutput(time.Now())
}

func stopActiveVideoSession(cancel context.CancelFunc, conn net.Conn) {
	if cancel != nil {
		cancel()
	}
	// Close the active transport to force a clean teardown/reconnect even if
	// a capture backend does not observe context cancellation promptly.
	if conn != nil {
		_ = conn.Close()
	}
}

func requestVideoReconnectHandoff() bool {
	disposition, cancel, conn := activeVideoSession.requestRestart(false, time.Now())
	switch disposition {
	case videoRestartDeferred, videoRestartNoSession:
		throttledLog("video-handoff-deferred", "Deferring video reconnect handoff: active session is not handoff-eligible yet")
		return false
	case videoRestartAlreadyRequested:
		return true
	case videoRestartRequested:
		log.Printf("Video reconnect arrived while a stable session is active; requesting handoff")
		operationalCounters.videoRestarts.Add(1)
		stopActiveVideoSession(cancel, conn)
		return true
	default:
		return false
	}
}

// restartActiveVideoSession drops the active video session, if any, so the
// reconnecting client gets a session built from the current settings.
// Returns true when a session was active.
func restartActiveVideoSession() bool {
	disposition, cancel, conn := activeVideoSession.requestRestart(true, time.Now())
	if disposition == videoRestartNoSession {
		return false
	}
	if disposition == videoRestartAlreadyRequested {
		return true
	}
	log.Printf("Restarting video session to apply new settings")
	operationalCounters.videoRestarts.Add(1)
	// Note: coordinator ownership is NOT released here. Only the session
	// goroutine releases it (deferred), after streamVideo has returned and
	// the virtual display has been closed. Releasing it early would let the
	// replacement overlap the old pipeline and virtual display teardown.
	stopActiveVideoSession(cancel, conn)
	return true
}

// Published session status, read by the web UI (webui.go).
type videoStatusInfo struct {
	Streaming   bool   `json:"streaming"`
	Encoder     string `json:"encoder"`
	Bitrate     string `json:"bitrate"`
	DisplayMode string `json:"displayMode,omitempty"` // extend | mirror | unknown
	CaptureKind string `json:"captureKind,omitempty"` // ddagrab | pipewire | x11grab
	Display     string `json:"display,omitempty"`     // human-readable display description
	// ResolutionControl is "host" when the configured resolution sets the
	// display's, which the desktop's display settings cannot change.
	ResolutionControl string `json:"resolutionControl,omitempty"`
}

var (
	videoStatusMu sync.Mutex
	videoStatus   videoStatusInfo
)

func publishVideoStatus(streaming bool, encoder, bitrate string) {
	videoStatusMu.Lock()
	if !streaming {
		videoStatus = videoStatusInfo{Streaming: false}
		videoStatusMu.Unlock()
		return
	}
	status := videoStatus
	status.Streaming = true
	status.Encoder = encoder
	status.Bitrate = bitrate
	videoStatus = status
	videoStatusMu.Unlock()
}

func publishVideoDisplay(mode, captureKind, description, resolutionControl string) {
	videoStatusMu.Lock()
	status := videoStatus
	status.DisplayMode = mode
	status.CaptureKind = captureKind
	status.Display = description
	status.ResolutionControl = resolutionControl
	videoStatus = status
	videoStatusMu.Unlock()
}

func displayModeFromTarget(target captureTarget) (mode, captureKind string) {
	captureKind = string(target.Kind)
	switch target.Kind {
	case captureDDAGrab:
		if target.Mirrored {
			return "mirror", captureKind
		}
		return "extend", captureKind
	case capturePipeWire, captureScreencopy:
		// A Wayland virtual output, unless it mirrors another display.
		if target.Mirrored {
			return "mirror", captureKind
		}
		return "extend", captureKind
	case captureX11Grab:
		// A rectangle is one output of the X screen: the X11 extended
		// display, unless it mirrors another display. Without one the whole
		// screen is mirrored.
		if !target.Rect.empty() && !target.Mirrored {
			return "extend", captureKind
		}
		return "mirror", captureKind
	default:
		return "unknown", captureKind
	}
}

// resolutionControlOf is the video status's ResolutionControl for target.
func resolutionControlOf(target captureTarget) string {
	if target.HostResolution {
		return "host"
	}
	return ""
}

func currentVideoStatus() videoStatusInfo {
	videoStatusMu.Lock()
	defer videoStatusMu.Unlock()
	return videoStatus
}

const (
	// Capture recovery stays inside the current client/display session so a
	// transient Windows modeset does not trigger a socket/VDD reconnect loop.
	captureRecoveryAttempts     = 8
	captureRecoveryInitialDelay = 250 * time.Millisecond
	captureRecoveryMaxDelay     = 2 * time.Second
	captureRecoveryStableWindow = 5 * time.Second
	// Pipelines that emit a few bytes and then die almost immediately are
	// treated as startup failures so the next candidate can be tried in the
	// same session instead of triggering a reconnect loop.
	pipelineStartupFailureWindow = 2 * time.Second
	displayRecoveryMaxAttempts   = 2
)

type captureRecoveryState struct {
	failures int
}

func (s *captureRecoveryState) reset() {
	s.failures = 0
}

func (s *captureRecoveryState) nextDelay() (time.Duration, int, bool) {
	if s.failures >= captureRecoveryAttempts {
		return 0, 0, false
	}
	delay := captureRecoveryInitialDelay
	for i := 0; i < s.failures && delay < captureRecoveryMaxDelay; i++ {
		delay *= 2
		if delay > captureRecoveryMaxDelay {
			delay = captureRecoveryMaxDelay
		}
	}
	s.failures++
	return delay, captureRecoveryAttempts - s.failures, true
}

func waitForCaptureRecovery(ctx context.Context, recovery *captureRecoveryState, cause error) error {
	delay, remaining, ok := recovery.nextDelay()
	if !ok {
		return fmt.Errorf("capture recovery exhausted: %w", cause)
	}
	operationalCounters.captureRestarts.Add(1)
	log.Printf("Capture unavailable (%v); retrying in %s (%d attempts left)", cause, delay, remaining)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type sessionRecoverableDisplay interface {
	Recover() error
}

type sessionMirrorFallbackDisplay interface {
	FallbackToMirror() (virtualDisplay, error)
}

func attemptDisplayRecovery(display virtualDisplay, attempts *int, cause error) bool {
	if *attempts >= displayRecoveryMaxAttempts {
		return false
	}
	rd, ok := display.(sessionRecoverableDisplay)
	if !ok {
		return false
	}
	log.Printf("Attempting display self-recovery after repeated capture failures (%v)", cause)
	if err := rd.Recover(); err != nil {
		log.Printf("Display self-recovery failed: %v", err)
		return false
	}
	*attempts++
	return true
}

func recoverDisplayOrFallback(display *virtualDisplay, attempts *int, cause error) bool {
	if attemptDisplayRecovery(*display, attempts, cause) {
		return true
	}
	fallbackDisplay, ok := (*display).(sessionMirrorFallbackDisplay)
	if !ok {
		return false
	}
	mirror, err := fallbackDisplay.FallbackToMirror()
	if err != nil {
		log.Printf("Display mirror fallback failed: %v", err)
		return false
	}
	if mirror == nil {
		log.Printf("Display mirror fallback returned no display")
		return false
	}
	displayMode, captureKind := displayModeFromTarget(mirror.Target())
	log.Printf("Falling back to %s after repeated capture loss", mirror.Description())
	*display = mirror
	setActiveTarget(nil)
	publishVideoDisplay(displayMode, captureKind, mirror.Description(), resolutionControlOf(mirror.Target()))
	setOperationalWarning("display-mode", "Virtual display capture failed repeatedly; streaming a mirror instead")
	return true
}

// userModeTracker detects mid-session resolution changes on the session
// display, which can only come from the user (the host sets the mode once,
// at display creation) and so become the tablet's stored preference.
//
// With ignoreDisplaySwaps, a display replaced by self-recovery or mirror
// fallback starts a new baseline instead of reading as a user change, and a
// mirrored target never reports one: its mode is the host monitor's, not
// the tablet's.
type userModeTracker struct {
	ignoreDisplaySwaps bool
	width, height      int
}

func (t *userModeTracker) observe(target captureTarget) bool {
	if target.Width <= 0 || target.Height <= 0 {
		return false
	}
	changed := t.width != 0 && (target.Width != t.width || target.Height != t.height)
	t.width, t.height = target.Width, target.Height
	if t.ignoreDisplaySwaps && target.Mirrored {
		return false
	}
	return changed
}

func (t *userModeTracker) displayReplaced() {
	if t.ignoreDisplaySwaps {
		t.width, t.height = 0, 0
	}
}

func isTransientCaptureError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"failed duplicating output",
		"failed to enumerate dxgi output",
		"acquirenextframe failed",
		"dxgi_error_access_lost",
		"887a0026",
		"887a0005",
		"887a0007",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// streamVideo creates the virtual display for this session, then runs the
// adaptive streaming loop: pipeline candidates are tried in preference order
// until one produces output, and the pipeline is restarted at a different
// bitrate-ladder rung when the link congests or recovers (see
// ratecontrol.go). Once a spec has streamed successfully it is tried first
// on subsequent restarts. Errors after the client/context went away are
// final.
//
// Every pipeline (re)start re-resolves the display (Refresh): monitor
// rearrangement moves the display's desktop rect (input mapping) and can
// renumber DXGI outputs (capture target). Mid-session capture loss is
// retried with backoff instead of ending the session, so a monitor move
// only blanks the stream for a moment.
func streamVideo(ctx context.Context, conn net.Conn, opts videoOptions) error {
	publishHostVideoCodecs(detectedHostVideoCodecs(opts.FFmpeg))
	opts.Codec = negotiateVideoCodec()

	// Display enumeration can fail transiently while the display topology
	// is settling (the previous session's virtual display tearing down, a
	// monitor hot-plug); retry briefly before giving up on the session.
	var display virtualDisplay
	var err error
	for attempt := 0; ; attempt++ {
		display, err = newVirtualDisplay(opts)
		if err == nil {
			break
		}
		if attempt >= 4 || ctx.Err() != nil {
			return fmt.Errorf("virtual display: %w", err)
		}
		log.Printf("Virtual display setup failed (attempt %d, retrying): %v", attempt+1, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	defer func() { display.Close() }()
	log.Printf("Display for session: %s", display.Description())
	mode, captureKind := displayModeFromTarget(display.Target())
	publishVideoDisplay(mode, captureKind, display.Description(), resolutionControlOf(display.Target()))
	if mode == "mirror" {
		setOperationalWarning("display-mode", fmt.Sprintf("Virtual display is unavailable; streaming mirror mode via %s", captureKind))
	} else {
		setOperationalWarning("display-mode", "")
	}
	defer func() {
		publishVideoDisplay("", "", "", "")
		setOperationalWarning("display-mode", "")
	}()

	defer setActiveTarget(nil)

	// Upstream stats reader: congestion feedback + fast dead-peer signal.
	monitor := &sessionMonitor{}
	go monitor.runStatsReader(conn)

	rc := newRateController(opts.Bitrate)
	var temporal *temporalController
	if opts.TemporalLayering {
		temporal = newTemporalController()
	}
	writer := &monitoredWriter{conn: conn, mon: monitor}
	goodSpecs := map[string]bool{}
	var recovery captureRecoveryState
	displayRecoveryAttempts := 0
	topKbps := 0
	modeTracker := userModeTracker{ignoreDisplaySwaps: modeBaselineIgnoresDisplaySwaps}
	lastEffectiveFPS := 0

	for {
		// Re-resolve the display and publish its rect so input injection
		// follows the monitor wherever the user puts it.
		target, err := display.Refresh()
		if err != nil {
			if recoveryErr := waitForCaptureRecovery(ctx, &recovery, err); recoveryErr != nil {
				if recoverDisplayOrFallback(&display, &displayRecoveryAttempts, recoveryErr) {
					modeTracker.displayReplaced()
					recovery.reset()
					continue
				}
				return fmt.Errorf("display refresh: %w", recoveryErr)
			}
			continue
		}
		setActiveTarget(&target)

		// A mid-session resolution change can only come from the user (the
		// host sets the mode once, at display creation): push it to the
		// tablet so it becomes the stored preference for future sessions.
		if modeTracker.observe(target) {
			log.Printf("Display mode changed by user to %dx%d; updating tablet preference", target.Width, target.Height)
			publishModeChange(target.Width, target.Height, 0)
		}

		// Scale the bitrate budget with the display's pixel count so
		// quality per pixel stays roughly constant across resolutions
		// (-bitrate is the 1080p reference). Rebuilt on mid-session mode
		// changes; adaptation state resets, which a modeset warrants.
		if kb := limitSessionBitrateKbps(scaledBitrateKbps(opts.Bitrate, target.Width, target.Height)); kb != topKbps {
			if topKbps != 0 {
				log.Printf("Display mode is %dx%d: bitrate ladder rescaled to top %dk",
					target.Width, target.Height, kb)
			}
			topKbps = kb
			rc = newRateControllerKbps(kb)
		}

		effOpts := opts
		effOpts.FPS = effectiveCaptureFPS(target, opts.FPS, opts.FFmpeg)
		if target.Kind == captureX11Grab && !opts.X11HighRate && effOpts.FPS > 60 {
			effOpts.FPS = 60
		}
		if lastEffectiveFPS != 0 && effOpts.FPS != lastEffectiveFPS {
			log.Printf("Capture framerate now following active display refresh: %d fps", effOpts.FPS)
			publishModeChange(target.Width, target.Height, effOpts.FPS)
		}
		lastEffectiveFPS = effOpts.FPS
		effOpts.Bitrate = rc.current()
		gate := newSessionCongestionGate(target)
		blackout := sessionDisplayBlackout()
		specs, err := buildSessionPipelines(target, effOpts, blackout)
		if err != nil {
			return err
		}
		promoteBestGoodSpec(specs, goodSpecs)

		restart, lastErr := false, error(nil)
		var recoveryCause error
		for _, spec := range specs {
			pipelineStarted := time.Now()
			produced, err := runEncodePipeline(ctx, writer, effOpts, spec, monitor, rc, temporal, blackout, gate)
			if errors.Is(err, errRateChanged) || errors.Is(err, errAccessUnitResync) {
				if produced {
					goodSpecs[spec.Name] = true
				}
				recovery.reset()
				restart = true
				break
			}
			if errors.Is(err, errPrivacyModeChanged) {
				recovery.reset()
				restart = true
				break
			}
			if err == nil || isFinalSessionError(ctx, err, monitor) {
				return err
			}
			if isStartupPipelineFailure(produced, time.Since(pipelineStarted)) {
				log.Printf("Pipeline %s exited too quickly after startup (%v); trying next candidate", spec.Name, err)
				lastErr = err
				continue
			}
			if produced {
				goodSpecs[spec.Name] = true
				encoderName := spec.BaseEncoder
				if encoderName == "" {
					encoderName = spec.Name
				}
				markEncoderHealthy(effOpts.FFmpeg, encoderName)
			}
			if !produced {
				encoderName := spec.BaseEncoder
				if encoderName == "" {
					encoderName = spec.Name
				}
				zeroCopySpec := strings.Contains(spec.Name, "_zerocopy")
				if zeroCopySpec && isZeroCopyInitError(err) {
					markZeroCopyBlocked(effOpts.FFmpeg, encoderName)
					log.Printf("Disabling zero-copy probe for %s on this host (startup failed)", encoderName)
				}
				// A zero-copy failure says nothing about the readback path of
				// the same encoder, which is tried next.
				if !zeroCopySpec && isPermanentEncoderInitError(err) {
					markEncoderUnavailable(effOpts.FFmpeg, encoderName)
					log.Printf("Marking encoder %s unavailable on this host (startup failed)", encoderName)
				}
			}
			lastErr = err
			if isTransientCaptureError(err) {
				if produced && time.Since(pipelineStarted) >= captureRecoveryStableWindow {
					recovery.reset()
				}
				log.Printf("Pipeline %s lost DXGI capture (%v); re-resolving display", spec.Name, err)
				recoveryCause = err
				restart = true
				break
			}
			// A pipeline that had been streaming and then died usually
			// means the display changed (mode or position): re-resolve the
			// display and retry from the best encoder instead of falling
			// through to lesser candidates with a stale capture target.
			if produced {
				log.Printf("Pipeline %s died mid-stream (%v); re-resolving display", spec.Name, err)
				if time.Since(pipelineStarted) >= captureRecoveryStableWindow {
					recovery.reset()
				}
				recoveryCause = err
				restart = true
				break
			}
			log.Printf("Pipeline %s failed (%v), trying next", spec.Name, err)
		}
		if restart {
			if recoveryCause != nil {
				if recoveryErr := waitForCaptureRecovery(ctx, &recovery, recoveryCause); recoveryErr != nil {
					if recoverDisplayOrFallback(&display, &displayRecoveryAttempts, recoveryErr) {
						modeTracker.displayReplaced()
						recovery.reset()
						continue
					}
					return recoveryErr
				}
			} else {
				operationalCounters.captureRestarts.Add(1)
				log.Printf("Restarting pipeline at %s", rc.current())
			}
			continue
		}
		// All candidates failed. If this session already streamed, the
		// likely cause is transient capture loss (display topology change):
		// wait and retry rather than dropping the session.
		if len(goodSpecs) > 0 {
			if recoveryErr := waitForCaptureRecovery(ctx, &recovery, lastErr); recoveryErr != nil {
				if recoverDisplayOrFallback(&display, &displayRecoveryAttempts, recoveryErr) {
					modeTracker.displayReplaced()
					recovery.reset()
					continue
				}
				return recoveryErr
			}
			continue
		}
		return fmt.Errorf("all capture pipelines failed, last error: %w", lastErr)
	}
}

// promoteBestGoodSpec moves the most-preferred spec that has already
// streamed successfully this session to the front, so restarts skip
// re-walking dead encoders without permanently pinning a software fallback:
// a hardware encoder that failed transiently (e.g. capture loss during a
// display-settings change) is retried first on the next restart.
func promoteBestGoodSpec(specs []pipelineSpec, good map[string]bool) {
	for i, s := range specs {
		if good[s.Name] {
			copy(specs[1:i+1], specs[:i])
			specs[0] = s
			return
		}
	}
}

// errRateChanged signals that the pipeline should restart at the rate
// controller's new bitrate (not a failure).
var errRateChanged = errors.New("bitrate changed")
var errPrivacyModeChanged = errors.New("video privacy mode changed")

// isFinalSessionError reports whether err ends the session (client gone or
// shutdown) rather than indicating an unusable encoder worth falling back on.
func isFinalSessionError(ctx context.Context, err error, monitor *sessionMonitor) bool {
	if ctx.Err() != nil || monitor.gone() {
		return true
	}
	return strings.Contains(err.Error(), "client write")
}

func isStartupPipelineFailure(produced bool, elapsed time.Duration) bool {
	return produced && elapsed < pipelineStartupFailureWindow
}

func buildSessionPipelines(target captureTarget, opts videoOptions, blackout bool) ([]pipelineSpec, error) {
	if !blackout {
		return buildCapturePipelines(target, opts)
	}
	return buildBlackoutPipelines(opts, target.Width, target.Height)
}

func buildBlackoutPipelines(opts videoOptions, width int, height int) ([]pipelineSpec, error) {
	if width <= 0 || height <= 0 {
		width, height = 1920, 1080
	}
	encoders := detectEncoders(opts.FFmpeg, opts.Codec)
	if len(encoders) == 0 {
		return nil, fmt.Errorf("no usable %s encoder found in ffmpeg", opts.Codec)
	}
	specs := make([]pipelineSpec, 0, len(encoders))
	source := fmt.Sprintf("color=c=black:s=%dx%d:r=%d", width, height, opts.FPS)
	for _, enc := range encoders {
		args := []string{
			"-f", "lavfi",
			"-i", source,
			"-pix_fmt", "yuv420p",
		}
		spec := buildFFmpegSpec(opts, args, enc, encoderArgs(enc, opts.FPS))
		spec.Name = enc + "_blackout"
		spec.BaseEncoder = enc
		specs = append(specs, spec)
	}
	return specs, nil
}

// pipelineTelemetry observes encoded-stream throughput for one pipeline run.
// Platform builds may install an implementation via startPipelineTelemetry;
// when none is installed the no-op below keeps the read path unchanged.
type pipelineTelemetry interface {
	addBytes(n int)
	addUnits(n int)
	addDropped(n int)
	finish()
}

// startPipelineTelemetry is set by platform-specific builds (Linux only today)
// to observe a pipeline run. Nil means telemetry is not available.
var startPipelineTelemetry func(specName string) pipelineTelemetry

type nopPipelineTelemetry struct{}

func (nopPipelineTelemetry) addBytes(int)   {}
func (nopPipelineTelemetry) addUnits(int)   {}
func (nopPipelineTelemetry) addDropped(int) {}
func (nopPipelineTelemetry) finish()        {}

// newPipelineTelemetry returns the platform telemetry sink, or a no-op sink.
func newPipelineTelemetry(specName string) pipelineTelemetry {
	if startPipelineTelemetry == nil {
		return nopPipelineTelemetry{}
	}
	if telemetry := startPipelineTelemetry(specName); telemetry != nil {
		return telemetry
	}
	return nopPipelineTelemetry{}
}

// runEncodePipeline runs one capture/encode process (see ffmpeg.go) and pumps
// its Annex-B output to the client as framed NAL units. Returns produced =
// true once any output was streamed. A pipeline that dies or stalls before
// producing output is reported as an error so the caller can fall back to
// the next candidate; errRateChanged means the rate controller requested a
// restart at a new bitrate.
func runEncodePipeline(ctx context.Context, w io.Writer, opts videoOptions, spec pipelineSpec, monitor *sessionMonitor, rc *rateController, temporal *temporalController, expectBlackout bool, gate *congestionGate) (bool, error) {
	if spec.Framed {
		return runFramedPipeline(ctx, w, opts, spec, monitor, rc, temporal, expectBlackout, gate)
	}
	pipeline, err := startCapturePipeline(ctx, spec)
	if err != nil {
		return false, err
	}
	defer pipeline.stop()
	if spec.IdleGate != nil && pipeline.cmd != nil {
		// Deferred after pipeline.stop so it runs first and resumes a paused
		// process before it is stopped.
		defer startIdleGate(ctx, pipeline.cmd.Process, spec.IdleGate)()
	}

	// Watcher: the pump below spends its life blocked in stdout.Read, which
	// cannot notice a dead client or a stalled-but-alive encoder on its own
	// (a wedged session rejects all reconnect attempts — "session already
	// active"). Stopping the pipeline forces the Read to return.
	var produced atomic.Bool
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		startDeadline := time.Now().Add(encodeStartTimeout)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				pipeline.stop()
				return
			case <-ticker.C:
				if monitor.gone() {
					pipeline.stop()
					return
				}
				if !produced.Load() && time.Now().After(startDeadline) {
					pipeline.stop()
					return
				}
			}
		}
	}()

	encoderName := spec.BaseEncoder
	if encoderName == "" {
		encoderName = spec.Name
	}

	startDeadline := time.Now().Add(encodeStartTimeout)
	nextSummary := time.Now().Add(sessionSummaryInterval)
	telemetry := newPipelineTelemetry(spec.Name)
	defer telemetry.finish()
	var sentNALs, sentBytes int64
	var lastChunkAt time.Time
	configSent := false
	buf := make([]byte, 0, 256*1024)
	sendConfig := func() error {
		if configSent {
			return nil
		}
		cfg, _ := json.Marshal(videoConfigMsg{
			Codec:      opts.Codec,
			HostCodecs: hostVideoCodecs,
			Encoder:    encoderName,
			FPS:        opts.FPS,
			Bitrate:    opts.Bitrate,
			Pacing:     "latency",
		})
		if err := writeVideoPacket(w, videoPacketConfig, cfg); err != nil {
			return fmt.Errorf("client write: %w", err)
		}
		publishVideoStatus(true, spec.Name, opts.Bitrate)
		configSent = true
		return nil
	}
	var taggedSPS spsVUICache
	emitUnit := func(unit []byte) error {
		if err := sendConfig(); err != nil {
			return err
		}
		if sentNALs < 5 {
			log.Printf("Video NAL[%d] %s: len=%d type=%d", sentNALs, spec.Name, len(unit), h264NALType(unit))
		}
		if spec.SignalBT709Limited && h264NALType(unit) == 7 {
			unit = taggedSPS.rewrite(unit)
		}
		if temporal.droppingNonRef() && spec.SupportsTemporalDrop && isDroppableNonRefNAL(unit) {
			telemetry.addDropped(1)
			return nil
		}
		if err := writeVideoPacket(w, videoPacketNAL, unit); err != nil {
			return fmt.Errorf("client write: %w", err)
		}
		sentNALs++
		sentBytes += int64(len(unit))
		return nil
	}
	type readChunk struct {
		data []byte
		err  error
		// writeBoundary: the read provably ended where a write ended.
		writeBoundary bool
	}
	tracker := accessUnitTrackerFor(spec, pipeline.fullPipeBound)
	if tracker != nil {
		log.Printf("Pipeline %s: early frame emission on (pipe bound %d bytes)", spec.Name, pipeline.fullPipeBound)
	}
	chunks := make(chan readChunk, 16)
	go func() {
		read := make([]byte, pipelineReadSize)
		proofs := boundaryReads{fullPipe: pipeline.fullPipeBound}
		for {
			n, rerr := pipeline.stdout.Read(read)
			if n > 0 {
				out := make([]byte, n)
				copy(out, read[:n])
				chunks <- readChunk{data: out, writeBoundary: proofs.next(n, len(read))}
			}
			if rerr != nil {
				chunks <- readChunk{err: rerr}
				close(chunks)
				return
			}
		}
	}()
	flushTicker := time.NewTicker(nalTailFlushInterval)
	defer flushTicker.Stop()
	updateCapturePriority := func() {
		if stats := monitor.stats(); stats != nil && stats.AppForeground != nil {
			pipeline.setClientForeground(*stats.AppForeground)
		}
	}
	for {
		if !produced.Load() && time.Now().After(startDeadline) {
			return false, fmt.Errorf("encoder produced no output: %s", pipeline.errorTail())
		}
		select {
		case <-ctx.Done():
			return produced.Load(), ctx.Err()
		case <-flushTicker.C:
			if sessionDisplayBlackout() != expectBlackout {
				pipeline.stop()
				return produced.Load(), errPrivacyModeChanged
			}
			updateCapturePriority()
			if len(buf) > 0 && !lastChunkAt.IsZero() && time.Since(lastChunkAt) >= nalTailFlushInterval {
				tail, ok := flushAnnexBTail(buf)
				if ok {
					telemetry.addUnits(1)
					if err := emitUnit(tail); err != nil {
						return produced.Load(), err
					}
					buf = buf[:0]
					lastChunkAt = time.Time{}
					if tracker != nil {
						tracker.forget()
					}
				}
			}
			if now := time.Now(); now.After(nextSummary) {
				nextSummary = now.Add(sessionSummaryInterval)
				logSessionSummary(spec.Name, opts.Bitrate, sentNALs, sentBytes, monitor.stats(), monitor.takeStaleFrames())
			}
			continue
		case chunk, ok := <-chunks:
			if sessionDisplayBlackout() != expectBlackout {
				pipeline.stop()
				return produced.Load(), errPrivacyModeChanged
			}
			if !ok {
				return produced.Load(), fmt.Errorf("pipeline output closed unexpectedly (%s)", pipeline.errorTail())
			}
			if len(chunk.data) > 0 {
				markActiveVideoSessionOutput()
				if produced.CompareAndSwap(false, true) {
					markActiveVideoSessionReady()
				}
				lastChunkAt = time.Now()
				telemetry.addBytes(len(chunk.data))
				buf = append(buf, chunk.data...)
				var units [][]byte
				var rest []byte
				if tracker != nil {
					var splitErr error
					units, rest, splitErr = tracker.split(buf, len(chunk.data), chunk.writeBoundary)
					if splitErr != nil {
						auEarlyFlushDisabled.Store(true)
						log.Printf("Pipeline %s output broke the access-unit write pattern; early frame emission disabled, restarting pipeline", spec.Name)
						return produced.Load(), splitErr
					}
				} else {
					units, rest = splitAnnexB(buf)
				}
				telemetry.addUnits(len(units))
				for _, unit := range units {
					if err := emitUnit(unit); err != nil {
						return produced.Load(), err
					}
				}
				// Preserve the trailing partial unit for the next read.
				buf = append(buf[:0], rest...)
				if len(buf) == 0 {
					lastChunkAt = time.Time{}
				}
				if len(buf) > nalBufferMax {
					return produced.Load(), fmt.Errorf("NAL unit exceeded %d bytes", nalBufferMax)
				}

				// Adaptive bitrate: react to congestion / sustained recovery.
				// How eagerly congestion becomes a restart is the capture
				// backend's policy, carried by gate (see congestion.go).
				switch adaptToLinkFeedback(monitor, rc, temporal, gate, spec) {
				case congestionActionRestart:
					return produced.Load(), errRateChanged
				case congestionActionAdapted:
					continue
				}
				if now := time.Now(); now.After(nextSummary) {
					nextSummary = now.Add(sessionSummaryInterval)
					logSessionSummary(spec.Name, opts.Bitrate, sentNALs, sentBytes, monitor.stats(), monitor.takeStaleFrames())
				}
			}
			if chunk.err != nil {
				tail, ok := flushAnnexBTail(buf)
				if ok {
					telemetry.addUnits(1)
					if err := emitUnit(tail); err != nil {
						return produced.Load(), err
					}
					buf = buf[:0]
				}
				if chunk.err == io.EOF && !produced.Load() {
					return false, fmt.Errorf("encoder exited immediately: %s", pipeline.errorTail())
				}
				return produced.Load(), fmt.Errorf("pipeline output: %w (%s)", chunk.err, pipeline.errorTail())
			}
		}
	}
}

// logSessionSummary emits the periodic throughput/health line. stale is the
// client's stale-frame skips since the previous summary.
func logSessionSummary(specName, bitrate string, nals, bytes int64, stats *clientStatsMsg, stale int64) {
	if stale > 0 {
		log.Printf("Video [%s @ %s]: client skipped %d stale frames since the last summary (it fell behind after a link or decoder stall and jumped to the newest picture)",
			specName, bitrate, stale)
	}
	if stats != nil {
		log.Printf("Video [%s @ %s]: sent %d NALs / %.1f MB; client: recv=%d rend=%d drop=%d decode=%.1fms present=%.1fms queue=%d mode=%s guardrail=%t refresh=%.1fHz (%s) (last %dms)",
			specName, bitrate, nals, float64(bytes)/1e6,
			stats.FramesReceived, stats.FramesRendered, stats.FrameDrops, stats.AvgDecodeMs, stats.AvgPresentMs,
			stats.QueuedFrames, stats.RenderMode, stats.PacingGuardrail, stats.RefreshHz, refreshClass(stats.RefreshHz), stats.IntervalMs)
		if stats.EchoMs > 0 {
			log.Printf("Video [%s]: input echo latency (tablet send to frame carrying it) %.1f ms", specName, stats.EchoMs)
		}
		return
	}
	log.Printf("Video [%s @ %s]: sent %d NALs / %.1f MB; no client stats yet",
		specName, bitrate, nals, float64(bytes)/1e6)
}

func refreshClass(refreshHz float64) string {
	if refreshHz <= 0 {
		return "unknown"
	}
	if refreshHz < 75 {
		return "low"
	}
	if refreshHz < 110 {
		return "mid"
	}
	return "high"
}

func h264NALType(nal []byte) int {
	offset := 0
	switch {
	case len(nal) >= 4 && nal[0] == 0 && nal[1] == 0 && nal[2] == 0 && nal[3] == 1:
		offset = 4
	case len(nal) >= 3 && nal[0] == 0 && nal[1] == 0 && nal[2] == 1:
		offset = 3
	default:
		return -1
	}
	if len(nal) <= offset {
		return -1
	}
	return int(nal[offset] & 0x1F)
}

// writeVideoPacket frames one payload per the video channel wire format.
func writeVideoPacket(w io.Writer, pktType byte, payload []byte) error {
	var header [5]byte
	header[0] = pktType
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if len(payload) == 0 {
		_, err := w.Write(header[:])
		return err
	}
	parts := net.Buffers{header[:], payload}
	_, err := parts.WriteTo(w)
	return err
}

// splitAnnexB extracts complete NAL units from buf. Units keep their leading
// start code (3- or 4-byte) so the decoder receives standard Annex-B input.
// The final, possibly incomplete unit is returned as rest — a unit is only
// "complete" once the next start code (or EOF) is seen.
func splitAnnexB(buf []byte) (units [][]byte, rest []byte) {
	starts := make([]int, 0, 8)
	i := 0
	for i+2 < len(buf) {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 1 {
			start := i
			// Fold a preceding zero into a 4-byte start code, but never
			// steal the tail byte of the previous unit's start code.
			if i > 0 && buf[i-1] == 0 && (len(starts) == 0 || starts[len(starts)-1] < i-1) {
				start = i - 1
			}
			starts = append(starts, start)
			i += 3
			continue
		}
		i++
	}
	if len(starts) == 0 {
		return nil, buf
	}
	for j := 0; j+1 < len(starts); j++ {
		unit := buf[starts[j]:starts[j+1]]
		units = append(units, unit)
	}
	return units, buf[starts[len(starts)-1]:]
}

// flushAnnexBTail emits a trailing unit when no new bytes have arrived for
// a short interval, avoiding an extra frame interval of host-side hold-back.
func flushAnnexBTail(buf []byte) ([]byte, bool) {
	if len(buf) < 5 {
		return nil, false
	}
	offset := annexBNALHeaderOffset(buf)
	if offset < 0 {
		return nil, false
	}
	unit := make([]byte, len(buf))
	copy(unit, buf)
	return unit, true
}

func authorizeTransportVideo(conn net.Conn, requiredToken string) bool {
	want := strings.TrimSpace(requiredToken)
	if want == "" {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(transportVideoAuthTimeout))
	reader := bufio.NewReaderSize(conn, transportVideoAuthMaxBytes)
	line, err := reader.ReadSlice('\n')
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return false
	}
	if len(line) > transportVideoAuthMaxBytes {
		return false
	}
	var msg videoAuthMsg
	if err := json.Unmarshal(line, &msg); err != nil {
		return false
	}
	if msg.Type != "auth" {
		return false
	}
	got := strings.TrimSpace(msg.AuthToken)
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
