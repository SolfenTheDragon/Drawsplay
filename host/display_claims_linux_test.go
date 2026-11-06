package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDisplayClaimsSeeOtherLiveOwnersOnly(t *testing.T) {
	dir := t.TempDir()
	old := displayClaimsDir
	displayClaimsDir = func() string { return dir }
	defer func() { displayClaimsDir = old }()

	claimDisplay(displayClaimOutput, "HDMI-1")
	if displayClaimedByOther("HDMI-1") {
		t.Fatal("our own claim must not count as another owner's")
	}
	// A live other owner: the parent process (go test runner), with its real comm.
	ppid := os.Getppid()
	other := displayClaim{Owner: "desktop-host", PID: ppid, Comm: processComm(ppid), Kind: displayClaimOutput, Name: "Virtual-1-1"}
	dead := displayClaim{Owner: "desktop-host", PID: ppid, Comm: "not-that-process", Kind: displayClaimConnector, Name: "HDMI-2"}
	if err := withDisplayClaims(func(f *displayClaimFile) bool {
		f.Claims = append(f.Claims, other, dead)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !displayClaimedByOther("Virtual-1-1") {
		t.Fatal("a live other owner's claim was ignored")
	}
	if displayClaimedByOther("HDMI-2") {
		t.Fatal("a claim whose process is gone must not block cleanup")
	}
	releaseDisplayClaim(displayClaimOutput, "HDMI-1")
	var f displayClaimFile
	data, _ := os.ReadFile(filepath.Join(dir, "display-claims.json"))
	_ = json.Unmarshal(data, &f)
	for _, c := range f.Claims {
		if c.Name == "HDMI-1" || c.Name == "HDMI-2" {
			t.Fatalf("claim %q should be gone: %+v", c.Name, f.Claims)
		}
	}
}
