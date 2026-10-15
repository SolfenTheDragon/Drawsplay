//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestX11AuthCookie(t *testing.T) {
	field := func(s string) []byte { return append([]byte{0, byte(len(s))}, s...) }
	entry := func(family uint16, addr, num, name, data string) []byte {
		b := []byte{byte(family >> 8), byte(family)}
		for _, f := range []string{addr, num, name, data} {
			b = append(b, field(f)...)
		}
		return b
	}
	var file []byte
	file = append(file, entry(0, "other", "1", "MIT-MAGIC-COOKIE-1", "wrong")...)
	file = append(file, entry(0xffff, "", "0", "MIT-MAGIC-COOKIE-1", "cookie16bytes!!!!")...)
	path := filepath.Join(t.TempDir(), "xauth")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAUTHORITY", path)
	name, data := x11AuthCookie("0")
	if name != "MIT-MAGIC-COOKIE-1" || string(data) != "cookie16bytes!!!!" {
		t.Fatalf("got %q %q", name, data)
	}
	if name, _ := x11AuthCookie("7"); name != "" {
		t.Fatalf("unexpected cookie for display 7: %q", name)
	}
}
