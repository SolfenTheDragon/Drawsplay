//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExtractLinuxBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "b.tar.gz")
	writeTestTarGz(t, bundle, map[string]string{
		"DrawSplay-linux-v1.0.0/" + linuxBundleHostName:      "#!/bin/sh\necho v1.0.0\n",
		"DrawSplay-linux-v1.0.0/70-drawsplay-aoa.rules":      "aoa",
		"DrawSplay-linux-v1.0.0/install-drawsplay-host.sh":   "ignored",
		"DrawSplay-linux-v1.0.0/" + linuxBundleIconName:      "png",
		"DrawSplay-linux-v1.0.0/../../escape/" + "something": "ignored",
	})
	hostDir := t.TempDir()
	newHost, files, err := extractLinuxBundle(bundle, hostDir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(newHost) != hostDir {
		t.Fatalf("new host written to %s, want a file in %s (same filesystem as the install)", newHost, hostDir)
	}
	fi, err := os.Stat(newHost)
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("new host must be executable: %v %v", fi, err)
	}
	if string(files["70-drawsplay-aoa.rules"]) != "aoa" || string(files[linuxBundleIconName]) != "png" || len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	if err := checkHostVersion(newHost, "v1.0.0"); err != nil {
		t.Fatalf("checkHostVersion: %v", err)
	}
	if err := checkHostVersion(newHost, "v2.0.0"); err == nil {
		t.Fatal("checkHostVersion must reject a binary reporting another version")
	}
}

func TestExtractLinuxBundleNeedsHost(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "b.tar.gz")
	writeTestTarGz(t, bundle, map[string]string{"x/70-drawsplay-aoa.rules": "aoa"})
	if _, _, err := extractLinuxBundle(bundle, t.TempDir()); err == nil || !strings.Contains(err.Error(), linuxBundleHostName) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateInstallSupportNeedsInstalledCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ok, note := updateInstallSupport()
	if ok || !strings.Contains(note, "release page") {
		t.Fatalf("a test binary is not the installed host: ok=%v note=%q", ok, note)
	}
}

func TestApplyUpdateReplacesInstalledHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target, err := linuxInstalledHostPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), updateBundleName("v1.0.1"))
	newHost := "#!/bin/sh\necho v1.0.1\n"
	writeTestTarGz(t, bundle, map[string]string{"DrawSplay-linux-v1.0.1/" + linuxBundleHostName: newHost})

	res, err := applyUpdate(bundle, "v1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != newHost {
		t.Fatalf("installed host = %q, want the new one", got)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".drawsplay-host-update-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
	stopped := false
	res.restart(func() { stopped = true })
	restartMu.Lock()
	exe := restartExe
	restartExe = ""
	restartMu.Unlock()
	if !stopped || exe != target {
		t.Fatalf("restart: stopped=%v exe=%q, want a graceful stop then exec of %s", stopped, exe, target)
	}

	// A bundle whose host reports another version leaves the install alone.
	writeTestTarGz(t, bundle, map[string]string{"x/" + linuxBundleHostName: "#!/bin/sh\necho v0.0.1\n"})
	if _, err := applyUpdate(bundle, "v1.0.2"); err == nil {
		t.Fatal("applyUpdate must refuse a host that reports the wrong version")
	}
	if got, _ := os.ReadFile(target); string(got) != newHost {
		t.Fatal("a refused update must not touch the installed host")
	}
}
