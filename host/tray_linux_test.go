//go:build linux

package main

import "testing"

func TestTrayMenuLayoutAndClick(t *testing.T) {
	var clicked int32
	m := &trayMenu{rev: 1, status: "s", detail: "d", onClick: func(id int32) { clicked = id }}
	_, root, err := m.GetLayout(0, -1, nil)
	if err != nil || len(root.Children) != 6 {
		t.Fatalf("layout err=%v children=%d", err, len(root.Children))
	}
	if _, _, err := m.GetLayout(99, -1, nil); err == nil {
		t.Fatal("unknown item should fail")
	}
	m.Event(trayIDQuit, "clicked", root.Props["x"], 0)
	if clicked != trayIDQuit {
		t.Fatalf("clicked = %d", clicked)
	}
}
