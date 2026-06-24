package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// launchMarkerFreshWindow bounds how recently .wipnote/.launch-mode must have
// been written for its recorded pid to be trusted as THIS session's owner. A
// bare/old launch leaves a stale marker from a PRIOR wipnote session; its
// dead-or-reused pid must NOT be written into a live session's .session-pid
// anchor (which would false-reap the live session once its heartbeat goes
// stale). Mirrors bareLaunchNudge's 30s staleness window (session_start.go).
// A tighter window is the SAFE direction: worst case a legit session degrades
// to LIVE and is simply never auto-reaped — never false-reaped.
const launchMarkerFreshWindow = 30 * time.Second

// Session process-liveness anchor (feat-0a7db952, slice-1).
//
// A session's SQLite heartbeat alone cannot distinguish a CRASHED session from a
// long-IDLE but LIVE one — both stop updating the heartbeat. To make reaping
// safe, SessionStart records the owning process's pid (and, on Linux, its /proc
// start time) in a `.session-pid` file alongside the session dir, mirroring the
// existing `.collector-pid` convention. Liveness is then a directly-pollable
// fact: kill(pid, 0) plus a start-time match defeats PID reuse.
//
// Boundary note (accepted minor duplication): the `core` module CANNOT import
// `observe` — `observe` already imports `core`, so importing back would risk a
// module cycle. The /proc start-time reader below is therefore a deliberate
// ~20-line copy of observe/otel/collector/lifecycle.go's readProcStartTime /
// readCollectorPIDFile logic, kept in sync by convention rather than by import.

// readProcStartTime returns the process start time from /proc/<pid>/stat field
// 22 (clock ticks since boot). Returns ok=false on non-Linux systems or when the
// proc entry is unreadable. Used for PID-reuse detection.
//
// Core-local copy of observe/otel/collector/lifecycle.go readProcStartTime (see
// the boundary note above): core cannot import observe.
func readProcStartTime(pid int) (uint64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	s := string(data)
	// Field 2 (comm) is wrapped in parens and may itself contain spaces or
	// parens. Split after the LAST closing paren.
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+1 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[idx+1:])
	// Index 0 here corresponds to field 3 (state); field 22 (starttime) is at
	// index 19 of this slice.
	if len(fields) < 20 {
		return 0, false
	}
	st, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return st, true
}

// resolveOwningPID determines the pid of the process that owns this session, so
// liveness can be polled directly. Resolution order:
//
//  1. .wipnote/.launch-mode (written by `wipnote claude` with os.Getpid() of the
//     launcher) — authoritative for wipnote-launched sessions, but ONLY when the
//     marker is FRESH (written within launchMarkerFreshWindow). A stale marker is
//     a leftover from a PRIOR session; its pid is dead/reused and trusting it
//     would false-reap THIS live session.
//  2. Subagents (WIPNOTE_PARENT_SESSION set) without a usable launch-mode pid:
//     fall through — we do not have a confidently-resolvable stable harness pid
//     here, and a wrong pid is worse than none.
//  3. Otherwise ok=false → caller OMITS .session-pid → the session safe-degrades
//     to LIVE (never reaped). This degrade direction is the core invariant:
//     never the reverse.
func resolveOwningPID(projectDir string) (pid int, ok bool) {
	markerPath := filepath.Join(projectDir, ".wipnote", ".launch-mode")
	info, statErr := os.Stat(markerPath)
	if statErr != nil {
		return 0, false
	}
	// Freshness gate: a marker older than the window belongs to a PRIOR session.
	// Its recorded pid is dead or reused; do NOT anchor this session to it.
	// Mirrors bareLaunchNudge's "not this session" staleness check.
	if time.Since(info.ModTime()) > launchMarkerFreshWindow {
		return 0, false
	}
	b, err := os.ReadFile(markerPath)
	if err != nil {
		return 0, false
	}
	var lm launchModeFile
	if err := json.Unmarshal(b, &lm); err != nil {
		return 0, false
	}
	if lm.PID > 0 {
		return lm.PID, true
	}
	// No usable launch-mode pid. For subagents (and everything else) we
	// deliberately fall through rather than guess a harness parent pid — a
	// missing anchor degrades safely to LIVE.
	return 0, false
}

