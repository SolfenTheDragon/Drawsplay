package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWifiPairingAPIHiddenWithoutFeatureFlag(t *testing.T) {
	t.Setenv(wifiFeatureFlagEnv, "0")
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/wifi/pairing")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 with the flag off", res.StatusCode)
	}
}

func TestWifiPairingAPIMutationsRequireCSRF(t *testing.T) {
	t.Setenv(wifiFeatureFlagEnv, "1")
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}))
	defer srv.Close()
	for _, path := range []string{"start", "approve", "deny", "cancel"} {
		res, err := http.Post(srv.URL+"/api/wifi/pairing/"+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s without CSRF token returned %d", path, res.StatusCode)
		}
	}
}

func TestPairedWifiSettingForcedOffWithoutFeatureFlag(t *testing.T) {
	t.Setenv(wifiFeatureFlagEnv, "0")
	if got := applyFeatureFlags(settings{PairedWifiEnabled: true, AOAEnabled: true}); got.PairedWifiEnabled {
		t.Fatal("paired Wi-Fi stayed on with the feature flag off")
	}
}

func TestStatusReportsActiveTransport(t *testing.T) {
	srv := httptest.NewServer(newWebUIHandler(newTestStore(t), func() {}))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var status map[string]any
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if _, ok := status["activeTransport"]; !ok {
		t.Fatal("status lacks activeTransport, which the web UI needs to hide USB details on Wi-Fi")
	}
}
