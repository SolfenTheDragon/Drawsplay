//go:build windows

package main

// Generic elevated-helper-task plumbing.
//
// The installer registers elevated scheduled tasks (WinUSB bind, etc.) so an
// installed, non-elevated host can trigger privileged work without raising a
// UAC prompt. The elevated helper writes a small JSON result file; the waiting
// host polls for it. These helpers are transport/driver agnostic.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type helperTaskResult struct {
	FinishedAt time.Time `json:"finishedAt"`
	Error      string    `json:"error,omitempty"`
}

// writeHelperTaskResult lets a non-elevated host waiting on an installed
// scheduled task learn when (and how) the elevated helper finished.
func writeHelperTaskResult(name string, taskErr error) {
	dir, err := userStateDir()
	if err != nil {
		return
	}
	res := helperTaskResult{FinishedAt: time.Now().UTC()}
	if taskErr != nil {
		res.Error = taskErr.Error()
	}
	if err := writeJSONAtomic(filepath.Join(dir, name), res); err != nil {
		log.Printf("Writing %s failed: %v", name, err)
	}
}

// runHelperTask starts an elevated helper task registered by `-install` and
// waits up to timeout for its result file.
func runHelperTask(task, resultName string, timeout time.Duration) error {
	dir, err := userStateDir()
	if err != nil {
		return err
	}
	resultPath := filepath.Join(dir, resultName)
	_ = os.Remove(resultPath)
	started := time.Now().UTC().Add(-time.Second)
	if out, err := runHiddenCommand(schtasksPath(), "/run", "/tn", task); err != nil {
		return fmt.Errorf("start scheduled task %q: %w (%s)", task, err, strings.TrimSpace(out))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(resultPath)
		if err == nil {
			var res helperTaskResult
			if json.Unmarshal(data, &res) == nil && !res.FinishedAt.Before(started) {
				if res.Error != "" {
					return fmt.Errorf("elevated task: %s", res.Error)
				}
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("scheduled task %q did not finish within %s", task, timeout)
}
