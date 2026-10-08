//go:build linux

package main

// System tray status menu for X11 sessions.
//
// The host exports a StatusNotifierItem (the protocol behind the Cinnamon,
// KDE, XFCE and GNOME-AppIndicator tray areas) and a com.canonical.dbusmenu
// menu over the session bus, so no GUI toolkit is needed. The menu shows the
// tablet connection, the display mode and the encoder, and offers
// "Open Settings" and "Quit". Without a StatusNotifierWatcher on the bus (no
// tray area) the item stays unregistered and registers once one appears.

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image/color"
	"image/png"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

const (
	trayItemPath    = dbus.ObjectPath("/StatusNotifierItem")
	trayMenuPath    = dbus.ObjectPath("/MenuBar")
	trayItemIface   = "org.kde.StatusNotifierItem"
	trayMenuIface   = "com.canonical.dbusmenu"
	trayWatcherName = "org.kde.StatusNotifierWatcher"
	trayWatcherPath = dbus.ObjectPath("/StatusNotifierWatcher")

	trayIDRoot     int32 = 0
	trayIDStatus   int32 = 1
	trayIDDetail   int32 = 2
	trayIDSep1     int32 = 3
	trayIDSettings int32 = 4
	trayIDSep2     int32 = 5
	trayIDQuit     int32 = 6
)

//go:embed drawsplay-tray.png
var trayIconPNG []byte

type trayPixmap struct {
	W, H int32
	Data []byte
}

// trayIconPixmaps converts the embedded PNG to the ARGB32 pixmap format of
// the StatusNotifierItem IconPixmap property.
func trayIconPixmaps() []trayPixmap {
	img, err := png.Decode(bytes.NewReader(trayIconPNG))
	if err != nil {
		log.Printf("Tray icon decode failed: %v", err)
		return []trayPixmap{}
	}
	b := img.Bounds()
	data := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			data = append(data, c.A, c.R, c.G, c.B)
		}
	}
	return []trayPixmap{{W: int32(b.Dx()), H: int32(b.Dy()), Data: data}}
}

type trayLayout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type trayPropEntry struct {
	ID    int32
	Props map[string]dbus.Variant
}

type trayEvent struct {
	ID      int32
	EventID string
	Data    dbus.Variant
	Ts      uint32
}

type trayToolTip struct {
	IconName string
	IconData []struct {
		W, H int32
		Data []byte
	}
	Title string
	Desc  string
}

type trayEntry struct {
	id        int32
	label     string
	separator bool
	enabled   bool
}

// trayStatusText builds the menu's connection and detail lines.
func trayStatusText(device string, vs videoStatusInfo) (status, detail string) {
	device = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(device))
	if device == "unknown-tablet" || device == "unknown-client" {
		device = ""
	}
	if device == "" {
		return "🔴 Disconnected", "Waiting for tablet"
	}
	status = "🟢 Connected: " + device
	if !vs.Streaming {
		return status, "Not streaming"
	}
	mode := "Streaming"
	switch vs.DisplayMode {
	case "extend":
		mode = "Extended display"
	case "mirror":
		mode = "Mirroring desktop"
	}
	if vs.Encoder != "" {
		mode += " · " + vs.Encoder
	}
	return status, mode
}

func trayMenuEntries(status, detail string) []trayEntry {
	return []trayEntry{
		{id: trayIDStatus, label: status},
		{id: trayIDDetail, label: detail},
		{id: trayIDSep1, separator: true},
		{id: trayIDSettings, label: "Open Settings", enabled: true},
		{id: trayIDSep2, separator: true},
		{id: trayIDQuit, label: "Quit DrawSplay", enabled: true},
	}
}

func (e trayEntry) props() map[string]dbus.Variant {
	if e.separator {
		return map[string]dbus.Variant{"type": dbus.MakeVariant("separator")}
	}
	return map[string]dbus.Variant{
		"label":   dbus.MakeVariant(e.label),
		"enabled": dbus.MakeVariant(e.enabled),
		"visible": dbus.MakeVariant(true),
	}
}

type trayMenu struct {
	mu      sync.Mutex
	rev     uint32
	status  string
	detail  string
	onClick func(id int32)
}

func (m *trayMenu) entries() []trayEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return trayMenuEntries(m.status, m.detail)
}

func (m *trayMenu) GetLayout(parentID, depth int32, _ []string) (uint32, trayLayout, *dbus.Error) {
	m.mu.Lock()
	rev := m.rev
	m.mu.Unlock()
	entries := m.entries()
	if parentID != trayIDRoot {
		for _, e := range entries {
			if e.id == parentID {
				return rev, trayLayout{ID: e.id, Props: e.props(), Children: []dbus.Variant{}}, nil
			}
		}
		return rev, trayLayout{}, dbus.MakeFailedError(fmt.Errorf("unknown menu item %d", parentID))
	}
	root := trayLayout{
		ID:       trayIDRoot,
		Props:    map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")},
		Children: []dbus.Variant{},
	}
	if depth != 0 {
		for _, e := range entries {
			root.Children = append(root.Children, dbus.MakeVariant(trayLayout{ID: e.id, Props: e.props(), Children: []dbus.Variant{}}))
		}
	}
	return rev, root, nil
}

func (m *trayMenu) GetGroupProperties(ids []int32, _ []string) ([]trayPropEntry, *dbus.Error) {
	out := []trayPropEntry{}
	for _, e := range m.entries() {
		if len(ids) == 0 {
			out = append(out, trayPropEntry{ID: e.id, Props: e.props()})
			continue
		}
		for _, id := range ids {
			if id == e.id {
				out = append(out, trayPropEntry{ID: e.id, Props: e.props()})
			}
		}
	}
	return out, nil
}

