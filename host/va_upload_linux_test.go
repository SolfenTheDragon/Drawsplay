//go:build linux

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func withVAUploadCopy(t *testing.T, needed bool) {
	t.Helper()
	old := vaUploadCopyNeeded
	vaUploadCopyNeeded = func() bool { return needed }
	t.Cleanup(func() { vaUploadCopyNeeded = old })
}

// Where the upload works, the pipelines are exactly what they were.
func TestGstPipeWireSpecsUnchangedWithoutSlowVAUpload(t *testing.T) {
	target, opts := pipeWireTestOpts()
	withVAUploadCopy(t, false)
	for _, spec := range gstPipeWireSpecs(target, opts) {
		if strings.Contains(strings.Join(spec.Args, " "), vaUploadCopy) {
			t.Errorf("spec %s copies frames although the upload is fine", spec.Name)
		}
	}
}

// With a slow upload only the system-memory vah264enc candidate changes: the
// copy goes straight in front of vapostproc.
func TestGstPipeWireSpecsCopyBeforeVapostprocWhenUploadSlow(t *testing.T) {
	target, opts := pipeWireTestOpts()
	withVAUploadCopy(t, false)
	before := gstPipeWireSpecs(target, opts)
	withVAUploadCopy(t, true)
	after := gstPipeWireSpecs(target, opts)
	if len(before) != len(after) {
		t.Fatalf("candidate count changed: %d -> %d", len(before), len(after))
	}
	for i := range after {
		joined := strings.Join(after[i].Args, " ")
		if after[i].Name != "vah264enc" {
			if !reflect.DeepEqual(before[i], after[i]) {
				t.Errorf("spec %s changed although only vah264enc should", after[i].Name)
			}
			continue
		}
		want := "! video/x-raw ! " + vaUploadCopy + " ! vapostproc ! video/x-raw(memory:VAMemory),format=NV12,width=1920,height=1080"
		if !strings.Contains(joined, want) {
			t.Errorf("vah264enc does not copy into vapostproc's pool:\n%s", joined)
		}
	}
}

func TestVAUploadVerdict(t *testing.T) {
	cases := []struct {
		name            string
		viaCopy, direct time.Duration
		err             error
		want            bool
	}{
		{"direct faster", 800 * time.Millisecond, 600 * time.Millisecond, nil, false},
		{"direct a little slower", 800 * time.Millisecond, 2 * time.Second, nil, false},
		{"direct far slower", 800 * time.Millisecond, 2500 * time.Millisecond, nil, true},
		{"direct timed out", 850 * time.Millisecond, 2550 * time.Millisecond, errors.New("signal: killed"), true},
	}
	for _, c := range cases {
		if got := vaUploadVerdict(c.viaCopy, c.direct, c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestVAUploadProbeArgs(t *testing.T) {
	direct := strings.Join(vaUploadProbeArgs(false), " ")
	viaCopy := strings.Join(vaUploadProbeArgs(true), " ")
	if strings.Contains(direct, "videoconvert") {
		t.Errorf("direct probe copies: %s", direct)
	}
	if !strings.Contains(viaCopy, "format=BGRx,width=1280,height=720,framerate=60/1 ! "+vaUploadCopy+" ! vapostproc") {
		t.Errorf("copy probe: %s", viaCopy)
	}
	for _, a := range [][]string{vaUploadProbeArgs(false), vaUploadProbeArgs(true)} {
		if a[0] != "-q" || !strings.Contains(strings.Join(a, " "), "fakesink sync=false") {
			t.Errorf("probe args: %v", a)
		}
	}
}
