//go:build windows

package main

import "testing"

func testDisplayOutput(name string, adapter, output int, attached bool) displayOutput {
	return displayOutput{
		AdapterIndex: adapter,
		OutputIndex:  output,
		DeviceName:   name,
		Attached:     attached,
		Rect:         dxgiRect{Right: 1920, Bottom: 1080},
	}
}

func TestResolveVDDOutputRefreshesOutputIndex(t *testing.T) {
	current := testDisplayOutput(`\\.\DISPLAY5`, 0, 1, true)
	resolved, err := resolveVDDOutput(
		current,
		map[string]bool{`\\.\DISPLAY1`: true},
		[]displayOutput{
			testDisplayOutput(`\\.\DISPLAY1`, 0, 0, true),
			testDisplayOutput(`\\.\DISPLAY5`, 0, 3, true),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.OutputIndex != 3 {
		t.Fatalf("resolved output index = %d, want 3", resolved.OutputIndex)
	}
}

func TestResolveVDDOutputFollowsDisplayNameChurn(t *testing.T) {
	current := testDisplayOutput(`\\.\DISPLAY5`, 0, 1, true)
	resolved, err := resolveVDDOutput(
		current,
		map[string]bool{`\\.\DISPLAY1`: true},
		[]displayOutput{
			testDisplayOutput(`\\.\DISPLAY1`, 0, 0, true),
			testDisplayOutput(`\\.\DISPLAY22`, 0, 2, true),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.DeviceName != `\\.\DISPLAY22` || resolved.OutputIndex != 2 {
		t.Fatalf("resolved output = %+v, want DISPLAY22 output 2", resolved)
	}
}

func TestResolveVDDOutputRejectsAmbiguousReplacement(t *testing.T) {
	current := testDisplayOutput(`\\.\DISPLAY5`, 0, 1, true)
	_, err := resolveVDDOutput(
		current,
		map[string]bool{`\\.\DISPLAY1`: true},
		[]displayOutput{
			testDisplayOutput(`\\.\DISPLAY1`, 0, 0, true),
			testDisplayOutput(`\\.\DISPLAY22`, 0, 2, true),
			testDisplayOutput(`\\.\DISPLAY23`, 0, 3, true),
		},
	)
	if err == nil {
		t.Fatal("ambiguous replacement unexpectedly resolved")
	}
}
