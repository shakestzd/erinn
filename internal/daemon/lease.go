// Package daemon implements the per-project write-owner transport
// (plan-bb91616a Phase A). It graduates the in-process serve_child
// writequeue into a process-spanning single-writer service reachable over
// a per-project Unix domain socket.
//
// MVP-2 SCOPE (feat-075c110d): transport + lifecycle foundation ONLY.
//
//	This package provides three building blocks and nothing more:
//	  - lease.go   — O_EXCL single-owner lease (writer.pid)
//	  - socket.go  — the Unix-socket listener that applies versioned
//	                 write-op envelopes through the existing writequeue
//	  - client.go  — WriterClient.Submit (dial, send envelope, await ack)
//
//	No hook or CLI caller is wired to this package in MVP-2. The existing
//	serve_child default path is unchanged. dbgate cutover (MVP-3) and CLI
//	cutover (MVP-4) follow. Durable cross-restart dedup, spool fallback,
//	and reindex coordination are DEFERRED (slices 3/6).
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LeasePath returns the path of the per-project writer lease file. The
// lease lives under .wipnote/ alongside the socket so a future global
// coordinator (trk-cb80b7da) can discover it by project root.
func LeasePath(projectRoot string) string {
	return filepath.Join(projectRoot, ".wipnote", "writer.pid")
}

// SocketPath returns the path of the per-project writer Unix domain
// socket. Co-located with the lease so both are registry-discoverable
// from the project root with no protocol change (slice-1 coordinator-ready
// constraint).
func SocketPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".wipnote", "writer.sock")
}

// Lease is an acquired single-owner write lease. The holder is the sole
// authority that may run the socket listener + writequeue for its project
// DB. Release deletes the lease file.
type Lease struct {
	path string
}

// ErrLeaseHeld is returned by AcquireLease when a live owner already holds
// the lease. A racing loser treats this as the signal to dial the existing
// socket instead of spawning its own listener.
var ErrLeaseHeld = fmt.Errorf("daemon: writer lease already held by a live owner")

// AcquireLease attempts to become the single write-owner for projectRoot.
//
// Ownership is established by atomically creating the lease file with
// O_CREATE|O_EXCL — only one racer can win that syscall. If the file
// already exists, the recorded PID is liveness-checked with kill(pid,0)
// (the exact idiom from cmd/wipnote/claude_serve_autostart.go:checkServeLock).
// A live owner yields ErrLeaseHeld; a stale owner (dead PID or malformed
// file) is reclaimed by removing the file and retrying the O_EXCL create
// once.
//
// MVP-2 does not auto-spawn: callers/serve own spawning. AcquireLease is
// the primitive the headless writer-only serve_child mode uses to claim
// ownership before opening the writable DB and binding the socket.
func AcquireLease(projectRoot string) (*Lease, error) {
	path := LeasePath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ensure .wipnote dir: %w", err)
	}

	if l, err := tryCreateLease(path); err == nil {
		return l, nil
	} else if !os.IsExist(err) {
		return nil, fmt.Errorf("create lease: %w", err)
	}

	// Lease file exists. Owner alive -> caller is a racing loser.
	if leaseOwnerAlive(path) {
		return nil, ErrLeaseHeld
	}

	// Stale lease (dead PID / malformed): reclaim by removing and
	// re-attempting the O_EXCL create exactly once. A second racer that
	// removed-and-recreated between our Remove and create loses the
	// O_EXCL — it surfaces as ErrLeaseHeld below, which is correct.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale lease: %w", err)
	}
	if l, err := tryCreateLease(path); err == nil {
		return l, nil
	} else if os.IsExist(err) {
		return nil, ErrLeaseHeld
	} else {
		return nil, fmt.Errorf("recreate lease after stale: %w", err)
	}
}

// tryCreateLease performs the atomic O_EXCL create + PID write. Returns an
// os.IsExist error when another owner won the race.
func tryCreateLease(path string) (*Lease, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		// Best-effort cleanup so a half-written lease doesn't wedge the
		// next acquirer behind a malformed (treated-as-stale) file.
		_ = os.Remove(path)
		return nil, fmt.Errorf("write pid: %w", err)
	}
	return &Lease{path: path}, nil
}

// leaseOwnerAlive reports whether the PID recorded in the lease file refers
// to a live process that is actually a wipnote writer. A malformed file,
// empty file, dead PID, or (on Linux) a live process that is NOT a wipnote
// writer counts as not-alive (reclaimable). Uses os.FindProcess + Signal(0)
// for portability, with /proc/<pid>/cmdline verification on Linux to guard
// against PID reuse.
func leaseOwnerAlive(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false // missing or unreadable
	}
	pidStr := strings.TrimSpace(string(data))
	if pidStr == "" {
		return false // empty file — no owner
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false // malformed — treat as stale
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// kill -0 checks process existence without delivering a signal.
	if proc.Signal(syscall.Signal(0)) != nil {
		return false // PID is dead
	}
	// Process is alive. On Linux, verify it's actually a wipnote writer to
	// guard against PID reuse. On other platforms, trust the liveness check.
	return isWriterProcess(pid, path)
}

// isWriterProcess verifies that a live PID is actually a wipnote headless
// writer process. On Linux, it checks /proc/<pid>/cmdline for the
// "headless-writer" invocation. On other platforms, it returns true (trust
// the liveness check). Returns false if the check fails or the process is not
// a writer.
func isWriterProcess(pid int, leasePath string) bool {
	// On non-Linux, we trust the liveness check (kill -0) and skip cmdline
	// verification. This keeps the code portable and avoids best-effort
	// failures on systems without /proc.
	return isWriterProcessImpl(pid, leasePath)
}

// LeaseOwnerAlive reports whether a live process currently holds the writer
// lease for projectRoot. It is the exported probe serve_child uses to decide
// whether a writer daemon is already running (and thus must NOT be double-
// spawned) — the O_EXCL lease remains the single-owner authority; this is just
// a cheap pre-check (feat-075c110d increment 2). A missing/malformed/dead-PID
// lease reports false.
func LeaseOwnerAlive(projectRoot string) bool {
	return leaseOwnerAlive(LeasePath(projectRoot))
}

// Release removes the lease file. Safe to call multiple times. The socket
// file is owned by the listener (socket.go) and removed there.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Path returns the lease file path (for diagnostics/tests).
func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