func (m *trayMenu) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	for _, e := range m.entries() {
		if e.id == id {
			if v, ok := e.props()[name]; ok {
				return v, nil
			}
		}
	}
	return dbus.MakeVariant(""), dbus.MakeFailedError(fmt.Errorf("no property %q on item %d", name, id))
}

func (m *trayMenu) Event(id int32, eventID string, _ dbus.Variant, _ uint32) *dbus.Error {
	if eventID == "clicked" && m.onClick != nil {
		m.onClick(id)
	}
	return nil
}

func (m *trayMenu) EventGroup(events []trayEvent) ([]int32, *dbus.Error) {
	for _, ev := range events {
		m.Event(ev.ID, ev.EventID, ev.Data, ev.Ts)
	}
	return []int32{}, nil
}

func (m *trayMenu) AboutToShow(int32) (bool, *dbus.Error) { return false, nil }

func (m *trayMenu) AboutToShowGroup([]int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

type trayItem struct {
	onActivate func()
}

func (i *trayItem) Activate(int32, int32) *dbus.Error          { i.onActivate(); return nil }
func (i *trayItem) SecondaryActivate(int32, int32) *dbus.Error { i.onActivate(); return nil }
func (i *trayItem) ContextMenu(int32, int32) *dbus.Error       { return nil }
func (i *trayItem) Scroll(int32, string) *dbus.Error           { return nil }

func isX11Session() bool {
	switch strings.ToLower(os.Getenv("XDG_SESSION_TYPE")) {
	case "x11":
		return true
	case "":
		return os.Getenv("DISPLAY") != "" && os.Getenv("WAYLAND_DISPLAY") == ""
	}
	return false
}

func runBackgroundTrayController(background bool, parent context.Context, cancel context.CancelFunc, store *settingsStore) bool {
	if !isX11Session() {
		return false
	}
	go func() {
		if err := runX11StatusTray(parent, cancel); err != nil && parent.Err() == nil {
			log.Printf("System tray unavailable: %v", err)
		}
	}()
	// The host runtime still runs on the main goroutine.
	return false
}

func openSettingsWebUI() {
	url := fmt.Sprintf("http://127.0.0.1:%d", webuiPort)
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		log.Printf("Open Web UI failed: %v", err)
	}
}

func runX11StatusTray(ctx context.Context, quit context.CancelFunc) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("session bus: %w", err)
	}
	defer conn.Close()

	menu := &trayMenu{rev: 1}
	menu.status, menu.detail = trayStatusText(activeInputDevice(), currentVideoStatus())
	menu.onClick = func(id int32) {
		switch id {
		case trayIDSettings:
			openSettingsWebUI()
		case trayIDQuit:
			log.Printf("Quit requested from tray")
			quit()
		}
	}
	item := &trayItem{onActivate: openSettingsWebUI}

	if err := conn.Export(menu, trayMenuPath, trayMenuIface); err != nil {
		return err
	}
	if err := conn.Export(item, trayItemPath, trayItemIface); err != nil {
		return err
	}
	if _, err := prop.Export(conn, trayMenuPath, prop.Map{trayMenuIface: {
		"Version":        {Value: uint32(3)},
		"TextDirection":  {Value: "ltr"},
		"Status":         {Value: "normal"},
		"IconThemePaths": {Value: []string{}},
	}}); err != nil {
		return err
	}
	tooltip := func(title, desc string) trayToolTip {
		return trayToolTip{Title: title, Desc: desc}
	}
	itemProps, err := prop.Export(conn, trayItemPath, prop.Map{trayItemIface: {
		"Category":   {Value: "ApplicationStatus"},
		"Id":         {Value: "drawsplay-host"},
		"Title":      {Value: "DrawSplay"},
		"Status":     {Value: "Active"},
		"WindowId":   {Value: uint32(0)},
		"IconName":   {Value: ""},
		"IconPixmap": {Value: trayIconPixmaps()},
		"ItemIsMenu": {Value: false},
		"Menu":       {Value: trayMenuPath},
		"ToolTip":    {Value: tooltip("DrawSplay", menu.status)},
	}})
	if err != nil {
		return err
	}

	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	if reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("could not own %s: %v", name, err)
	}

	registered := false
	register := func() {
		var hasOwner bool
		call := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, trayWatcherName)
		if call.Err != nil || call.Store(&hasOwner) != nil || !hasOwner {
			registered = false
			return
		}
		if registered {
			return
		}
		watcher := conn.Object(trayWatcherName, trayWatcherPath)
		if err := watcher.Call(trayWatcherName+".RegisterStatusNotifierItem", 0, name).Err; err != nil {
			log.Printf("Tray registration failed: %v", err)
			return
		}
		registered = true
		log.Printf("System tray status menu registered")
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	register()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			register()
			status, detail := trayStatusText(activeInputDevice(), currentVideoStatus())
			menu.mu.Lock()
			changed := status != menu.status || detail != menu.detail
			if changed {
				menu.status, menu.detail = status, detail
				menu.rev++
			}
			rev := menu.rev
			menu.mu.Unlock()
			if changed {
				_ = conn.Emit(trayMenuPath, trayMenuIface+".LayoutUpdated", rev, trayIDRoot)
				itemProps.SetMust(trayItemIface, "ToolTip", tooltip("DrawSplay", status+"\n"+detail))
				_ = conn.Emit(trayItemPath, trayItemIface+".NewToolTip")
			}
		}
	}
}
