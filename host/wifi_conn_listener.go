package main

import (
	"errors"
	"net"
	"sync"
)

var errWiFiListenerFull = errors.New("Wi-Fi session queue full")

// wifiConnListener feeds already-authenticated Wi-Fi connections to the
// transport-parameterized accept loops. It deliberately has no SetDeadline so
// those loops block in Accept until the listener is closed.
type wifiConnListener struct {
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newWiFiConnListener() *wifiConnListener {
	return &wifiConnListener{conns: make(chan net.Conn, 2), done: make(chan struct{})}
}

// Push hands over ownership of conn; on error the conn is closed.
func (l *wifiConnListener) Push(conn net.Conn) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		conn.Close()
		return net.ErrClosed
	}
	select {
	case l.conns <- conn:
		return nil
	default:
		conn.Close()
		return errWiFiListenerFull
	}
}

func (l *wifiConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *wifiConnListener) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		close(l.done)
		l.mu.Unlock()
		for {
			select {
			case c := <-l.conns:
				c.Close()
			default:
				return
			}
		}
	})
	return nil
}

func (l *wifiConnListener) Addr() net.Addr { return wifiListenerAddr{} }

type wifiListenerAddr struct{}

func (wifiListenerAddr) Network() string { return "wifi" }
func (wifiListenerAddr) String() string  { return "wifi-session" }
