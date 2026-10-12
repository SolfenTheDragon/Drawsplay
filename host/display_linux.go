//go:build linux

package main

// Linux session display, by session type:
//
//   - X11: a virtual RandR output switched on as an extended display
//     (x11_extend_linux.go);
//   - Wayland: a GNOME (Mutter) virtual monitor (mutter_extend_linux.go), or
//     a KDE Plasma, sway or Hyprland virtual output (wl_extend_linux.go);
//   - otherwise, or when those are unavailable: a mirror of the X screen
//     recorded with x11grab, so video always works. Under a Wayland session
//     that is XWayland's screen, which only shows X11 applications: native
//     Wayland windows appear black, and the desktop is not extended.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// newVirtualDisplay creates the session display: a virtual output of the
// compositor on Wayland (GNOME, KDE Plasma, sway, Hyprland), a virtual RandR
// output on X11, otherwise an x11grab mirror of the X screen.
//
// The Mutter path is only taken when the GStreamer PipeWire consumer tooling
// is actually runnable: creating the virtual monitor without a way to consume
// its PipeWire stream would leave the session with a monitor but no video
// ("all capture pipelines failed"). Skipping it up front makes the fallback
// deterministic and the reason observable in the log.
func newVirtualDisplay(opts videoOptions) (virtualDisplay, error) {
	if currentLinuxSessionBackend() == "x11" {
		display := os.Getenv("DISPLAY")
		if display == "" {
			return nil, fmt.Errorf("no X11 DISPLAY; cannot capture")
		}
		d, err := newX11ExtendedDisplay(opts, display)
		if err == nil {
			return d, nil
		}
		throttledLog("x11-extend-unavailable", "X11 extended display unavailable (%v); mirroring the desktop instead", err)
		return newX11MirrorDisplay(display, !x11VirtualDisplayReady()), nil
	}

	toolingErr := cachedGstPipeWireCaptureSupport()
	backend := detectWlExtBackend()
	switch {
	case backend != nil && wlExtNeedsPipeWire(backend) && toolingErr != nil:
		log.Printf("PipeWire capture tooling unavailable (%v); skipping the %s extended display", toolingErr, backend.Label())
	case backend != nil:
		d, err := newWlExtDisplay(backend, opts)
		if err == nil {
			return d, nil
		}
		log.Printf("%s extended display unavailable (%v); falling back to X11 mirror", backend.Label(), err)
	case toolingErr != nil:
		log.Printf("PipeWire capture tooling unavailable (%v); skipping Mutter virtual monitor", toolingErr)
	default:
		d, err := newMutterDisplay(opts)
		if err == nil {
			return d, nil
		}
		log.Printf("Mutter virtual monitor unavailable (%v); falling back to X11 mirror", err)
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		if toolingErr != nil {
			return nil, fmt.Errorf("PipeWire capture tooling unavailable (%v) and no X11 DISPLAY for the x11grab fallback; cannot capture", toolingErr)
		}
		return nil, fmt.Errorf("no Mutter ScreenCast API and no X11 DISPLAY; cannot capture")
	}
	if currentLinuxSessionBackend() == "wayland" {
		advice := "log into an X11 session, or GNOME, KDE Plasma, sway or Hyprland, for an extended display (see README)"
		switch {
		case isCinnamonDesktop():
			advice = "Cinnamon's Wayland session cannot make virtual displays; log into Cinnamon's X11 session for an extended display (see README)"
		case backend != nil && wlExtNeedsPipeWire(backend) && toolingErr != nil:
			advice = "install the GStreamer PipeWire tooling for a KDE Plasma extended display (see README)"
		case backend != nil:
			advice = "see the log above for why the " + backend.Label() + " extended display failed"
		case toolingErr != nil:
			if ok, err := mutterScreenCastAvailable(); err == nil && ok {
				advice = "install the GStreamer PipeWire tooling for a GNOME virtual monitor (see README)"
			}
		}
		log.Printf("Warning: mirroring XWayland's screen under a Wayland session: only X11 applications' windows are captured (native Wayland windows appear black) and the desktop is not extended; %s.", advice)
	}
	return &x11MirrorDisplay{display: display}, nil
}

// detectWlExtBackend picks the extended display backend of the running
// Wayland compositor, or nil (GNOME and the rest).
func detectWlExtBackend() wlExtBackend {
	if sock := os.Getenv("SWAYSOCK"); sock != "" {
		if _, err := os.Stat(sock); err == nil {
			return swayBackend{sock: sock}
		}
	}
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		if dir, err := hyprSocketDir(); err == nil {
			return hyprBackend{dir: dir}
		}
	}
	for _, d := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if strings.EqualFold(d, "KDE") {
			return kwinBackend{}
		}
	}
	return nil
}

