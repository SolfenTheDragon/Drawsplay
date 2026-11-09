package main

// Self-update from GitHub releases, driven by the settings page.
//
// The page asks GET /api/update for the update state. When automatic checks
// are on (settings.UpdateCheck), that request also starts a check if the
// last one is older than updateCheckInterval; POST /api/update/check checks
// now. A check reads the repository's latest release from the GitHub API and
// compares its vMAJOR.MINOR.PATCH tag with this build's version.
//
// POST /api/update/install downloads this platform's release bundle, verifies
// it against the SHA256SUMS.txt published with the release, and hands it to
// the platform's applyUpdate (update_linux.go, update_windows.go), which
// replaces the installed host and restarts it. Development builds and copies
// that were not installed by the installer only link to the release page.
//
// Before restarting, the old process records what it was doing in
// update-pending.json; the new process reads it so the page can say whether
// the update took.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	releaseRepo         = "SolfenTheDragon/Drawsplay"
	updateCheckInterval = 6 * time.Hour
	// A manual check within this long of the last one answers from the last
	// result (GitHub allows 60 unauthenticated API requests an hour).
	updateManualMinInterval = 30 * time.Second
	updateChecksumsName     = "SHA256SUMS.txt"
	updatePendingName       = "update-pending.json"
	maxReleaseJSONBytes     = 2 << 20
	maxChecksumsBytes       = 64 << 10
	maxBundleBytes          = 1 << 30
)

// latestReleaseURL is a variable so tests can point it at a local server.
var latestReleaseURL = "https://api.github.com/repos/" + releaseRepo + "/releases/latest"

// updateURLAllowed limits downloads to GitHub. Tests replace it.
var updateURLAllowed = func(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "github.com" || host == "api.github.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type releaseInfo struct {
	Tag         string         `json:"tag_name"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	PublishedAt time.Time      `json:"published_at"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	Assets      []releaseAsset `json:"assets"`
}

func (r releaseInfo) asset(name string) (releaseAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return releaseAsset{}, false
}

// Update states shown by the settings page.
const (
	updateIdle        = "idle"        // never checked
	updateChecking    = "checking"    // asking GitHub
	updateCurrent     = "current"     // this is the newest release
	updateAvailable   = "available"   // a newer release exists
	updateDevBuild    = "dev"         // development build: versions cannot be compared
	updateDownloading = "downloading" // fetching and verifying the bundle
	updateInstalling  = "installing"  // the installer is replacing this copy
	updateRestarting  = "restarting"  // replaced; restarting into the new version
	updateFailed      = "failed"      // the last check or install failed
)

type updateStatus struct {
	State       string    `json:"state"`
	Current     string    `json:"current"`
	Latest      string    `json:"latest,omitempty"`
	ReleaseURL  string    `json:"releaseURL,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
	CheckedAt   time.Time `json:"checkedAt,omitzero"`
	AutoCheck   bool      `json:"autoCheck"`
	// CanInstall is true when this copy can install the update itself;
	// InstallNote says why not, or what installing involves.
	CanInstall  bool   `json:"canInstall"`
	InstallNote string `json:"installNote,omitempty"`
	Downloaded  int64  `json:"downloaded,omitempty"`
	Total       int64  `json:"total,omitempty"`
	Error       string `json:"error,omitempty"`
	// LastResult reports the update that restarted this process, if any.
	LastResult *updateResult `json:"lastResult,omitempty"`
}

// updateResult is what update-pending.json turns into after the restart.
type updateResult struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings,omitempty"`
}

type updatePending struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	At       time.Time `json:"at"`
	Warnings []string  `json:"warnings,omitempty"`
}

type updater struct {
	mu        sync.Mutex
	st        updateStatus
	release   releaseInfo
	lastCheck time.Time
	busy      bool // a check or install is running
	client    *http.Client
	loaded    bool // update-pending.json has been read
}

var hostUpdater = newUpdater()

// exitHost ends the whole host process gracefully (main's root context). An
// update uses it instead of the web UI's stop, which in Windows tray mode
// stops only the runtime and leaves the tray process running.
var exitHost func()

func newUpdater() *updater {
	return &updater{
		st: updateStatus{State: updateIdle, Current: hostVersion()},
		client: &http.Client{
			Timeout: 30 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				if !updateURLAllowed(req.URL) {
					return fmt.Errorf("refusing redirect to %s", req.URL.Redacted())
				}
				return nil
			},
		},
	}
}

// status returns the current state. When autoCheck is on and the last check
// is stale, it starts a check in the background.
func (u *updater) status(autoCheck bool) updateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.loadPendingLocked()
	u.st.AutoCheck = autoCheck
	if autoCheck && !u.busy && time.Since(u.lastCheck) > updateCheckInterval {
		u.beginCheckLocked()
		go u.runCheck(context.Background())
	}
	return u.st
}

