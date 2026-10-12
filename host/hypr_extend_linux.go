//go:build linux

package main

// Hyprland extended display (see wl_extend_linux.go): a headless output made
// with `hyprctl output create`, set up with monitor rules over Hyprland's
// request socket, and recorded with wlr-screencopy. Hyprland 0.55 and later
// may run a Lua configuration, which takes rules through `eval` rather than
// `keyword`.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	hyprOutputName = "Drawsplay"
	hyprIPCTimeout = 3 * time.Second
)

var hyprPenPattern = regexp.MustCompile(`^drawsplay-pen(-\d+)?$`)

// hyprSocketDir is the running Hyprland instance's socket directory.
func hyprSocketDir() (string, error) {
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if sig == "" {
		return "", errors.New("HYPRLAND_INSTANCE_SIGNATURE is not set")
	}
	for _, base := range []string{filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "hypr"), "/tmp/hypr"} {
		dir := filepath.Join(base, sig)
		if _, err := os.Stat(filepath.Join(dir, ".socket.sock")); err == nil {
			return dir, nil
		}
	}
	return "", errors.New("Hyprland's request socket was not found")
}

// hyprCall sends one request and returns the reply.
func hyprCall(dir, request string) (string, error) {
	conn, err := net.DialTimeout("unix", filepath.Join(dir, ".socket.sock"), hyprIPCTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hyprIPCTimeout))
	if _, err := conn.Write([]byte(request)); err != nil {
		return "", err
	}
	reply, err := io.ReadAll(io.LimitReader(conn, 16<<20))
	return string(reply), err
}

func hyprOK(reply string, err error) error {
	if err != nil {
		return err
	}
	if r := strings.TrimSpace(reply); r != "ok" {
		return fmt.Errorf("Hyprland: %s", r)
	}
	return nil
}

