//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVerifyEmbeddedIDDPackage(t *testing.T) {
	dir := t.TempDir()
	if _, err := extractEmbeddedDriver(dir); err != nil {
		t.Fatal(err)
	}
	if err := verifyEmbeddedIDDPackage(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DrawSplayIdd.dll", "DrawSplayIdd.inf", "DrawSplayIdd.cat"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("replacement"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := verifyEmbeddedIDDPackage(dir); err == nil {
				t.Fatal("accepted driver files that differ from the embedded package")
			}
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInstallLocallySignedIDD(t *testing.T) {
	for _, failure := range []string{"", "sign", "stage", "device", "rollback"} {
		t.Run(failure, func(t *testing.T) {
			var calls []string
			ops := iddPackageInstallOps{
				sign: func() (string, error) {
					calls = append(calls, "sign")
					if failure == "sign" {
						return "", errors.New("signing failed")
					}
					return "certificate", nil
				},
				stage: func() error {
					calls = append(calls, "stage")
					if failure == "stage" || failure == "rollback" {
						return errors.New("staging failed")
					}
					return nil
				},
				device: func() (bool, error) {
					calls = append(calls, "device")
					if failure == "device" {
						return false, errors.New("device install failed")
					}
					return true, nil
				},
				removeCert: func(thumbprint string) error {
					if thumbprint != "certificate" {
						t.Fatalf("unexpected certificate selected for rollback: %q", thumbprint)
					}
					calls = append(calls, "remove")
					if failure == "rollback" {
						return errors.New("certificate cleanup failed")
					}
					return nil
				},
			}
			reboot, err := installLocallySignedIDD(ops)
			want := []string{"sign", "stage", "device"}
			switch failure {
			case "sign":
				want = []string{"sign"}
			case "stage", "rollback":
				want = []string{"sign", "stage", "remove"}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("operations = %v, want %v", calls, want)
			}
			if (err != nil) != (failure != "") {
				t.Fatalf("error = %v for failure %q", err, failure)
			}
			if failure == "" && !reboot {
				t.Fatal("lost the device install's reboot requirement")
			}
			if failure == "rollback" && (!strings.Contains(err.Error(), "staging failed") ||
				!strings.Contains(err.Error(), "certificate cleanup failed")) {
				t.Fatalf("lost staging or cleanup failure: %v", err)
			}
		})
	}
}

func TestInstallLocallySignedIDDPreservesExistingTrust(t *testing.T) {
	_, err := installLocallySignedIDD(iddPackageInstallOps{
		sign:  func() (string, error) { return "", nil },
		stage: func() error { return errors.New("rejected package") },
		device: func() (bool, error) {
			t.Fatal("attempted device install after rejected staging")
			return false, nil
		},
		removeCert: func(string) error {
			t.Fatal("attempted to remove existing certificate trust")
			return nil
		},
	})
	if err == nil {
		t.Fatal("staging rejection was not reported")
	}
}

func TestIDDSigningRequiresElevation(t *testing.T) {
	if isProcessElevated() {
		t.Skip("exercises the non-administrator entry point")
	}
	if _, err := signIDDDriverPackage(t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "administrator") {
		t.Fatalf("expected actionable administrator error, got %v", err)
	}
}

// Opt-in only: this creates machine-wide trust temporarily, never a driver device.
func TestIDDLocalSigningIntegration(t *testing.T) {
	if os.Getenv("DRAWSPLAY_TEST_DRIVER_SIGNING") != "1" {
		t.Skip("set DRAWSPLAY_TEST_DRIVER_SIGNING=1 in an elevated test process")
	}
	if !isProcessElevated() {
		t.Fatal("local signing integration test requires administrator approval")
	}
	dir, err := driverStageDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if _, err := extractEmbeddedDriver(dir); err != nil {
		t.Fatal(err)
	}
	thumbprint, err := signIDDDriverPackage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if thumbprint == "" {
		t.Skip("embedded catalog was already trusted; no local certificate generated")
	}
	t.Cleanup(func() {
		if err := removeIDDSigningCerts(thumbprint); err != nil {
			t.Error(err)
		}
	})
	script := "$ErrorActionPreference='Stop'; $thumb=$env:DRAWSPLAY_TEST_CERT; " +
		"foreach($name in 'Root','TrustedPublisher') { " +
		"if(-not (Test-Path ('Cert:\\LocalMachine\\'+$name+'\\'+$thumb))) { throw ('missing trust in '+$name) } }; " +
		"if(Test-Path ('Cert:\\LocalMachine\\My\\'+$thumb)) { throw 'private signing certificate retained' }; " +
		"$sig=Get-AuthenticodeSignature -FilePath $env:DRAWSPLAY_TEST_CAT; " +
		"if($sig.Status -ne 'Valid' -or $sig.SignerCertificate.Thumbprint -ne $thumb) { throw 'catalog is not trusted with the expected signer' }"
	cmd := exec.Command(windowsPowerShellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "DRAWSPLAY_TEST_CERT="+thumbprint,
		"DRAWSPLAY_TEST_CAT="+filepath.Join(dir, "DrawSplayIdd.cat"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("verify local certificate lifecycle: %v: %s", err, out)
	}
	got, err := signLocalDriverCatalog(dir, "", "DrawSplayIdd.cat", iddSignerSubject,
		"DrawSplay virtual display driver (this PC only)")
	if err != nil || got != "" {
		t.Fatalf("did not preserve already trusted catalog: thumbprint=%q error=%v", got, err)
	}
}

// Opt-in only: briefly adds a real desktop output. Run without an active session.
func TestIDDInstalledMonitorIntegration(t *testing.T) {
	if os.Getenv("DRAWSPLAY_TEST_INSTALLED_IDD") != "1" {
		t.Skip("set DRAWSPLAY_TEST_INSTALLED_IDD=1 to exercise an installed driver")
	}
	before, err := enumerateOutputs()
	if err != nil {
		t.Fatal(err)
	}
	dev, err := openIDDDevice()
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	id, output, err := plugIDDMonitor(dev, 1280, 720, 60, 1280, 720, attachedOutputNames(before))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dev.RemoveMonitor(id); err != nil {
			t.Error(err)
		}
	}()
	if !output.Attached || output.DeviceName == "" {
		t.Fatalf("driver did not create an attached desktop output: %+v", output)
	}
	t.Logf("installed driver created %s (%dx%d)", output.DeviceName,
		output.Rect.Right-output.Rect.Left, output.Rect.Bottom-output.Rect.Top)
}
