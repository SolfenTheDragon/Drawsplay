package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseReleaseVersion(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"v1.2.3", true}, {"v0.0.10", true}, {" v2.0.0\n", true},
		{"dev", false}, {"dev-abc1234", false}, {"v1.2", false}, {"1.2.3", false},
		{"v1.2.3-rc1", false}, {"v01.2.3", false},
	} {
		if _, ok := parseReleaseVersion(tc.in); ok != tc.ok {
			t.Errorf("parseReleaseVersion(%q) ok=%v, want %v", tc.in, ok, tc.ok)
		}
	}
	v := func(s string) releaseVersion { r, _ := parseReleaseVersion(s); return r }
	if !v("v1.10.0").newerThan(v("v1.9.9")) || v("v1.2.3").newerThan(v("v1.2.3")) || v("v1.2.3").newerThan(v("v2.0.0")) {
		t.Fatal("newerThan compares numerically, field by field")
	}
}

func TestParseSHA256Sums(t *testing.T) {
	a, b := strings.Repeat("ab", 32), strings.Repeat("CD", 32)
	got := parseSHA256Sums(a + "  DrawSplay-linux-v1.0.0.tar.gz\n" + b + " *DrawSplay.APK\nnot a line\n")
	if got["DrawSplay-linux-v1.0.0.tar.gz"] != a || got["DrawSplay.APK"] != strings.ToLower(b) || len(got) != 2 {
		t.Fatalf("parseSHA256Sums = %v", got)
	}
}

// fakeReleaseServer serves a latest-release document whose assets point back
// at the server: the platform bundle (with bundleBody) and SHA256SUMS.txt
// listing sumOf's digest for it.
func fakeReleaseServer(t *testing.T, tag, bundleBody, sumOf string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(sumOf))
	bundle := updateBundleName(tag)
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{
			Tag: tag, Body: "notes", HTMLURL: srv.URL + "/page",
			Assets: []githubAsset{
				{Name: bundle, URL: srv.URL + "/bundle", Size: int64(len(bundleBody))},
				{Name: updateChecksumsName, URL: srv.URL + "/sums"},
			},
		})
	})
	mux.HandleFunc("/bundle", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, bundleBody) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), bundle)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	oldURL, oldAllowed := latestReleaseURL, githubURLAllowed
	latestReleaseURL = srv.URL + "/latest"
	githubURLAllowed = func(u *url.URL) bool { return u.Host == strings.TrimPrefix(srv.URL, "http://") }
	t.Cleanup(func() { latestReleaseURL, githubURLAllowed = oldURL, oldAllowed })
	return srv
}

func withHostVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestUpdateCheckStates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	fakeReleaseServer(t, "v1.2.0", "bundle", "bundle")
	for _, tc := range []struct{ running, want string }{
		{"v1.1.9", updateAvailable},
		{"v1.2.0", updateCurrent},
		{"v1.3.0", updateCurrent},
		{"dev", updateDevBuild},
	} {
		withHostVersion(t, tc.running)
		u := newUpdater()
		st := u.checkNow(context.Background())
		if st.State != tc.want || st.Latest != "v1.2.0" || st.Notes != "notes" {
			t.Errorf("running %s: state=%q latest=%q error=%q, want %q", tc.running, st.State, st.Latest, st.Error, tc.want)
		}
		if tc.want == updateDevBuild && st.CanInstall {
			t.Errorf("a development build must not offer to install")
		}
	}
}

func TestUpdateInstallRejectsChecksumMismatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	fakeReleaseServer(t, "v9.0.0", "tampered bundle", "the real bundle")
	withHostVersion(t, "v1.0.0")
	u := newUpdater()
	rel, err := u.fetchLatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	err = u.install(rel, func() { stopped = true })
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("install error = %v, want a SHA-256 mismatch", err)
	}
	if stopped {
		t.Fatal("a failed update must not stop the host")
	}
}

func TestUpdateRefusesOtherHosts(t *testing.T) {
	for _, raw := range []string{
		"http://github.com/x", "https://example.com/x", "https://github.com.evil.test/x",
	} {
		u, _ := url.Parse(raw)
		if githubURLAllowed(u) {
			t.Errorf("githubURLAllowed(%s) = true", raw)
		}
	}
	for _, raw := range []string{
		"https://github.com/SolfenTheDragon/Drawsplay/releases/download/v1.0.0/SHA256SUMS.txt",
		"https://objects.githubusercontent.com/github-production-release-asset/1",
	} {
		u, _ := url.Parse(raw)
		if !githubURLAllowed(u) {
			t.Errorf("githubURLAllowed(%s) = false", raw)
		}
	}
}

func TestUpdatePendingBecomesLastResult(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	withHostVersion(t, "v2.0.0")
	if err := writeUpdatePending(updatePending{From: "v1.0.0", To: "v2.0.0", At: time.Now().UTC(), Warnings: []string{"w"}}); err != nil {
		t.Fatal(err)
	}
	st := newUpdater().status(false)
	if st.LastResult == nil || !st.LastResult.OK || st.LastResult.From != "v1.0.0" || len(st.LastResult.Warnings) != 1 {
		t.Fatalf("LastResult = %+v", st.LastResult)
	}
	if again := newUpdater().status(false); again.LastResult != nil {
		t.Fatal("the pending record is read once")
	}
}

func TestUpdateAPI(t *testing.T) {
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}, func() {}))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/update")
	if err != nil {
		t.Fatal(err)
	}
	var st updateStatus
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if st.Current == "" {
		t.Fatal("GET /api/update must report the running version")
	}
	token := testCSRFToken(t, srv.URL)
	res = protectedRequest(t, http.MethodPost, srv.URL, "/api/update/install", "", token)
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("install with no update ready: HTTP %d, want 409", res.StatusCode)
	}
	res = protectedRequest(t, http.MethodPost, srv.URL, "/api/update/install", "", "")
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("install without the CSRF token: HTTP %d, want 403", res.StatusCode)
	}
}
