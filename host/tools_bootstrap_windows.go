//go:build windows

package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// ffmpeg is the only tool DrawSplay downloads. It always installs the newest
// release published for the machine's native architecture, verified against
// the SHA-256 the publisher's GitHub release reports for the archive:
//
//   - x64: gyan.dev "essentials" build (GyanD/codexffmpeg), falling back to
//     the newest BtbN stable win64 build;
//   - ARM64: the newest BtbN stable winarm64 build.
//
// adb is never downloaded: the adb tunnel is opt-in and users who enable it
// install platform-tools themselves.
const (
	gyanLatestReleaseAPI = "https://api.github.com/repos/GyanD/codexffmpeg/releases/latest"
	btbnLatestReleaseAPI = "https://api.github.com/repos/BtbN/FFmpeg-Builds/releases/latest"
	runtimeManifestName  = "drawsplay-runtime.json"
)

// legacyRuntimeHashes verifies runtimes installed by releases that pinned
// ffmpeg 8.0.1 and predate the install manifest.
var legacyRuntimeHashes = map[string]map[string]string{
	"ffmpeg": {
		"ffmpeg.exe": "5af82a0d4fe2b9eae211b967332ea97edfc51c6b328ca35b827e73eac560dc0d",
	},
}

type runtimeArchive struct {
	Version string
	URL     string
	SHA256  string
	Source  string
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

// runtimeManifest records what was installed and the SHA-256 of every
// executable and DLL, so a later resolve only trusts unmodified files.
type runtimeManifest struct {
	Tool    string            `json:"tool"`
	Version string            `json:"version"`
	Source  string            `json:"source"`
	URL     string            `json:"url"`
	Files   map[string]string `json:"files"`
}

// installBundledTool downloads the newest ffmpeg next to the host
// executable. It is only used when the user left the default bare tool name
// in settings and no existing tool was found.
func installBundledTool(tool string) (string, error) {
	if tool != "ffmpeg" {
		// Not auto-installed; resolveTool falls back to the configured name.
		return "", nil
	}
	exeDir, err := executableDir()
	if err != nil {
		return "", err
	}
	destDir := filepath.Join(exeDir, "runtime", "ffmpeg")
	if err := installFFmpeg(destDir); err != nil {
		return "", err
	}
	exe := filepath.Join(destDir, "ffmpeg.exe")
	if !fileExists(exe) {
		return "", fmt.Errorf("ffmpeg install finished but %s is missing", exe)
	}
	return exe, nil
}

// installFFmpeg installs the newest compatible ffmpeg release into destDir,
// replacing any previous runtime only once the new one is complete.
func installFFmpeg(destDir string) error {
	if err := requireSupportedWindows(); err != nil {
		return err
	}
	arch := windowsNativeArch()
	candidates, err := latestFFmpegArchives(arch)
	if err != nil {
		return err
	}
	var errs []error
	for _, archive := range candidates {
		if err := installRuntimeArchive("ffmpeg", destDir, archive); err != nil {
			log.Printf("ffmpeg %s from %s could not be installed: %v", archive.Version, archive.Source, err)
			errs = append(errs, fmt.Errorf("%s: %w", archive.Source, err))
			continue
		}
		log.Printf("Installed ffmpeg %s (%s, %s) in %s", archive.Version, archive.Source, arch, destDir)
		return nil
	}
	return errors.Join(errs...)
}

func requireSupportedWindows() error {
	if v := windows.RtlGetVersion(); v.MajorVersion < 10 {
		return fmt.Errorf("current ffmpeg builds need Windows 10 or newer (this is Windows %d.%d)", v.MajorVersion, v.MinorVersion)
	}
	return nil
}

// windowsNativeArch is the OS architecture, which can differ from the host
// binary's (an x64 host emulated on ARM64 Windows).
func windowsNativeArch() string {
	var processMachine, nativeMachine uint16
	if err := windows.IsWow64Process2(windows.CurrentProcess(), &processMachine, &nativeMachine); err == nil {
		switch nativeMachine {
		case 0xAA64: // IMAGE_FILE_MACHINE_ARM64
			return "arm64"
		case 0x8664: // IMAGE_FILE_MACHINE_AMD64
			return "amd64"
		case 0x014C: // IMAGE_FILE_MACHINE_I386
			return "386"
		}
	}
	return runtime.GOARCH
}

// latestFFmpegArchives lists the newest ffmpeg builds for arch in
// preference order. A source that cannot be reached is skipped.
func latestFFmpegArchives(arch string) ([]runtimeArchive, error) {
	type source struct {
		name   string
		api    string
		choose func(githubRelease) (runtimeArchive, error)
	}
	var sources []source
	switch arch {
	case "amd64":
		sources = []source{
			{"gyan.dev essentials", gyanLatestReleaseAPI, selectGyanEssentials},
			{"BtbN win64", btbnLatestReleaseAPI, func(r githubRelease) (runtimeArchive, error) { return selectBtbNStable(r, "amd64") }},
		}
	case "arm64":
		sources = []source{
			{"BtbN winarm64", btbnLatestReleaseAPI, func(r githubRelease) (runtimeArchive, error) { return selectBtbNStable(r, "arm64") }},
		}
	default:
		return nil, fmt.Errorf("no ffmpeg build is published for Windows %s; install ffmpeg yourself and put it on PATH", arch)
	}
	var found []runtimeArchive
	var errs []error
	for _, s := range sources {
		rel, err := fetchGitHubRelease(s.api)
		if err == nil {
			var archive runtimeArchive
			if archive, err = s.choose(rel); err == nil {
				archive.Source = s.name
				found = append(found, archive)
				continue
			}
		}
		log.Printf("ffmpeg source %s unavailable: %v", s.name, err)
		errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no ffmpeg release found: %w", errors.Join(errs...))
	}
	return found, nil
}

func fetchGitHubRelease(url string) (githubRelease, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DrawSplay")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("release lookup failed: %s", resp.Status)
	}
	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rel); err != nil {
		return githubRelease{}, fmt.Errorf("decode release: %w", err)
	}
	return rel, nil
}

