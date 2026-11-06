package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// The legacy adb transport reaches the host through `adb reverse`, which
// connects to 127.0.0.1:27183 (input) and :27184 (video). Those listeners
// carry no authentication of their own, so they are opened only while the
// adb tunnel setting is on (AOA hands its virtual connections to the same
// handlers in-process and needs no port), and while open they refuse
// connections made by another user's process. Turning the setting off
// closes the listeners and any session that came through them.

const adbLoopbackPoll = time.Second

// runADBLoopbackListeners follows the ADBTunnelEnabled setting until ctx ends.
func runADBLoopbackListeners(ctx context.Context, store *settingsStore) {
	var (
		stop context.CancelFunc
		wg   sync.WaitGroup
	)
	shutdown := func() {
		if stop != nil {
			stop()
			wg.Wait()
			stop = nil
			log.Printf("ADB loopback listeners closed (adb tunnel off)")
		}
	}
	defer shutdown()
	tick := time.NewTicker(adbLoopbackPoll)
	defer tick.Stop()
	for {
		on := store.get().ADBTunnelEnabled
		switch {
		case on && stop == nil:
			lctx, cancel := context.WithCancel(ctx)
			stop = cancel
			startADBLoopbackListeners(lctx, store, &wg)
		case !on && stop != nil:
			shutdown()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func startADBLoopbackListeners(ctx context.Context, store *settingsStore, wg *sync.WaitGroup) {
	inputAddr := fmt.Sprintf("127.0.0.1:%d", inputPort)
	in, err := listenSameUser(inputAddr)
	if err != nil {
		log.Printf("ADB input listener disabled: %v", err)
	} else {
		log.Printf("ADB input listener on %s", inputAddr)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-ctx.Done()
			_ = in.Close()
		}()
		go func() {
			defer wg.Done()
			runInputListener(ctx, in, store, "usb")
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		runVideoListenerAt(ctx, store, fmt.Sprintf("127.0.0.1:%d", videoPort), "usb", func(addr string) (net.Listener, error) {
			return listenSameUser(addr)
		})
	}()
}

// sameUserListener accepts only loopback connections whose client socket is
// owned by this user (or by root/SYSTEM, which could act as anyone anyway),
// and closes every connection it handed out when it is closed.
type sameUserListener struct {
	*net.TCPListener
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

func listenSameUser(addr string) (*sameUserListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &sameUserListener{TCPListener: ln.(*net.TCPListener), conns: map[net.Conn]struct{}{}}, nil
}

func (l *sameUserListener) Accept() (net.Conn, error) {
	for {
		c, err := l.TCPListener.Accept()
		if err != nil {
			return nil, err
		}
		if other, err := loopbackPeerIsOtherUser(c); other {
			throttledLog("adb-loopback-foreign", "Refused a loopback connection from %s: it belongs to another user", c.RemoteAddr())
			_ = c.Close()
			continue
		} else if err != nil {
			throttledLog("adb-loopback-owner", "Could not tell which user opened %s (%v); accepting it", c.RemoteAddr(), err)
		}
		tc := &trackedConn{Conn: c, l: l}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = c.Close()
			return nil, net.ErrClosed
		}
		l.conns[tc] = struct{}{}
		l.mu.Unlock()
		return tc, nil
	}
}

func (l *sameUserListener) Close() error {
	l.mu.Lock()
	l.closed = true
	conns := l.conns
	l.conns = map[net.Conn]struct{}{}
	l.mu.Unlock()
	err := l.TCPListener.Close()
	for c := range conns {
		_ = c.Close()
	}
	return err
}

type trackedConn struct {
	net.Conn
	l    *sameUserListener
	once sync.Once
}

// NetConn exposes the TCP socket for socket options (see underlyingTCPConn).
func (c *trackedConn) NetConn() net.Conn { return c.Conn }

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.l.mu.Lock()
		delete(c.l.conns, c)
		c.l.mu.Unlock()
	})
	return c.Conn.Close()
}
