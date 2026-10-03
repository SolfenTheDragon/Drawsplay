//go:build windows

package main

import (
	"net/http"
)

func (api webUIAPI) handleVDDCustomResolutionSetup(w http.ResponseWriter, _ *http.Request) {
	cfg := api.store.get()
	if err := ensureVDDCustomResolutionWritable(cfg.Width, cfg.Height); err != nil {
		httpError(w, http.StatusUnprocessableEntity, "VDD custom-resolution admin setup failed: %v", err)
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"width":   cfg.Width,
		"height":  cfg.Height,
		"message": "VDD custom-resolution registry access is now configured for this host user.",
	})
}
