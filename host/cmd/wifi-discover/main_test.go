package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDiscoveryCommandRejectsInvalidArgumentsWithoutNetworkWork(t *testing.T) {
	for _, args := range [][]string{
		{}, {"-duration", "0s"}, {"-duration", "61s"},
		{"-interface", "drawsplay-no-such-interface"},
		{"unexpected-hostname.example.org"}, {"-unknown"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			if code := run(args, &out, &diagnostics); code != 1 {
				t.Fatalf("want failure, got %d", code)
			}
			if out.Len() != 0 || diagnostics.Len() == 0 {
				t.Fatalf("expected explicit diagnostics and no candidates: %q / %q", out.String(), diagnostics.String())
			}
		})
	}
}

func TestDiscoveryCommandHelpDoesNotStartDiscovery(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-help"}, &out, &diagnostics); code != 0 {
		t.Fatalf("help returned %d", code)
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), "interface") {
		t.Fatal("missing help or unexpected candidates")
	}
}
