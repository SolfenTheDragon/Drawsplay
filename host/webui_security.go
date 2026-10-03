package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// getOnly rejects any method other than GET for read-only endpoints.
func getOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h(w, r)
	}
}

// securePost guards a state-changing endpoint: POST only, plus the CSRF
// token and local-origin checks (see requireCSRF).
func securePost(csrfToken string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !requireCSRF(w, r, csrfToken) {
			return
		}
		h(w, r)
	}
}

func newCSRFToken() string {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic(fmt.Sprintf("generate CSRF token: %v", err))
	}
	return hex.EncodeToString(token[:])
}

func requireCSRF(w http.ResponseWriter, r *http.Request, expected string) bool {
	provided := r.Header.Get("X-DrawSplay-CSRF")
	if len(provided) != len(expected) ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		httpError(w, http.StatusForbidden, "missing or invalid CSRF token")
		return false
	}
	if site := strings.ToLower(r.Header.Get("Sec-Fetch-Site")); site == "cross-site" {
		httpError(w, http.StatusForbidden, "cross-site request rejected")
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || !sameLocalAuthority(u.Host, r.Host) {
		httpError(w, http.StatusForbidden, "request origin is not the local web UI")
		return false
	}
	return true
}

func sameLocalAuthority(originHost, requestHost string) bool {
	if !strings.EqualFold(originHost, requestHost) {
		return false
	}
	host := originHost
	if h, _, err := net.SplitHostPort(originHost); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
