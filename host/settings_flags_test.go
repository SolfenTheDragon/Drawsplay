package main

import (
	"flag"
	"testing"
)

func TestSettingFlagsOverrideOnlyWhatIsGiven(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fromFlags := defaultSettings()
	bindSettingFlags(fs, &fromFlags)
	if err := fs.Parse([]string{"-fps", "60", "-bitrate", "8M", "-aoa-enabled=false"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultSettings()
	cfg.Width, cfg.FFmpeg = 2560, "/opt/ffmpeg"
	applySettingFlags(fs, &cfg)
	if cfg.FPS != 60 || cfg.Bitrate != "8M" || cfg.AOAEnabled {
		t.Fatalf("given flags not applied: %+v", cfg)
	}
	if cfg.Width != 2560 || cfg.FFmpeg != "/opt/ffmpeg" {
		t.Fatalf("saved values were overwritten: width=%d ffmpeg=%q", cfg.Width, cfg.FFmpeg)
	}
}

func TestSettingFlagDefaultsAreTheSettingDefaults(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	s := defaultSettings()
	bindSettingFlags(fs, &s)
	if got := fs.Lookup("zero-copy-capture").DefValue; got != "true" {
		t.Fatalf("zero-copy-capture default = %s, want true like defaultSettings", got)
	}
}
