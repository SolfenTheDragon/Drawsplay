package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"drawsplay/host/internal/wifilan"
)

func TestWiFiEncoderArgsForceConstrainedBaselineSingleSlice(t *testing.T) {
	for _, enc := range []string{"libx264", "h264_nvenc", "h264_amf", "h264_qsv"} {
		joined := " " + strings.Join(wifiEncoderArgs(enc, 30, "4.0"), " ") + " "
		if !strings.Contains(joined, " -profile:v ") || !strings.Contains(joined, " -level ") || !strings.Contains(joined, " -bf 0 ") {
			t.Errorf("%s args missing profile/level/no-B: %s", enc, joined)
		}
		if strings.Contains(joined, "sliced-threads=1") {
			t.Errorf("%s allows multiple slices", enc)
		}
	}
	if got := wifiEncoderArgs("hevc_nvenc", 30, "4.0"); got != nil {
		t.Errorf("non-H.264 encoder accepted: %v", got)
	}
}

func testSplitAnnexB(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				nals = append(nals, bytes.TrimRight(b[start:i], "\x00"))
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 {
		nals = append(nals, b[start:])
	}
	return nals
}

func TestWiFiEncoderLibx264OutputPassesPacketizerAsConstrainedBaseline(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if out, _ := exec.Command(ff, "-hide_banner", "-encoders").Output(); !bytes.Contains(out, []byte("libx264")) {
		t.Skip("libx264 unavailable")
	}
	args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-frames:v", "90", "-c:v", "libx264"}
	args = append(args, wifiEncoderArgs("libx264", 30, "4.0")...)
	args = append(args, "-f", "h264", "pipe:1")
	out, err := exec.Command(ff, args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var p wifilan.AUPacketizer
	dst := make([]byte, 1<<20)
	pictures := 0
	for _, nal := range testSplitAnnexB(out) {
		n, _, err := p.Push(append([]byte{0, 0, 0, 1}, nal...), dst)
		if err != nil {
			t.Fatalf("packetizer rejected NAL type %d: %v", nal[0]&0x1f, err)
		}
		if n > 0 {
			pictures++
		}
	}
	if pictures != 90 {
		t.Fatalf("pictures=%d", pictures)
	}
	ps, ok := p.ParameterSets()
	if !ok || ps.SPS[5] != 66 || ps.SPS[6]&0x40 == 0 {
		t.Fatalf("not constrained baseline: %x", ps.SPS)
	}
}

func TestBuildFFmpegSpecWiFiBaselineIsOptInAndLeavesUSBArgsUnchanged(t *testing.T) {
	capture := []string{"-f", "lavfi", "-i", "x"}
	usb := buildFFmpegSpec(videoOptions{FFmpeg: "ffmpeg", FPS: 30, Bitrate: "4M"}, capture, "libx264", encoderArgs("libx264", 30))
	for _, a := range usb.Args {
		if a == "baseline" || a == "-level" {
			t.Fatalf("USB spec gained Wi-Fi args: %v", usb.Args)
		}
	}
	wifi := buildFFmpegSpec(videoOptions{FFmpeg: "ffmpeg", FPS: 30, Bitrate: "4M", WiFiBaseline: true}, capture, "libx264", encoderArgs("libx264", 30))
	if strings.Join(wifi.Args, " ") == strings.Join(usb.Args, " ") || !strings.Contains(strings.Join(wifi.Args, " "), "-profile:v baseline -level 4.2") {
		t.Fatalf("Wi-Fi spec missing baseline args: %v", wifi.Args)
	}
}
