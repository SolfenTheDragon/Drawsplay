package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), installManifestName)
	if _, ok, err := readInstallManifest(path); ok || err != nil {
		t.Fatalf("missing manifest: ok=%v err=%v, want false/nil", ok, err)
	}
	want := installManifest{
		SchemaVersion: installManifestVersion,
		InstallDir:    `C:\Program Files\DrawSplay`,
		HostExe:       `C:\Program Files\DrawSplay\DrawSplay.exe`,
		VDDDriver:     "installed",
		WinUSB:        map[string]string{"18d1:2d01": "installed"},
	}
	if err := writeJSONAtomic(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := readInstallManifest(path)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if got.HostExe != want.HostExe || got.VDDDriver != want.VDDDriver || got.WinUSB["18d1:2d01"] != "installed" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestWinUSBAttemptPersistsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	if winusbAttemptRecorded(dir, "04e8:6860") {
		t.Fatal("fresh state reports an attempt")
	}
	if err := recordWinUSBAttempt(dir, "04E8:6860", "failed: rejected", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !winusbAttemptRecorded(dir, "04e8:6860") {
		t.Fatal("recorded attempt not found (keys must be case-insensitive)")
	}
	if winusbAttemptRecorded(dir, "18d1:2d01") {
		t.Fatal("unrelated identity reported as attempted")
	}
}

func TestIsUninstallerExeName(t *testing.T) {
	for path, want := range map[string]bool{
		`C:\Program Files\DrawSplay\DrawSplay-Uninstall.exe`: true,
		`C:\Temp\DrawSplay-Uninstall-1234.exe`:               true,
		`C:\Program Files\DrawSplay\DrawSplay.exe`:           false,
	} {
		if got := isUninstallerExeName(path); got != want {
			t.Errorf("isUninstallerExeName(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIsInstallerExeName(t *testing.T) {
	for path, want := range map[string]bool{
		`C:\Users\me\Downloads\drawsplay_installer_v1.2.3.exe`:     true,
		`C:\Users\me\Downloads\drawsplay_installer_v1.2.3 (1).exe`: true,
		`C:\Program Files\DrawSplay\DrawSplay.exe`:                 false,
		`C:\Program Files\DrawSplay\DrawSplay-Uninstall.exe`:       false,
	} {
		if got := isInstallerExeName(path); got != want {
			t.Errorf("isInstallerExeName(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestPathInsideDir(t *testing.T) {
	dir := filepath.Join("base", "DrawSplay")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join("base", "DrawSplay", "DrawSplay.exe"), true},
		{filepath.Join("BASE", "drawsplay", "x.exe"), true},
		{dir, true},
		{filepath.Join("base", "DrawSplayOther", "x.exe"), false},
		{"", false},
	}
	for _, c := range cases {
		if got := pathInsideDir(c.path, dir); got != c.want {
			t.Errorf("pathInsideDir(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestClassifyWinUSBInf(t *testing.T) {
	superDisplay := "; kelocube_aoap.inf\n[Version]\nProvider = %ManufacturerName%\n[Standard.NTamd64]\n%DeviceName% = USB_Install,USB\\VID_18D1&PID_2D01\n[USB_Install]\nInclude=winusb.inf\n"
	zadig := "; Android.inf\n; Copyright (c) 2010-2023 Pete Batard <pete@akeo.ie> (GNU LGPL)\n; libwdi generated\n[Version]\nProvider = \"libwdi\"\n[Devices.NTamd64]\n\"Android\" = USB_Install, USB\\VID_18D1&PID_2D00\n[USB_Install]\nInclude = winusb.inf\n"
	zadigOther := strings.Replace(zadig, "VID_18D1&PID_2D00", "VID_04E8&PID_6860", 1)
	samsung := "; ssudadb.inf\n[Version]\nProvider=%SAMSUNG%\n%Desc% = Install, USB\\VID_18D1&PID_2D01&ADB\n"
	cases := []struct {
		name string
		text string
		want winusbInfOwner
	}{
		{"drawsplay generated", aoaWinUSBPackageInf(aoaWinUSBHardwareIDs(nil), time.Now()), winusbInfDrawSplay},
		{"zadig for aoa", zadig, winusbInfZadigAOA},
		{"zadig for other device", zadigOther, winusbInfOther},
		{"other vendor aoa driver", superDisplay, winusbInfOther},
		{"vendor non-winusb driver", samsung, winusbInfOther},
	}
	for _, c := range cases {
		if got := classifyWinUSBInf(c.text); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsParsecVDDInf(t *testing.T) {
	if !isParsecVDDInf("[Standard.NTamd64]\n%DeviceName% = ParsecVDADevice_Install, Root\\Parsec\\VDA\n") {
		t.Error("Parsec VDA display INF not recognized")
	}
	if isParsecVDDInf("; parsecvusba.inf\nProvider=Parsec Cloud, Inc.\n%Desc% = Install, ROOT\\Parsec\\VUSBA\n") {
		t.Error("Parsec virtual USB adapter INF must not match")
	}
}
