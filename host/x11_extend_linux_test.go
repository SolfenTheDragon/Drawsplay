//go:build linux

package main

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- xrandr output parsing ----------------------------------------------

// A laptop panel with the vkms output switched on to its right. The last
// lines are how xrandr lists a mode that no output uses.
const xrandrLaptopWithVKMS = `Screen 0: minimum 320 x 200, current 3840 x 1080, maximum 16384 x 16384
eDP-1 connected primary 1920x1080+0+0 (normal left inverted right x axis y axis) 344mm x 194mm
   1920x1080     60.00*+  60.00    48.00
   1280x720      60.00
HDMI-1 disconnected (normal left inverted right x axis y axis)
Virtual-1-1 connected 1920x1080+1920+0 (normal left inverted right x axis y axis) 0mm x 0mm
   1024x768      60.00 +
   1920x1080     60.00*
   1920x1080i    60.00
   2960x1848_drawsplay  59.96
DP-2 unknown connection (normal left inverted right x axis y axis)
  3000x2000_drawsplay (0x4f) 509.750MHz +HSync -VSync
        h: width  3000 start 3048 end 3080 total 3160 skew    0 clock 161.31KHz
        v: height 2000 start 2003 end 2013 total 2056           clock  78.46Hz
Screen 1: minimum 320 x 200, current 1024 x 768, maximum 8192 x 8192
VGA-1 connected primary 1024x768+0+0 (normal left inverted right x axis y axis) 0mm x 0mm
`

func TestParseXrandrQuery(t *testing.T) {
	st, err := parseXrandrQuery(xrandrLaptopWithVKMS)
	if err != nil {
		t.Fatalf("parseXrandrQuery: %v", err)
	}
	want := xrandrState{
		ScreenW: 3840, ScreenH: 1080, MaxW: 16384, MaxH: 16384,
		Outputs: []xrandrOutput{
			{
				Name: "eDP-1", Connected: true, Primary: true, Active: true,
				Rect: displayRect{X: 0, Y: 0, W: 1920, H: 1080},
				Modes: []xrandrMode{
					{Name: "1920x1080", W: 1920, H: 1080, Current: true, Preferred: true},
					{Name: "1280x720", W: 1280, H: 720},
				},
			},
			{Name: "HDMI-1"},
			{
				Name: "Virtual-1-1", Connected: true, Active: true,
				Rect: displayRect{X: 1920, Y: 0, W: 1920, H: 1080},
				Modes: []xrandrMode{
					{Name: "1024x768", W: 1024, H: 768, Preferred: true},
					{Name: "1920x1080", W: 1920, H: 1080, Current: true},
					{Name: "1920x1080i", W: 1920, H: 1080, Interlaced: true},
					{Name: "2960x1848_drawsplay", W: 2960, H: 1848},
				},
			},
			{Name: "DP-2"},
		},
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("parseXrandrQuery =\n%+v\nwant\n%+v", st, want)
	}

	if _, err := parseXrandrQuery("Can't open display :9\n"); err == nil {
		t.Fatal("parsed an xrandr error message")
	}
}

func TestFindX11ModeSkipsInterlacedModes(t *testing.T) {
	o := xrandrOutput{Modes: []xrandrMode{
		{Name: "1920x1080i", W: 1920, H: 1080, Interlaced: true},
		{Name: "1920x1080", W: 1920, H: 1080},
	}}
	if name, ok := findX11Mode(o, 1920, 1080); !ok || name != "1920x1080" {
		t.Fatalf("findX11Mode = %q %v, want the progressive mode", name, ok)
	}
	if _, ok := findX11Mode(o, 2960, 1848); ok {
		t.Fatal("found a mode the output does not have")
	}
}

// --- output choice --------------------------------------------------------

func TestPickX11VirtualOutput(t *testing.T) {
	panel := xrandrOutput{Name: "eDP-1", Connected: true, Primary: true, Active: true, Rect: displayRect{W: 1920, H: 1080}}
	cases := []struct {
		name    string
		outputs []xrandrOutput
		env     string
		want    string
		err     string
	}{
		{
			name:    "vkms output",
			outputs: []xrandrOutput{panel, {Name: "HDMI-1"}, {Name: "Virtual-1-1", Connected: true}},
			want:    "Virtual-1-1",
		},
		{
			name:    "intel VIRTUAL head reports disconnected",
			outputs: []xrandrOutput{panel, {Name: "VIRTUAL1"}},
			want:    "VIRTUAL1",
		},
		{
			name:    "disconnected vkms output",
			outputs: []xrandrOutput{panel, {Name: "Virtual-1-1"}},
			err:     errNoX11VirtualOutput.Error(),
		},
		{
			name: "a VM's own displays are not virtual outputs",
			outputs: []xrandrOutput{
				{Name: "Virtual-1", Connected: true, Primary: true, Active: true, Rect: displayRect{W: 1920, H: 1080}},
				{Name: "Virtual-2"},
			},
			err: errNoX11VirtualOutput.Error(),
		},
		{
			name:    "no virtual output",
			outputs: []xrandrOutput{panel, {Name: "HDMI-1"}},
			err:     errNoX11VirtualOutput.Error(),
		},
		{
			name:    "configured output",
			outputs: []xrandrOutput{panel, {Name: "DVI-I-1-1"}, {Name: "Virtual-1-1", Connected: true}},
			env:     "DVI-I-1-1",
			want:    "DVI-I-1-1",
		},
		{
			name:    "configured output missing",
			outputs: []xrandrOutput{panel},
			env:     "DVI-I-1-1",
			err:     "RandR output DVI-I-1-1 not found",
		},
		{
			name:    "configured output is the primary display",
			outputs: []xrandrOutput{panel, {Name: "Virtual-1-1", Connected: true}},
			env:     "eDP-1",
			err:     "RandR output eDP-1 is the primary display",
		},
		{
			name: "vkms output made the primary display",
			outputs: []xrandrOutput{
				{Name: "eDP-1", Connected: true, Active: true, Rect: displayRect{W: 1920, H: 1080}},
				{Name: "Virtual-1-1", Connected: true, Primary: true, Active: true, Rect: displayRect{X: 1920, W: 1920, H: 1080}},
			},
			err: "RandR output Virtual-1-1 is the primary display",
		},
		{
			name: "nothing else stays on",
			outputs: []xrandrOutput{
				{Name: "eDP-1", Connected: true, Primary: true},
				{Name: "Virtual-1-1", Connected: true, Active: true, Rect: displayRect{W: 1920, H: 1080}},
			},
			err: "RandR output Virtual-1-1 is the only active display",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickX11VirtualOutput(xrandrState{Outputs: tc.outputs}, tc.env)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("pickX11VirtualOutput = %q, %v; want error %q", got.Name, err, tc.err)
				}
				return
			}
			if err != nil || got.Name != tc.want {
				t.Fatalf("pickX11VirtualOutput = %q, %v; want %q", got.Name, err, tc.want)
			}
		})
	}
}

// --- mode timings ---------------------------------------------------------

