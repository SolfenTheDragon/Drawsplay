//go:build !windows

package main

import "net/http"

func (api webUIAPI) handleVDDCustomResolutionSetup(w http.ResponseWriter, _ *http.Request) {
	httpError(w, http.StatusNotImplemented, "VDD custom-resolution setup is only available on Windows hosts")
}
