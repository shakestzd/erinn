package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// AutoSpawn controls whether SubmitOrSpawn is allowed to fork a headless
// writer daemon when none is reachable. Spawn is a separate, opt-in step
// from a plain dial so callers that only want to USE an existing writer
// (and never start one) keep the dumb dial-or-fail client.
//
// LIVE-SESSION SAFETY (feat-075c110d): the whole spawn+readiness+submit
// sequence is strictly time-bounded by the caller's context (see
// SubmitOrSpawn). On ANY failure, timeout, or sandbox/forbidden-spawn the
// caller receives ErrWriterUnavailable and falls back to direct-open. The
// spawn is guarded by the O_EXCL writer lease so two racing hooks never
// fork-bomb a pair of daemons.

// spawnReadinessBudget bounds how long SubmitOrSpawn waits for a freshly
// forked writer to bind its socket. Kept well under the ~2s total budget so
// the dial + submit that follow still fit before the caller's deadline.
const spawnReadinessBudget = 1500 * time.Millisecond

// spawnDialInterval is the backoff between readiness dials while waiting for
// a just-spawned writer to bind.
const spawnDialInterval = 25 * time.Millisecond

// SubmitOrSpawn submits env to the per-project writer, auto-spawning a
// headless writer daemon when none is reachable.
//
// Flow:
//  1. Try a plain Submit. If it succeeds (writer already up) → done.
//  2. On ErrWriterUnavailable, try to acquire the O_EXCL writer lease.
//       - Lease acquired → we are the spawner: fork the headless writer,
//         release our lease so the child can claim it, then dial-wait for
//         readiness and submit.
//       - ErrLeaseHeld → another racer already owns/started the writer:
//         just dial-wait for its socket and submit.
//  3. Any error/timeout within the caller's ctx deadline → ErrWriterUnavailable.
//
// SubmitOrSpawn NEVER blocks indefinitely: every wait is bounded by ctx and
// by spawnReadinessBudget. selfExe is the wipnote binary to fork (resolved
// by the caller via os.Executable); projectRoot is the project whose writer
// to target.
func (c *WriterClient) SubmitOrSpawn(ctx context.Context, projectRoot, selfExe string, env Envelope) (Ack, error) {
	// Fast path: an existing writer answers immediately.
	ack, err := c.Submit(ctx, env)
	if err == nil {
		return ack, nil
	}
	if err != ErrWriterUnavailable {
		// A real transport fault (not "no daemon") — surface as unavailable
		// so the caller falls back rather than interpreting a half-dead
		// owner as a hard failure.
		return Ack{}, ErrWriterUnavailable
	}
	if ctx.Err() != nil {
		return Ack{}, ErrWriterUnavailable
	}

	// Opt-out: operators (and tests) can disable auto-spawn entirely. When
	// set, an unreachable writer immediately yields ErrWriterUnavailable so
	// the caller falls back to direct-open without ever forking.
	if os.Getenv("WIPNOTE_NO_AUTO_WRITER") != "" {
		return Ack{}, ErrWriterUnavailable
	}

	// No writer reachable. Decide whether WE spawn one or join a racer's.
	lease, lerr := AcquireLease(projectRoot)
	switch {
	case lerr == nil:
		// We won the lease. We are not the long-lived writer, though — the
		// child must own the lease. Release ours, then spawn. If the child
		// fails to start, the lease is simply free for the next attempt.
		_ = lease.Release()
		if serr := spawnHeadlessWriter(selfExe, projectRoot); serr != nil {
			return Ack{}, ErrWriterUnavailable
		}
	case lerr == ErrLeaseHeld:
		// Another racer holds the lease; it is (or is about to be) the
		// writer. Fall through to dial-wait for its socket.
	default:
		// Lease acquisition error (e.g. sandbox forbids file create) → bail
		// to fallback.
		return Ack{}, ErrWriterUnavailable
	}

	// Dial-wait for the socket to come up, then submit. Bounded by the
	// smaller of ctx deadline and spawnReadinessBudget.
	if !c.waitForSocket(ctx) {
		return Ack{}, ErrWriterUnavailable
	}
	ack, err = c.Submit(ctx, env)
	if err != nil {
		return Ack{}, ErrWriterUnavailable
	}
	return ack, nil
}

// waitForSocket polls the writer socket by DIALING (not by scanning child
// stdout) until a connection succeeds, the readiness budget elapses, or ctx
// fires. A successful dial is the only proof the writer is ready to accept
// envelopes. The probe connection is closed immediately.
func (c *WriterClient) waitForSocket(ctx context.Context) bool {
	deadline := time.Now().Add(spawnReadinessBudget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		conn, err := net.DialTimeout("unix", c.socketPath, spawnDialInterval)
		if err == nil {
			_ = conn.Close()
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(spawnDialInterval):
		}
	}
}

// spawnHeadlessWriter forks `<selfExe> --project-dir <projectRoot>
// _serve-child --headless` as a DETACHED process in its own process group so
// it outlives the spawning hook subprocess. Unlike the supervised
// _serve-child children, the writer must SURVIVE its spawner's exit (the
// hook exits in milliseconds), so it deliberately does NOT set Pdeathsig.
//
// stdout/stderr are routed to .wipnote/logs/writer.log; stdin is closed. The
// returned error is non-nil only when the fork itself fails (e.g. sandboxed
// exec) — readiness is proven separately by the caller's dial-wait.
func spawnHeadlessWriter(selfExe, projectRoot string) error {
	if selfExe == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve self exe: %w", err)
		}
		selfExe = exe
	}
	logDir := filepath.Join(projectRoot, ".wipnote", "logs")
	_ = os.MkdirAll(logDir, 0o755)
	var out *os.File
	if f, err := os.OpenFile(filepath.Join(logDir, "writer.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		out = f
	}

	cmd := exec.Command(selfExe, "--project-dir", projectRoot, "_serve-child", "--headless")
	cmd.Dir = projectRoot
	cmd.Stdin = nil
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
	}
	cmd.Env = os.Environ()
	// Detach into a new process group so a SIGKILL to the hook's group does
	// not propagate. Portable across Linux + darwin (no Pdeathsig: the
	// writer must outlive the short-lived spawning hook).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		if out != nil {
			_ = out.Close()
		}
		return fmt.Errorf("spawn headless writer: %w", err)
	}
	// Reap the immediate child handle in the background so we don't leave a
	// zombie if it exits early (e.g. lost the lease race and exited 0). The
	// long-lived writer is in its own process group; this Wait only reaps
	// our direct fork handle.
	go func() { _ = cmd.Wait() }()
	if out != nil {
		_ = out.Close() // child holds its own fd after Start
	}
	return nil
}
