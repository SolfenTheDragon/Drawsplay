package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPairCommandRequiresExplicitLocalSelectionBeforeNetworkOrStorage(t *testing.T) {
	for _, args := range [][]string{
		{}, {"-interface", "test"}, {"-interface", "test", "-tablet", "tablet"},
		{"-interface", "test", "-tablet", "tablet", "-trust-dir", t.TempDir(), "unexpected"},
		{"-interface", "test", "-tablet", "\x1b[31m", "-trust-dir", t.TempDir()},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(args, strings.NewReader("yes\n"), &out, &diagnostics); code != 1 {
			t.Fatalf("invalid invocation %v returned %d", args, code)
		}
		if diagnostics.Len() == 0 || strings.Contains(out.String(), "Pairing code:") {
			t.Fatal("invalid invocation started pairing or lacked actionable diagnostics")
		}
	}
}

func TestPairCommandHelpDoesNotOpenTrust(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-help"}, strings.NewReader(""), &out, &diagnostics); code != 0 {
		t.Fatalf("help failed: %d", code)
	}
	if !strings.Contains(diagnostics.String(), "trust-dir") || out.Len() != 0 {
		t.Fatal("help lacked options or unexpectedly performed pairing")
	}
}

func TestPairCommandHasExplicitOptInForPairedSessionVerification(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-verify-session", "-help"}, strings.NewReader(""), &out, &diagnostics); code != 0 {
		t.Fatal("paired-session diagnostic is not an explicit opt-in CLI option")
	}

}

func TestPairCommandHasExplicitSyntheticVideoOptIn(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-test-video", "synthetic.dsvt", "-help"}, strings.NewReader(""), &out, &diagnostics); code != 0 {
		t.Fatal("synthetic media diagnostic is not an explicit opt-in CLI option")
	}
}