func TestCVTReducedBlankingMatchesCvt(t *testing.T) {
	// `cvt -r W H` modelines.
	cases := map[[2]int]string{
		{2960, 1848}: "355.75 2960 3008 3040 3120 1848 1851 1861 1901 +hsync -vsync",
		{1920, 1080}: "138.50 1920 1968 2000 2080 1080 1083 1088 1111 +hsync -vsync",
		{1920, 1200}: "154.00 1920 1968 2000 2080 1200 1203 1209 1235 +hsync -vsync",
		{2560, 1600}: "268.50 2560 2608 2640 2720 1600 1603 1609 1646 +hsync -vsync",
		{1480, 924}:  "93.50 1480 1528 1560 1640 924 927 937 951 +hsync -vsync",
		{2736, 1824}: "325.75 2736 2784 2816 2896 1824 1827 1837 1876 +hsync -vsync",
		{1280, 1024}: "90.75 1280 1328 1360 1440 1024 1027 1034 1054 +hsync -vsync",
		{3840, 2160}: "533.00 3840 3888 3920 4000 2160 2163 2168 2222 +hsync -vsync",
		{2880, 1800}: "337.50 2880 2928 2960 3040 1800 1803 1809 1852 +hsync -vsync",
		{1600, 1200}: "130.25 1600 1648 1680 1760 1200 1203 1207 1235 +hsync -vsync",
	}
	for size, want := range cases {
		if got := strings.Join(cvtReducedBlanking(size[0], size[1]).xrandrArgs(), " "); got != want {
			t.Errorf("cvtReducedBlanking(%dx%d) = %q, want %q", size[0], size[1], got, want)
		}
	}
}

func TestCVTReducedBlankingKeepsWidthsThatAreNotMultiplesOf8(t *testing.T) {
	// cvt would round 1366 down to 1360; the tablet needs its exact width.
	tm := cvtReducedBlanking(1366, 768)
	if tm.HDisplay != 1366 || tm.HTotal != 1366+160 || tm.VDisplay != 768 {
		t.Fatalf("cvtReducedBlanking(1366x768) = %+v", tm)
	}
	if hz := float64(tm.ClockKHz) * 1000 / float64(tm.HTotal*tm.VTotal); hz < 59 || hz > 61 {
		t.Fatalf("cvtReducedBlanking(1366x768) refreshes at %.2f Hz", hz)
	}
}

// --- layout -------------------------------------------------------------

