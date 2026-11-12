package main

// aoaStatusInfo describes the USB accessory link for the setup checklist.
type aoaStatusInfo struct {
	// State is one of: searching, file-transfer, manual-switch, switching, waiting-app, linked, error.
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

var aoaStatusState latest[aoaStatusInfo]

func setAOAStatus(state, detail string) {
	aoaStatusState.Set(aoaStatusInfo{State: state, Detail: detail})
}

func currentAOAStatus() aoaStatusInfo {
	if st := aoaStatusState.Get(); st.State != "" {
		return st
	}
	return aoaStatusInfo{State: "searching"}
}
