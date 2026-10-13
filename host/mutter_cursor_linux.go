//go:build linux

package main

// Cursor companion for the GNOME virtual monitor.
//
// Mutter (seen on 50.1) does not show the mouse pointer in an embedded-cursor
// virtual monitor stream while any physical display has a hardware cursor
// plane: it decides "needs a software cursor" once for the whole stage, and a
// physical CRTC with a hardware cursor sprite makes that false everywhere. The
// virtual monitor, which has no cursor plane, then gets no cursor overlay,
// and since nothing is painted for pointer motion, no new frames either.
//
// Mutter turns hardware cursors off while the pointer is over a monitor that
// an embedded-cursor monitor stream (RecordMonitor) records. So the host
// keeps such a stream of the virtual monitor itself, consumed at 1 fps and
// thrown away: while the pointer is on the virtual monitor the stage paints
// the software cursor there, and the virtual stream gets a frame per pointer
// move, cursor included.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// mutterCursorRetry spaces restarts of a companion that ended on its own.
var mutterCursorRetry = 3 * time.Second

// startMutterCursorCompanion keeps the cursor companion stream of connector
// running until stop is called or castClosed is closed. A variable for tests.
var startMutterCursorCompanion = runMutterCursorCompanion

func runMutterCursorCompanion(connector string, castClosed <-chan struct{}) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		logged := false
		for {
			err := mutterCursorCompanionOnce(connector, castClosed, done)
			select {
			case <-done:
				return
			case <-castClosed:
				return
			default:
			}
			if err != nil && !logged {
				log.Printf("GNOME virtual monitor %s: cursor companion stream: %v (retrying)", connector, err)
				logged = true
			}
			select {
			case <-done:
				return
			case <-castClosed:
				return
			case <-time.After(mutterCursorRetry):
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}

// mutterCursorCompanionOnce runs one companion session until it ends or
// done/castClosed is closed.
func mutterCursorCompanionOnce(connector string, castClosed, done <-chan struct{}) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("session bus: %w", err)
	}
	defer conn.Close()
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	call := func(path dbus.ObjectPath, method string, args ...interface{}) *dbus.Call {
		ctx, cancel := context.WithTimeout(context.Background(), mutterCallTimeout)
		defer cancel()
		return conn.Object(mutterBusName, path).CallWithContext(ctx, method, 0, args...)
	}

	var session dbus.ObjectPath
	if err := call(mutterObjectPath, mutterIface+".CreateSession", map[string]dbus.Variant{}).Store(&session); err != nil {
		return fmt.Errorf("CreateSession: %w", err)
	}
	defer func() { _ = call(session, mutterSessionIface+".Stop").Err }()
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(session),
		dbus.WithMatchInterface(mutterSessionIface),
		dbus.WithMatchMember("Closed"),
	); err != nil {
		return err
	}
	var stream dbus.ObjectPath
	props := map[string]dbus.Variant{"cursor-mode": dbus.MakeVariant(uint32(1))}
	if err := call(session, mutterSessionIface+".RecordMonitor", connector, props).Store(&stream); err != nil {
		return fmt.Errorf("RecordMonitor: %w", err)
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(stream),
		dbus.WithMatchInterface(mutterStreamIface),
		dbus.WithMatchMember("PipeWireStreamAdded"),
	); err != nil {
		return err
	}
	if err := call(session, mutterSessionIface+".Start").Err; err != nil {
		return fmt.Errorf("session start: %w", err)
	}

	sessionClosed := errors.New("the session ended")
	wait := func(timeout <-chan time.Time) (uint32, error) {
		for {
			select {
			case <-done:
				return 0, nil
			case <-castClosed:
				return 0, nil
			case <-timeout:
				return 0, fmt.Errorf("no PipeWireStreamAdded signal within %s", pipewireNodeTimeout)
			case sig, ok := <-signals:
				if !ok {
					return 0, errors.New("D-Bus connection closed")
				}
				switch {
				case sig.Path == session && sig.Name == mutterSessionIface+".Closed":
					return 0, sessionClosed
				case sig.Path == stream && sig.Name == mutterStreamIface+".PipeWireStreamAdded":
					if node, ok := firstUint32(sig.Body); ok && timeout != nil {
						return node, nil
					}
				}
			}
		}
	}
	node, err := wait(time.After(pipewireNodeTimeout))
	if err != nil || node == 0 {
		return err
	}

	// max-framerate caps what Mutter records for this stream; it only has
	// to be streaming for the hardware cursor to stay off.
	cmd := exec.Command("gst-launch-1.0", "-q",
		"pipewiresrc", fmt.Sprintf("path=%d", node), "!",
		"video/x-raw,max-framerate=1/1", "!",
		"fakesink", "sync=false")
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		select {
		case <-exited:
		default:
			_ = cmd.Process.Kill()
			<-exited
		}
	}()
	go func() {
		// Forward the end of the session or the D-Bus connection.
		if _, err := wait(nil); err != nil {
			_ = cmd.Process.Kill()
		}
	}()
	select {
	case <-done:
		return nil
	case <-castClosed:
		return nil
	case err := <-exited:
		exited <- err
		if err == nil {
			return errors.New("the PipeWire consumer exited")
		}
		return fmt.Errorf("the PipeWire consumer exited: %w", err)
	}
}
