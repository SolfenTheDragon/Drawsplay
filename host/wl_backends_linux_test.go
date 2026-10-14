//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortTempDir keeps unix socket paths under the 108-byte limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func serveUnix(t *testing.T, path string, handle func(net.Conn)) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
}

type fakeSway struct {
	mu      sync.Mutex
	outputs []map[string]any
	cmds    []string
	subs    []net.Conn
}

func (f *fakeSway) serve(c net.Conn) {
	for {
		typ, payload, err := swayRead(c)
		if err != nil {
			c.Close()
			return
		}
		f.mu.Lock()
		var reply any
		switch typ {
		case swayGetOutputs:
			reply = f.outputs
		case swaySubscribe:
			f.subs = append(f.subs, c)
			reply = map[string]bool{"success": true}
		case swayRunCommand:
			var results []map[string]any
			for _, cmd := range strings.Split(string(payload), "; ") {
				f.cmds = append(f.cmds, cmd)
				ok := !strings.HasSuffix(cmd, " unplug") || len(f.outputs) > 0
				if cmd == "create_output" {
					f.outputs = append(f.outputs, map[string]any{"name": "HEADLESS-1", "make": "headless", "active": true,
						"rect": map[string]int{"x": 1920, "width": 1920, "height": 1080}, "current_mode": map[string]int{"width": 1920, "height": 1080}, "scale": 1.0, "transform": "normal"})
				}
				results = append(results, map[string]any{"success": ok})
			}
			reply = results
		}
		f.mu.Unlock()
		data, _ := json.Marshal(reply)
		if err := swayWrite(c, typ, string(data)); err != nil {
			c.Close()
			return
		}
	}
}

func (f *fakeSway) takeCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmds := f.cmds
	f.cmds = nil
	return cmds
}