func TestPlanX11Layout(t *testing.T) {
	panel := xrandrOutput{Name: "eDP-1", Primary: true, Active: true, Rect: displayRect{W: 1920, H: 1080}}
	off := xrandrOutput{Name: "Virtual-1-1", Connected: true}
	on := func(r displayRect) xrandrOutput {
		return xrandrOutput{Name: "Virtual-1-1", Connected: true, Active: true, Rect: r}
	}
	cases := []struct {
		name       string
		outputs    []xrandrOutput
		maxW       int
		w, h       int
		remembered *x11Placement
		want       x11Layout
		err        string
	}{
		{
			name:    "right of the panel by default",
			outputs: []xrandrOutput{panel, off},
			w:       2960, h: 1848,
			want: x11Layout{Rect: displayRect{X: 1920, Y: 0, W: 2960, H: 1848}},
		},
		{
			name: "right of every other display",
			outputs: []xrandrOutput{panel, {Name: "HDMI-1", Active: true, Rect: displayRect{X: 1920, Y: -200, W: 2560, H: 1440}},
				off},
			w: 1920, h: 1080,
			want: x11Layout{
				Rect:  displayRect{X: 4480, Y: 0, W: 1920, H: 1080},
				Moves: []x11OutputPos{{Name: "eDP-1", X: 0, Y: 200}, {Name: "HDMI-1", X: 1920, Y: 0}},
			},
		},
		{
			name:    "already showing the size",
			outputs: []xrandrOutput{{Name: "eDP-1", Primary: true, Active: true, Rect: displayRect{X: 1920, W: 1920, H: 1080}}, on(displayRect{X: 0, Y: 0, W: 1280, H: 800})},
			w:       1280, h: 800,
			want: x11Layout{Keep: true, Rect: displayRect{X: 0, Y: 0, W: 1280, H: 800}},
		},
		{
			name:    "resized on the side it is on",
			outputs: []xrandrOutput{panel, on(displayRect{X: 1920, Y: 200, W: 1024, H: 768})},
			w:       2960, h: 1848,
			want: x11Layout{Rect: displayRect{X: 1920, Y: 200, W: 2960, H: 1848}},
		},
		{
			name:    "resized on the left",
			outputs: []xrandrOutput{{Name: "eDP-1", Primary: true, Active: true, Rect: displayRect{X: 1024, W: 1920, H: 1080}}, on(displayRect{X: 0, Y: 0, W: 1024, H: 768})},
			w:       1920, h: 1200,
			want: x11Layout{
				Rect:  displayRect{X: 0, Y: 0, W: 1920, H: 1200},
				Moves: []x11OutputPos{{Name: "eDP-1", X: 1920, Y: 0}},
			},
		},
		{
			name:    "back on the left",
			outputs: []xrandrOutput{panel, off},
			w:       1920, h: 1080,
			remembered: &x11Placement{Side: "left"},
			want: x11Layout{
				Rect:  displayRect{X: 0, Y: 0, W: 1920, H: 1080},
				Moves: []x11OutputPos{{Name: "eDP-1", X: 1920, Y: 0}},
			},
		},
		{
			name:    "back above, shifted along the edge",
			outputs: []xrandrOutput{panel, off},
			w:       1280, h: 800,
			remembered: &x11Placement{Side: "above", Offset: 100},
			want: x11Layout{
				Rect:  displayRect{X: 100, Y: 0, W: 1280, H: 800},
				Moves: []x11OutputPos{{Name: "eDP-1", X: 0, Y: 800}},
			},
		},
		{
			name:    "an offset past the edge still touches the displays",
			outputs: []xrandrOutput{panel, off},
			w:       1280, h: 800,
			remembered: &x11Placement{Side: "below", Offset: 5000},
			want:       x11Layout{Rect: displayRect{X: 1919, Y: 1080, W: 1280, H: 800}},
		},
		{
			name:    "mirroring another display at the size is left alone",
			outputs: []xrandrOutput{panel, on(displayRect{X: 0, Y: 0, W: 1920, H: 1080})},
			w:       1920, h: 1080,
			want: x11Layout{Keep: true, Rect: displayRect{X: 0, Y: 0, W: 1920, H: 1080}},
		},
		{
			name:    "mirroring another display at another size goes beside it",
			outputs: []xrandrOutput{panel, on(displayRect{X: 0, Y: 0, W: 1920, H: 1080})},
			w:       2960, h: 1848,
			want: x11Layout{Rect: displayRect{X: 1920, Y: 0, W: 2960, H: 1848}},
		},
		{
			name:    "mirroring another display at another size returns to where it was",
			outputs: []xrandrOutput{panel, on(displayRect{X: 0, Y: 0, W: 1920, H: 1080})},
			w:       2960, h: 1848,
			remembered: &x11Placement{Side: "left"},
			want: x11Layout{
				Rect:  displayRect{X: 0, Y: 0, W: 2960, H: 1848},
				Moves: []x11OutputPos{{Name: "eDP-1", X: 2960, Y: 0}},
			},
		},
		{
			name:    "larger than the X screen allows",
			outputs: []xrandrOutput{panel, off},
			maxW:    4096,
			w:       2960, h: 1848,
			err: "a 2960x1848 display does not fit in the X screen's 4096x0 maximum",
		},
		{
			name:    "no display to extend",
			outputs: []xrandrOutput{{Name: "eDP-1", Primary: true}, off},
			w:       1920, h: 1080,
			err: "no other active display to extend",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := xrandrState{MaxW: tc.maxW, Outputs: tc.outputs}
			got, err := planX11Layout(st, "Virtual-1-1", tc.w, tc.h, tc.remembered)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("planX11Layout = %+v, %v; want error %q", got, err, tc.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("planX11Layout = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestX11ExtendedTarget(t *testing.T) {
	panel := xrandrOutput{Name: "eDP-1", Active: true, Rect: displayRect{W: 1920, H: 1080}}
	st := xrandrState{ScreenW: 3287, ScreenH: 1080}
	beside := xrandrOutput{Name: "Virtual-1-1", Active: true, Rect: displayRect{X: 1920, Y: 0, W: 1367, H: 769}}
	st.Outputs = []xrandrOutput{panel, beside}
	got := x11ExtendedTarget(st, beside)
	want := captureTarget{
		Kind: captureX11Grab, Width: 1366, Height: 768,
		Rect:         displayRect{X: 1920, Y: 0, W: 1367, H: 769},
		DesktopWidth: 3287, DesktopHeight: 1080,
	}
	if got != want {
		t.Fatalf("x11ExtendedTarget = %+v, want %+v", got, want)
	}

	cloned := xrandrOutput{Name: "Virtual-1-1", Active: true, Rect: displayRect{W: 1920, H: 1080}}
	st = xrandrState{ScreenW: 1920, ScreenH: 1080, Outputs: []xrandrOutput{panel, cloned}}
	if got := x11ExtendedTarget(st, cloned); !got.Mirrored {
		t.Fatalf("x11ExtendedTarget = %+v, want Mirrored for an output cloning the panel", got)
	}
}

func TestPlacementOfRoundTripsThroughPlaceAt(t *testing.T) {
	b := displayRect{X: 0, Y: 0, W: 1920, H: 1080}
	for _, r := range []displayRect{
		{X: 1920, Y: 300, W: 1280, H: 800},
		{X: -1280, Y: -500, W: 1280, H: 800},
		{X: 640, Y: 1080, W: 1280, H: 800},
		{X: -100, Y: -800, W: 1280, H: 800},
	} {
		p, ok := placementOf(r, b)
		if !ok {
			t.Fatalf("placementOf(%+v) reported an overlap", r)
		}
		if got := placeAt(p, b, r.W, r.H); got != r {
			t.Errorf("placeAt(placementOf(%+v)) = %+v (%+v)", r, got, p)
		}
	}
	if _, ok := placementOf(displayRect{X: 1000, Y: 500, W: 1280, H: 800}, b); ok {
		t.Error("placementOf accepted an overlapping display")
	}
}

// --- fake X server ------------------------------------------------------

type fakeRandrOutput struct {
	name      string
	connected bool
	primary   bool
	modes     []string
	preferred string
	mode      string // "" when switched off
	x, y      int
}

// fakeXrandr models the X server's RandR state as xrandr shows and changes
// it.
type fakeXrandr struct {
	mu      sync.Mutex
	outputs []*fakeRandrOutput
	sizes   map[string][2]int // modes the server knows, by name
	maxW    int               // screen size limit; 16384 when zero
	calls   []string          // invocations other than queries
	queries int
	// failSwitchOffs makes that many of the next commands that switch an
	// output off fail.
	failSwitchOffs int
	// onAddMode runs after --addmode, like a window manager reacting to the
	// hotplug.
	onAddMode func(f *fakeXrandr, o *fakeRandrOutput)
	// afterApply runs before the query that follows the host's next layout
	// change, like a window manager overriding it.
	afterApply  func(f *fakeXrandr)
	beforeQuery func(f *fakeXrandr)
}

// newFakeLaptopWithVKMS is a laptop panel and the vkms output (switched off),
// with a subset of vkms' modes.
func newFakeLaptopWithVKMS() *fakeXrandr {
	f := &fakeXrandr{sizes: map[string][2]int{}}
	for _, name := range []string{"1920x1080", "1280x720", "1024x768", "2560x1600"} {
		var w, h int
		fmt.Sscanf(name, "%dx%d", &w, &h)
		f.sizes[name] = [2]int{w, h}
	}
	f.outputs = []*fakeRandrOutput{
		{name: "eDP-1", connected: true, primary: true, modes: []string{"1920x1080", "1280x720"}, preferred: "1920x1080", mode: "1920x1080"},
		{name: "HDMI-1"},
		{name: "Virtual-1-1", connected: true, modes: []string{"1024x768", "1920x1080", "2560x1600"}, preferred: "1024x768"},
	}
	return f
}

func (f *fakeXrandr) outputLocked(name string) *fakeRandrOutput {
	for _, o := range f.outputs {
		if o.name == name {
			return o
		}
	}
	return nil
}

func (f *fakeXrandr) update(fn func(f *fakeXrandr)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeXrandr) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeXrandr) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries
}

func (f *fakeXrandr) renderLocked() string {
	screenW, screenH := 0, 0
	for _, o := range f.outputs {
		if o.mode != "" {
			s := f.sizes[o.mode]
			screenW, screenH = max(screenW, o.x+s[0]), max(screenH, o.y+s[1])
		}
	}
	var b strings.Builder
	maxW := f.maxW
	if maxW == 0 {
		maxW = 16384
	}
	fmt.Fprintf(&b, "Screen 0: minimum 320 x 200, current %d x %d, maximum %d x 16384\n", screenW, screenH, maxW)
	for _, o := range f.outputs {
		state := "disconnected"
		if o.connected {
			state = "connected"
		}
		b.WriteString(o.name + " " + state)
		if o.primary {
			b.WriteString(" primary")
		}
		if o.mode != "" {
			s := f.sizes[o.mode]
			fmt.Fprintf(&b, " %dx%d+%d+%d", s[0], s[1], o.x, o.y)
		}
		b.WriteString(" (normal left inverted right x axis y axis)\n")
		for _, m := range o.modes {
			flags := " "
			if m == o.mode {
				flags = "*"
			}
			if m == o.preferred {
				flags += "+"
			}
			fmt.Fprintf(&b, "   %-12s  60.00%s\n", m, flags)
		}
	}
	return b.String()
}

func (f *fakeXrandr) run(display string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if display != ":0" {
		return "", fmt.Errorf("unexpected display %q", display)
	}
	if len(args) == 1 && args[0] == "--current" {
		f.queries++
		if hook := f.beforeQuery; hook != nil {
			f.beforeQuery = nil
			hook(f)
		}
		return f.renderLocked(), nil
	}
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "--newmode":
		if _, ok := f.sizes[args[1]]; ok {
			return "", errors.New("xrandr --newmode: exit status 1 (X Error of failed request: BadName (named color or font does not exist))")
		}
		w, _ := strconv.Atoi(args[3])
		h, _ := strconv.Atoi(args[7])
		f.sizes[args[1]] = [2]int{w, h}
		return "", nil
	case "--addmode":
		o := f.outputLocked(args[1])
		if _, ok := f.sizes[args[2]]; o == nil || !ok {
			return "", fmt.Errorf("xrandr --addmode: cannot add %s to %s", args[2], args[1])
		}
		o.modes = append(o.modes, args[2])
		if f.onAddMode != nil {
			f.onAddMode(f, o)
		}
		return "", nil
	case "--output":
		if f.failSwitchOffs > 0 && slices.Contains(args, "--off") {
			f.failSwitchOffs--
			return "", errors.New("xrandr: exit status 1 (Can't open display :0)")
		}
		var o *fakeRandrOutput
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--off":
				o.mode = ""
				continue
			case "--auto":
				o.mode = o.preferred
				if o.mode == "" && len(o.modes) > 0 {
					o.mode = o.modes[0]
				}
				continue
			case "--primary":
				for _, other := range f.outputs {
					other.primary = false
				}
				o.primary = true
				continue
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("xrandr: %s needs an argument", args[i])
			}
			switch args[i] {
			case "--output":
				i++
				if o = f.outputLocked(args[i]); o == nil {
					return "", fmt.Errorf("xrandr: output %s not found", args[i])
				}
			case "--mode":
				i++
				found := false
				for _, m := range o.modes {
					found = found || m == args[i]
				}
				if !found {
					return "", fmt.Errorf("xrandr: cannot find mode %s", args[i])
				}
				o.mode = args[i]
			case "--pos":
				i++
				if _, err := fmt.Sscanf(args[i], "%dx%d", &o.x, &o.y); err != nil {
					return "", err
				}
			default:
				return "", fmt.Errorf("xrandr: unexpected argument %q", args[i])
			}
		}
		if hook := f.afterApply; hook != nil {
			f.afterApply = nil
			f.beforeQuery = hook
		}
		return "", nil
	}
	return "", fmt.Errorf("xrandr: unexpected command %q", args)
}

