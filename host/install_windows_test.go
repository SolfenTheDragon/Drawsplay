//go:build windows

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAOAAccessoryIDs(t *testing.T) {
	seen := map[[2]uint16]bool{}
	for _, id := range aoaAccessoryIDs() {
		if seen[id] {
			t.Fatalf("duplicate target %04x:%04x", id[0], id[1])
		}
		seen[id] = true
	}
	for pid := uint16(aoaPIDFirst); pid <= aoaPIDLast; pid++ {
		if !seen[[2]uint16{aoaGoogleVID, pid}] {
			t.Errorf("missing accessory identity 18d1:%04x", pid)
		}
	}
}

func TestHelperTaskXML(t *testing.T) {
	x := helperTaskXML(`C:\Program Files\DrawSplay\DrawSplay.exe`, "", "Bind <driver>", "-winusb-bind-attached")
	for _, want := range []string{"<UserId></UserId>", "<Arguments>-winusb-bind-attached</Arguments>", "Bind &lt;driver&gt;", "<RunLevel>HighestAvailable</RunLevel>"} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML missing %q", want)
		}
	}
}

func TestUninstallDepsPlanRequest(t *testing.T) {
	cases := []struct {
		name         string
		plan         uninstallDepsPlan
		removeShared bool
		want         uninstallRequest
	}{
		{"owned vdd removed without asking", uninstallDepsPlan{VDDPresent: true, VDDOwned: true}, false, uninstallRequest{RemoveVDD: true}},
		{"owned vdd kept when parsec app uses it", uninstallDepsPlan{VDDPresent: true, VDDOwned: true, ParsecApp: true}, false, uninstallRequest{}},
		{"pre-existing vdd kept by default", uninstallDepsPlan{VDDPresent: true}, false, uninstallRequest{}},
		{"shared removed when confirmed", uninstallDepsPlan{VDDPresent: true, ZadigINFs: []string{"oem1.inf"}}, true, uninstallRequest{RemoveVDD: true, RemoveZadig: true}},
		{"no vdd present", uninstallDepsPlan{}, true, uninstallRequest{}},
	}
	for _, c := range cases {
		if got := c.plan.request(c.removeShared); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDecodeINFTextUTF16(t *testing.T) {
	data := []byte{0xFF, 0xFE, 'W', 0, 'i', 0, 'n', 0}
	if got := decodeINFText(data); got != "Win" {
		t.Fatalf("got %q", got)
	}
}

func TestEncodeTaskXMLIsUTF16LEWithBOM(t *testing.T) {
	got := encodeTaskXML("<a>é</a>")
	want := []byte{0xFF, 0xFE, '<', 0, 'a', 0, '>', 0, 0xE9, 0, '<', 0, '/', 0, 'a', 0, '>', 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeTaskXML = % x, want % x", got, want)
	}
}

// The task definition is never written outside the protected staging
// folder: when that folder cannot be made, nothing is registered.
func TestRegisterHelperTaskRefusesWithoutProtectedStaging(t *testing.T) {
	defer func(old func() (string, error)) { helperTaskStageDir = old }(helperTaskStageDir)
	helperTaskStageDir = func() (string, error) { return "", errors.New("not elevated") }
	err := registerHelperTask(`\DrawSplay\Test Task That Must Not Exist`, helperTaskXML(`C:\x.exe`, "", "t", ""))
	if err == nil || !strings.Contains(err.Error(), "stage task definition") {
		t.Fatalf("registerHelperTask = %v, want a staging error", err)
	}
}