func TestSwayBackendSpeaksIPC(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	sock := filepath.Join(dir, "sway.sock")
	f := &fakeSway{outputs: []map[string]any{
		{"name": "eDP-1", "make": "BOE", "active": true, "rect": map[string]int{"width": 1920, "height": 1080},
			"current_mode": map[string]int{"width": 1920, "height": 1080}, "scale": 1.0, "transform": "normal"},
		{"name": "FALLBACK", "active": false},
	}}
	serveUnix(t, sock, f.serve)
	t.Setenv("SWAYSOCK", sock)
	b, ok := detectWlExtBackend().(swayBackend)
	if !ok {
		t.Fatalf("SWAYSOCK did not select sway")
	}
	hd, err := b.Create(2960, 1848)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if hd.Name() != "HEADLESS-1" {
		t.Fatalf("output %q", hd.Name())
	}
	if data, _ := os.ReadFile(swayOrphanFile()); string(data) != sock+"\nHEADLESS-1\n" {
		t.Fatalf("orphan file = %q", data)
	}
	f.takeCmds()
	err = hd.Configure(wlExtConfig{ModeW: 1848, ModeH: 2960, X: 0, Y: 0, Transform: wlOutputTransform90, Scale: 1.5,
		Moves: []x11OutputPos{{Name: "eDP-1", X: 1973, Y: 0}}})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	want := []string{"output HEADLESS-1 enable mode 1848x2960@60Hz pos 0 0 transform 90 scale 1.5", "output eDP-1 pos 1973 0"}
	if got := f.takeCmds(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	st, err := hd.State()
	if err != nil || len(st.Outputs) != 2 || st.Outputs[0].Virtual || !st.Outputs[1].Virtual || st.Outputs[1].ModeW != 1920 {
		t.Fatalf("state = %+v, %v", st, err)
	}

	// A configuration reload resets the output's rules.
	f.mu.Lock()
	sub := f.subs[0]
	f.mu.Unlock()
	if err := swayWrite(sub, swayEventBit, `{"change":"reload"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hd.Changed():
	case <-time.After(5 * time.Second):
		t.Fatal("no change heard")
	}
	if st, _ := hd.State(); st.Reloads != 1 {
		t.Fatalf("reloads = %d", st.Reloads)
	}

	if err := hd.MapTablet(); err != nil {
		t.Fatal(err)
	}
	if err := hd.Remove(nil, "eDP-1"); err != nil {
		t.Fatal(err)
	}
	want = []string{"input 7504:20550:Drawsplay_Touch map_to_output HEADLESS-1", "input 7504:20548:Drawsplay_Pen map_to_output HEADLESS-1", "output HEADLESS-1 unplug", "output eDP-1 enable"}
	if got := f.takeCmds(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if _, err := os.Stat(swayOrphanFile()); !os.IsNotExist(err) {
		t.Fatalf("orphan file left after unplug: %v", err)
	}

	// The next host process adopts an output a dead one left behind.
	if err := os.WriteFile(swayOrphanFile(), []byte(sock+"\nHEADLESS-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hd, err = b.Create(2960, 1848)
	if err != nil || hd.Name() != "HEADLESS-1" {
		t.Fatalf("adopt: %v", err)
	}
	if got := f.takeCmds(); len(got) != 0 {
		t.Fatalf("adopting ran %q", got)
	}
}

type fakeHypr struct {
	mu       sync.Mutex
	lua      bool
	monitors []hyprJSONMonitor
	reqs     []string
}

func (f *fakeHypr) serve(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 64<<10)
	n, err := c.Read(buf)
	if err != nil {
		return
	}
	req := string(buf[:n])
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := "ok"
	switch {
	case req == "j/monitors all":
		data, _ := json.Marshal(f.monitors)
		reply = string(data)
	case req == "j/devices":
		reply = `{"tablets":[{"name":"some-tablet"},{"name":"drawsplay-pen-1"}]}`
	case req == "output create headless Drawsplay":
		for _, m := range f.monitors {
			if m.Name == "Drawsplay" {
				reply = "Name already taken"
			}
		}
		if reply == "ok" {
			f.monitors = append(f.monitors, hyprJSONMonitor{Name: "Drawsplay", Width: 1920, Height: 1080, X: 1920, Scale: 1, Make: "headless", MirrorOf: "none"})
		}
	case strings.HasPrefix(req, "keyword ") && f.lua:
		reply = "keyword can't be used with a Lua config, use eval"
	}
	f.reqs = append(f.reqs, req)
	c.Write([]byte(reply))
}

func (f *fakeHypr) takeReqs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	reqs := f.reqs
	f.reqs = nil
	return reqs
}

func TestHyprBackendSpeaksIPC(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("SWAYSOCK", "")
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "sig")
	sockDir := filepath.Join(dir, "hypr", "sig")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeHypr{lua: true, monitors: []hyprJSONMonitor{
		{Name: "DP-1", Width: 2560, Height: 1440, Scale: 1.25, Transform: wlOutputTransform90, MirrorOf: "none"},
	}}
	serveUnix(t, filepath.Join(sockDir, ".socket.sock"), f.serve)
	events := make(chan net.Conn, 1)
	serveUnix(t, filepath.Join(sockDir, ".socket2.sock"), func(c net.Conn) { events <- c })
	b, ok := detectWlExtBackend().(hyprBackend)
	if !ok {
		t.Fatalf("HYPRLAND_INSTANCE_SIGNATURE did not select Hyprland")
	}
	hd, err := b.Create(2960, 1848)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	st, err := hd.State()
	if err != nil {
		t.Fatal(err)
	}
	if dp, _ := st.output("DP-1"); dp.Rect != (displayRect{W: 1152, H: 2048}) || dp.Virtual || dp.MirrorOf != "" {
		t.Fatalf("DP-1 = %+v", dp)
	}
	if o, _ := st.output("Drawsplay"); !o.Virtual || !o.Active {
		t.Fatalf("Drawsplay = %+v", o)
	}
	f.takeReqs()
	if err := hd.Configure(wlExtConfig{ModeW: 2960, ModeH: 1848, X: -2368, Y: 0, Scale: 1.25}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := hd.MapTablet(); err != nil {
		t.Fatalf("MapTablet: %v", err)
	}
	want := []string{
		"keyword monitor Drawsplay,2960x1848@60,-2368x0,1.25,transform,0",
		`eval hl.monitor({ output = "Drawsplay", mode = "2960x1848@60", position = "-2368x0", scale = "1.25", transform = 0 })`,
		"j/devices",
		`eval hl.device({ name = "drawsplay-touch", output = "Drawsplay" })`,
		`eval hl.device({ name = "drawsplay-pen-1", output = "Drawsplay" })`,
	}
	if got := f.takeReqs(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("requests = %q, want %q", got, want)
	}

	ev := <-events
	if _, err := ev.Write([]byte("workspace>>1\nconfigreloaded>>\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hd.Changed():
	case <-time.After(5 * time.Second):
		t.Fatal("no change heard")
	}
	waitUntil(t, "the reload", func() bool { st, _ := hd.State(); return st.Reloads == 1 })

	f.takeReqs()
	if err := hd.Remove(nil, "DP-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	want = []string{"output remove Drawsplay", `eval hl.monitor({ output = "DP-1", mode = "preferred", position = "auto", scale = "auto" })`}
	if got := f.takeReqs(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("requests = %q, want %q", got, want)
	}
}

func TestKWinDesktopEntryQuotesExec(t *testing.T) {
	got := kwinDesktopEntry(`/opt/my "apps"/$HOME\drawsplay-host`)
	want := `Exec="/opt/my \\"apps\\"/\\$HOME\\\\drawsplay-host"` + "\n"
	if !strings.Contains(got, want) || !strings.Contains(got, "X-KDE-Wayland-Interfaces=zkde_screencast_unstable_v1\n") {
		t.Fatalf("entry = %q", got)
	}
}

func TestDetectWlExtBackendNeedsALiveCompositor(t *testing.T) {
	t.Setenv("SWAYSOCK", "/nonexistent/sway.sock")
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	if b := detectWlExtBackend(); b != nil {
		t.Fatalf("detected %s", b.Label())
	}
	t.Setenv("XDG_CURRENT_DESKTOP", "KDE")
	if b := detectWlExtBackend(); b == nil || !wlExtNeedsPipeWire(b) {
		t.Fatalf("KDE detected as %v", b)
	}
}