var gyanEssentialsAsset = regexp.MustCompile(`^ffmpeg-(\d+(?:\.\d+)*)-essentials_build\.zip$`)

// selectGyanEssentials picks the essentials zip from gyan's latest release.
func selectGyanEssentials(rel githubRelease) (runtimeArchive, error) {
	for _, a := range rel.Assets {
		m := gyanEssentialsAsset.FindStringSubmatch(a.Name)
		if m == nil {
			continue
		}
		return archiveFromAsset(a, m[1])
	}
	return runtimeArchive{}, fmt.Errorf("release %s has no essentials zip", rel.TagName)
}

var btbnStableAsset = regexp.MustCompile(`^ffmpeg-n(\d+(?:\.\d+)*)-latest-(win64|winarm64)-gpl-(\d+(?:\.\d+)*)\.zip$`)

// selectBtbNStable picks the newest stable-branch (nX.Y, not master) GPL
// build for arch from BtbN's rolling "latest" release.
func selectBtbNStable(rel githubRelease, arch string) (runtimeArchive, error) {
	platform := map[string]string{"amd64": "win64", "arm64": "winarm64"}[arch]
	var best githubAsset
	bestVersion := ""
	for _, a := range rel.Assets {
		m := btbnStableAsset.FindStringSubmatch(a.Name)
		if m == nil || m[2] != platform {
			continue
		}
		if bestVersion == "" || compareVersions(m[1], bestVersion) > 0 {
			best, bestVersion = a, m[1]
		}
	}
	if bestVersion == "" {
		return runtimeArchive{}, fmt.Errorf("release %s has no stable %s GPL build", rel.TagName, platform)
	}
	return archiveFromAsset(best, bestVersion)
}

func archiveFromAsset(a githubAsset, version string) (runtimeArchive, error) {
	digest, ok := strings.CutPrefix(strings.ToLower(a.Digest), "sha256:")
	if !ok || len(digest) != sha256.Size*2 {
		return runtimeArchive{}, fmt.Errorf("%s has no SHA-256 digest to verify against", a.Name)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return runtimeArchive{}, fmt.Errorf("%s has an invalid SHA-256 digest", a.Name)
	}
	if !strings.HasPrefix(a.URL, "https://github.com/") {
		return runtimeArchive{}, fmt.Errorf("%s has an unexpected download URL %q", a.Name, a.URL)
	}
	return runtimeArchive{Version: version, URL: a.URL, SHA256: digest}, nil
}

