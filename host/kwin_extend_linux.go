//go:build linux

package main

// KDE Plasma extended display (see wl_extend_linux.go): a KWin virtual
// output made through KWin's zkde_screencast_unstable_v1 protocol, which
// streams it over PipeWire. KWin lays the output out and remembers where
// the user puts it, how it is turned and scaled, under its connector name;
// its one mode is the size it was made with, so the host's configured
// resolution sets it. The output lasts as long as the host's stream.
//
// KWin only offers the protocol to programs whose .desktop file asks for
// it, so the host installs one for itself.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	kwinScreencastInterface = "zkde_screencast_unstable_v1"
	kwinOutputName          = "Virtual-Drawsplay"
	kwinPointerEmbedded     = 2
)

// Test hooks.
var (
	kwinDial            = dialWayland
	kwinSetTabletOutput = setKWinTabletOutput
	kwinSetTouchOutput  = setKWinTouchOutput
	kwinEnableOutput    = enableKWinOutput
	kwinPrepare         = installKWinDesktopFile
	kwinStreamTimeout   = 5 * time.Second
	kwinRedialDelay     = 2 * time.Second
)

var errKWinNoScreencast = errors.New("KWin does not offer its screencast protocol to the host")

type kwinBackend struct{}

func (kwinBackend) Label() string                                  { return "KWin" }
func (kwinBackend) Arranges() bool                                 { return false }
func (kwinBackend) Anchored() bool                                 { return false }
func (kwinBackend) LogicalSize(w, h int, scale float64) (int, int) { return w, h }

func (kwinBackend) Create(modeW, modeH int) (wlExtHandle, error) {
	installed := kwinPrepare()
	hd, err := startKWinStream(modeW, modeH)
	if errors.Is(err, errKWinNoScreencast) && installed {
		// KWin reads the new .desktop file once its service cache catches
		// up.
		time.Sleep(kwinRedialDelay)
		hd, err = startKWinStream(modeW, modeH)
	}
	if err != nil && !errors.Is(err, errKWinNoScreencast) {
		// The output may have been off (see startKWinStream).
		hd, err = startKWinStream(modeW, modeH)
	}
	if err != nil {
		return nil, err
	}
	return hd, nil
}

// kwinOutput is the host's stream of the virtual output.
type kwinOutput struct {
	c      *wlConn
	outs   *wlOutputs
	stream uint32
	node   uint32

	ready  chan error // the stream's first created or failed event
	closed chan struct{}
	once   sync.Once
}

func (k *kwinOutput) streamEvent(op uint16, m *wlMessage) {
	switch op {
	case 0: // closed
		k.end()
	case 1: // created
		k.node = m.Uint()
		select {
		case k.ready <- nil:
		default:
		}
	case 2: // failed
		err := fmt.Errorf("KWin: %s", m.Str())
		select {
		case k.ready <- err:
		default:
		}
		k.end()
	}
}

func (k *kwinOutput) end() { k.once.Do(func() { close(k.closed) }) }

