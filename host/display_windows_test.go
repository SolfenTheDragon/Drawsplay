//go:build windows

package main

import "testing"
import "time"

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

func TestMirrorFallbackPrefersPreSessionDisplay(t *testing.T) {
	outputs := []displayOutput{
		testDisplayOutput(`\\.\DISPLAY38`, 0, 1, true),
		testDisplayOutput(`\\.\DISPLAY1`, 0, 0, true),
	}
	display, err := newMirrorDisplayForBaseline(outputs, map[string]bool{`\\.\DISPLAY1`: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := display.(*mirrorDisplay).output.DeviceName; got != `\\.\DISPLAY1` {
		t.Fatalf("mirror output = %s, want pre-session display DISPLAY1", got)
	}
	if !display.Target().Mirrored {
		t.Fatal("mirror capture target is not marked as mirrored")
	}
}

func TestVDDCaptureFallbackCooldown(t *testing.T) {
	now := time.Now()
	baseline := map[string]bool{`\\.\DISPLAY1`: true}
	markVDDCaptureFallback(now, baseline)
	until, gotBaseline, active := vddCaptureFallbackActive(now.Add(time.Second))
	if !active || !until.Equal(now.Add(vddCaptureFallbackCooldown)) {
		t.Fatalf("cooldown = (%s, %v), want active until %s", until, active, now.Add(vddCaptureFallbackCooldown))
	}
	if !gotBaseline[`\\.\DISPLAY1`] {
		t.Fatalf("cooldown baseline = %v, want %v", gotBaseline, baseline)
	}
	if _, _, active := vddCaptureFallbackActive(until); active {
		t.Fatal("cooldown must expire at its deadline")
	}
}
