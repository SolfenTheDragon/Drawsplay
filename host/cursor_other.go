//go:build !windows

package main

import "context"

func runCursorPublisher(context.Context, func(hostCursorMsg)) {}