// startKWinStream asks KWin for a virtual output of modeW x modeH and its
// PipeWire stream.
func startKWinStream(modeW, modeH int) (*kwinOutput, error) {
	c, err := kwinDial()
	if err != nil {
		return nil, err
	}
	outs, reg, err := watchWlOutputs(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	gs := reg.find(kwinScreencastInterface)
	if len(gs) == 0 || gs[0].Version < 2 {
		c.Close()
		return nil, errKWinNoScreencast
	}
	sc, v, err := reg.bind(gs[0], 5, nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	k := &kwinOutput{c: c, outs: outs, ready: make(chan error, 1), closed: make(chan struct{})}
	if v >= 4 {
		// stream_virtual_output_with_description: the display settings
		// describe the output.
		k.stream, err = c.create(k.streamEvent, sc, 5, func(id uint32) []wlArg {
			return []wlArg{wlUint(id), wlString("Drawsplay"), wlString("DrawSplay tablet"), wlInt(modeW), wlInt(modeH), wlFixed(1), wlUint(kwinPointerEmbedded)}
		})
	} else {
		k.stream, err = c.create(k.streamEvent, sc, 3, func(id uint32) []wlArg { // stream_virtual_output
			return []wlArg{wlUint(id), wlString("Drawsplay"), wlInt(modeW), wlInt(modeH), wlFixed(1), wlUint(kwinPointerEmbedded)}
		})
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	select {
	case err = <-k.ready:
	case <-c.Done():
		err = c.Err()
	case <-time.After(kwinStreamTimeout):
		err = errors.New("KWin did not start the virtual output's stream")
	}
	if err != nil {
		// KWin streams only an enabled output; it keeps the one it made
		// until the stream closes, so it can be switched on for next time.
		if _, _, ok := outs.lookup(kwinOutputName); !ok {
			if eerr := kwinEnableOutput(kwinOutputName); eerr != nil {
				log.Printf("KWin extended display: could not switch %s on: %v", kwinOutputName, eerr)
			}
		}
		k.close()
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		if _, _, ok := outs.lookup(kwinOutputName); ok {
			break
		}
		if attempt >= 20 {
			k.close()
			return nil, fmt.Errorf("KWin did not show %s", kwinOutputName)
		}
		time.Sleep(100 * time.Millisecond)
	}
	go func() {
		select {
		case <-c.Done():
			k.end()
		case <-k.closed:
		}
	}()
	return k, nil
}

func (k *kwinOutput) close() {
	_ = k.c.destroy(k.stream, 0) // close
	_ = k.c.roundtrip()
	k.c.Close()
	k.end()
}

func (k *kwinOutput) Name() string             { return kwinOutputName }
func (k *kwinOutput) Changed() <-chan struct{} { return k.outs.Changed() }
func (k *kwinOutput) Closed() <-chan struct{}  { return k.closed }
func (k *kwinOutput) Configure(wlExtConfig) error {
	return errors.New("KWin lays its outputs out itself")
}

func (k *kwinOutput) Target() captureTarget {
	// KWin streams the output upright, and its display settings offer it
	// no other mode.
	return captureTarget{Kind: capturePipeWire, Node: k.node, HostResolution: true}
}

func (k *kwinOutput) State() (wlExtState, error) {
	select {
	case <-k.c.Done():
		return wlExtState{}, k.c.Err()
	default:
	}
	var st wlExtState
	// KWin lists only enabled outputs.
	for _, o := range k.outs.Snapshot() {
		st.Outputs = append(st.Outputs, wlExtOutput{
			Name: o.Name, Active: true, Rect: o.Rect, ModeW: o.ModeW, ModeH: o.ModeH,
			Transform: o.Transform, Scale: float64(o.Scale), Virtual: strings.HasPrefix(o.Name, "Virtual-"),
		})
	}
	return st, nil
}

func (k *kwinOutput) MapTablet() error {
	noteTouchMapping("KWin", kwinOutputName, kwinSetTouchOutput(kwinOutputName))
	return kwinSetTabletOutput(kwinOutputName)
}

func (k *kwinOutput) Remove([]x11OutputPos, string) error {
	k.close()
	return nil
}

// setKWinTabletOutput maps the Drawsplay Pen onto output through KWin's
// input device D-Bus interface.
func setKWinTabletOutput(output string) error {
	return setKWinDeviceOutput("Drawsplay Pen", "tabletTool", output)
}

// setKWinTouchOutput maps the Drawsplay Touch device onto output.
func setKWinTouchOutput(output string) error {
	return setKWinDeviceOutput("Drawsplay Touch", "touch", output)
}

// setKWinDeviceOutput maps the input device called deviceName, whose boolean
// capability property is kind, onto output.
func setKWinDeviceOutput(deviceName, kind, output string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	const dev = "org.kde.KWin.InputDevice"
	v, err := conn.Object("org.kde.KWin", "/org/kde/KWin/InputDevice").GetProperty("org.kde.KWin.InputDeviceManager.devicesSysNames")
	if err != nil {
		return err
	}
	names, _ := v.Value().([]string)
	found := false
	for _, sys := range names {
		obj := conn.Object("org.kde.KWin", dbus.ObjectPath("/org/kde/KWin/InputDevice/"+sys))
		name, err := obj.GetProperty(dev + ".name")
		if err != nil || name.Value() != deviceName {
			continue
		}
		if tool, err := obj.GetProperty(dev + "." + kind); err != nil || tool.Value() != true {
			continue
		}
		found = true
		_ = obj.SetProperty(dev+".mapToWorkspace", dbus.MakeVariant(false))
		// KWin looks the output up again only when the name changes.
		_ = obj.SetProperty(dev+".outputName", dbus.MakeVariant(""))
		if err := obj.SetProperty(dev+".outputName", dbus.MakeVariant(output)); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("KWin does not list the %s", deviceName)
	}
	return nil
}

// enableKWinOutput switches the output on, so KWin remembers it on.
func enableKWinOutput(output string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kscreen-doctor", "output."+output+".enable").CombinedOutput()
	if err != nil {
		return fmt.Errorf("kscreen-doctor: %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// installKWinDesktopFile installs the .desktop file that lets the host use
// KWin's screencast protocol, and reports whether it changed.
func installKWinDesktopFile() bool {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		log.Printf("KWin extended display: cannot locate the host executable: %v", err)
		return false
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".local", "share")
	}
	path := filepath.Join(dir, "applications", "drawsplay-host.desktop")
	content := kwinDesktopEntry(exe)
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("KWin extended display: %v", err)
		return false
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Printf("KWin extended display: %v", err)
		return false
	}
	log.Printf("KWin extended display: installed %s so KWin lets the host create virtual outputs", path)
	for _, tool := range []string{"kbuildsycoca6", "kbuildsycoca5"} {
		if _, err := exec.LookPath(tool); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = exec.CommandContext(ctx, tool).Run()
			cancel()
			break
		}
	}
	return true
}

func kwinDesktopEntry(exe string) string {
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(exe)
	// The string escape rule applies on top of the quoting rule.
	quoted = strings.ReplaceAll(quoted, `\`, `\\`)
	return "[Desktop Entry]\nType=Application\nName=DrawSplay host\nExec=\"" + quoted + "\"\nNoDisplay=true\nX-KDE-Wayland-Interfaces=" + kwinScreencastInterface + "\n"
}
