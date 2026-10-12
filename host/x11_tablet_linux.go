//go:build linux

package main

// Tablet mapping for the X11 extended display.
//
// The X server spreads an absolute input device such as the Drawsplay Pen
// over the whole X screen unless something sets the device's "Coordinate
// Transformation Matrix". Muffin (Cinnamon) and Mutter (GNOME on Xorg) do: a
// pen libwacom does not know is mapped onto the laptop panel when there is
// one, which would keep tablet input off the extended display. Both honour
// the tablet's "Map to Monitor" setting, keyed by the monitor's EDID
// vendor/product/serial, and re-apply it whenever the monitor layout
// changes. So the host points that setting at the extended display's output,
// as it does for the GNOME virtual monitor on Wayland (mapMutterTablet in
// mutter_extend_linux.go), and the injector sends plain tablet coordinates.
//
// Under other window managers nothing maps the device, and the injector
// aims at the output's rectangle within the whole screen instead (see
// tabletAbsCoords in inject_linux.go). When Muffin or Mutter runs but the
// setting cannot be pointed at the output, the injector leaves the
// coordinates alone all the same, since the compositor still maps the
// device.
//
// The setting is left in place when the output is switched off: the
// compositor then finds no such monitor and falls back to its default
// mapping.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// x11TabletCompositor is a window manager that maps tablets to monitors
// from a per-device settings schema.
type x11TabletCompositor struct {
	name       string
	busName    string // DisplayConfig service, also its interface name
	objectPath dbus.ObjectPath
	schema     string // relocatable tablet settings schema
	schemaPath string // prefix of the schema's per-device path
}

// mutterTabletCompositor is GNOME's Mutter, under Xorg or Wayland.
var mutterTabletCompositor = x11TabletCompositor{
	name:       "Mutter",
	busName:    displayConfigBusName,
	objectPath: displayConfigObjectPath,
	schema:     "org.gnome.desktop.peripherals.tablet",
	schemaPath: "/org/gnome/desktop/peripherals/tablets/",
}

var x11TabletCompositors = []x11TabletCompositor{
	{
		name:       "Muffin",
		busName:    "org.cinnamon.Muffin.DisplayConfig",
		objectPath: "/org/cinnamon/Muffin/DisplayConfig",
		schema:     "org.cinnamon.desktop.peripherals.tablet",
		schemaPath: "/org/cinnamon/desktop/peripherals/tablets/",
	},
	mutterTabletCompositor,
}

// monitorSpec identifies a monitor as DisplayConfig does: its connector and
// the EDID strings tablet mappings are keyed by.
type monitorSpec struct {
	Connector, Vendor, Product, Serial string
}

var errCompositorAbsent = errors.New("compositor not running")

// Test hooks.
var (
	// displayConfigMonitors lists a compositor's monitors, or returns
	// errCompositorAbsent when that compositor does not run.
	displayConfigMonitors = queryDisplayConfigMonitors
	// setTabletOutput writes the Drawsplay Pen's "Map to Monitor" setting.
	setTabletOutput = runGsettingsSetTabletOutput
	// mapX11Tablet is mapX11TabletToOutput.
	mapX11Tablet = mapX11TabletToOutput

	// The compositor can list a newly switched-on output a little after
	// RandR does.
	x11TabletLookupAttempts = 10
	x11TabletLookupDelay    = 150 * time.Millisecond
)

// mapX11TabletToOutput points the running compositor's tablet mapping for the
// Drawsplay Pen at the RandR output connector, and reports whether a
// compositor maps the pen. That is also the case when the mapping could not
// be set: the compositor then maps the pen by its own rules (or by a mapping
// to this output set before), so aiming the coordinates at the output as
// well would misplace them.
func mapX11TabletToOutput(connector string) bool {
	for _, c := range x11TabletCompositors {
		spec, err := lookupMonitorSpec(c, connector)
		if errors.Is(err, errCompositorAbsent) {
			continue
		}
		if err == nil {
			err = setTabletOutput(c, spec)
		}
		if err != nil {
			log.Printf("X11 extended display: could not map Drawsplay Pen input to %s in %s (%v); touch/pen input may land on another monitor", connector, c.name, err)
			return true
		}
		log.Printf("Mapped Drawsplay Pen touch/pen input to %s (%s)", connector, c.name)
		return true
	}
	log.Printf("X11 extended display: no Muffin or Mutter tablet mapping; aiming touch/pen input at %s within the X screen", connector)
	return false
}

