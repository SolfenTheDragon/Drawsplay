//go:build !linux

package main

const (
	displayClaimOutput    = "output"
	displayClaimConnector = "connector"
)

// Display claims only coordinate Linux virtual displays (see display_claims_linux.go).
// kdeWaylandSession: KDE Plasma's extended display is Linux-only.
func kdeWaylandSession() bool { return false }

func claimDisplay(kind, name string)         {}
func releaseDisplayClaim(kind, name string)  {}
func displayClaimedByOther(name string) bool { return false }
