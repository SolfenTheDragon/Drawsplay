package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxPacketBytes                      = 64 * 1024
	maxConcurrentConnectionsPerListener = 1
	initialHelloTimeout                 = 5 * time.Second
)

var globalInputConnID atomic.Uint64

type inputWireMode int

const (
	inputWireLegacyJSON inputWireMode = iota
	inputWireMuxV1
)

func runInputListener(ctx context.Context, ln net.Listener, store *settingsStore, transport string) {
	events := newEventLogger(eventLogBuffer)
	inj := newInjector()
	defer inj.close()

	connectionSlots := make(chan struct{}, maxConcurrentConnectionsPerListener)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if dl, ok := ln.(interface{ SetDeadline(time.Time) error }); ok {
			if err := dl.SetDeadline(time.Now().Add(1 * time.Second)); err != nil {
				log.Printf("SetDeadline error: %v", err)
			}
		}

		conn, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("Accept error: %v", err)
			continue
		}

		id := globalInputConnID.Add(1)
		select {
		case connectionSlots <- struct{}{}:
		default:
			throttledLog("input-conn-limit", "Rejecting connection: listener connection limit reached (%d)", maxConcurrentConnectionsPerListener)
			_ = conn.Close()
			continue
		}
		operationalCounters.inputConnections.Add(1)
		log.Printf("Client connected (session=%d, remote=%s)", id, conn.RemoteAddr().String())
		go func() {
			defer func() { <-connectionSlots }()
			defer logPanic(fmt.Sprintf("input session %d", id))
			handleConnection(ctx, id, conn, events, inj, store, transport)
		}()
	}
}

