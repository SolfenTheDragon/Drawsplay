//go:build linux

package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestFindMonitorSpec(t *testing.T) {
	panel := monitorSpec{Connector: "eDP-1", Vendor: "BOE", Product: "0x0869", Serial: "0x00000000"}
	vkms := monitorSpec{Connector: "Virtual-1-1", Vendor: "unknown", Product: "unknown", Serial: "unknown"}
	cases := []struct {
		name     string
		monitors []monitorSpec
		found    bool
		err      bool
	}{
		{name: "listed", monitors: []monitorSpec{panel, vkms}, found: true},
		{name: "not listed yet", monitors: []monitorSpec{panel}},
		{name: "no EDID strings", monitors: []monitorSpec{panel, {Connector: "Virtual-1-1"}}, found: true, err: true},
		{
			name:     "same EDID strings as another monitor",
			monitors: []monitorSpec{panel, vkms, {Connector: "Virtual-1-2", Vendor: "unknown", Product: "unknown", Serial: "unknown"}},
			found:    true, err: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, found, err := findMonitorSpec(tc.monitors, "Virtual-1-1")
			if found != tc.found || (err != nil) != tc.err {
				t.Fatalf("findMonitorSpec = %+v, %v, %v; want found=%v err=%v", spec, found, err, tc.found, tc.err)
			}
			if found && !tc.err && spec != vkms {
				t.Fatalf("findMonitorSpec = %+v, want %+v", spec, vkms)
			}
		})
	}
}

type fakeTabletDesktop struct {
	// monitors answers DisplayConfig for each compositor, by name; a
	// missing entry means the compositor does not run.
	monitors map[string]func() ([]monitorSpec, error)
	setErr   error
	set      []string
}

func withFakeTabletDesktop(t *testing.T, d *fakeTabletDesktop) {
	t.Helper()
	prevMonitors, prevSet := displayConfigMonitors, setTabletOutput
	prevAttempts, prevDelay := x11TabletLookupAttempts, x11TabletLookupDelay
	displayConfigMonitors = func(c x11TabletCompositor) ([]monitorSpec, error) {
		if fn, ok := d.monitors[c.name]; ok {
			return fn()
		}
		return nil, errCompositorAbsent
	}
	setTabletOutput = func(c x11TabletCompositor, spec monitorSpec) error {
		d.set = append(d.set, c.schema+":"+x11TabletSettingsPath(c)+" output "+gvariantStringArray(spec.Vendor, spec.Product, spec.Serial))
		return d.setErr
	}
	x11TabletLookupAttempts, x11TabletLookupDelay = 3, 0
	t.Cleanup(func() {
		displayConfigMonitors, setTabletOutput = prevMonitors, prevSet
		x11TabletLookupAttempts, x11TabletLookupDelay = prevAttempts, prevDelay
	})
}

func listing(monitors ...monitorSpec) func() ([]monitorSpec, error) {
	return func() ([]monitorSpec, error) { return monitors, nil }
}

var (
	testPanel = monitorSpec{Connector: "eDP-1", Vendor: "BOE", Product: "0x0869", Serial: "0x00000000"}
	testVKMS  = monitorSpec{Connector: "Virtual-1-1", Vendor: "unknown", Product: "unknown", Serial: "unknown"}
)

func TestMapX11TabletUnderMuffin(t *testing.T) {
	d := &fakeTabletDesktop{monitors: map[string]func() ([]monitorSpec, error){"Muffin": listing(testPanel, testVKMS)}}
	withFakeTabletDesktop(t, d)
	if !mapX11TabletToOutput("Virtual-1-1") {
		t.Fatal("mapX11TabletToOutput = false under Muffin")
	}
	want := []string{"org.cinnamon.desktop.peripherals.tablet:/org/cinnamon/desktop/peripherals/tablets/1d50:5044/ output ['unknown', 'unknown', 'unknown']"}
	if !reflect.DeepEqual(d.set, want) {
		t.Fatalf("settings written = %q, want %q", d.set, want)
	}
}

