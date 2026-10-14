//go:build linux

package main

// sway extended display (see wl_extend_linux.go): a headless output made
// with sway's create_output command, set up and removed over sway's IPC
// (i3-ipc), and recorded with wlr-screencopy.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	swayRunCommand = 0
	swaySubscribe  = 2
	swayGetOutputs = 3
	swayEventBit   = 1 << 31
	swayIPCTimeout = 3 * time.Second
	// swayPenIdentifier is the Drawsplay Pen as sway's input commands name
	// it: vendor:product:name.
	swayPenIdentifier = "7504:20548:Drawsplay_Pen"
	// swayTouchIdentifier is the Drawsplay Touch multi-touch device.
	swayTouchIdentifier = "7504:20550:Drawsplay_Touch"
)

var swayTransforms = []string{"normal", "90", "180", "270", "flipped", "flipped-90", "flipped-180", "flipped-270"}

// swayCall sends one i3-ipc message and returns the reply's payload.
func swayCall(sock string, typ uint32, payload string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", sock, swayIPCTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(swayIPCTimeout))
	if err := swayWrite(conn, typ, payload); err != nil {
		return nil, err
	}
	_, reply, err := swayRead(conn)
	return reply, err
}

func swayWrite(w io.Writer, typ uint32, payload string) error {
	msg := make([]byte, 14, 14+len(payload))
	copy(msg, "i3-ipc")
	binary.NativeEndian.PutUint32(msg[6:], uint32(len(payload)))
	binary.NativeEndian.PutUint32(msg[10:], typ)
	_, err := w.Write(append(msg, payload...))
	return err
}

func swayRead(r io.Reader) (uint32, []byte, error) {
	var hdr [14]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if string(hdr[:6]) != "i3-ipc" {
		return 0, nil, errors.New("not an i3-ipc reply")
	}
	n := binary.NativeEndian.Uint32(hdr[6:])
	if n > 64<<20 {
		return 0, nil, errors.New("i3-ipc reply too large")
	}
	payload := make([]byte, n)
	_, err := io.ReadFull(r, payload)
	return binary.NativeEndian.Uint32(hdr[10:]), payload, err
}

// swayRun runs sway commands, failing on the first that fails.
func swayRun(sock string, cmds ...string) error {
	reply, err := swayCall(sock, swayRunCommand, strings.Join(cmds, "; "))
	if err != nil {
		return err
	}
	var results []struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(reply, &results); err != nil {
		return fmt.Errorf("sway: %w", err)
	}
	for _, r := range results {
		if !r.Success {
			return fmt.Errorf("sway: %s", r.Error)
		}
	}
	return nil
}

type swayJSONOutput struct {
	Name   string `json:"name"`
	Make   string `json:"make"`
	Active bool   `json:"active"`
	Rect   struct {
		X, Y, Width, Height int
	} `json:"rect"`
	CurrentMode *struct {
		Width, Height int
	} `json:"current_mode"`
	Scale     float64 `json:"scale"`
	Transform string  `json:"transform"`
}

func swayOutputs(sock string) ([]wlExtOutput, error) {
	reply, err := swayCall(sock, swayGetOutputs, "")
	if err != nil {
		return nil, err
	}
	var outs []swayJSONOutput
	if err := json.Unmarshal(reply, &outs); err != nil {
		return nil, fmt.Errorf("sway outputs: %w", err)
	}
	var res []wlExtOutput
	for _, o := range outs {
		if o.Name == "FALLBACK" {
			continue
		}
		e := wlExtOutput{
			Name:    o.Name,
			Active:  o.Active,
			Rect:    displayRect{X: o.Rect.X, Y: o.Rect.Y, W: o.Rect.Width, H: o.Rect.Height},
			Scale:   o.Scale,
			Virtual: strings.EqualFold(o.Make, "headless") || strings.HasPrefix(o.Name, "HEADLESS-"),
		}
		if o.CurrentMode != nil {
			e.ModeW, e.ModeH = o.CurrentMode.Width, o.CurrentMode.Height
		}
		for i, t := range swayTransforms {
			if t == o.Transform {
				e.Transform = i
			}
		}
		res = append(res, e)
	}
	return res, nil
}

// swayOrphanFile records the output the host made, so the next host
// process adopts it when this one dies before removing it.
func swayOrphanFile() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "drawsplay", "sway-output")
}

type swayBackend struct{ sock string }

func (swayBackend) Label() string  { return "sway" }
func (swayBackend) Arranges() bool { return true }
func (swayBackend) Anchored() bool { return true }

// LogicalSize: wlroots truncates.
func (swayBackend) LogicalSize(w, h int, scale float64) (int, int) {
	return int(float64(w) / scale), int(float64(h) / scale)
}

