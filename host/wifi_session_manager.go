package main

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"drawsplay/host/internal/wifilan"
)

// wifiSessionManager discovers a paired tablet, establishes one authenticated
// Wi-Fi session at a time and hands its conns to the Wi-Fi accept loops. It
// yields to any other owner (USB first) and does no work unless enabled.
type wifiSessionManager struct {
	enabled    bool
	retryDelay time.Duration
	inputs     *wifiConnListener
	videos     *wifiConnListener
	// owner returns the transport currently owning the session ("" if none).
	owner     func() string
	browse    func(ctx context.Context, found func(wifilan.Candidate)) error
	establish func(ctx context.Context, c wifilan.Candidate) (input, video net.Conn, err error)
}

func (m *wifiSessionManager) run(ctx context.Context) {
	if !m.enabled {
		return
	}
	candidates := make(chan wifilan.Candidate, 1)
	go func() {
		for ctx.Err() == nil {
			err := m.browse(ctx, func(c wifilan.Candidate) {
				select {
				case candidates <- c:
				default: // the manager is busy; discovery repeats
				}
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("Wi-Fi discovery stopped: %v; retrying", err)
			}
			if !sleepCtx(ctx, m.retryDelay) {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-candidates:
			if owner := m.owner(); owner != "" && owner != transportWiFiPaired {
				continue
			}
			m.serve(ctx, c)
			if !sleepCtx(ctx, m.retryDelay) {
				return
			}
		}
	}
}

func (m *wifiSessionManager) serve(ctx context.Context, c wifilan.Candidate) {
	input, video, err := m.establish(ctx, c)
	if err != nil {
		log.Printf("Wi-Fi session with %q failed: %v", c.Name, err)
		return
	}
	closed := make(chan struct{})
	var once sync.Once
	notify := func() { once.Do(func() { close(closed) }) }
	input, video = &closeNotifyConn{Conn: input, notify: notify}, &closeNotifyConn{Conn: video, notify: notify}
	if err := m.inputs.Push(input); err != nil {
		video.Close()
		log.Printf("Wi-Fi input session rejected: %v", err)
		return
	}
	if err := m.videos.Push(video); err != nil {
		input.Close()
		log.Printf("Wi-Fi video session rejected: %v", err)
		return
	}
	select {
	case <-closed:
	case <-ctx.Done():
	}
	input.Close()
	video.Close()
}

type closeNotifyConn struct {
	net.Conn
	notify func()
}

func (c *closeNotifyConn) Close() error {
	err := c.Conn.Close()
	c.notify()
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