// checkNow runs a check unless one ran moments ago or work is in progress.
func (u *updater) checkNow(ctx context.Context) updateStatus {
	u.mu.Lock()
	if u.busy || time.Since(u.lastCheck) < updateManualMinInterval {
		st := u.st
		u.mu.Unlock()
		return st
	}
	u.beginCheckLocked()
	u.mu.Unlock()
	u.runCheck(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func (u *updater) beginCheckLocked() {
	u.busy = true
	u.lastCheck = time.Now()
	u.st.State = updateChecking
	u.st.Error = ""
}

func (u *updater) runCheck(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, err := u.fetchLatestRelease(ctx)

	u.mu.Lock()
	defer u.mu.Unlock()
	u.busy = false
	u.st.CheckedAt = time.Now()
	if err != nil {
		log.Printf("Update check failed: %v", err)
		u.st.State = updateFailed
		u.st.Error = "Could not check for updates: " + err.Error()
		return
	}
	u.release = rel
	u.st.Latest = rel.Tag
	u.st.ReleaseURL = rel.HTMLURL
	u.st.Notes = rel.Body
	u.st.PublishedAt = rel.PublishedAt
	u.st.CanInstall, u.st.InstallNote = false, ""

	current, currentOK := parseReleaseVersion(u.st.Current)
	latest, latestOK := parseReleaseVersion(rel.Tag)
	switch {
	case !latestOK:
		u.st.State = updateFailed
		u.st.Error = fmt.Sprintf("The latest release has an unexpected tag %q.", rel.Tag)
	case !currentOK:
		u.st.State = updateDevBuild
		u.st.InstallNote = "This is a development build, so it cannot tell whether the release is newer. Install a release to get updates here."
	case !latest.newerThan(current):
		u.st.State = updateCurrent
	default:
		u.st.State = updateAvailable
		u.st.CanInstall, u.st.InstallNote = u.installability(rel)
		log.Printf("Update available: %s (running %s)", rel.Tag, u.st.Current)
	}
}

// installability says whether this copy can install rel itself.
func (u *updater) installability(rel releaseInfo) (bool, string) {
	ok, note := updateInstallSupport()
	if !ok {
		return false, note
	}
	bundle := updateBundleName(rel.Tag)
	if _, found := rel.asset(bundle); !found {
		return false, fmt.Sprintf("The release has no %s download.", bundle)
	}
	if _, found := rel.asset(updateChecksumsName); !found {
		return false, "The release has no " + updateChecksumsName + " to verify the download with."
	}
	return true, note
}

func (u *updater) fetchLatestRelease(ctx context.Context) (releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DrawSplay-host/"+hostVersion())
	resp, err := u.client.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return releaseInfo{}, errors.New("no release has been published yet")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return releaseInfo{}, fmt.Errorf("GitHub is limiting requests (HTTP %d); try again later", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return releaseInfo{}, fmt.Errorf("GitHub answered HTTP %s", resp.Status)
	}
	var rel releaseInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseJSONBytes)).Decode(&rel); err != nil {
		return releaseInfo{}, fmt.Errorf("read release information: %w", err)
	}
	if rel.Draft || rel.Prerelease {
		return releaseInfo{}, errors.New("the latest release is a draft or pre-release")
	}
	return rel, nil
}

// startInstall begins downloading and installing the available update.
// stop shuts this host down gracefully (the web UI's stop function).
func (u *updater) startInstall(stop func()) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy {
		return errors.New("an update check or install is already running")
	}
	if u.st.State != updateAvailable || !u.st.CanInstall {
		return errors.New("no update is ready to install")
	}
	u.busy = true
	u.st.State = updateDownloading
	u.st.Error = ""
	u.st.Downloaded, u.st.Total = 0, 0
	rel := u.release
	go func() {
		err := u.install(rel, stop)
		if err == nil {
			return
		}
		log.Printf("Update to %s failed: %v", rel.Tag, err)
		u.mu.Lock()
		u.busy = false
		// Back to "available" so the page can offer to try again.
		u.st.State = updateAvailable
		u.st.Error = "The update did not install: " + err.Error()
		u.mu.Unlock()
	}()
	return nil
}