type fakeX11Session struct {
	xrandr *fakeXrandr
	mapped []string // outputs the tablet was mapped to
}

// withFakeX11Ext points the X11 extended display at a fake X server, with a
// client whose native resolution is 2960x1848.
func withFakeX11Ext(t *testing.T, f *fakeXrandr) *fakeX11Session {
	t.Helper()
	s := &fakeX11Session{xrandr: f}
	prevRun, prevMap := xrandrRun, mapX11Tablet
	prevHotplug, prevPoll, prevRounds := x11HotplugWait, x11SettlePoll, x11SettleRounds
	prevWatch, prevGrace, prevRetry := x11WatchInterval, x11ReleaseGrace, x11SwitchOffRetry
	xrandrRun = f.run
	mapX11Tablet = func(output string) bool {
		s.mapped = append(s.mapped, output)
		return true
	}
	x11HotplugWait, x11SettlePoll, x11SettleRounds = 0, 0, 3
	x11WatchInterval, x11ReleaseGrace, x11SwitchOffRetry = time.Hour, time.Hour, time.Millisecond
	t.Setenv("DISPLAY", ":0")
	t.Setenv(x11OutputEnv, "")
	resetX11Ext()

	const sessionID = 1 << 60
	bindSessionClientIdentity(sessionID, clientIdentity{Platform: "android", NativeWidth: 2960, NativeHeight: 1848})
	t.Cleanup(func() {
		unbindSessionClientIdentity(sessionID)
		resetX11Ext()
		xrandrRun, mapX11Tablet = prevRun, prevMap
		x11HotplugWait, x11SettlePoll, x11SettleRounds = prevHotplug, prevPoll, prevRounds
		x11WatchInterval, x11ReleaseGrace, x11SwitchOffRetry = prevWatch, prevGrace, prevRetry
	})
	return s
}

func resetX11Ext() {
	x11Ext.mu.Lock()
	defer x11Ext.mu.Unlock()
	stopX11ReleaseTimerLocked()
	x11Ext.gen, x11Ext.claimed, x11Ext.exiting = 0, false, false
	x11Ext.enabled = map[string]x11EnabledOutput{}
	x11Ext.placement = map[string]*x11Placement{}
	x11Ext.released = map[string]*x11ReleasedOutput{}
}

func newTestX11Display(t *testing.T, width, height int) *x11ExtendedDisplay {
	t.Helper()
	d, err := newX11ExtendedDisplay(videoOptions{Width: width, Height: height}, ":0")
	if err != nil {
		t.Fatalf("newX11ExtendedDisplay: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func x11Enabled() []string {
	x11Ext.mu.Lock()
	defer x11Ext.mu.Unlock()
	var keys []string
	for key := range x11Ext.enabled {
		keys = append(keys, key)
	}
	return keys
}

const (
	newTabletMode = "--newmode 2960x1848_drawsplay 355.75 2960 3008 3040 3120 1848 1851 1861 1901 +hsync -vsync"
	addTabletMode = "--addmode Virtual-1-1 2960x1848_drawsplay"
)

// --- session lifecycle ----------------------------------------------------

func TestX11ExtendedDisplaySwitchesTheOutputOnNextToThePanel(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	d := newTestX11Display(t, 1920, 1080)

	want := []string{newTabletMode, addTabletMode, "--output Virtual-1-1 --mode 1920x1080 --pos 1920x0"}
	if got := s.xrandr.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("xrandr commands =\n%q\nwant\n%q", got, want)
	}
	wantTarget := captureTarget{
		Kind: captureX11Grab, Width: 1920, Height: 1080,
		Rect:         displayRect{X: 1920, Y: 0, W: 1920, H: 1080},
		DesktopWidth: 3840, DesktopHeight: 1080,
		TabletMapped: true,
	}
	if got := d.Target(); got != wantTarget {
		t.Fatalf("Target() = %+v, want %+v", got, wantTarget)
	}
	if !reflect.DeepEqual(s.mapped, []string{"Virtual-1-1"}) {
		t.Fatalf("tablet mapped to %v, want [Virtual-1-1]", s.mapped)
	}
	if got := d.Description(); got != "X11 extended display Virtual-1-1 (1920x1080 at +1920+0)" {
		t.Fatalf("Description() = %q", got)
	}
	if mode, kind := displayModeFromTarget(d.Target()); mode != "extend" || kind != "x11grab" {
		t.Fatalf("displayModeFromTarget = %q, %q; want extend, x11grab", mode, kind)
	}
}

func TestX11ExtendedDisplayUsesTheConfiguredOutput(t *testing.T) {
	f := newFakeLaptopWithVKMS()
	f.outputs = append(f.outputs, &fakeRandrOutput{name: "DVI-I-1-1", connected: true, modes: []string{"1920x1080"}})
	s := withFakeX11Ext(t, f)
	t.Setenv(x11OutputEnv, " DVI-I-1-1 ")
	d := newTestX11Display(t, 1920, 1080)
	if got := d.Target().Rect; got != (displayRect{X: 1920, Y: 0, W: 1920, H: 1080}) {
		t.Fatalf("Rect = %+v", got)
	}
	want := []string{
		"--newmode 2960x1848_drawsplay 355.75 2960 3008 3040 3120 1848 1851 1861 1901 +hsync -vsync",
		"--addmode DVI-I-1-1 2960x1848_drawsplay",
		"--output DVI-I-1-1 --mode 1920x1080 --pos 1920x0",
	}
	if got := s.xrandr.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("xrandr commands =\n%q\nwant\n%q", got, want)
	}
}

