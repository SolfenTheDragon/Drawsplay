package main

import "sync"

// aoaStatusInfo describes the USB accessory link for the setup checklist.
type aoaStatusInfo struct {
	// State is one of: searching, switching, waiting-app, linked, error.
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

var aoaStatusState struct {
	sync.RWMutex
	value aoaStatusInfo
}

func setAOAStatus(state, detail string) {
	aoaStatusState.Lock()
	aoaStatusState.value = aoaStatusInfo{State: state, Detail: detail}
	aoaStatusState.Unlock()
}

func currentAOAStatus() aoaStatusInfo {
	aoaStatusState.RLock()
	defer aoaStatusState.RUnlock()
	if aoaStatusState.value.State == "" {
		return aoaStatusInfo{State: "searching"}
	}
	return aoaStatusState.value
}
