package main

// Tests against the shared vectors in ../testdata/protocols, which the
// Android app and dd-host test against too.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type protocolVector struct {
	name   string
	fields map[string]string
}

func (v protocolVector) hex(t *testing.T, key string) []byte {
	t.Helper()
	s := v.fields[key]
	if s == "-" {
		return []byte{}
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("%s: %s=%q: %v", v.name, key, s, err)
	}
	return b
}

func (v protocolVector) uint(t *testing.T, key string) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(v.fields[key], 10, 64)
	if err != nil {
		t.Fatalf("%s: %s=%q: %v", v.name, key, v.fields[key], err)
	}
	return n
}

func loadProtocolVectors(t *testing.T, file string) []protocolVector {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "testdata", "protocols", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []protocolVector
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		v := protocolVector{name: parts[0], fields: map[string]string{}}
		for _, kv := range parts[1:] {
			k, val, _ := strings.Cut(kv, "=")
			v.fields[k] = val
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no vectors", file)
	}
	return out
}

func TestAOAFramesMatchSharedVectors(t *testing.T) {
	for _, v := range loadProtocolVectors(t, "aoa-frames.vectors") {
		typ, id, payload, wire := byte(v.uint(t, "type")), uint16(v.uint(t, "id")), v.hex(t, "payload"), v.hex(t, "wire")
		if got := encodeAOAFrame(typ, id, payload); !bytes.Equal(got, wire) {
			t.Errorf("%s: encoded % x, want % x", v.name, got, wire)
		}
		gotTyp, gotID, gotPayload, err := readAOAFrame(bytes.NewReader(wire))
		if err != nil || gotTyp != typ || gotID != id || !bytes.Equal(gotPayload, payload) {
			t.Errorf("%s: decoded %d/%d/% x (%v)", v.name, gotTyp, gotID, gotPayload, err)
		}
	}
}

func TestMuxFramesMatchSharedVectors(t *testing.T) {
	for _, v := range loadProtocolVectors(t, "mux-frames.vectors") {
		want := muxFrame{StreamID: byte(v.uint(t, "stream")), Flags: byte(v.uint(t, "flags")), Seq: uint32(v.uint(t, "seq")), Payload: v.hex(t, "payload")}
		wire := v.hex(t, "wire")
		var out bytes.Buffer
		if err := writeMuxFrame(&out, want); err != nil || !bytes.Equal(out.Bytes(), wire) {
			t.Errorf("%s: encoded % x (%v), want % x", v.name, out.Bytes(), err, wire)
		}
		got, err := readMuxFrame(bytes.NewReader(wire))
		if err != nil || got.StreamID != want.StreamID || got.Flags != want.Flags || got.Seq != want.Seq || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("%s: decoded %+v (%v)", v.name, got, err)
		}
	}
}

func TestIDDProtocolMatchesSharedVectors(t *testing.T) {
	codes := map[string]uint32{"ioctl-add-monitor": iddIoctlAddMonitor, "ioctl-remove-monitor": iddIoctlRemoveMonitor, "ioctl-remove-all": iddIoctlRemoveAll}
	for _, v := range loadProtocolVectors(t, "idd-ioctl.vectors") {
		switch {
		case v.name == "interface":
			if v.fields["guid"] != iddInterfaceGUIDString {
				t.Errorf("GUID %s, want %s", iddInterfaceGUIDString, v.fields["guid"])
			}
		case codes[v.name] != 0:
			if want, _ := strconv.ParseUint(v.fields["code"], 16, 32); uint32(want) != codes[v.name] {
				t.Errorf("%s = %#08x, want %#08x", v.name, codes[v.name], want)
			}
		case strings.HasPrefix(v.name, "add-monitor-request"):
			got := encodeIDDAddMonitor(int(v.uint(t, "width")), int(v.uint(t, "height")), int(v.uint(t, "hz")), int(v.uint(t, "nativeWidth")), int(v.uint(t, "nativeHeight")))
			if want := v.hex(t, "wire"); !bytes.Equal(got[:], want) {
				t.Errorf("%s: % x, want % x", v.name, got, want)
			}
		default:
			t.Errorf("unknown vector %s", v.name)
		}
	}
}

func TestDesktopStatusGoldenFileReads(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "protocols", "desktop-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, desktopStatusFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		UpdatedUnixMs int64 `json:"updatedUnixMs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	st := readDesktopHostStatus(dir, time.UnixMilli(raw.UpdatedUnixMs).Add(time.Second))
	if !st.Running || st.PID != 4242 || st.Listen != "0.0.0.0:47800" || st.OS != "linux" || st.Session == nil ||
		st.Session.Width != 2560 || !st.Session.VirtualDisplay || st.Session.Backend != "x11-virtual" ||
		len(st.Sessions) != 1 || len(st.Trusted) != 1 || st.Trusted[0].Key != "0123456789ab" {
		t.Fatalf("status = %+v", st)
	}
}
