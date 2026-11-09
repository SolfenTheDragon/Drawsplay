//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The release bundle and the names install-host-linux.sh installs from it.
const (
	linuxBundleHostName = "drawsplay-host-linux-amd64"
	linuxBundleIconName = "drawsplay-tray.png"
	maxBundledHostBytes = 256 << 20
	maxBundledFileBytes = 1 << 20
)

func updateBundleName(tag string) string { return "DrawSplay-linux-" + tag + ".tar.gz" }

// linuxInstalledHostPath is where install-host-linux.sh puts the host.
func linuxInstalledHostPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin", "drawsplay-host"), nil
}

// linuxSystemFiles are the bundled files the install script copies into
// system folders with sudo. The updater cannot write them; it only reports
// when a release changes one that is installed (or, if required, missing).
var linuxSystemFiles = []struct {
	name, target string
	required     bool
}{
	{"99-drawsplay-uinput.rules", "/etc/udev/rules.d/99-drawsplay-uinput.rules", true},
	{"70-drawsplay-aoa.rules", "/etc/udev/rules.d/70-drawsplay-aoa.rules", true},
	{"70-drawsplay-drm-connector.rules", "/etc/udev/rules.d/70-drawsplay-drm-connector.rules", false},
	{"drawsplay-display-helper", "/usr/libexec/drawsplay/drawsplay-display-helper", false},
	{"com.drawsplay.display-helper.policy", "/usr/share/polkit-1/actions/com.drawsplay.display-helper.policy", false},
}

func updateInstallSupport() (bool, string) {
	const download = " Download the update from the release page instead."
	if runtime.GOARCH != "amd64" {
		return false, "Releases include a Linux host for x86-64 only." + download
	}
	target, err := linuxInstalledHostPath()
	if err != nil {
		return false, "Cannot find your home folder." + download
	}
	if !sameFile(currentExecutable(), target) {
		return false, "This copy is not the one install-host-linux.sh installed (" + target + ")." + download
	}
	if unix.Access(filepath.Dir(target), unix.W_OK) != nil {
		return false, filepath.Dir(target) + " is not writable." + download
	}
	return true, "Replaces " + target + " and restarts DrawSplay. A connected tablet reconnects after a few seconds."
}

func currentExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// applyUpdate puts the new host binary in place of the installed one. The
// running process keeps its old, now unlinked, file until it re-executes.
func applyUpdate(bundlePath, tag string) (applyResult, error) {
	target, err := linuxInstalledHostPath()
	if err != nil {
		return applyResult{}, err
	}
	newHost, files, err := extractLinuxBundle(bundlePath, filepath.Dir(target))
	if err != nil {
		return applyResult{}, err
	}
	defer os.Remove(newHost) // gone already once renamed

	if err := checkHostVersion(newHost, tag); err != nil {
		return applyResult{}, err
	}
	if err := os.Rename(newHost, target); err != nil {
		return applyResult{}, fmt.Errorf("replace %s: %w", target, err)
	}
	return applyResult{
		warnings: installLinuxUserFiles(files),
		restart: func(stop func()) {
			requestRestartExec(target)
			stop()
		},
	}, nil
}

// extractLinuxBundle writes the host binary from the tarball to a temporary
// file in hostDir (so it can be renamed over the installed one) and reads the
// other small bundled files into memory.
func extractLinuxBundle(bundlePath, hostDir string) (string, map[string][]byte, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("open bundle: %w", err)
	}
	defer gz.Close()

	wanted := map[string]bool{linuxBundleIconName: true}
	for _, sf := range linuxSystemFiles {
		wanted[sf.name] = true
	}
	files := map[string][]byte{}
	newHost := ""
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			removeIfSet(newHost)
			return "", nil, fmt.Errorf("read bundle: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Base(hdr.Name)
		switch {
		case name == linuxBundleHostName && newHost == "":
			newHost, err = writeBundledHost(tr, hostDir)
			if err != nil {
				return "", nil, err
			}
		case wanted[name]:
			data, err := io.ReadAll(io.LimitReader(tr, maxBundledFileBytes+1))
			if err != nil || len(data) > maxBundledFileBytes {
				removeIfSet(newHost)
				return "", nil, fmt.Errorf("read %s from bundle: too large or unreadable", name)
			}
			files[name] = data
		}
	}
	if newHost == "" {
		return "", nil, fmt.Errorf("the bundle has no %s", linuxBundleHostName)
	}
	return newHost, files, nil
}

func writeBundledHost(r io.Reader, dir string) (string, error) {
	out, err := os.CreateTemp(dir, ".drawsplay-host-update-*")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxBundledHostBytes+1))
	if err == nil && n > maxBundledHostBytes {
		err = errors.New("the bundled host is too large")
	}
	if err == nil {
		err = out.Chmod(0o755)
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(out.Name())
		return "", fmt.Errorf("extract the new host: %w", err)
	}
	return out.Name(), nil
}

func removeIfSet(path string) {
	if path != "" {
		os.Remove(path)
	}
}

// checkHostVersion runs the new binary once to make sure it starts on this
// system and is the release it claims to be.
func checkHostVersion(bin, tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-version").Output()
	if err != nil {
		return fmt.Errorf("the new host does not start: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("the new host reports version %q, expected %s", got, tag)
	}
	return nil
}

// installLinuxUserFiles updates the menu icon (in the user's home) and lists
// the system files that need the install script to update them.
func installLinuxUserFiles(files map[string][]byte) []string {
	var warnings []string
	if icon, ok := files[linuxBundleIconName]; ok {
		if home, err := os.UserHomeDir(); err == nil {
			target := filepath.Join(home, ".local", "share", "icons", "hicolor", "64x64", "apps", "drawsplay-host.png")
			if old, err := os.ReadFile(target); err == nil && !bytes.Equal(old, icon) {
				if err := writeFileAtomic(target, icon, 0o644); err != nil {
					log.Printf("Update: menu icon: %v", err)
				}
			}
		}
	}
	var changed []string
	for _, sf := range linuxSystemFiles {
		data, ok := files[sf.name]
		if !ok {
			continue
		}
		old, err := os.ReadFile(sf.target)
		switch {
		case err != nil && sf.required:
			changed = append(changed, sf.name)
		case err == nil && !bytes.Equal(old, data):
			changed = append(changed, sf.name)
		}
	}
	if len(changed) > 0 {
		warnings = append(warnings, "This release changes system files the settings page cannot update: "+
			strings.Join(changed, ", ")+". Download the Linux release and run ./install-drawsplay-host-linux.sh to install them (it asks for your password).")
	}
	return warnings
}

var (
	restartMu  sync.Mutex
	restartExe string
)

func requestRestartExec(exe string) {
	restartMu.Lock()
	restartExe = exe
	restartMu.Unlock()
}

// execPendingRestart replaces this process with the updated host, keeping
// the command line, once main has shut everything down. File descriptors are
// close-on-exec, so the instance lock and the web UI port are released.
func execPendingRestart() {
	restartMu.Lock()
	exe := restartExe
	restartMu.Unlock()
	if exe == "" {
		return
	}
	log.Printf("Restarting into %s", exe)
	args := append([]string{exe}, os.Args[1:]...)
	if err := syscall.Exec(exe, args, os.Environ()); err != nil {
		log.Printf("Restart after update failed: %v; start DrawSplay again from the menu", err)
	}
}