// updateSessionPIDAnchor refreshes (or, when no fresh owner resolves, REMOVES)
// the .session-pid anchor for sessDir. Best-effort: never blocks/fails the hook.
//
// The removal arm is the FIX for stale-anchor false-reaps on resume: if the old
// code only WROTE when an owner resolved, an unresolvable owner (stale/absent
// .launch-mode) left a DEAD pre-existing .session-pid in place, which the reaper
// would then treat as a provably-dead owner and false-reap. Dropping the anchor
// degrades the session to LIVE instead.
func updateSessionPIDAnchor(projectDir, sessDir string) {
	if pid, ok := resolveOwningPID(projectDir); ok {
		if err := writeSessionPID(sessDir, pid); err != nil {
			debugLog(projectDir, "[session] writeSessionPID failed (%s pid=%d): %v", filepath.Base(sessDir), pid, err)
		}
		return
	}
	// No fresh owner: drop any stale anchor so the session degrades to LIVE
	// rather than carrying a dead/foreign pid.
	_ = os.Remove(filepath.Join(sessDir, ".session-pid"))
}

// writeSessionPID writes the owning pid (and, on Linux, its /proc start time) to
// <sessDir>/.session-pid, mirroring the .collector-pid format:
//
//	line 1: <pid>
//	line 2: <starttime_ticks>   (omitted when /proc is unreadable / non-Linux)
//
// Best-effort: any failure is swallowed by the caller's logging — a write error
// must NEVER block or fail the hook. Consumers (IsSessionProcessAlive) tolerate
// both the 1-line and 2-line forms.
func writeSessionPID(sessDir string, pid int) error {
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return err
	}
	content := strconv.Itoa(pid)
	if st, ok := readProcStartTime(pid); ok {
		content += "\n" + strconv.FormatUint(st, 10)
	}
	return os.WriteFile(filepath.Join(sessDir, ".session-pid"), []byte(content), 0o644)
}

// readSessionPIDFile parses <sessDir>/.session-pid. hasStart is false for legacy
// single-line files (or when start-time was unreadable at write time).
func readSessionPIDFile(sessDir string) (pid int, starttime uint64, hasStart bool, err error) {
	data, err := os.ReadFile(filepath.Join(sessDir, ".session-pid"))
	if err != nil {
		return 0, 0, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return 0, 0, false, fmt.Errorf("empty .session-pid in %s", sessDir)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse session pid %q: %w", lines[0], err)
	}
	if len(lines) >= 2 {
		if st, perr := strconv.ParseUint(strings.TrimSpace(lines[1]), 10, 64); perr == nil {
			return pid, st, true, nil
		}
	}
	return pid, 0, false, nil
}

// IsSessionProcessAlive reports whether the process that owns the session at
// sessDir is still alive. The contract is SAFE-DEGRADE-TO-LIVE: anything we
// cannot positively prove dead is reported alive, so a live idle session is
// never reaped.
//
//   - .session-pid missing / unreadable → true (legacy or unresolved owner).
//   - process gone (kill(pid,0) -> ESRCH) → false.
//   - kill(pid,0) -> EPERM → process exists under another uid → true (ALIVE).
//   - start-time recorded AND readable now AND mismatched → PID reuse → false.
//   - otherwise → true.
func IsSessionProcessAlive(sessDir string) bool {
	pid, recordedStart, hasStart, err := readSessionPIDFile(sessDir)
	if err != nil {
		// Missing or unreadable anchor: legacy/unresolved degrades to LIVE.
		return true
	}
	if pid <= 0 {
		return true
	}
	if err := syscall.Kill(pid, 0); err != nil {
		// EPERM means the process EXISTS but is owned by a different uid (e.g. a
		// root process that reused the recorded pid). The process is ALIVE — we
		// must NOT report it dead, or a live session whose pid was reused under
		// another uid would be false-reaped. Safe-degrade to LIVE.
		if errors.Is(err, syscall.EPERM) {
			return true
		}
		// ESRCH (or any other error) → no such process → provably dead.
		return false
	}
	if !hasStart {
		// Legacy / non-Linux write: PID-only liveness, already proven by Kill.
		return true
	}
	actualStart, ok := readProcStartTime(pid)
	if !ok {
		// /proc unavailable at read time (non-Linux): fall back to PID-only.
		return true
	}
	return actualStart == recordedStart
}

// SessionReapEligible is the pure reap-eligibility predicate. A session may be
// reaped ONLY when its heartbeat is stale AND its owning process is provably not
// alive. No DB access — slice-2 computes heartbeatStale from the DB and
// ReaperSessionTTL, then calls this.
func SessionReapEligible(heartbeatStale bool, sessDir string) bool {
	return heartbeatStale && !IsSessionProcessAlive(sessDir)
}
