//go:build !linux

package main

import (
	"context"
	"os"
)

// startIdleGate is Linux-only (X11 DAMAGE + SIGSTOP); elsewhere it is a no-op.
func startIdleGate(ctx context.Context, proc *os.Process, g *idleGateSpec) func() {
	return func() {}
}