func (b swayBackend) Create(modeW, modeH int) (wlExtHandle, error) {
	before, err := swayOutputs(b.sock)
	if err != nil {
		return nil, err
	}
	name := ""
	if data, err := os.ReadFile(swayOrphanFile()); err == nil {
		if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) == 2 && lines[0] == b.sock {
			for _, o := range before {
				if o.Name == lines[1] && o.Virtual {
					name = o.Name
				}
			}
		}
	}
	if name == "" {
		if err := swayRun(b.sock, "create_output"); err != nil {
			return nil, err
		}
		after, err := swayOutputs(b.sock)
		if err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, o := range before {
			known[o.Name] = true
		}
		for _, o := range after {
			if !known[o.Name] && o.Virtual {
				name = o.Name
			}
		}
		if name == "" {
			return nil, errors.New("sway did not create a headless output")
		}
	}
	if f := swayOrphanFile(); os.MkdirAll(filepath.Dir(f), 0o700) == nil {
		_ = os.WriteFile(f, []byte(b.sock+"\n"+name+"\n"), 0o600)
	}
	hd := &swayOutput{sock: b.sock, name: name, changed: make(chan struct{}, 1), closed: make(chan struct{})}
	hd.subscribe()
	return hd, nil
}

type swayOutput struct {
	sock, name string
	changed    chan struct{}
	closed     chan struct{}

	mu      sync.Mutex
	reloads uint64
	events  net.Conn
}

// subscribe follows sway's output and workspace events; a workspace
// "reload" event is a configuration reload.
func (s *swayOutput) subscribe() {
	conn, err := net.DialTimeout("unix", s.sock, swayIPCTimeout)
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(swayIPCTimeout))
	if err := swayWrite(conn, swaySubscribe, `["output","workspace"]`); err != nil {
		conn.Close()
		return
	}
	if _, _, err := swayRead(conn); err != nil {
		conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	s.mu.Lock()
	s.events = conn
	s.mu.Unlock()
	go func() {
		defer conn.Close()
		for {
			typ, payload, err := swayRead(conn)
			if err != nil {
				return
			}
			if typ&swayEventBit == 0 {
				continue
			}
			if typ == swayEventBit {
				var ev struct {
					Change string `json:"change"`
				}
				if json.Unmarshal(payload, &ev) != nil || ev.Change != "reload" {
					continue
				}
				s.mu.Lock()
				s.reloads++
				s.mu.Unlock()
			}
			select {
			case s.changed <- struct{}{}:
			default:
			}
		}
	}()
}

func (s *swayOutput) Name() string             { return s.name }
func (s *swayOutput) Changed() <-chan struct{} { return s.changed }
func (s *swayOutput) Closed() <-chan struct{}  { return s.closed }
func (s *swayOutput) Target() captureTarget {
	return captureTarget{Kind: captureScreencopy, OutputName: s.name}
}
func (s *swayOutput) MapTablet() error {
	noteTouchMapping("sway", s.name, swayRun(s.sock, "input "+swayTouchIdentifier+" map_to_output "+s.name))
	return swayRun(s.sock, "input "+swayPenIdentifier+" map_to_output "+s.name)
}
func (s *swayOutput) State() (wlExtState, error) {
	outs, err := swayOutputs(s.sock)
	s.mu.Lock()
	defer s.mu.Unlock()
	return wlExtState{Outputs: outs, Reloads: s.reloads}, err
}

func (s *swayOutput) Configure(c wlExtConfig) error {
	cmds := []string{fmt.Sprintf("output %s enable mode %dx%d@60Hz pos %d %d transform %s scale %g",
		s.name, c.ModeW, c.ModeH, c.X, c.Y, swayTransforms[c.Transform&7], c.Scale)}
	for _, m := range c.Moves {
		cmds = append(cmds, fmt.Sprintf("output %s pos %d %d", m.Name, m.X, m.Y))
	}
	return swayRun(s.sock, cmds...)
}

func (s *swayOutput) Remove(moves []x11OutputPos, fallback string) error {
	s.mu.Lock()
	if s.events != nil {
		s.events.Close()
	}
	s.mu.Unlock()
	var cmds []string
	for _, m := range moves {
		cmds = append(cmds, fmt.Sprintf("output %s pos %d %d", m.Name, m.X, m.Y))
	}
	if fallback != "" {
		cmds = append(cmds, "output "+fallback+" enable")
	}
	// unplug needs sway 1.9; older ones can only switch the output off,
	// and the next host process adopts it.
	err := swayRun(s.sock, "output "+s.name+" unplug")
	if err != nil {
		err = swayRun(s.sock, "output "+s.name+" disable")
	} else {
		_ = os.Remove(swayOrphanFile())
	}
	if len(cmds) > 0 {
		if merr := swayRun(s.sock, cmds...); err == nil {
			err = merr
		}
	}
	return err
}
