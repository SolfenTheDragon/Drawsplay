//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAsset(name string) githubAsset {
	return githubAsset{
		Name:   name,
		URL:    "https://github.com/x/y/releases/download/t/" + name,
		Digest: "sha256:" + strings.Repeat("ab", 32),
	}
}

func TestSelectGyanEssentials(t *testing.T) {
	rel := githubRelease{TagName: "9.0.2", Assets: []githubAsset{
		testAsset("ffmpeg-9.0.2-essentials_build.7z"),
		testAsset("ffmpeg-9.0.2-full_build.zip"),
		testAsset("ffmpeg-9.0.2-essentials_build.zip"),
	}}
	got, err := selectGyanEssentials(rel)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "9.0.2" || !strings.HasSuffix(got.URL, "ffmpeg-9.0.2-essentials_build.zip") || got.SHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("got %+v", got)
	}
}

func TestSelectBtbNStablePicksNewestForArch(t *testing.T) {
	rel := githubRelease{TagName: "latest", Assets: []githubAsset{
		testAsset("ffmpeg-master-latest-win64-gpl.zip"),
		testAsset("ffmpeg-n8.1-latest-win64-gpl-8.1.zip"),
		testAsset("ffmpeg-n9.0-latest-win64-gpl-9.0.zip"),
		testAsset("ffmpeg-n9.0-latest-win64-lgpl-9.0.zip"),
		testAsset("ffmpeg-n10.0-latest-winarm64-gpl-10.0.zip"),
		testAsset("ffmpeg-n9.0-latest-winarm64-gpl-9.0.zip"),
	}}
	got, err := selectBtbNStable(rel, "amd64")
	if err != nil || got.Version != "9.0" || !strings.HasSuffix(got.URL, "ffmpeg-n9.0-latest-win64-gpl-9.0.zip") {
		t.Fatalf("amd64: got %+v, %v", got, err)
	}
	got, err = selectBtbNStable(rel, "arm64")
	if err != nil || got.Version != "10.0" {
		t.Fatalf("arm64: got %+v, %v", got, err)
	}
	if _, err := selectBtbNStable(githubRelease{Assets: []githubAsset{testAsset("ffmpeg-master-latest-win64-gpl.zip")}}, "amd64"); err == nil {
		t.Fatal("master-only release must not be selected")
	}
}

func TestArchiveFromAssetRequiresDigestAndGitHubURL(t *testing.T) {
	a := testAsset("ffmpeg-9.0.2-essentials_build.zip")
	a.Digest = ""
	if _, err := archiveFromAsset(a, "9.0.2"); err == nil {
		t.Fatal("missing digest must be rejected")
	}
	a = testAsset("ffmpeg-9.0.2-essentials_build.zip")
	a.URL = "http://example.com/ffmpeg.zip"
	if _, err := archiveFromAsset(a, "9.0.2"); err == nil {
		t.Fatal("non-GitHub URL must be rejected")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"9.0", "9.0.2", -1}, {"10.0", "9.1", 1}, {"8.1", "8.1", 0}, {"9.0.0", "9.0", 0}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRuntimeManifestVerification(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ffmpeg.exe")
	dll := filepath.Join(dir, "avcodec.dll")
	os.WriteFile(exe, []byte("exe"), 0o644)
	os.WriteFile(dll, []byte("dll"), 0o644)
	if err := writeRuntimeManifest(dir, runtimeManifest{Tool: "ffmpeg", Version: "9.0.2"}); err != nil {
		t.Fatal(err)
	}
	want, err := runtimeExpectedHashes(dir, exe, "ffmpeg")
	if err != nil || len(want) != 2 {
		t.Fatalf("expected exe+dll hashes, got %v, %v", want, err)
	}
	for name, digest := range want {
		if err := verifyFileSHA256(filepath.Join(dir, name), digest); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	os.WriteFile(dll, []byte("tampered"), 0o644)
	if err := verifyFileSHA256(dll, want["avcodec.dll"]); err == nil {
		t.Fatal("tampered DLL must fail verification")
	}
	if _, err := runtimeExpectedHashes(dir, filepath.Join(dir, "other.exe"), "ffmpeg"); err == nil {
		t.Fatal("file outside the manifest must be rejected")
	}
}