func TestX11ExtendedDisplayCreatesMissingModes(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	d := newTestX11Display(t, 2960, 1848)
	want := []string{newTabletMode, addTabletMode, "--output Virtual-1-1 --mode 2960x1848_drawsplay --pos 1920x0"}
	if got := s.xrandr.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("xrandr commands =\n%q\nwant\n%q", got, want)
	}
	if got := d.Target(); got.Width != 2960 || got.Height != 1848 || got.DesktopWidth != 4880 || got.DesktopHeight != 1848 {
		t.Fatalf("Target() = %+v", got)
	}
}

func TestX11ExtendedDisplayReusesModesFromAnEarlierRun(t *testing.T) {
	f := newFakeLaptopWithVKMS()
	// The host created the mode before it was restarted; xrandr refuses to
	// create it again.
	f.sizes["2960x1848_drawsplay"] = [2]int{2960, 1848}
	s := withFakeX11Ext(t, f)
	newTestX11Display(t, 1920, 1080)
	want := []string{newTabletMode, addTabletMode, "--output Virtual-1-1 --mode 1920x1080 --pos 1920x0"}
	if got := s.xrandr.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("xrandr commands =\n%q\nwant\n%q", got, want)
	}
}

func TestX11ExtendedDisplayOutlastsTheWindowManagersHotplugLayout(t *testing.T) {
	f := newFakeLaptopWithVKMS()
	// Muffin answers the mode-list change by switching the output on at its
	// preferred mode, and then once more after the host's layout.
	f.onAddMode = func(f *fakeXrandr, o *fakeRandrOutput) {
		o.mode, o.x, o.y = o.preferred, 1920, 0
	}
	f.afterApply = func(f *fakeXrandr) {
		o := f.outputLocked("Virtual-1-1")
		o.mode, o.x, o.y = o.preferred, 1920, 0
	}
	s := withFakeX11Ext(t, f)
	d := newTestX11Display(t, 1920, 1080)

	apply := "--output Virtual-1-1 --mode 1920x1080 --pos 1920x0"
	want := []string{newTabletMode, addTabletMode, apply, apply}
	if got := s.xrandr.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("xrandr commands =\n%q\nwant\n%q", got, want)
	}
	if got := d.Target().Rect; got != (displayRect{X: 1920, Y: 0, W: 1920, H: 1080}) {
		t.Fatalf("Rect = %+v", got)
	}
}

func TestX11ExtendedDisplayGivesUpWhenTheLayoutDoesNotStick(t *testing.T) {
	f := newFakeLaptopWithVKMS()
	s := withFakeX11Ext(t, f)
	var revert func(f *fakeXrandr)
	revert = func(f *fakeXrandr) {
		o := f.outputLocked("Virtual-1-1")
		o.mode, o.x, o.y = o.preferred, 1920, 0
		f.afterApply = revert
	}
	f.afterApply = revert
	_, err := newX11ExtendedDisplay(videoOptions{Width: 1920, Height: 1080}, ":0")
	if err == nil || !strings.Contains(err.Error(), "did not keep the 1920x1080 mode") {
		t.Fatalf("newX11ExtendedDisplay error = %v", err)
	}
	// The output was left on by the window manager: the host switches it
	// off before the session falls back to mirroring.
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off" {
		t.Fatalf("last xrandr command = %q, want the switch-off", last)
	}
	if got := x11Enabled(); len(got) != 0 {
		t.Fatalf("enabled outputs = %v", got)
	}
	if len(s.mapped) != 0 {
		t.Fatalf("tablet mapped to %v after a failure", s.mapped)
	}
	x11Ext.mu.Lock()
	claimed := x11Ext.claimed
	x11Ext.mu.Unlock()
	if claimed {
		t.Fatalf("failed session left the output claimed")
	}
}

func TestX11ExtendedDisplaySwitchesOffAnOutputTheWindowManagerSwitchedOn(t *testing.T) {
	f := newFakeLaptopWithVKMS()
	f.maxW = 4096
	// Muffin answers the mode-list change by switching the output on; the
	// tablet's mode then does not fit next to the panel.
	f.onAddMode = func(f *fakeXrandr, o *fakeRandrOutput) {
		o.mode, o.x, o.y = o.preferred, 1920, 0
	}
	s := withFakeX11Ext(t, f)
	_, err := newX11ExtendedDisplay(videoOptions{Width: 2960, Height: 1848}, ":0")
	if err == nil || !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("newX11ExtendedDisplay error = %v", err)
	}
	want := []string{newTabletMode, addTabletMode, "--output Virtual-1-1 --off"}
	if got := s.xrandr.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("xrandr commands =\n%q\nwant\n%q", got, want)
	}
	if got := x11Enabled(); len(got) != 0 {
		t.Fatalf("enabled outputs = %v", got)
	}
}

func TestX11ExtendedDisplayFailsWithoutAVirtualOutput(t *testing.T) {
	f := newFakeLaptopWithVKMS()
	f.outputs = f.outputs[:2]
	s := withFakeX11Ext(t, f)
	_, err := newX11ExtendedDisplay(videoOptions{Width: 1920, Height: 1080}, ":0")
	if !errors.Is(err, errNoX11VirtualOutput) {
		t.Fatalf("newX11ExtendedDisplay error = %v, want errNoX11VirtualOutput", err)
	}
	if got := s.xrandr.commands(); len(got) != 0 {
		t.Fatalf("xrandr commands = %q, want none", got)
	}
	if got := x11Enabled(); len(got) != 0 {
		t.Fatalf("enabled outputs = %v", got)
	}
}