type hyprJSONMonitor struct {
	Name      string  `json:"name"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	Scale     float64 `json:"scale"`
	Transform int     `json:"transform"`
	Disabled  bool    `json:"disabled"`
	MirrorOf  string  `json:"mirrorOf"`
	Make      string  `json:"make"`
	Model     string  `json:"model"`
}

func hyprMonitors(dir string) ([]wlExtOutput, error) {
	reply, err := hyprCall(dir, "j/monitors all")
	if err != nil {
		return nil, err
	}
	var mons []hyprJSONMonitor
	if err := json.Unmarshal([]byte(reply), &mons); err != nil {
		return nil, fmt.Errorf("Hyprland monitors: %w", err)
	}
	var res []wlExtOutput
	for _, m := range mons {
		if m.Name == "FALLBACK" {
			continue
		}
		o := wlExtOutput{
			Name:      m.Name,
			Active:    !m.Disabled,
			ModeW:     m.Width,
			ModeH:     m.Height,
			Transform: m.Transform,
			Scale:     m.Scale,
			Virtual:   m.Name == hyprOutputName || strings.HasPrefix(m.Name, "HEADLESS-") || strings.EqualFold(m.Make, "headless"),
		}
		if m.MirrorOf != "" && m.MirrorOf != "none" {
			o.MirrorOf = m.MirrorOf
		}
		if o.Active {
			lw, lh := hyprBackend{}.LogicalSize(m.Width, m.Height, m.Scale)
			if wlTransformSwapsAxes(m.Transform) {
				lw, lh = hyprBackend{}.LogicalSize(m.Height, m.Width, m.Scale)
			}
			o.Rect = displayRect{X: m.X, Y: m.Y, W: lw, H: lh}
		}
		res = append(res, o)
	}
	return res, nil
}

type hyprBackend struct{ dir string }

func (hyprBackend) Label() string  { return "Hyprland" }
func (hyprBackend) Arranges() bool { return true }

// Anchored: Hyprland allows negative positions, so the host leaves the
// other monitors' rules alone.
func (hyprBackend) Anchored() bool { return false }

func (hyprBackend) LogicalSize(w, h int, scale float64) (int, int) {
	if scale <= 0 {
		scale = 1
	}
	return int(float64(w)/scale + 0.5), int(float64(h)/scale + 0.5)
}

func (b hyprBackend) Create(modeW, modeH int) (wlExtHandle, error) {
	reply, err := hyprCall(b.dir, "output create headless "+hyprOutputName)
	if err != nil {
		return nil, err
	}
	// "Name already taken": an output a previous host process made.
	if r := strings.TrimSpace(reply); r != "ok" && r != "Name already taken" {
		return nil, fmt.Errorf("Hyprland: %s", r)
	}
	hd := &hyprOutput{dir: b.dir, changed: make(chan struct{}, 1), closed: make(chan struct{})}
	for attempt := 0; ; attempt++ {
		outs, err := hyprMonitors(b.dir)
		if err != nil {
			return nil, err
		}
		if _, ok := (wlExtState{Outputs: outs}).output(hyprOutputName); ok {
			break
		}
		if attempt >= 20 {
			return nil, errors.New("Hyprland did not create the headless output")
		}
		time.Sleep(100 * time.Millisecond)
	}
	hd.subscribe()
	return hd, nil
}

type hyprOutput struct {
	dir     string
	changed chan struct{}
	closed  chan struct{}

	mu      sync.Mutex
	reloads uint64
	lua     bool // the configuration is Lua: rules go through eval
	events  net.Conn
}

// subscribe follows Hyprland's events: monitors coming and going, and
// configuration reloads.
func (s *hyprOutput) subscribe() {
	conn, err := net.DialTimeout("unix", filepath.Join(s.dir, ".socket2.sock"), hyprIPCTimeout)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.events = conn
	s.mu.Unlock()
	go func() {
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			event, _, _ := strings.Cut(sc.Text(), ">>")
			switch event {
			case "configreloaded":
				s.mu.Lock()
				s.reloads++
				s.mu.Unlock()
			case "monitoradded", "monitoraddedv2", "monitorremoved", "monitorremovedv2":
			default:
				continue
			}
			select {
			case s.changed <- struct{}{}:
			default:
			}
		}
	}()
}

func (s *hyprOutput) Name() string             { return hyprOutputName }
func (s *hyprOutput) Changed() <-chan struct{} { return s.changed }
func (s *hyprOutput) Closed() <-chan struct{}  { return s.closed }
func (s *hyprOutput) Target() captureTarget {
	return captureTarget{Kind: captureScreencopy, OutputName: hyprOutputName}
}

func (s *hyprOutput) State() (wlExtState, error) {
	outs, err := hyprMonitors(s.dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	return wlExtState{Outputs: outs, Reloads: s.reloads}, err
}

// rule applies a configuration rule: legacy is the `keyword` form, lua the
// `eval` form for Lua configurations.
func (s *hyprOutput) rule(legacy, lua string) error {
	s.mu.Lock()
	useLua := s.lua
	s.mu.Unlock()
	if !useLua {
		reply, err := hyprCall(s.dir, "keyword "+legacy)
		if err != nil || !strings.Contains(reply, "eval") {
			return hyprOK(reply, err)
		}
		s.mu.Lock()
		s.lua = true
		s.mu.Unlock()
	}
	return hyprOK(hyprCall(s.dir, "eval "+lua))
}

func (s *hyprOutput) Configure(c wlExtConfig) error {
	mode := fmt.Sprintf("%dx%d@60", c.ModeW, c.ModeH)
	pos := fmt.Sprintf("%dx%d", c.X, c.Y)
	scale := strconv.FormatFloat(c.Scale, 'f', -1, 64)
	return s.rule(
		fmt.Sprintf("monitor %s,%s,%s,%s,transform,%d", hyprOutputName, mode, pos, scale, c.Transform),
		fmt.Sprintf(`hl.monitor({ output = %q, mode = %q, position = %q, scale = %q, transform = %d })`, hyprOutputName, mode, pos, scale, c.Transform))
}

// MapTablet points the Drawsplay Pen's device rule at the output.
func (s *hyprOutput) MapTablet() error {
	pen := "drawsplay-pen"
	if reply, err := hyprCall(s.dir, "j/devices"); err == nil {
		var devs struct {
			Tablets []struct {
				Name string `json:"name"`
			} `json:"tablets"`
		}
		if json.Unmarshal([]byte(reply), &devs) == nil {
			for _, t := range devs.Tablets {
				if hyprPenPattern.MatchString(t.Name) {
					pen = t.Name
					break
				}
			}
		}
	}
	return s.rule(
		fmt.Sprintf("device[%s]:output %s", pen, hyprOutputName),
		fmt.Sprintf(`hl.device({ name = %q, output = %q })`, pen, hyprOutputName))
}

func (s *hyprOutput) Remove(moves []x11OutputPos, fallback string) error {
	s.mu.Lock()
	if s.events != nil {
		s.events.Close()
	}
	s.mu.Unlock()
	err := hyprOK(hyprCall(s.dir, "output remove "+hyprOutputName))
	if fallback != "" {
		if ferr := s.rule(
			fmt.Sprintf("monitor %s,preferred,auto,auto", fallback),
			fmt.Sprintf(`hl.monitor({ output = %q, mode = "preferred", position = "auto", scale = "auto" })`, fallback)); err == nil {
			err = ferr
		}
	}
	return err
}
