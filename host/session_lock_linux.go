//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	sessionLockPollInterval   = 500 * time.Millisecond
	sessionLockCallTimeout    = 2 * time.Second
	sessionLockRetryInterval  = 2 * time.Second
	sessionLockServiceWarning = "linux-session-lock"
)

type screenSaverService struct {
	name          string
	path          dbus.ObjectPath
	interfaceName string
}

var screenSaverServices = []screenSaverService{
	{name: "org.freedesktop.ScreenSaver", path: "/org/freedesktop/ScreenSaver", interfaceName: "org.freedesktop.ScreenSaver"},
	{name: "org.cinnamon.ScreenSaver", path: "/org/cinnamon/ScreenSaver", interfaceName: "org.cinnamon.ScreenSaver"},
	{name: "org.gnome.ScreenSaver", path: "/org/gnome/ScreenSaver", interfaceName: "org.gnome.ScreenSaver"},
	{name: "org.mate.ScreenSaver", path: "/org/mate/ScreenSaver", interfaceName: "org.mate.ScreenSaver"},
}

func startSessionLockMonitor(ctx context.Context) {
	go func() {
		var last bool
		haveLast := false
		for {
			conn, err := dbus.ConnectSessionBus()
			if err != nil {
				throttledLog(sessionLockServiceWarning, "Linux session lock monitor: session D-Bus unavailable (%v)", err)
				if !waitSessionLockRetry(ctx) {
					return
				}
				continue
			}

			locked, err := querySessionLocked(ctx, conn)
			if err != nil {
				throttledLog(sessionLockServiceWarning, "Linux session lock monitor: no supported screen-saver lock service (%v)", err)
				conn.Close()
				if !waitSessionLockRetry(ctx) {
					return
				}
				continue
			}
			if applySessionLockState(locked, &last, &haveLast) {
				restartActiveVideoSession()
			}

			ticker := time.NewTicker(sessionLockPollInterval)
			connected := true
			for connected {
				select {
				case <-ctx.Done():
					ticker.Stop()
					conn.Close()
					return
				case <-ticker.C:
					locked, err = querySessionLocked(ctx, conn)
					if err != nil {
						throttledLog(sessionLockServiceWarning, "Linux session lock monitor: screen-saver query failed (%v)", err)
						connected = false
						break
					}
					if applySessionLockState(locked, &last, &haveLast) {
						restartActiveVideoSession()
					}
				}
			}
			ticker.Stop()
			conn.Close()
			if !waitSessionLockRetry(ctx) {
				return
			}
		}
	}()
}

// preferredScreenSaver is the index of the service that last answered, asked
// first so desktops matching a later entry do not pay for the failing
// queries of the earlier ones on every poll.
var preferredScreenSaver atomic.Int32

// querySessionLocked asks the running screen-saver service whether the session
// is locked. Calls never auto-start a service: on GNOME, asking
// org.cinnamon.ScreenSaver activated cinnamon-screensaver (which exits at
// once on Wayland) on every poll, two processes and a burst of syslog lines
// per second.
func querySessionLocked(ctx context.Context, conn *dbus.Conn) (bool, error) {
	var errs []error
	first := int(preferredScreenSaver.Load())
	for n := range screenSaverServices {
		i := (first + n) % len(screenSaverServices)
		service := screenSaverServices[i]
		callCtx, cancel := context.WithTimeout(ctx, sessionLockCallTimeout)
		var active bool
		err := conn.Object(service.name, service.path).CallWithContext(
			callCtx,
			service.interfaceName+".GetActive",
			dbus.FlagNoAutoStart,
		).Store(&active)
		cancel()
		if err == nil {
			preferredScreenSaver.Store(int32(i))
			return active, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", service.name, err))
	}
	return false, errors.Join(errs...)
}

func applySessionLockState(locked bool, last *bool, haveLast *bool) bool {
	if *haveLast && locked == *last {
		return false
	}
	setSessionDisplayBlackout(locked)
	if locked {
		log.Printf("Linux session locked: streaming blackout frames")
	} else {
		log.Printf("Linux session unlocked: resuming desktop capture")
	}
	*last = locked
	*haveLast = true
	return true
}

func waitSessionLockRetry(ctx context.Context) bool {
	timer := time.NewTimer(sessionLockRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
