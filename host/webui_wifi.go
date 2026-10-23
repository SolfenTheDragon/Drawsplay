package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"sort"
)

// wifiPairing is the host-side pairing service; set at startup.
var wifiPairing *wifiPairingService

type wifiPairingView struct {
	Enabled     bool             `json:"enabled"`
	Interface   string           `json:"interface"`
	Interfaces  []string         `json:"interfaces"`
	Paired      bool             `json:"paired"`
	PairedName  string           `json:"pairedName,omitempty"`
	Pairing     wifiPairingState `json:"pairing"`
	Unavailable string           `json:"unavailable,omitempty"`
}

// lanInterfaceNames lists interfaces usable for LAN discovery (up, multicast,
// not loopback or point-to-point, with an IPv4 address).
func lanInterfaceNames() []string {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var names []string
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 ||
			iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				names = append(names, iface.Name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

func (api webUIAPI) wifiView() wifiPairingView {
	cfg := api.store.get()
	view := wifiPairingView{
		Enabled:    cfg.PairedWifiEnabled && wifiFeatureEnabled(),
		Interface:  wifiPairedIface(api.store),
		Interfaces: lanInterfaceNames(),
		Pairing:    wifiPairingState{Phase: "idle"},
	}
	if wifiPairing != nil {
		view.Pairing = wifiPairing.Snapshot()
	}
	view.PairedName, view.Paired = pairedWiFiRuntime.paired()
	return view
}

func (api webUIAPI) wifiAvailable(w http.ResponseWriter) bool {
	if !wifiFeatureEnabled() || wifiPairing == nil {
		httpError(w, http.StatusNotFound, "Wi-Fi is not enabled on this host")
		return false
	}
	return true
}

func (api webUIAPI) handleWifiPairing(w http.ResponseWriter, _ *http.Request) {
	if !api.wifiAvailable(w) {
		return
	}
	writeJSON(w, api.wifiView())
}

func (api webUIAPI) handleWifiPairStart(w http.ResponseWriter, r *http.Request) {
	if !api.wifiAvailable(w) {
		return
	}
	var req struct {
		Replace bool `json:"replace"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid JSON: %v", err)
			return
		}
	}
	if !api.store.get().PairedWifiEnabled {
		httpError(w, http.StatusConflict, "turn Wi-Fi on first")
		return
	}
	if err := wifiPairing.Start(req.Replace); err != nil {
		httpError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, api.wifiView())
}

func (api webUIAPI) handleWifiPairApprove(w http.ResponseWriter, _ *http.Request) {
	if !api.wifiAvailable(w) {
		return
	}
	if err := wifiPairing.Approve(); err != nil {
		httpError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, api.wifiView())
}

func (api webUIAPI) handleWifiPairDeny(w http.ResponseWriter, _ *http.Request) {
	if !api.wifiAvailable(w) {
		return
	}
	if err := wifiPairing.Deny(); err != nil {
		httpError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, api.wifiView())
}

func (api webUIAPI) handleWifiPairCancel(w http.ResponseWriter, _ *http.Request) {
	if !api.wifiAvailable(w) {
		return
	}
	wifiPairing.Cancel()
	writeJSON(w, api.wifiView())
}

// applyPairedWifiSettings reconciles the paired runtime after a settings save
// and starts pairing when the toggle was just turned on with nobody paired.
func applyPairedWifiSettings(store *settingsStore, old, now settings) {
	if old.PairedWifiEnabled == now.PairedWifiEnabled && old.PairedWifiInterface == now.PairedWifiInterface {
		return
	}
	if wifiPairing != nil {
		if phase := wifiPairing.Snapshot().Phase; phase == "waiting" || phase == "approval" {
			wifiPairing.Cancel()
		}
	}
	pairedWiFiRuntime.reconcile()
	if !now.PairedWifiEnabled || wifiPairing == nil {
		return
	}
	if _, paired := pairedWiFiRuntime.paired(); !paired {
		if err := wifiPairing.Start(false); err != nil {
			log.Printf("Wi-Fi pairing did not start: %v", err)
		}
	}
}
