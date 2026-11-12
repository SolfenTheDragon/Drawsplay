package main

import "testing"

func TestTrayStatusText(t *testing.T) {
	cases := []struct {
		running      bool
		device       string
		vs           videoStatusInfo
		status, desc string
	}{
		{true, "", videoStatusInfo{}, "🔴 Disconnected", "Waiting for tablet"},
		{false, "Tablet A", videoStatusInfo{}, "🔴 Disconnected", "Waiting for tablet"},
		{true, "unknown-tablet", videoStatusInfo{}, "🔴 Disconnected", "Waiting for tablet"},
		{true, "unknown-client", videoStatusInfo{}, "🔴 Disconnected", "Waiting for tablet"},
		{true, "SM-X920", videoStatusInfo{}, "🟢 Connected: SM-X920", "Not streaming"},
		{true, " SM\nX920 ", videoStatusInfo{}, "🟢 Connected: SM X920", "Not streaming"},
		{true, "SM-X920", videoStatusInfo{Streaming: true, DisplayMode: "extend", Encoder: "h264_vaapi"}, "🟢 Connected: SM-X920", "Extended display · h264_vaapi"},
		{true, "SM-X920", videoStatusInfo{Streaming: true, DisplayMode: "mirror"}, "🟢 Connected: SM-X920", "Mirroring desktop"},
	}
	for _, c := range cases {
		status, desc := trayStatusText(c.running, c.device, c.vs)
		if status != c.status || desc != c.desc {
			t.Errorf("trayStatusText(%v, %q) = %q / %q, want %q / %q", c.running, c.device, status, desc, c.status, c.desc)
		}
	}
}
