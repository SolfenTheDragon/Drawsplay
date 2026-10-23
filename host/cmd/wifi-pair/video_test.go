package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestSyntheticVideoFixtureHasBoundedHeaderAndCompleteAccessUnits(t *testing.T) {
	fixture := make([]byte, 16+16+8+8)
	copy(fixture, "DSVT")
	binary.BigEndian.PutUint32(fixture[4:], 2)
	binary.BigEndian.PutUint32(fixture[8:], 16)
	binary.BigEndian.PutUint16(fixture[16:], 1)
	binary.BigEndian.PutUint16(fixture[18:], 640)
	binary.BigEndian.PutUint16(fixture[20:], 360)
	binary.BigEndian.PutUint16(fixture[22:], 30)
	binary.BigEndian.PutUint32(fixture[32:], 4)
	copy(fixture[36:], []byte{0, 0, 1, 9})
	binary.BigEndian.PutUint32(fixture[40:], 4)
	copy(fixture[44:], []byte{0, 0, 1, 9})
	video, err := readSyntheticVideo(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if video.frames != 2 || video.fps != 30 || len(video.config) != 16 {
		t.Fatal("fixture metadata was not validated")
	}
	var storage [64]byte
	for i := 0; i < 2; i++ {
		n, err := video.readAU(storage[:])
		if err != nil || n != 4 || storage[3] != 9 {
			t.Fatalf("AU %d: %d %v", i, n, err)
		}
	}
	if err := video.finish(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func([]byte){
		func(raw []byte) { raw[0] = 0 },
		func(raw []byte) { binary.BigEndian.PutUint32(raw[4:], 1801) },
		func(raw []byte) { binary.BigEndian.PutUint32(raw[8:], 65537) },
		func(raw []byte) { raw[15] = 1 },
		func(raw []byte) { raw[23] = 0 },
	} {
		raw := bytes.Clone(fixture)
		change(raw)
		if _, err := readSyntheticVideo(bytes.NewReader(raw)); err == nil {
			t.Fatal("malformed synthetic fixture accepted")
		}
	}
	video, err = readSyntheticVideo(bytes.NewReader(fixture[:47]))
	if err != nil {
		t.Fatal(err)
	}
	video.readAU(storage[:])
	if _, err := video.readAU(storage[:]); err == nil {
		t.Fatal("truncated AU accepted")
	}
}

func TestCheckDecodeResultRecoveryModeRequiresRealRecovery(t *testing.T) {
	// Clean mode keeps the strict >=90% release requirement.
	if err := checkDecodeResult(300, 300, 300, 300, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := checkDecodeResult(300, 300, 200, 200, 90, 0, false); err == nil {
		t.Fatal("clean mode accepted dropped pictures")
	}
	// Recovery mode must observe drops, and every picture the queue kept must be decoded.
	if err := checkDecodeResult(300, 300, 200, 190, 100, 5, true); err != nil {
		t.Fatal(err)
	}
	if err := checkDecodeResult(300, 300, 300, 300, 0, 0, true); err == nil {
		t.Fatal("recovery mode accepted a run that never exercised recovery")
	}
	if err := checkDecodeResult(300, 300, 150, 150, 100, 0, true); err == nil {
		t.Fatal("recovery mode accepted undecoded surviving pictures")
	}
	if err := checkDecodeResult(300, 300, 0, 0, 300, 0, true); err == nil {
		t.Fatal("recovery mode accepted a run that never resumed decoding")
	}
}