// wlExtNeedsPipeWire: the backend's output is captured over PipeWire.
func wlExtNeedsPipeWire(b wlExtBackend) bool {
	_, ok := b.(kwinBackend)
	return ok
}

func isCinnamonDesktop() bool {
	for _, d := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if strings.EqualFold(d, "X-Cinnamon") || strings.EqualFold(d, "Cinnamon") {
			return true
		}
	}
	return false
}

func linuxSessionBackend(sessionType, display, waylandDisplay string) string {
	switch strings.ToLower(strings.TrimSpace(sessionType)) {
	case "x11":
		return "x11"
	case "wayland":
		return "wayland"
	}
	if strings.TrimSpace(waylandDisplay) != "" {
		return "wayland"
	}
	if strings.TrimSpace(display) != "" {
		return "x11"
	}
	return "unknown"
}

func currentLinuxSessionBackend() string {
	return linuxSessionBackend(
		os.Getenv("XDG_SESSION_TYPE"),
		os.Getenv("DISPLAY"),
		os.Getenv("WAYLAND_DISPLAY"),
	)
}

// gstPipeWireCaptureSupport reports why GStreamer-based PipeWire capture
// cannot work on this host, or nil when the tooling is runnable. Dependencies
// are injected for testability.
func gstPipeWireCaptureSupport(lookPath func(string) (string, error), inspect func(element string) error) error {
	if _, err := lookPath("gst-launch-1.0"); err != nil {
		return fmt.Errorf("gst-launch-1.0 not found (install gstreamer1.0-tools)")
	}
	if err := inspect("pipewiresrc"); err != nil {
		return fmt.Errorf("GStreamer element pipewiresrc unavailable (install gstreamer1.0-pipewire)")
	}
	return nil
}

// gstInspectElement checks that a GStreamer element is installed.
func gstInspectElement(element string) error {
	return exec.Command("gst-inspect-1.0", element).Run()
}

// cachedGstPipeWireCaptureSupport memoizes gstPipeWireCaptureSupport for the
// life of the host process. The check spawns gst-inspect-1.0 (a process
// fork+exec, tens of milliseconds), and newVirtualDisplay runs it again on
// every session start/reconnect (cable replug, app restart, adaptive
// display recovery). Installed GStreamer plugins don't change while the
// host is running, so the first result is reused, shaving that latency off
// every subsequent session start.
var (
	gstPipeWireCaptureSupportOnce sync.Once
	gstPipeWireCaptureSupportErr  error
)

func cachedGstPipeWireCaptureSupport() error {
	gstPipeWireCaptureSupportOnce.Do(func() {
		gstPipeWireCaptureSupportErr = gstPipeWireCaptureSupport(exec.LookPath, gstInspectElement)
	})
	return gstPipeWireCaptureSupportErr
}

// x11MirrorDisplay mirrors the X11 root screen (M1 behavior).
type x11MirrorDisplay struct {
	display string
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

var (
	x11MirrorRepairInterval = time.Second
	x11MirrorOutputReady    = x11VirtualDisplayReady
	x11MirrorRestart        = restartActiveVideoSession
)

func newX11MirrorDisplay(display string, watchForVirtualOutput bool) *x11MirrorDisplay {
	d := &x11MirrorDisplay{display: display, stop: make(chan struct{}), done: make(chan struct{})}
	if watchForVirtualOutput {
		go watchX11MirrorForVirtualOutput(d.stop, d.done, x11MirrorRepairInterval,
			x11SessionActive, currentVideoStatus, x11MirrorOutputReady, x11MirrorRestart)
	} else {
		close(d.done)
	}
	return d
}

func watchX11MirrorForVirtualOutput(
	stop <-chan struct{},
	done chan<- struct{},
	interval time.Duration,
	isX11 func() bool,
	status func() videoStatusInfo,
	outputReady func() bool,
	restart func() bool,
) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		video := status()
		if shouldRestartX11AfterSetupRepair(isX11(), video, outputReady()) {
			refreshX11VkmsWarning()
			go func() {
				if restart() {
					log.Printf("X11 virtual display became available after startup; restarting mirrored video session")
				}
			}()
			return
		}
	}
}

func (d *x11MirrorDisplay) Target() captureTarget {
	return captureTarget{Kind: captureX11Grab}
}

func (d *x11MirrorDisplay) Description() string {
	return fmt.Sprintf("mirror of X11 display %s", d.display)
}

// Refresh is a no-op: x11grab always records the root screen.
func (d *x11MirrorDisplay) Refresh() (captureTarget, error) { return d.Target(), nil }

func (d *x11MirrorDisplay) Close() {
	d.once.Do(func() {
		close(d.stop)
		<-d.done
	})
}
