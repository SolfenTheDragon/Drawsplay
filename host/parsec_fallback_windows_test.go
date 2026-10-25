//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWindowsDriverSetupOnlyInstallsParsecWhenNeeded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		iddReady   bool
		parsecErr  error
		wantCalls  []string
		wantStatus string
	}{
		{"working IDD", true, nil, []string{"idd", "ready"}, "installed"},
		{"failed IDD", false, nil, []string{"idd", "ready", "parsec"}, "Parsec fallback: installed"},
		{"both failed", false, errors.New("download refused"), []string{"idd", "ready", "parsec"}, "mirror mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			result := setupWindowsDisplayDriver(
				func() string { calls = append(calls, "idd"); return "installed" },
				func() bool { calls = append(calls, "ready"); return tc.iddReady },
				func() (bool, error) {
					calls = append(calls, "parsec")
					return tc.parsecErr == nil, tc.parsecErr
				},
			)
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("setup calls = %v, want %v", calls, tc.wantCalls)
			}

			if !strings.Contains(result.status, tc.wantStatus) {
				t.Fatalf("setup status = %q, want %q", result.status, tc.wantStatus)
			}
			if result.parsecOwned != (!tc.iddReady && tc.parsecErr == nil) {
				t.Fatalf("incorrect Parsec ownership: %+v", result)
			}
		})
	}
}

func TestWindowsDisplayBackendFallbackOrder(t *testing.T) {
	for _, fail := range []int{0, 1, 2, 3} {
		var calls []int
		wantDisplay := &mirrorDisplay{}
		var candidates []func() (virtualDisplay, error)
		for i := range 3 {
			candidates = append(candidates, func() (virtualDisplay, error) {
				calls = append(calls, i)
				if i < fail {
					return nil, errors.New("unavailable")
				}
				return wantDisplay, nil
			})
		}
		got, err := firstWindowsDisplay(candidates...)
		wantCalls := []int{0}
		if fail >= 1 {
			wantCalls = append(wantCalls, 1)
		}
		if fail >= 2 {
			wantCalls = append(wantCalls, 2)
		}
		if !reflect.DeepEqual(calls, wantCalls) {
			t.Fatalf("fail=%d calls=%v, want %v", fail, calls, wantCalls)
		}
		if fail < 3 && (err != nil || got != wantDisplay) {
			t.Fatalf("fail=%d display=%v error=%v", fail, got, err)
		}
		if fail == 3 && err == nil {
			t.Fatal("all backend failures were hidden")
		}
	}
}

func TestParsecDisplayRemovalPayload(t *testing.T) {
	got, err := parsecRemovePayload(258)
	if err != nil || got[0] != 1 || got[1] != 2 || len(got) != 32 {
		t.Fatalf("payload=%v error=%v", got, err)
	}
	for _, index := range []int{-1, 65536} {
		if _, err := parsecRemovePayload(index); err == nil {
			t.Fatalf("accepted invalid display index %d", index)
		}
	}
}

func TestParsecOwnershipDoesNotFollowDrawSplayDriverOwnership(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	plan := planUninstallDeps(installManifest{VDDOwned: true})
	if plan.VDDOwned {
		t.Fatal("owning DrawSplay's IDD must not claim ownership of a shared Parsec VDD")
	}
	plan = planUninstallDeps(installManifest{ParsecVDDOwned: true})
	if !plan.VDDOwned {
		t.Fatal("newly installed Parsec fallback must remain owned on reinstall")
	}
}

func TestParsecInstallerRejectsUnexpectedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untrusted.exe")
	if err := os.WriteFile(path, []byte("not the pinned Parsec installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyParsecInstaller(path); err == nil {
		t.Fatal("accepted an unpinned installer")
	}
}

// Opt-in only: adds a real monitor on the shared Parsec adapter without
// installing, disabling, restarting or modifying either driver.
func TestParsecInstalledMonitorIntegration(t *testing.T) {
	if os.Getenv("DRAWSPLAY_TEST_INSTALLED_PARSEC") != "1" {
		t.Skip("set DRAWSPLAY_TEST_INSTALLED_PARSEC=1 with no active session")
	}
	before, err := enumerateOutputs()
	if err != nil {
		t.Fatal(err)
	}
	display, err := newDriverDisplay(videoOptions{Width: 1280, Height: 720, FPS: 60}, before, true)
	if err != nil {
		t.Fatal(err)
	}
	defer display.Close()
	if display.Target().Mirrored {
		t.Fatal("Parsec fallback did not create an extended display")
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := display.(*iddDisplay).Refresh(); err != nil {
		t.Fatalf("Parsec monitor did not survive keepalives: %v", err)
	}
	t.Log(display.Description())
}