func TestMapX11TabletUnderMutter(t *testing.T) {
	d := &fakeTabletDesktop{monitors: map[string]func() ([]monitorSpec, error){"Mutter": listing(testPanel, testVKMS)}}
	withFakeTabletDesktop(t, d)
	if !mapX11TabletToOutput("Virtual-1-1") {
		t.Fatal("mapX11TabletToOutput = false under Mutter")
	}
	want := []string{"org.gnome.desktop.peripherals.tablet:/org/gnome/desktop/peripherals/tablets/1d50:5044/ output ['unknown', 'unknown', 'unknown']"}
	if !reflect.DeepEqual(d.set, want) {
		t.Fatalf("settings written = %q, want %q", d.set, want)
	}
}

func TestMapX11TabletWaitsForTheCompositorToListTheOutput(t *testing.T) {
	calls := 0
	d := &fakeTabletDesktop{monitors: map[string]func() ([]monitorSpec, error){"Muffin": func() ([]monitorSpec, error) {
		calls++
		if calls < 3 {
			return []monitorSpec{testPanel}, nil
		}
		return []monitorSpec{testPanel, testVKMS}, nil
	}}}
	withFakeTabletDesktop(t, d)
	if !mapX11TabletToOutput("Virtual-1-1") || calls != 3 || len(d.set) != 1 {
		t.Fatalf("mapX11TabletToOutput after %d lookups wrote %q", calls, d.set)
	}
}

func TestMapX11TabletFailures(t *testing.T) {
	cases := []struct {
		name    string
		desktop *fakeTabletDesktop
		// managed: a compositor still maps the pen, so the injector must
		// not aim at the output as well.
		managed bool
	}{
		{name: "no compositor that maps tablets", desktop: &fakeTabletDesktop{}},
		{name: "no session bus", desktop: &fakeTabletDesktop{monitors: map[string]func() ([]monitorSpec, error){
			"Muffin": func() ([]monitorSpec, error) {
				return nil, fmt.Errorf("%w (no D-Bus session bus)", errCompositorAbsent)
			},
			"Mutter": func() ([]monitorSpec, error) {
				return nil, fmt.Errorf("%w (no D-Bus session bus)", errCompositorAbsent)
			},
		}}},
		{name: "output never listed", managed: true, desktop: &fakeTabletDesktop{monitors: map[string]func() ([]monitorSpec, error){
			"Muffin": listing(testPanel),
			"Mutter": listing(testPanel, testVKMS),
		}}},
		{name: "DisplayConfig fails", managed: true, desktop: &fakeTabletDesktop{monitors: map[string]func() ([]monitorSpec, error){
			"Muffin": func() ([]monitorSpec, error) { return nil, errors.New("timeout") },
			"Mutter": listing(testPanel, testVKMS),
		}}},
		{name: "output without EDID strings", managed: true, desktop: &fakeTabletDesktop{monitors: map[string]func() ([]monitorSpec, error){
			"Muffin": listing(testPanel, monitorSpec{Connector: "Virtual-1-1"}),
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeTabletDesktop(t, tc.desktop)
			if got := mapX11TabletToOutput("Virtual-1-1"); got != tc.managed {
				t.Fatalf("mapX11TabletToOutput = %v, want %v", got, tc.managed)
			}
			if len(tc.desktop.set) != 0 {
				t.Fatalf("settings written: %q", tc.desktop.set)
			}
		})
	}

	d := &fakeTabletDesktop{
		monitors: map[string]func() ([]monitorSpec, error){"Muffin": listing(testPanel, testVKMS)},
		setErr:   errors.New("gsettings: No such schema"),
	}
	withFakeTabletDesktop(t, d)
	if !mapX11TabletToOutput("Virtual-1-1") {
		t.Fatal("mapX11TabletToOutput = false although Muffin maps the pen")
	}
}

func TestGVariantStringArray(t *testing.T) {
	if got := gvariantStringArray("BOE", "0x0869", ""); got != "['BOE', '0x0869', '']" {
		t.Errorf("gvariantStringArray = %s", got)
	}
	if got := gvariantStringArray(`Dell's`, `a\b`); got != `['Dell\'s', 'a\\b']` {
		t.Errorf("gvariantStringArray escaping = %s", got)
	}
}
