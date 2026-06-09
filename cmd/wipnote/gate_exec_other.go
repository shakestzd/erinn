//go:build !linux

package main

// mountIsNoexec is a no-op fast-path on non-Linux platforms; exec-capability is
// determined entirely by the write+exec probe in probeExecCapable.
func mountIsNoexec(string) bool { return false }