func (u *updater) install(rel releaseInfo, stop func()) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	bundleName := updateBundleName(rel.Tag)
	bundle, _ := rel.asset(bundleName)
	sums, _ := rel.asset(updateChecksumsName)

	var sumsText strings.Builder
	if err := u.download(ctx, sums, &sumsText, maxChecksumsBytes, false); err != nil {
		return fmt.Errorf("download %s: %w", updateChecksumsName, err)
	}
	want, ok := parseSHA256Sums(sumsText.String())[bundleName]
	if !ok {
		return fmt.Errorf("%s does not list %s", updateChecksumsName, bundleName)
	}

	removeStaleUpdateDirs()
	dir, err := os.MkdirTemp("", updateDirPattern)
	if err != nil {
		return err
	}
	// The Windows installer still runs from dir after this returns; the
	// next update removes the folder.
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, bundleName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = u.download(ctx, bundle, f, maxBundleBytes, true)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", bundleName, err)
	}
	if err := verifyFileSHA256(path, want); err != nil {
		return fmt.Errorf("verify %s: %w", bundleName, err)
	}
	log.Printf("Update %s downloaded and verified; installing", rel.Tag)

	u.mu.Lock()
	u.st.State = updateInstalling
	from := u.st.Current
	u.mu.Unlock()

	res, err := applyUpdate(path, rel.Tag)
	if err != nil {
		return err
	}
	keep = res.keepDir
	if err := writeUpdatePending(updatePending{From: from, To: rel.Tag, At: time.Now().UTC(), Warnings: res.warnings}); err != nil {
		log.Printf("Record pending update: %v", err)
	}

	u.mu.Lock()
	u.st.State = updateRestarting
	u.mu.Unlock()
	log.Printf("Update to %s installed; restarting", rel.Tag)
	// Let the page see "restarting" before the server goes away.
	time.Sleep(500 * time.Millisecond)
	if res.restart != nil {
		res.restart(stop)
	}
	return nil
}

const updateDirPattern = "drawsplay-update-*"

// removeStaleUpdateDirs deletes download folders left by earlier updates.
func removeStaleUpdateDirs() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), updateDirPattern))
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && time.Since(fi.ModTime()) > time.Hour {
			_ = os.RemoveAll(d)
		}
	}
}

// applyResult is what a platform's applyUpdate hands back.
type applyResult struct {
	warnings []string
	keepDir  bool         // the download folder is still in use (installer)
	restart  func(func()) // shuts this host down into the new version
}

// download fetches a release asset into w, refusing anything over limit.
func (u *updater) download(ctx context.Context, a releaseAsset, w io.Writer, limit int64, progress bool) error {
	src, err := url.Parse(a.URL)
	if err != nil || !updateURLAllowed(src) {
		return fmt.Errorf("unexpected download address %q", a.URL)
	}
	if a.Size > limit {
		return fmt.Errorf("%s is larger than %d MiB", a.Name, limit>>20)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "DrawSplay-host/"+hostVersion())
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if progress {
		u.mu.Lock()
		u.st.Total = max(a.Size, resp.ContentLength, 0)
		u.mu.Unlock()
		w = &progressWriter{w: w, u: u}
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s is larger than %d MiB", a.Name, limit>>20)
	}
	if a.Size > 0 && n != a.Size {
		return fmt.Errorf("got %d bytes, expected %d", n, a.Size)
	}
	return nil
}

type progressWriter struct {
	w io.Writer
	u *updater
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.u.mu.Lock()
	p.u.st.Downloaded += int64(n)
	p.u.mu.Unlock()
	return n, err
}

// releaseVersion is a parsed vMAJOR.MINOR.PATCH tag.
type releaseVersion [3]int

var releaseVersionRE = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// parseReleaseVersion accepts only release tags, the form release.yml
// creates; "dev" builds and anything with a suffix do not parse.
func parseReleaseVersion(s string) (releaseVersion, bool) {
	m := releaseVersionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return releaseVersion{}, false
	}
	var v releaseVersion
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return releaseVersion{}, false
		}
		v[i] = n
	}
	return v, true
}

func (v releaseVersion) newerThan(o releaseVersion) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] > o[i]
		}
	}
	return false
}

// parseSHA256Sums reads sha256sum output ("<hex>  <name>", or "<hex> *<name>"
// for binary mode) into name -> lowercase digest.
func parseSHA256Sums(text string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return out
}

func updatePendingPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, updatePendingName), nil
}

func writeUpdatePending(p updatePending) error {
	path, err := updatePendingPath()
	if err != nil {
		return err
	}
	return writeJSONAtomic(path, p)
}

// loadPendingLocked turns a pending-update record left by the previous
// process into LastResult, once.
func (u *updater) loadPendingLocked() {
	if u.loaded {
		return
	}
	u.loaded = true
	path, err := updatePendingPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	var p updatePending
	if json.Unmarshal(data, &p) != nil || time.Since(p.At) > time.Hour {
		return
	}
	u.st.LastResult = &updateResult{From: p.From, To: p.To, OK: u.st.Current == p.To, Warnings: p.Warnings}
}