func TestX11ExtendedDisplaySwitchesOffAfterTheGracePeriod(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = 5 * time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	d.Close()
	d.Close() // idempotent

	waitUntil(t, "the switch-off", func() bool { return len(x11Enabled()) == 0 })
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off" {
		t.Fatalf("last xrandr command = %q, want the switch-off", last)
	}
	x11Ext.mu.Lock()
	placement := x11Ext.placement[":0/Virtual-1-1"]
	x11Ext.mu.Unlock()
	if placement == nil || *placement != (x11Placement{Side: "right"}) {
		t.Fatalf("remembered placement = %+v, want right", placement)
	}
}

func TestX11ExtendedDisplayReturnsWhereTheUserPutIt(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = 5 * time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	// The user drags the tablet to the left of the panel.
	s.xrandr.update(func(f *fakeXrandr) {
		f.outputLocked("Virtual-1-1").x = 0
		f.outputLocked("eDP-1").x = 1920
	})
	d.Close()
	waitUntil(t, "the switch-off", func() bool { return len(x11Enabled()) == 0 })
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off --output eDP-1 --pos 0x0" {
		t.Fatalf("switch-off = %q, want the panel moved back to the origin", last)
	}

	d = newTestX11Display(t, 1920, 1080)
	got = s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --mode 1920x1080 --pos 0x0 --output eDP-1 --pos 1920x0" {
		t.Fatalf("second session layout = %q, want the tablet back on the left", last)
	}
	if r := d.Target().Rect; r != (displayRect{X: 0, Y: 0, W: 1920, H: 1080}) {
		t.Fatalf("Rect = %+v", r)
	}
}

func TestX11ExtendedDisplayHandsThePrimaryRoleOnWhenSwitchingOff(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = 5 * time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	// The user makes the tablet the primary display.
	s.xrandr.update(func(f *fakeXrandr) {
		f.outputLocked("eDP-1").primary = false
		f.outputLocked("Virtual-1-1").primary = true
	})
	d.Close()
	waitUntil(t, "the switch-off", func() bool { return len(x11Enabled()) == 0 })
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off --output eDP-1 --primary" {
		t.Fatalf("switch-off = %q, want the panel made primary", last)
	}
	s.xrandr.update(func(f *fakeXrandr) {
		if !f.outputLocked("eDP-1").primary {
			t.Error("the panel is not the primary display")
		}
	})
}

func TestX11ExtendedDisplaySwitchesThePanelBackOnWhenItWasTheOnlyDisplay(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = 5 * time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	// The user turns the panel off and keeps only the tablet.
	s.xrandr.update(func(f *fakeXrandr) {
		panel, tablet := f.outputLocked("eDP-1"), f.outputLocked("Virtual-1-1")
		panel.mode, panel.primary = "", false
		tablet.x, tablet.primary = 0, true
	})
	d.Close()
	waitUntil(t, "the switch-off", func() bool { return len(x11Enabled()) == 0 })
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off --output eDP-1 --auto --pos 0x0 --primary" {
		t.Fatalf("switch-off = %q, want the panel switched back on", last)
	}
	s.xrandr.update(func(f *fakeXrandr) {
		if panel := f.outputLocked("eDP-1"); panel.mode != "1920x1080" || !panel.primary {
			t.Errorf("panel mode %q primary %v, want 1920x1080 primary", panel.mode, panel.primary)
		}
	})
}

func TestX11ExtendedDisplaySwitchesOffWithoutAnotherDisplayToTurnOn(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = 5 * time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	// The only monitor is unplugged while the tablet shows the desktop.
	s.xrandr.update(func(f *fakeXrandr) {
		panel := f.outputLocked("eDP-1")
		panel.connected, panel.mode = false, ""
		f.outputLocked("Virtual-1-1").x = 0
	})
	d.Close()
	waitUntil(t, "the switch-off", func() bool { return len(x11Enabled()) == 0 })
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off" {
		t.Fatalf("switch-off = %q", last)
	}
}

func TestX11ExtendedDisplayGivesTheDesktopBackToThePanel(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	d := newTestX11Display(t, 1920, 1080)
	// The user switches the panel off (or closes the lid), which leaves the
	// tablet as the only and primary display, and the watcher restarts the
	// session.
	s.xrandr.update(func(f *fakeXrandr) {
		panel, tablet := f.outputLocked("eDP-1"), f.outputLocked("Virtual-1-1")
		panel.mode, panel.primary = "", false
		tablet.x, tablet.primary = 0, true
	})
	d.Close()
	_, err := newX11ExtendedDisplay(videoOptions{Width: 1920, Height: 1080}, ":0")
	if err == nil || err.Error() != "RandR output Virtual-1-1 is the primary display" {
		t.Fatalf("newX11ExtendedDisplay error = %v", err)
	}
	// The session mirrors the X screen instead, back on the panel, rather
	// than leave a display that goes dark with the host.
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off --output eDP-1 --auto --pos 0x0 --primary" {
		t.Fatalf("last xrandr command = %q, want the panel switched back on", last)
	}
	if got := x11Enabled(); len(got) != 0 {
		t.Fatalf("enabled outputs = %v", got)
	}
}

func switchOffAttempts(f *fakeXrandr) int {
	n := 0
	for _, c := range f.commands() {
		if c == "--output Virtual-1-1 --off" {
			n++
		}
	}
	return n
}

func TestX11ExtendedDisplayRetriesAFailedSwitchOff(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = 5 * time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	s.xrandr.update(func(f *fakeXrandr) { f.failSwitchOffs = 1 })
	d.Close()
	waitUntil(t, "the switch-off", func() bool { return len(x11Enabled()) == 0 })
	if n := switchOffAttempts(s.xrandr); n != 2 {
		t.Fatalf("switch-off attempts = %d, want 2", n)
	}
	s.xrandr.update(func(f *fakeXrandr) {
		if f.outputLocked("Virtual-1-1").mode != "" {
			t.Error("the output is still on")
		}
	})
}

func TestX11ExtendedDisplayGivesUpSwitchingOffUntilExit(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	s.xrandr.update(func(f *fakeXrandr) { f.failSwitchOffs = x11SwitchOffAttempts })
	d.Close()
	waitUntil(t, "the switch-off attempts", func() bool { return switchOffAttempts(s.xrandr) == x11SwitchOffAttempts })
	time.Sleep(20 * time.Millisecond)
	x11Ext.mu.Lock()
	timer := x11Ext.timer
	x11Ext.mu.Unlock()
	if n := switchOffAttempts(s.xrandr); n != x11SwitchOffAttempts || timer != nil {
		t.Fatalf("after giving up: %d attempts, timer %v", n, timer != nil)
	}
	if got := x11Enabled(); len(got) != 1 {
		t.Fatalf("enabled outputs = %v, want the output kept for the exit hook", got)
	}
	releaseVirtualDisplaysOnExit()
	if got := x11Enabled(); len(got) != 0 {
		t.Fatalf("enabled outputs after exit = %v", got)
	}
}

