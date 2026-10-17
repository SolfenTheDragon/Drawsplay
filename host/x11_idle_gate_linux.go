//go:build linux

package main

import (
	"context"
	"log"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// idleGateStartDelay lets the encoder deliver its first frames (config,
	// IDR) before it can be paused.
	idleGateStartDelay = 3 * time.Second
	// idleGateAfter is how long the screen must be quiet before the encoder
	// is paused. It covers the grab-to-last-byte latency of the final change.
	idleGateAfter = 150 * time.Millisecond
	// idleGateHeartbeat briefly resumes a paused encoder so the client keeps
	// receiving frames (liveness, and a safety net if an event is missed).
	idleGateHeartbeat      = time.Second
	idleGateHeartbeatBurst = 60 * time.Millisecond
)

// startIdleGate pauses (SIGSTOP) the capture process while the watched screen
// area is idle and resumes it (SIGCONT) on the first damage or pointer
// movement. A paused ffmpeg grabs, uploads and encodes nothing, which is what
// removes the CPU, bitrate and tablet decode cost of a static screen.
// Measured SIGCONT-to-first-frame: 0.5-12 ms. Any failure leaves the process
// running ungated. The returned function resumes the process and stops
// watching; call it before stopping the pipeline.
func startIdleGate(ctx context.Context, proc *os.Process, g *idleGateSpec) func() {
	if g == nil || proc == nil {
		return func() {}
	}
	activity := make(chan struct{}, 1)
	watcher, err := newX11ActivityWatcher(g.Display, x11Rect{X: g.X, Y: g.Y, W: g.W, H: g.H}, func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	})
	if err != nil {
		log.Printf("Idle capture gate unavailable, capturing continuously: %v", err)
		return func() {}
	}
	log.Printf("Idle capture gate active for %dx%d+%d+%d", g.W, g.H, g.X, g.Y)

	var paused atomic.Bool
	resume := func() {
		if paused.Swap(false) {
			_ = proc.Signal(syscall.SIGCONT)
		}
	}
	stopCh := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer resume()
		select {
		case <-time.After(idleGateStartDelay):
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-watcher.closed():
			return
		}
		idle := time.NewTimer(idleGateAfter)
		defer idle.Stop()
		for {
			if !paused.Load() {
				select {
				case <-activity:
					if !idle.Stop() {
						select {
						case <-idle.C:
						default:
						}
					}
					idle.Reset(idleGateAfter)
				case <-idle.C:
					paused.Store(true)
					_ = proc.Signal(syscall.SIGSTOP)
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				case <-watcher.closed():
					return
				}
				continue
			}
			heartbeat := time.NewTimer(idleGateHeartbeat)
			select {
			case <-activity:
				heartbeat.Stop()
				resume()
				idle.Reset(idleGateAfter)
			case <-heartbeat.C:
				resume()
				select {
				case <-time.After(idleGateHeartbeatBurst):
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				}
				paused.Store(true)
				_ = proc.Signal(syscall.SIGSTOP)
			case <-stopCh:
				heartbeat.Stop()
				return
			case <-ctx.Done():
				heartbeat.Stop()
				return
			case <-watcher.closed():
				heartbeat.Stop()
				return
			}
		}
	}()
	return func() {
		close(stopCh)
		<-finished
		watcher.close()
	}
}
