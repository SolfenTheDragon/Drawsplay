package main

import (
	"strings"
	"sync"
)

type clientIdentity struct {
	Platform     string `json:"platform"`
	Type         string `json:"type"`
	Version      string `json:"version"`
	NativeWidth  int    `json:"nativeWidth,omitempty"`
	NativeHeight int    `json:"nativeHeight,omitempty"`
}

var clientIdentityState = struct {
	mu      sync.RWMutex
	session map[uint64]clientIdentity
	active  clientIdentity
}{
	session: make(map[uint64]clientIdentity),
}

func bindSessionClientIdentity(sessionID uint64, ident clientIdentity) {
	ident.Platform = strings.TrimSpace(ident.Platform)
	ident.Type = strings.TrimSpace(ident.Type)
	ident.Version = strings.TrimSpace(ident.Version)
	if ident.Platform == "" {
		ident.Platform = "unknown"
	}
	if ident.Type == "" {
		ident.Type = "unknown"
	}
	if ident.NativeWidth < 0 {
		ident.NativeWidth = 0
	}
	if ident.NativeHeight < 0 {
		ident.NativeHeight = 0
	}
	clientIdentityState.mu.Lock()
	clientIdentityState.session[sessionID] = ident
	clientIdentityState.active = ident
	clientIdentityState.mu.Unlock()
}

func unbindSessionClientIdentity(sessionID uint64) {
	clientIdentityState.mu.Lock()
	delete(clientIdentityState.session, sessionID)
	if len(clientIdentityState.session) == 0 {
		clientIdentityState.active = clientIdentity{}
	}
	clientIdentityState.mu.Unlock()
}

func activeClientIdentity() clientIdentity {
	clientIdentityState.mu.RLock()
	defer clientIdentityState.mu.RUnlock()
	return clientIdentityState.active
}