func TestX11ExtendedDisplayStaysOnAcrossAQuickReconnect(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	first := newTestX11Display(t, 1920, 1080)
	first.Close()
	before := s.xrandr.commands()

	second := newTestX11Display(t, 1920, 1080)
	if got := s.xrandr.commands(); !reflect.DeepEqual(got, before) {
		t.Fatalf("reconnect changed the layout: %q", got[len(before):])
	}
	if got := second.Target(); got != first.Target() {
		t.Fatalf("reconnect target = %+v, want %+v", got, first.Target())
	}
	x11Ext.mu.Lock()
	claimed, timer := x11Ext.claimed, x11Ext.timer
	x11Ext.mu.Unlock()
	if !claimed || timer != nil {
		t.Fatalf("after reconnect claimed=%v timer=%v", claimed, timer != nil)
	}
}

func TestX11ExtendedDisplayIgnoresAStaleSessionsClose(t *testing.T) {
	withFakeX11Ext(t, newFakeLaptopWithVKMS())
	first := newTestX11Display(t, 1920, 1080)
	second := newTestX11Display(t, 1920, 1080)
	first.Close()
	x11Ext.mu.Lock()
	claimed, timer := x11Ext.claimed, x11Ext.timer
	x11Ext.mu.Unlock()
	if !claimed || timer != nil {
		t.Fatalf("closing the old session released the new one: claimed=%v timer=%v", claimed, timer != nil)
	}
	second.Close()
	x11Ext.mu.Lock()
	claimed, timer = x11Ext.claimed, x11Ext.timer
	x11Ext.mu.Unlock()
	if claimed || timer == nil {
		t.Fatalf("closing the current session: claimed=%v timer=%v", claimed, timer != nil)
	}
}

func TestReleaseVirtualDisplaysOnExitSwitchesTheOutputOff(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	d := newTestX11Display(t, 1920, 1080)
	releaseVirtualDisplaysOnExit()
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --off" {
		t.Fatalf("last xrandr command = %q, want the switch-off", last)
	}
	d.Close()
	x11Ext.mu.Lock()
	timer := x11Ext.timer
	x11Ext.mu.Unlock()
	if timer != nil {
		t.Fatal("a release timer was started while exiting")
	}
	if _, err := newX11ExtendedDisplay(videoOptions{Width: 1920, Height: 1080}, ":0"); err == nil {
		t.Fatal("a session started while the host exits")
	}
	if err := d.Recover(); err == nil {
		t.Fatal("Recover switched the output on while the host exits")
	}
	if n := len(s.xrandr.commands()); n != len(got) {
		t.Fatalf("xrandr ran after exit: %q", s.xrandr.commands()[len(got):])
	}
}

func TestX11ExtendedDisplayRefreshFollowsTheOutput(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	d := newTestX11Display(t, 1920, 1080)
	s.xrandr.update(func(f *fakeXrandr) {
		f.outputLocked("Virtual-1-1").x, f.outputLocked("Virtual-1-1").y = 0, 1080
	})
	got, err := d.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := captureTarget{
		Kind: captureX11Grab, Width: 1920, Height: 1080,
		Rect:         displayRect{X: 0, Y: 1080, W: 1920, H: 1080},
		DesktopWidth: 1920, DesktopHeight: 2160,
		TabletMapped: true,
	}
	if got != want || d.Target() != want {
		t.Fatalf("Refresh() = %+v, Target() = %+v; want %+v", got, d.Target(), want)
	}

	s.xrandr.update(func(f *fakeXrandr) { f.outputLocked("Virtual-1-1").mode = "" })
	if _, err := d.Refresh(); err == nil {
		t.Fatal("Refresh accepted a switched-off output")
	}
}

func TestX11ExtendedDisplayRecoverSwitchesTheOutputBackOn(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	d := newTestX11Display(t, 1920, 1080)
	s.xrandr.update(func(f *fakeXrandr) { f.outputLocked("Virtual-1-1").mode = "" })
	if err := d.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got := s.xrandr.commands()
	if last := got[len(got)-1]; last != "--output Virtual-1-1 --mode 1920x1080 --pos 1920x0" {
		t.Fatalf("last xrandr command = %q", last)
	}
	if r := d.Target().Rect; r != (displayRect{X: 1920, Y: 0, W: 1920, H: 1080}) {
		t.Fatalf("Rect = %+v", r)
	}
}

// --- watcher ------------------------------------------------------------

func withTestSettingsStore(t *testing.T) *settingsStore {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prev := globalSettingsStore
	store := newSettingsStore(defaultSettings())
	globalSettingsStore = store
	t.Cleanup(func() { globalSettingsStore = prev })
	return store
}

func waitForWatchExit(t *testing.T, d *x11ExtendedDisplay) {
	t.Helper()
	select {
	case <-d.watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not restart the session")
	}
}

func TestX11ExtendedDisplaySavesAResolutionPickedInTheDisplaySettings(t *testing.T) {
	store := withTestSettingsStore(t)
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11WatchInterval = time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	s.xrandr.update(func(f *fakeXrandr) { f.outputLocked("Virtual-1-1").mode = "2560x1600" })
	waitForWatchExit(t, d)

	if got := store.get(); got.Width != 2560 || got.Height != 1600 {
		t.Fatalf("saved resolution = %dx%d, want 2560x1600", got.Width, got.Height)
	}
	d.mu.Lock()
	reqW, reqH := d.reqW, d.reqH
	d.mu.Unlock()
	if reqW != 2560 || reqH != 1600 {
		t.Fatalf("session resolution = %dx%d, want 2560x1600", reqW, reqH)
	}
}

// waitForWatcherBaseline waits until the watcher has handled the session's
// first stable state: two polls, and a third that shows the second was
// handled.
func waitForWatcherBaseline(t *testing.T, f *fakeXrandr) {
	t.Helper()
	start := f.queryCount()
	waitUntil(t, "watcher polls", func() bool { return f.queryCount() >= start+3 })
}

func TestX11ExtendedDisplaySavesThePreferredModeWhenTheUserPicksIt(t *testing.T) {
	store := withTestSettingsStore(t)
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11WatchInterval = time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	waitForWatcherBaseline(t, s.xrandr)
	s.xrandr.update(func(f *fakeXrandr) { f.outputLocked("Virtual-1-1").mode = "1024x768" })
	waitForWatchExit(t, d)
	if got := store.get(); got.Width != 1024 || got.Height != 768 {
		t.Fatalf("saved resolution = %dx%d, want 1024x768", got.Width, got.Height)
	}
}

func TestX11ExtendedDisplayDoesNotSaveTheWindowManagersDefaultModeAtStartup(t *testing.T) {
	store := withTestSettingsStore(t)
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11WatchInterval = time.Millisecond
	// The window manager falls back to the output's preferred mode just
	// after the session took over, before the watcher's first poll.
	mapX11Tablet = func(string) bool {
		s.xrandr.update(func(f *fakeXrandr) {
			f.beforeQuery = func(f *fakeXrandr) { f.outputLocked("Virtual-1-1").mode = "1024x768" }
		})
		return true
	}
	d := newTestX11Display(t, 1920, 1080)
	waitForWatchExit(t, d)
	if got := store.get(); got.Width != defaultSettings().Width || got.Height != defaultSettings().Height {
		t.Fatalf("saved resolution = %dx%d, want the default kept", got.Width, got.Height)
	}
}