// compareVersions compares dotted numeric versions ("9.0" < "9.0.2" < "10").
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// installRuntimeArchive downloads, verifies and extracts the archive's bin/
// directory into a staging directory, then swaps it into destDir.
func installRuntimeArchive(tool, destDir string, archive runtimeArchive) error {
	log.Printf("Downloading %s %s (%s) from %s", tool, archive.Version, archive.Source, archive.URL)
	zipPath, err := downloadArchive(tool, archive.URL, archive.SHA256)
	if err != nil {
		return err
	}
	staging := destDir + ".new"
	if err := os.RemoveAll(staging); err != nil {
		os.Remove(zipPath)
		return fmt.Errorf("clear staging directory: %w", err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		os.Remove(zipPath)
		return err
	}
	err = extractZipSubset(zipPath, staging, func(name string) (string, bool) {
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
	if err == nil && !fileExists(filepath.Join(staging, tool+".exe")) {
		err = fmt.Errorf("archive has no %s.exe", tool)
	}
	if err == nil {
		err = writeRuntimeManifest(staging, runtimeManifest{
			Tool: tool, Version: archive.Version, Source: archive.Source, URL: archive.URL,
		})
	}
	if err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.RemoveAll(destDir); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("clear previous %s runtime: %w", tool, err)
	}
	if err := os.Rename(staging, destDir); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("activate %s runtime: %w", tool, err)
	}
	return nil
}

func isRuntimeBinary(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".exe" || ext == ".dll"
}

func writeRuntimeManifest(dir string, m runtimeManifest) error {
	m.Files = map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isRuntimeBinary(path) {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return err
		}
		m.Files[filepath.ToSlash(rel)] = digest
		return nil
	})
	if err != nil {
		return fmt.Errorf("hash runtime files: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, runtimeManifestName), data, 0o644)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Runtime auto-download directories are never trusted merely because an
// executable is present. Verify the executable and its DLLs against the
// install manifest (or the legacy pinned hashes) before using it.
func verifyBundledToolCandidate(candidate, tool string) bool {
	exeDir, err := executableDir()
	if err != nil {
		return false
	}
	runtimeRoot := filepath.Join(exeDir, "runtime", tool)
	if !isPathWithin(candidate, runtimeRoot) {
		return true // packaged, non-auto-downloaded bundle
	}
	hashes, err := runtimeExpectedHashes(runtimeRoot, candidate, tool)
	if err != nil {
		log.Printf("Ignoring unverified bundled %s runtime (%s): %v", tool, candidate, err)
		return false
	}
	for name, digest := range hashes {
		path := filepath.Join(runtimeRoot, filepath.FromSlash(name))
		if err := verifyFileSHA256(path, digest); err != nil {
			log.Printf("Ignoring unverified bundled %s runtime (%s): %v", tool, path, err)
			return false
		}
	}
	return true
}

// runtimeExpectedHashes returns the files to verify for candidate: the
// candidate itself plus every DLL it may load from the runtime directory.
func runtimeExpectedHashes(runtimeRoot, candidate, tool string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(runtimeRoot, runtimeManifestName))
	if errors.Is(err, os.ErrNotExist) {
		if legacy := legacyRuntimeHashes[tool]; legacy != nil {
			return legacy, nil
		}
		return nil, errors.New("no install manifest")
	}
	if err != nil {
		return nil, err
	}
	var m runtimeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("read install manifest: %w", err)
	}
	rel, err := filepath.Rel(runtimeRoot, candidate)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	digest, ok := m.Files[rel]
	if !ok {
		return nil, fmt.Errorf("%s is not in the install manifest", rel)
	}
	want := map[string]string{rel: digest}
	for name, d := range m.Files {
		if strings.EqualFold(filepath.Ext(name), ".dll") {
			want[name] = d
		}
	}
	return want, nil
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
