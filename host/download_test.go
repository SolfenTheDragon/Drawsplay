package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDownloadLimits(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("0123456789")) })
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/file", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	local := func(u *url.URL) bool { return u.Host == strings.TrimPrefix(srv.URL, "http://") }
	get := func(path string, limit, size int64) (string, error) {
		var buf bytes.Buffer
		d := download{URL: srv.URL + path, Allowed: local, Timeout: 5 * time.Second, Limit: limit, Size: size}
		err := d.to(context.Background(), &buf)
		return buf.String(), err
	}

	if body, err := get("/file", 10, 10); err != nil || body != "0123456789" {
		t.Fatalf("download = %q, %v", body, err)
	}
	if _, err := get("/file", 9, 0); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("over the limit: err = %v", err)
	}
	if _, err := get("/file", 100, 11); err == nil || !strings.Contains(err.Error(), "expected 11") {
		t.Errorf("wrong size: err = %v", err)
	}
	if _, err := get("/away", 100, 0); err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Errorf("redirect to another host: err = %v", err)
	}
	d := download{URL: "http://elsewhere.invalid/file", Allowed: local, Timeout: time.Second, Limit: 100}
	if err := d.to(context.Background(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unexpected download address") {
		t.Errorf("disallowed address: err = %v", err)
	}
}
