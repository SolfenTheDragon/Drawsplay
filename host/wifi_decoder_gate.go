package main

import (
	"errors"
	"sync"
	"time"

	"drawsplay/host/internal/wifilan"
)

var (
	errWiFiDecoderRecord  = errors.New("invalid tablet control record; expected decoder-ready")
	errWiFiDecoderTimeout = errors.New("tablet did not report its decoder ready")
)

// wifiDecoderGate opens when the tablet's decoder-ready control record (7,0,0,1)
// arrives. It is fed by wifiInputConn's control handler.
type wifiDecoderGate struct {
	once sync.Once
	open chan struct{}
}

func newWiFiDecoderGate() *wifiDecoderGate { return &wifiDecoderGate{open: make(chan struct{})} }

func (g *wifiDecoderGate) Handle(_ wifilan.FrameHeader, payload []byte) error {
	if len(payload) != 4 || payload[0] != 7 || payload[1] != 0 || payload[2] != 0 || payload[3] != 1 {
		return errWiFiDecoderRecord
	}
	g.once.Do(func() { close(g.open) })
	return nil
}

func (g *wifiDecoderGate) Wait(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-g.open:
		return nil
	case <-timer.C:
		return errWiFiDecoderTimeout
	}
}
