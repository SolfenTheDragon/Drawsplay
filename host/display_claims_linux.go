package main

// Shared display claims (Linux).
//
// The Android host (this program) and DrawSplay Desktop's dd-host can run on
// the same machine, and each cleans up virtual displays: forced HDMI
// connectors, vkms/VIRTUAL RandR outputs and wlroots headless outputs. Without
// coordination each would remove the other's display. Both therefore record
// what they hold in one file and leave alone anything another live process
// has claimed.
//
// File: $XDG_RUNTIME_DIR/drawsplay/display-claims.json (falls back to
// /tmp/drawsplay-<uid>/), guarded by flock on display-claims.lock next to it.
// Format, shared with desktop/host/src/display_claims.rs:
//
//	{"claims":[{"owner":"android-host","pid":123,"comm":"drawsplay-host","kind":"output","name":"HDMI-1"}]}
//
// kind is "output" (a RandR or Wayland output in use) or "connector" (a
// spare HDMI connector forced on; the name is its RandR output name). A claim
// counts only while /proc/<pid>/comm still equals comm, so a crashed owner
// never blocks cleanup.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	displayClaimOwner     = "android-host"
	displayClaimOutput    = "output"
	displayClaimConnector = "connector"
)

type displayClaim struct {
	Owner string `json:"owner"`
	PID   int    `json:"pid"`
	Comm  string `json:"comm"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
}

type displayClaimFile struct {
	Claims []displayClaim `json:"claims"`
}

// displayClaimsDir can be overridden by tests.
var displayClaimsDir = func() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "drawsplay")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("drawsplay-%d", os.Getuid()))
}

func processComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c displayClaim) live() bool {
	return c.PID > 0 && c.Comm != "" && processComm(c.PID) == c.Comm
}

// withDisplayClaims runs fn on the claims under the lock; when fn returns
// true the (pruned) list is written back.
func withDisplayClaims(fn func(*displayClaimFile) bool) error {
	dir := displayClaimsDir()
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" && filepath.Dir(dir) == runtimeDir {
		if err := ensurePrivateClaimDir(runtimeDir); err != nil {
			return fmt.Errorf("unsafe XDG_RUNTIME_DIR: %w", err)
		}
	}
	if err := ensurePrivateClaimDir(dir); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, "display-claims.lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	var lockStat unix.Stat_t
	if err := unix.Fstat(fd, &lockStat); err != nil {
		lock.Close()
		return err
	}
	if lockStat.Mode&unix.S_IFMT != unix.S_IFREG || int(lockStat.Uid) != os.Getuid() {
		lock.Close()
		return errors.New("display-claims lock is not a regular file owned by this user")
	}
	if err := lock.Chmod(0o600); err != nil {
		lock.Close()
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	path := filepath.Join(dir, "display-claims.json")
	var f displayClaimFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	kept := f.Claims[:0]
	for _, c := range f.Claims {
		if c.live() {
			kept = append(kept, c)
		}
	}
	f.Claims = kept
	if !fn(&f) {
		return nil
	}
	return writeJSONAtomic(path, f)
}

func ensurePrivateClaimDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by this user", path)
	}
	return os.Chmod(path, 0o700)
}

// claimDisplay records that this process holds the display.
func claimDisplay(kind, name string) {
	if name == "" {
		return
	}
	me := displayClaim{Owner: displayClaimOwner, PID: os.Getpid(), Comm: processComm(os.Getpid()), Kind: kind, Name: name}
	err := withDisplayClaims(func(f *displayClaimFile) bool {
		for _, c := range f.Claims {
			if c.PID == me.PID && c.Kind == kind && c.Name == name {
				return false
			}
		}
		f.Claims = append(f.Claims, me)
		return true
	})
	if err != nil {
		log.Printf("Display claims: could not record %s %s: %v", kind, name, err)
	}
}

// releaseDisplayClaim drops this process's claim on the display.
func releaseDisplayClaim(kind, name string) {
	_ = withDisplayClaims(func(f *displayClaimFile) bool {
		kept := f.Claims[:0]
		changed := false
		for _, c := range f.Claims {
			if c.PID == os.Getpid() && c.Kind == kind && c.Name == name {
				changed = true
				continue
			}
			kept = append(kept, c)
		}
		f.Claims = kept
		return changed
	})
}

// displayClaimedByOther reports whether another live process (dd-host, or a
// second host) holds the display in any kind. An unreadable file claims nothing.
func displayClaimedByOther(name string) bool {
	claimed := false
	_ = withDisplayClaims(func(f *displayClaimFile) bool {
		for _, c := range f.Claims {
			if c.Name == name && c.PID != os.Getpid() {
				claimed = true
			}
		}
		return false
	})
	return claimed
}