func handleConnection(ctx context.Context, id uint64, conn net.Conn, events *eventLogger, inj injector, store *settingsStore, transport string) {
	defer conn.Close()
	registerInputConnection(id, conn)
	defer unregisterInputConnection(id)
	defer unbindSessionDevice(id)
	defer unbindSessionClientIdentity(id)
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	inputOwned := false
	defer func() {
		if inputOwned {
			inj.cancelAll()
			hostSessionOwnership.releaseInput(transport)
		}
	}()

	if tcpConn := underlyingTCPConn(conn); tcpConn != nil {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetReadBuffer(256 * 1024)
		_ = tcpConn.SetWriteBuffer(256 * 1024)
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	reader := bufio.NewReaderSize(conn, 256*1024)
	if err := conn.SetReadDeadline(time.Now().Add(initialHelloTimeout)); err != nil {
		log.Printf("Read deadline error (session=%d): %v", id, err)
		return
	}
	mode, err := detectInputWireMode(reader)
	if err != nil {
		log.Printf("Connection probe failed (session=%d): %v", id, err)
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		log.Printf("Read deadline reset error (session=%d): %v", id, err)
		return
	}

	var writeMu sync.Mutex
	var txSeq uint32 = 1
	writeControl := func(msg any) bool {
		payload, err := json.Marshal(msg)
		if err != nil {
			return true
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		switch mode {
		case inputWireMuxV1:
			err = writeMuxFrame(conn, muxFrame{
				StreamID: muxStreamControl,
				Flags:    0,
				Seq:      txSeq,
				Payload:  payload,
			})
			txSeq++
		default:
			_, err = conn.Write(append(payload, '\n'))
		}
		return err == nil
	}

	sendDisplayMode := func(w, h int) {
		if !writeControl(hostDisplayModeMsg{Type: "displayMode", Width: w, Height: h}) {
			log.Printf("Display-mode push failed (session=%d)", id)
		}
	}
	sendClearDisplayMode := func() {
		if !writeControl(hostClearDisplayModeMsg{Type: "clearDisplayMode"}) {
			log.Printf("Display-preference clear push failed (session=%d)", id)
		}
	}
	sendSessionPolicy := func(keepAlive bool, wifiEnabled bool, wifiAuthToken string) {
		tlsRequired, tlsFP := currentWiFiPolicyTLS()
		clientWiFiEnabled := wifiEnabled && tlsRequired && strings.TrimSpace(tlsFP) != ""
		clientWiFiToken := ""
		if clientWiFiEnabled {
			clientWiFiToken = wifiAuthToken
		}
		if !writeControl(hostSessionPolicyMsg{
			Type:                  "sessionPolicy",
			KeepAliveOnBackground: keepAlive,
			WifiEnabled:           clientWiFiEnabled,
			WifiAuthToken:         clientWiFiToken,
			WifiTLSRequired:       tlsRequired,
			WifiTLSFingerprint:    tlsFP,
		}) {
			log.Printf("Session-policy push failed (session=%d)", id)
		}
	}
	defer unsubscribeModeChanges(id)
	defer unsubscribeModePreferenceClears(id)

	var cursorWriteFailure sync.Once
	sendCursor := func(msg hostCursorMsg) {
		if writeControl(msg) {
			return
		}
		cursorWriteFailure.Do(func() {
			log.Printf("Cursor push failed (session=%d)", id)
		})
	}

	helloSeen := false
	onMsg := func(msg packet) bool {
		msg.normalizeAliases()
		switch msg.Type {
		case "hello":
			if helloSeen {
				log.Printf("Duplicate hello ignored (session=%d)", id)
				return true
			}
			if want, protected := transportAuthToken(store.get(), transport); protected {
				got := strings.TrimSpace(msg.AuthToken)
				if want == "" {
					log.Printf("%s hello rejected (session=%d): auth token unavailable", transportDisplayName(transport), id)
					return false
				}
				if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
					log.Printf("%s hello rejected (session=%d): auth token mismatch", transportDisplayName(transport), id)
					return false
				}
			}
			if !hostSessionOwnership.acquireInput(transport) {
				log.Printf(
					"Input hello rejected (session=%d, transport=%s): active session owned by %s",
					id, transport, hostSessionOwnership.currentTransport(),
				)
				return false
			}
			inputOwned = true
			helloSeen = true
			handleHello(msg, id)
			subscribeModeChanges(id, sendDisplayMode)
			subscribeModePreferenceClears(id, sendClearDisplayMode)
			cfg := store.get()
			if transport == "usb" || transport == "wifi" {
				sendSessionPolicy(cfg.KeepAliveOnBackground, cfg.WifiEnabled, cfg.WifiAuthToken)
			} else {
				sendSessionPolicy(cfg.KeepAliveOnBackground, false, "")
			}
			if cfg.LocalCursor {
				go runCursorPublisher(sessionCtx, sendCursor)
			} else {
				sendCursor(hostCursorMsg{Type: "cursor", Visible: false})
			}

			return true
		case "input":
			if !helloSeen {
				log.Printf("Input before hello, closing connection (session=%d)", id)
				return false
			}
			if sessionInputLocked() {
				return true
			}
			handleInput(&msg, id, events, inj, store)
			return true
		case "key":
			if !helloSeen {
				log.Printf("Key before hello, closing connection (session=%d)", id)
				return false
			}
			if sessionInputLocked() {
				return true
			}
			if !store.get().KeyboardEnabled {
				return true
			}
			inj.injectKey(&msg)
			if eventDiagnosticsEnabled() {
				events.enqueue(fmt.Sprintf(
					"KEY session=%d action=%s keycode=%d meta=%d ts=%d",
					id, msg.Action, msg.Keycode, msg.Meta, msg.Timestamp,
				))
			}
			return true
		default:
			log.Printf("Unknown packet type (session=%d): %s", id, msg.Type)
			return true
		}
	}

	switch mode {
	case inputWireMuxV1:
		if err := conn.SetReadDeadline(time.Now().Add(initialHelloTimeout)); err != nil {
			log.Printf("Read deadline error (session=%d): %v", id, err)
			return
		}
		for {
			frame, err := readMuxFrame(reader)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() && !helloSeen {
					log.Printf("Initial hello timeout, closing connection (session=%d)", id)
					return
				}
				if errors.Is(err, net.ErrClosed) {
					break
				}
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return
				}
				if err.Error() == "EOF" {
					break
				}
				log.Printf("Read error (session=%d): %v", id, err)
				return
			}
			if !helloSeen {
				_ = conn.SetReadDeadline(time.Time{})
			}
			if frame.StreamID != muxStreamInput && frame.StreamID != muxStreamControl {
				continue
			}
			var msg packet
			if err := json.Unmarshal(frame.Payload, &msg); err != nil {
				log.Printf("Decode error (session=%d): %v", id, err)
				return
			}
			if !onMsg(msg) {
				return
			}
		}
	default:
		if err := conn.SetReadDeadline(time.Now().Add(initialHelloTimeout)); err != nil {
			log.Printf("Read deadline error (session=%d): %v", id, err)
			return
		}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 8*1024), maxPacketBytes)
		for scanner.Scan() {
			if !helloSeen {
				_ = conn.SetReadDeadline(time.Time{})
			}
			var msg packet
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				log.Printf("Decode error (session=%d): %v", id, err)
				return
			}
			if !onMsg(msg) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				log.Printf("Packet exceeded %d bytes, closing connection (session=%d)", maxPacketBytes, id)
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() && !helloSeen {
				log.Printf("Initial hello timeout, closing connection (session=%d)", id)
				return
			}
			log.Printf("Read error (session=%d): %v", id, err)
			return
		}
	}

	log.Printf("Client disconnected (session=%d)", id)
}

