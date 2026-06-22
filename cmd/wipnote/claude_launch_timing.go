package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Launch-phase timing instrumentation. Gated behind WIPNOTE_LAUNCH_TIMING so it
// is a zero-cost no-op in normal operation. When the env var is set, each
// launchTiming call prints the elapsed time since the first call to stderr,
// making it trivial to locate where launch latency (e.g. SQLite busy_timeout
// write-lock stalls) is spent across the launcher critical path.
var (
	launchTimingOnce  sync.Once
	launchTimingStart time.Time
	launchTimingOn    bool
)

func launchTiming(label string) {
	launchTimingOnce.Do(func() {
		launchTimingStart = time.Now()
		launchTimingOn = os.Getenv("WIPNOTE_LAUNCH_TIMING") != ""
	})
	if !launchTimingOn {
		return
	}
	fmt.Fprintf(os.Stderr, "launch-timing: %7.2fs  %s\n", time.Since(launchTimingStart).Seconds(), label)
}

// LogTimed is a gated no-op retained for call-site stability. The
// core/hooks package owns the canonical LogTimed implementation
// (core/hooks/log.go:LogTimed) used by the hook subprocess tree.
// This package-level symbol ensures any cmd/wipnote caller that
// references LogTimed directly compiles without error; the
// WIPNOTE_LAUNCH_TIMING gate means it is zero-cost at runtime.
//
// Do NOT add new callers of this function — use hooks.LogTimed from
// core/hooks for hook-path timing, or launchTiming for launcher
// critical-path timing.
func LogTimed(label string, _ time.Time) {
	launchTiming(label)
}
