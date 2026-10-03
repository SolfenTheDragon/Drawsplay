//go:build !windows

package main

import "context"

func startSessionLockMonitor(ctx context.Context) {
	_ = ctx
}
