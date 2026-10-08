//go:build !windows && !linux

package main

import "context"

func startSessionLockMonitor(ctx context.Context) {
	_ = ctx
}
