package main

// GitHub release lookups and size-limited downloads, shared by the updater,
// the Windows ffmpeg bootstrap and the Parsec fallback driver. Every request
// carries a context, a time limit, a size limit and an allowed-address check
// that also applies to redirects.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxReleaseJSONBytes bounds a release document (BtbN's lists hundreds of
// assets).
const maxReleaseJSONBytes = 8 << 20

type githubAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>" on newer uploads
}

type githubRelease struct {
	Tag         string        `json:"tag_name"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	PublishedAt time.Time     `json:"published_at"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	Assets      []githubAsset `json:"assets"`
}

func (r githubRelease) asset(name string) (githubAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return githubAsset{}, false
}

// githubURLAllowed limits release lookups and downloads to GitHub over
// HTTPS. Tests replace it.
var githubURLAllowed = func(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "github.com" || host == "api.github.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

// downloadClient is an HTTP client whose requests, redirects included, must
// pass allowed.
func downloadClient(timeout time.Duration, allowed func(*url.URL) bool) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if !allowed(req.URL) {
				return fmt.Errorf("refusing redirect to %s", req.URL.Redacted())
			}
			return nil
		},
	}
}

// downloadRequest is a GET for rawURL, refused unless allowed accepts it.
func downloadRequest(ctx context.Context, rawURL string, allowed func(*url.URL) bool) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !allowed(u) {
		return nil, fmt.Errorf("unexpected download address %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DrawSplay-host/"+hostVersion())
	return req, nil
}

// fetchGitHubRelease reads a release document from the GitHub API.
func fetchGitHubRelease(ctx context.Context, apiURL string) (githubRelease, error) {
	req, err := downloadRequest(ctx, apiURL, githubURLAllowed)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := downloadClient(30*time.Second, githubURLAllowed).Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return githubRelease{}, errors.New("no release has been published yet")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return githubRelease{}, fmt.Errorf("GitHub is limiting requests (HTTP %d); try again later", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return githubRelease{}, fmt.Errorf("GitHub answered HTTP %s", resp.Status)
	}
	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseJSONBytes)).Decode(&rel); err != nil {
		return githubRelease{}, fmt.Errorf("read release information: %w", err)
	}
	return rel, nil
}

// download is one size-limited download.
type download struct {
	URL     string
	Allowed func(*url.URL) bool // checked for the URL and every redirect
	Timeout time.Duration       // for the whole transfer
	Limit   int64               // refuse anything larger
	Size    int64               // the exact size expected, or 0 if unknown
}

// to copies the download into w.
func (d download) to(ctx context.Context, w io.Writer) error {
	if d.Size > d.Limit {
		return fmt.Errorf("the download is larger than %d MiB", d.Limit>>20)
	}
	req, err := downloadRequest(ctx, d.URL, d.Allowed)
	if err != nil {
		return err
	}
	resp, err := downloadClient(d.Timeout, d.Allowed).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, d.Limit+1))
	if err != nil {
		return err
	}
	if n > d.Limit {
		return fmt.Errorf("the download is larger than %d MiB", d.Limit>>20)
	}
	if d.Size > 0 && n != d.Size {
		return fmt.Errorf("got %d bytes, expected %d", n, d.Size)
	}
	return nil
}
