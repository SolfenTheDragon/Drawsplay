package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// displayHelperPath is where the Linux installer puts the root-side helper
// (packaging/linux/drawsplay-display-helper) and its polkit action
// com.drawsplay.display-helper. Through pkexec, the password prompt then
// names that action instead of asking to run a shell as root.
const displayHelperPath = "/usr/libexec/drawsplay/drawsplay-display-helper"

// displayHelper returns the helper's path when it is installed safely: a
// regular file owned by root and writable by nobody else. Otherwise callers
// fall back to the generic `pkexec sh -c` command.
func displayHelper() (string, bool) {
	return displayHelperAt(displayHelperPath)
}

func displayHelperAt(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return "", false
	}
	return path, true
}

// embeddedDisplayHelper is a copy of packaging/linux/drawsplay-display-helper
// (a test keeps them identical). When the installed helper is missing or
// older and lacks an action, the host runs this copy through pkexec instead,
// so a fix button works without reinstalling DrawSplay first.
//
//go:embed linuxhelper/drawsplay-display-helper
var embeddedDisplayHelper string

// helperCurrentFn reports whether the installed helper is this host's own
// version. Only then is it used: an older one may know an action by name but
// do it the old way (an older vkms-load loads vkms without holding it from
// the X server), so anything else runs the embedded copy.
var helperCurrentFn = func(helper string) bool {
	data, err := os.ReadFile(helper)
	return err == nil && string(data) == embeddedDisplayHelper
}

// runDisplayHelperFn is replaced in tests: nothing there may run as root.
var runDisplayHelperFn = runDisplayHelperAction

// runDisplayHelperAction runs one display-helper action as root: directly
// when the host is root, through passwordless sudo, or, when mayPrompt is
// set, through one polkit password prompt (the installed helper when it is
// current, otherwise the embedded copy). Without mayPrompt and without root
// it fails with errNeedsRoot. It returns the helper's combined output.
func runDisplayHelperAction(mayPrompt bool, args ...string) (string, error) {
	env := append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/sbin:/sbin")
	run := func(argv ...string) (string, error) {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return strings.TrimSpace(out.String()), err
	}
	wrap := func(out string, err error) (string, error) {
		if err != nil {
			return out, fmt.Errorf("%s: %w (%s)", strings.Join(args, " "), err, lastLines(out, 3))
		}
		return out, nil
	}
	// bash -c SCRIPT NAME ARGS...: NAME becomes $0, as for the installed file.
	embedded := append([]string{"bash", "-c", embeddedDisplayHelper, "drawsplay-display-helper"}, args...)
	if os.Geteuid() == 0 {
		return wrap(run(embedded...))
	}
	if sudo, err := exec.LookPath("sudo"); err == nil {
		// -n never prompts: a sudo that would need a password refuses at
		// once with a "sudo: ..." message and runs nothing. Any other
		// failure came from the helper itself.
		out, err := run(append([]string{sudo, "-n"}, embedded...)...)
		if err == nil {
			return out, nil
		}
		if !strings.HasPrefix(out, "sudo:") {
			return wrap(out, err)
		}
	}
	if !mayPrompt {
		return "", fmt.Errorf("%s: %w", args[0], errNeedsRoot)
	}
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return "", errors.New("pkexec is not available, so DrawSplay cannot ask for your password; install polkit (pkexec) and try again")
	}
	var out string
	if helper, ok := displayHelper(); ok && helperCurrentFn(helper) {
		out, err = run(append([]string{pkexec, helper}, args...)...)
	} else {
		out, err = run(append([]string{pkexec}, embedded...)...)
	}
	var ee *exec.ExitError
	// 126: the user dismissed the prompt or is not authorized.
	if errors.As(err, &ee) && ee.ExitCode() == 126 {
		return out, errPasswordPromptDismissed
	}
	return wrap(out, err)
}

// lastLines keeps the end of a command's output for an error message.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
