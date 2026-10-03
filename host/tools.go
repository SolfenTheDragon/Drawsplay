package main

// External tool discovery: resolving adb and ffmpeg.
//
// Production builds can bundle both tools next to DrawSplay.exe. When the
// configured value is still the default bare tool name ("adb"/"ffmpeg"), the
// bundled copy is preferred first so end users do not need global installs.
// A configured path (flag or web UI) is still honored when it works; if not,
// well-known per-OS install locations are probed.

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveTool returns a runnable path for the named tool. Resolution order:
//
//  1. Bundled runtime copy (when configured is default bare name).
//  2. The configured value (absolute path, or a name found on PATH).
//  3. Well-known install locations for the current OS.
//  4. Auto-install bundled runtime copy (when configured is default bare
//     name and no tool was found).
//
// If nothing is found the configured value is returned unchanged so
// downstream errors name what the user asked for.
func resolveTool(configured, tool string, allowAutoInstall bool) string {
	preferBundled := preferBundledTool(configured, tool)
	if preferBundled {
		for _, candidate := range bundledToolPaths(tool) {
			if fileExists(candidate) && verifyBundledToolCandidate(candidate, tool) {
				log.Printf("Using bundled %s at %s", tool, candidate)
				return candidate
			}
		}
	}
	if _, err := exec.LookPath(configured); err == nil {
		return configured
	}
	for _, candidate := range wellKnownToolPaths(tool) {
		if fileExists(candidate) {
			log.Printf("%s not on PATH; using %s", tool, candidate)
			return candidate
		}
	}
	if preferBundled && allowAutoInstall {
		if installed, err := installBundledTool(tool); err == nil && installed != "" {
			log.Printf("Installed bundled %s at %s", tool, installed)
			return installed
		} else if err != nil {
			log.Printf("Bundled %s auto-install failed: %v", tool, err)
		}
	}
	return configured
}

func preferBundledTool(configured, tool string) bool {
	// If user/config supplied a path, respect it first.
	if strings.ContainsAny(configured, `\/`) || filepath.IsAbs(configured) {
		return false
	}
	if strings.EqualFold(configured, tool) {
		return true
	}
	if runtime.GOOS == "windows" && strings.EqualFold(configured, tool+".exe") {
		return true
	}
	return false
}

func bundledToolPaths(tool string) []string {
	exeDir, err := executableDir()
	if err != nil {
		return nil
	}
	exe := tool
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	switch tool {
	case "adb":
		return []string{
			filepath.Join(exeDir, "runtime", "adb", exe),
			filepath.Join(exeDir, "adb", exe),
			filepath.Join(exeDir, exe),
		}
	case "ffmpeg":
		return []string{
			filepath.Join(exeDir, "runtime", "ffmpeg", exe),
			filepath.Join(exeDir, "runtime", "ffmpeg", "bin", exe),
			filepath.Join(exeDir, "ffmpeg", "bin", exe),
			filepath.Join(exeDir, exe),
		}
	default:
		return nil
	}
}

func executableDir() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// wellKnownToolPaths lists per-OS candidate locations for a tool binary.
func wellKnownToolPaths(tool string) []string {
	exe := tool
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	var paths []string
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("ProgramFiles")
		switch tool {
		case "adb":
			paths = append(paths,
				filepath.Join(local, "Android", "Sdk", "platform-tools", exe),
				filepath.Join(programFiles, "Android", "platform-tools", exe),
				filepath.Join(home, "scoop", "shims", exe),
				filepath.Join(`C:\platform-tools`, exe),
			)
		case "ffmpeg":
			paths = append(paths,
				filepath.Join(programFiles, "ffmpeg", "bin", exe),
				filepath.Join(home, "scoop", "shims", exe),
				filepath.Join(`C:\ffmpeg\bin`, exe),
				filepath.Join(`C:\ProgramData\chocolatey\bin`, exe),
			)
			// winget installs ffmpeg under a versioned directory; glob it.
			if local != "" {
				pattern := filepath.Join(local, "Microsoft", "WinGet", "Packages",
					"Gyan.FFmpeg*", "ffmpeg-*", "bin", exe)
				if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
					paths = append(paths, matches...)
				}
			}
		}
		return paths
	}
	// Linux
	switch tool {
	case "adb":
		paths = append(paths,
			filepath.Join(home, "Android", "Sdk", "platform-tools", exe),
			"/usr/lib/android-sdk/platform-tools/adb",
			"/usr/local/bin/adb",
			"/usr/bin/adb",
			"/snap/bin/adb",
		)
	case "ffmpeg":
		paths = append(paths,
			"/usr/local/bin/ffmpeg",
			"/usr/bin/ffmpeg",
			"/snap/bin/ffmpeg",
		)
	}
	return paths
}
