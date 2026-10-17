//go:build windows

package main

// Windows replaces the session display on VDD self-recovery and mirror
// fallback; neither is a user resolution change.
const modeBaselineIgnoresDisplaySwaps = true
