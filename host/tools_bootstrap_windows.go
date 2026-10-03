//go:build windows

package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	adbZipURL       = "https://dl.google.com/android/repository/platform-tools_r37.0.0-win.zip"
	adbZipSHA256    = "4fe305812db074cea32903a489d061eb4454cbc90a49e8fea677f4b7af764918"
	ffmpegZipURL    = "https://www.gyan.dev/ffmpeg/builds/packages/ffmpeg-8.0.1-essentials_build.zip"
	ffmpegZipSHA256 = "e2aaeaa0fdbc397d4794828086424d4aaa2102cef1fb6874f6ffd29c0b88b673"
)

var bundledRuntimeHashes = map[string]map[string]string{
	"adb": {
		"adb.exe":             "957e46b8615f7af5b7292a2ddabe98d2e61940c3fb2b0545756507f080613e71",
		"AdbWinApi.dll":       "120bef587119c6cb926b86b9be90fdfbce38937588eae28cd91a94ce63c7b965",
		"AdbWinUsbApi.dll":    "6ca69a2ca0e31309c087d288f058977d421ad03500e4c3e1dbd981241a069c60",
		"libwinpthread-1.dll": "283a8c3f8d1542df333e0899757b00d484888978d62ccf8c99db662a95cce764",
	},
	"ffmpeg": {
		"ffmpeg.exe": "5af82a0d4fe2b9eae211b967332ea97edfc51c6b328ca35b827e73eac560dc0d",
	},
}

// installBundledTool downloads and installs a bundled runtime copy of the
// requested tool next to the host executable. It is only used when the user
// left the default bare tool name in settings and no existing tool was found.
func installBundledTool(tool string) (string, error) {
	exeDir, err := executableDir()
	if err != nil {
		return "", err
	}
	switch tool {
	case "adb":
		destDir := filepath.Join(exeDir, "runtime", "adb")
		if err := installADB(destDir); err != nil {
			return "", err
		}
		exe := filepath.Join(destDir, "adb.exe")
		if !fileExists(exe) {
			return "", fmt.Errorf("adb install finished but %s is missing", exe)
		}
		return exe, nil
	case "ffmpeg":
		destDir := filepath.Join(exeDir, "runtime", "ffmpeg")
		if err := installFFmpeg(destDir); err != nil {
			return "", err
		}
		exe := filepath.Join(destDir, "ffmpeg.exe")
		if !fileExists(exe) {
			return "", fmt.Errorf("ffmpeg install finished but %s is missing", exe)
		}
		return exe, nil
	default:
		return "", fmt.Errorf("unsupported bundled tool %q", tool)
	}
}

func installADB(destDir string) error {
	logInstallStart("adb", adbZipURL)
	zipPath, err := downloadArchive("adb-platform-tools", adbZipURL, adbZipSHA256)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(destDir); err != nil {
		os.Remove(zipPath)
		return fmt.Errorf("clear previous adb runtime: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		os.Remove(zipPath)
		return err
	}
	prefixes := []string{"platform-tools/"}
	return extractZipSubset(zipPath, destDir, func(name string) (string, bool) {
		n := filepath.ToSlash(name)
		for _, p := range prefixes {
			if strings.HasPrefix(n, p) {
				rel := strings.TrimPrefix(n, p)
				if rel == "" || strings.HasSuffix(rel, "/") {
					return "", false
				}
				return rel, true
			}
		}
		return "", false
	})
}

func installFFmpeg(destDir string) error {
	logInstallStart("ffmpeg", ffmpegZipURL)
	zipPath, err := downloadArchive("ffmpeg-essentials", ffmpegZipURL, ffmpegZipSHA256)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(destDir); err != nil {
		os.Remove(zipPath)
		return fmt.Errorf("clear previous ffmpeg runtime: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		os.Remove(zipPath)
		return err
	}
	return extractZipSubset(zipPath, destDir, func(name string) (string, bool) {
		n := filepath.ToSlash(name)
		i := strings.Index(n, "/bin/")
		if i < 0 {
			return "", false
		}
		rel := strings.TrimPrefix(n[i+len("/bin/"):], "/")
		if rel == "" || strings.HasSuffix(rel, "/") {
			return "", false
		}
		return rel, true
	})
}

// Runtime auto-download directories are never trusted merely because an
// executable is present. Verify the executable and its load-time DLLs before
// resolving a previously installed copy for use.
func verifyBundledToolCandidate(candidate, tool string) bool {
	exeDir, err := executableDir()
	if err != nil {
		return false
	}
	runtimeRoot := filepath.Join(exeDir, "runtime", tool)
	if !isPathWithin(candidate, runtimeRoot) {
		return true // packaged, non-auto-downloaded bundle
	}
	hashes := bundledRuntimeHashes[tool]
	for name, digest := range hashes {
		path := filepath.Join(runtimeRoot, name)
		if err := verifyFileSHA256(path, digest); err != nil {
			log.Printf("Ignoring unverified bundled %s runtime (%s): %v", tool, path, err)
			return false
		}
	}
	return true
}

func logInstallStart(tool, url string) {
	log.Printf("%s not found; downloading bundled runtime from %s", tool, url)
}

func downloadArchive(prefix, url, expectedSHA256 string) (string, error) {
	tmp, err := os.CreateTemp(os.TempDir(), prefix+"-*.zip")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := verifyFileSHA256(tmpPath, expectedSHA256); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("download integrity verification failed: %w", err)
	}
	return tmpPath, nil
}

func extractZipSubset(zipPath, destRoot string, mapName func(name string) (rel string, ok bool)) error {
	defer os.Remove(zipPath)

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	extractedAny := false
	for _, f := range r.File {
		rel, ok := mapName(f.Name)
		if !ok {
			continue
		}
		target := filepath.Join(destRoot, filepath.FromSlash(rel))
		if !isPathWithin(target, destRoot) {
			return errors.New("archive path traversal attempt blocked")
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
		extractedAny = true
	}
	if !extractedAny {
		return fmt.Errorf("archive did not contain expected files")
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func isPathWithin(path, root string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