// lookupMonitorSpec waits for the compositor to list connector.
func lookupMonitorSpec(c x11TabletCompositor, connector string) (monitorSpec, error) {
	for attempt := 1; ; attempt++ {
		monitors, err := displayConfigMonitors(c)
		if err != nil {
			return monitorSpec{}, err
		}
		if spec, found, err := findMonitorSpec(monitors, connector); found {
			return spec, err
		}
		if attempt >= x11TabletLookupAttempts {
			return monitorSpec{}, fmt.Errorf("%s does not list %s", c.name, connector)
		}
		time.Sleep(x11TabletLookupDelay)
	}
}

// findMonitorSpec returns connector's monitor, with an error when the
// compositor could not tell it apart from another monitor by its EDID
// strings.
func findMonitorSpec(monitors []monitorSpec, connector string) (monitorSpec, bool, error) {
	var spec monitorSpec
	found := false
	for _, m := range monitors {
		if m.Connector == connector {
			spec, found = m, true
			break
		}
	}
	if !found {
		return spec, false, nil
	}
	if spec.Vendor == "" && spec.Product == "" && spec.Serial == "" {
		return spec, true, fmt.Errorf("%s has no EDID identity", connector)
	}
	for _, m := range monitors {
		if m.Connector != connector && m.Vendor == spec.Vendor && m.Product == spec.Product && m.Serial == spec.Serial {
			return spec, true, fmt.Errorf("%s and %s have the same EDID identity", connector, m.Connector)
		}
	}
	return spec, true, nil
}

func queryDisplayConfigMonitors(c x11TabletCompositor) ([]monitorSpec, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		// Muffin and Mutter run with a session bus.
		return nil, fmt.Errorf("%w (no D-Bus session bus: %v)", errCompositorAbsent, err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var running bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, c.busName).Store(&running); err != nil {
		return nil, err
	}
	if !running {
		return nil, errCompositorAbsent
	}
	call := conn.Object(c.busName, c.objectPath).CallWithContext(ctx, c.busName+".GetCurrentState", 0)
	if call.Err != nil {
		return nil, fmt.Errorf("GetCurrentState: %w", call.Err)
	}
	if len(call.Body) < 2 {
		return nil, errors.New("GetCurrentState: unexpected reply")
	}
	var monitors []struct {
		Spec  monitorSpec
		Modes interface{}
		Props interface{}
	}
	if err := dbus.Store(call.Body[1:2], &monitors); err != nil {
		return nil, fmt.Errorf("GetCurrentState: %w", err)
	}
	specs := make([]monitorSpec, len(monitors))
	for i, m := range monitors {
		specs[i] = m.Spec
	}
	return specs, nil
}

// x11TabletSettingsPath is the Drawsplay Pen's path of the compositor's
// relocatable tablet schema. The compositors format the device IDs as "%.4x".
func x11TabletSettingsPath(c x11TabletCompositor) string {
	return fmt.Sprintf("%s%04x:%04x/", c.schemaPath, drawsplayPenVendorID, drawsplayPenProductID)
}

func runGsettingsSetTabletOutput(c x11TabletCompositor, spec monitorSpec) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", "set", c.schema+":"+x11TabletSettingsPath(c), "output",
		gvariantStringArray(spec.Vendor, spec.Product, spec.Serial)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gsettings set: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// gvariantStringArray formats values as a GVariant text-format string array.
func gvariantStringArray(values ...string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		v = strings.ReplaceAll(v, `\`, `\\`)
		quoted[i] = "'" + strings.ReplaceAll(v, `'`, `\'`) + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