func TestX11ExtendedDisplayDoesNotSaveTheWindowManagersDefaultModeOnAHotplug(t *testing.T) {
	store := withTestSettingsStore(t)
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11WatchInterval = time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	waitForWatcherBaseline(t, s.xrandr)
	// A monitor is plugged in, and the window manager lays the screen out
	// again with every display at its preferred mode.
	s.xrandr.update(func(f *fakeXrandr) {
		hdmi := f.outputLocked("HDMI-1")
		hdmi.connected, hdmi.modes, hdmi.preferred = true, []string{"1280x720"}, "1280x720"
		hdmi.mode, hdmi.x = "1280x720", 1920
		tablet := f.outputLocked("Virtual-1-1")
		tablet.mode, tablet.x = "1024x768", 3200
	})
	waitForWatchExit(t, d)
	if got := store.get(); got.Width != defaultSettings().Width || got.Height != defaultSettings().Height {
		t.Fatalf("saved resolution = %dx%d, want the default kept", got.Width, got.Height)
	}
}

func TestX11ExtendedDisplayRestartsWhenTheOutputIsSwitchedOff(t *testing.T) {
	withTestSettingsStore(t)
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11WatchInterval = time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	s.xrandr.update(func(f *fakeXrandr) { f.outputLocked("Virtual-1-1").mode = "" })
	waitForWatchExit(t, d)
}

func TestX11ExtendedDisplayWatcherIgnoresAnUnchangedLayout(t *testing.T) {
	store := withTestSettingsStore(t)
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11WatchInterval = time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	start := s.xrandr.queryCount()
	waitUntil(t, "watcher polls", func() bool { return s.xrandr.queryCount() >= start+5 })
	select {
	case <-d.watchDone:
		t.Fatal("watcher restarted the session for an unchanged layout")
	default:
	}
	d.Close()
	if got := store.get(); got.Width != defaultSettings().Width {
		t.Fatalf("saved resolution changed to %dx%d", got.Width, got.Height)
	}
}

// --- capture -------------------------------------------------------------

func TestX11GrabCaptureArgs(t *testing.T) {
	cases := []struct {
		target captureTarget
		want   string
	}{
		{captureTarget{Kind: captureX11Grab}, "-f x11grab -framerate 60 -i :0"},
		{
			captureTarget{Kind: captureX11Grab, Rect: displayRect{X: 1920, Y: 0, W: 1920, H: 1080}},
			"-f x11grab -framerate 60 -video_size 1920x1080 -i :0+1920,0",
		},
		{
			captureTarget{Kind: captureX11Grab, Rect: displayRect{X: 0, Y: 1080, W: 1367, H: 769}},
			"-f x11grab -framerate 60 -video_size 1366x768 -i :0+0,1080",
		},
	}
	for _, tc := range cases {
		if got := strings.Join(x11GrabCaptureArgs(tc.target, ":0", 60), " "); got != tc.want {
			t.Errorf("x11GrabCaptureArgs(%+v) = %q, want %q", tc.target.Rect, got, tc.want)
		}
	}
}

func TestX11GrabRecordsOnlyTheExtendedDisplay(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-extend", " V..... h264_vaapi \n V..... libx264 \n")
	withFakeVAAPIProbe(t, "/dev/dri/renderD128", true)
	probed := withFakeX11RootSize(t, 3840, 1080, true)
	t.Setenv("DISPLAY", ":0")
	target := captureTarget{
		Kind: captureX11Grab, Width: 2960, Height: 1848,
		Rect:         displayRect{X: 1920, Y: 0, W: 2960, H: 1848},
		DesktopWidth: 4880, DesktopHeight: 1848,
	}
	specs, err := buildCapturePipelines(target, videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg})
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	if got := specNames(specs); !reflect.DeepEqual(got, []string{"h264_vaapi", "libx264"}) {
		t.Fatalf("candidates = %v", got)
	}
	for _, spec := range specs {
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, "-f x11grab -framerate 60 -video_size 2960x1848 -i :0+1920,0 ") {
			t.Errorf("%s does not record the extended display: %s", spec.Name, joined)
		}
	}
	want := fmt.Sprint(x264FrameThreads(2960, 1848, 60))
	if got := argValues(specByName(t, specs, "libx264").Args, "-threads"); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("libx264 -threads = %v, want [%s] for the output's size", got, want)
	}
	if len(*probed) != 0 {
		t.Errorf("probed the X screen size for a sized target: %v", *probed)
	}
}

func TestX11GrabVBVHoldsAKeyframe(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-vbv",
		" V..... h264_nvenc \n V..... h264_vaapi \n V..... libx264 \n")
	withFakeVAAPIProbe(t, "/dev/dri/renderD128", true)
	withFakeX11RootSize(t, 1920, 1080, true)
	for _, target := range []captureTarget{
		{Kind: captureX11Grab},
		{Kind: captureX11Grab, Width: 1920, Height: 1080, Rect: displayRect{X: 1920, W: 1920, H: 1080}},
	} {
		specs, err := buildCapturePipelines(target, videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg})
		if err != nil {
			t.Fatalf("buildCapturePipelines: %v", err)
		}
		if len(specs) != 3 {
			t.Fatalf("candidates = %v", specNames(specs))
		}
		for _, spec := range specs {
			// 125 ms of the 24 Mbit/s stream.
			if got := argValues(spec.Args, "-bufsize"); !reflect.DeepEqual(got, []string{"3000k"}) {
				t.Errorf("%s -bufsize = %v, want [3000k]", spec.Name, got)
			}
		}
	}
	// Other ffmpeg paths keep the shared ~1.25-frame VBV.
	if got := argValues(rateControlArgs(videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264"}), "-bufsize"); !reflect.DeepEqual(got, []string{"500k"}) {
		t.Errorf("shared -bufsize = %v, want [500k]", got)
	}
	if got := x11VBVKbits("400k"); got != 100 {
		t.Errorf("x11VBVKbits(400k) = %d, want the 100 kbit floor", got)
	}
}

func TestX11ExtendedDisplaySwitchesOffAnOutputThatComesBackAfterTheSession(t *testing.T) {
	s := withFakeX11Ext(t, newFakeLaptopWithVKMS())
	x11ReleaseGrace = 5 * time.Millisecond
	d := newTestX11Display(t, 1920, 1080)
	d.Close()
	waitUntil(t, "the switch-off", func() bool { return len(x11Enabled()) == 0 })
	if n := switchOffAttempts(s.xrandr); n != 1 {
		t.Fatalf("switch-off attempts = %d, want 1", n)
	}
	// The window manager switches the connected output back on.
	s.xrandr.update(func(f *fakeXrandr) {
		o := f.outputLocked("Virtual-1-1")
		o.mode = "1920x1080"
	})
	waitUntil(t, "the second switch-off", func() bool { return switchOffAttempts(s.xrandr) >= 2 })
}