func underlyingTCPConn(conn net.Conn) *net.TCPConn {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		return tcpConn
	}
	if wrapped, ok := conn.(interface{ NetConn() net.Conn }); ok {
		tcpConn, _ := wrapped.NetConn().(*net.TCPConn)
		return tcpConn
	}
	return nil
}

func detectInputWireMode(reader *bufio.Reader) (inputWireMode, error) {
	hdr, err := reader.Peek(2)
	if err != nil {
		return inputWireLegacyJSON, err
	}
	if len(hdr) == 2 && hdr[0] == byte(usbMuxMagic>>8) && hdr[1] == byte(usbMuxMagic&0xFF) {
		return inputWireMuxV1, nil
	}
	return inputWireLegacyJSON, nil
}

func handleHello(msg packet, id uint64) {
	platform, clientType, clientVersion := normalizeClientIdentity(msg)
	device := strings.TrimSpace(msg.Device)
	if device == "" {
		device = "unknown-client"
	}
	bindSessionDevice(id, device)
	bindSessionClientIdentity(id, clientIdentity{
		Platform:     platform,
		Type:         clientType,
		Version:      clientVersion,
		NativeWidth:  msg.ScreenWidth,
		NativeHeight: msg.ScreenHeight,
	})
	log.Printf(
		"HELLO session=%d device=%s platform=%s type=%s clientVersion=%s android=%s app=%s screen=%dx%d pref=%dx%d codecs=%v ts=%d",
		id,
		device,
		platform,
		clientType,
		clientVersion,
		msg.Android,
		msg.App,
		msg.ScreenWidth,
		msg.ScreenHeight,
		msg.PrefWidth,
		msg.PrefHeight,
		msg.VideoCodecs,
		msg.Timestamp,
	)
	publishClientVideoCodecs(msg.VideoCodecs)
}

func normalizeClientIdentity(msg packet) (string, string, string) {
	platform := strings.ToLower(strings.TrimSpace(msg.ClientOS))
	if platform == "" {
		if strings.TrimSpace(msg.Android) != "" {
			platform = "android"
		} else {
			platform = "unknown"
		}
	}
	clientType := strings.ToLower(strings.TrimSpace(msg.ClientType))
	if clientType == "" {
		if platform == "android" {
			clientType = "tablet"
		} else {
			clientType = "client"
		}
	}
	version := strings.TrimSpace(msg.ClientVer)
	if version == "" {
		version = strings.TrimSpace(msg.App)
	}
	return platform, clientType, version
}

func handleInput(msg *packet, id uint64, events *eventLogger, inj injector, store *settingsStore) {
	s := store.get()
	if !inputAllowed(s, msg.ToolType) {
		return
	}
	if msg.Width > 0 && msg.Height > 0 {
		device := sessionDevice(id)
		cal := calibrationForDevice(device)
		x := msg.X / float32(msg.Width)
		y := msg.Y / float32(msg.Height)
		x, y = cal.apply(x, y)
		msg.X = x * float32(msg.Width)
		msg.Y = y * float32(msg.Height)
	}
	inj.inject(msg)
	if eventDiagnosticsEnabled() {
		events.enqueue(fmt.Sprintf(
			"INPUT session=%d action=%s x=%.2f y=%.2f pressure=%.3f tilt=%.2f orient=%.2f buttons=%d tool=%s pointer=%d/%d area=%dx%d ts=%d",
			id,
			msg.Action,
			msg.X,
			msg.Y,
			msg.Pressure,
			msg.Tilt,
			msg.Orientation,
			msg.Buttons,
			msg.ToolType,
			msg.PointerID,
			msg.PointerCnt,
			msg.Width,
			msg.Height,
			msg.Timestamp,
		))
	}
}

func inputAllowed(s settings, toolType string) bool {
	if toolType == "finger" {
		return s.TouchEnabled
	}
	if isPenTool(toolType) {
		return s.PenEnabled
	}
	return true
}

var activeInputConnections = struct {
	sync.Mutex
	conns map[uint64]net.Conn
}{conns: make(map[uint64]net.Conn)}

func registerInputConnection(id uint64, conn net.Conn) {
	activeInputConnections.Lock()
	activeInputConnections.conns[id] = conn
	activeInputConnections.Unlock()
}

func unregisterInputConnection(id uint64) {
	activeInputConnections.Lock()
	delete(activeInputConnections.conns, id)
	activeInputConnections.Unlock()
}

func restartActiveInputSessions() bool {
	activeInputConnections.Lock()
	conns := make([]net.Conn, 0, len(activeInputConnections.conns))
	for _, conn := range activeInputConnections.conns {
		conns = append(conns, conn)
	}
	activeInputConnections.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	if len(conns) > 0 {
		operationalCounters.inputRestarts.Add(1)
	}
	return len(conns) > 0
}
