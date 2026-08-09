package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Launcher-side daemon guarantee (feat-f6759e37).
//
// OWNERSHIP. The launcher owns availability. It starts a daemon, or attaches to
// one already running, and then writes the socket path into the session
// environment so hooks never have to negotiate. It NEVER stops the daemon on
// session exit: a shared daemon outlives any one session, and stopping it would
// break every other session attached to it. What bounds the daemon's life
// instead is the attachment — see Listener.isIdle: once every attached launcher
// process has exited, the ordinary idle-exit resumes.

// ErrDaemonReadUnsupported is returned by EnsureDaemon when a daemon is already
// resident for this project but does not speak the read protocol — an older
// build left running from a previous install.
//
// This is deliberately NOT treated as "start another one": the lease permits a
// single writer per project, and killing another session's daemon to upgrade it
// is not the launcher's call. The launcher instead declines to promise
// availability, announces that, and the session runs under the unguaranteed
// contract — which works exactly as it always has.
var ErrDaemonReadUnsupported = errors.New("daemon: resident daemon does not speak the read protocol")

// ensureReadyBudget bounds the wait for a freshly spawned daemon to bind and
// answer a ping. It is larger than the write path's spawnReadinessBudget
// because this runs once at launch, in front of a human, not inside a hook.
const ensureReadyBudget = 5 * time.Second

// ensureDialInterval is the backoff between readiness dials.
const ensureDialInterval = 25 * time.Millisecond

// EnsureDaemon guarantees a read-capable daemon for projectRoot and returns the
// socket path to publish into the session environment.
//
// attachPID is the process whose lifetime should hold the daemon open —
// normally the launcher's own pid. While it lives the daemon will not
// idle-exit, so a hook firing after a long pause still finds the daemon the
// launcher promised.
//
// A non-nil error means NO guarantee could be made. The caller must then run
// the session under the unguaranteed contract and SAY SO, rather than
// publishing a socket path that hooks would trust.
func EnsureDaemon(ctx context.Context, projectRoot, selfExe string, attachPID int) (string, error) {
	client := NewReadClient(projectRoot)

	// Fast path: a daemon is already up. Attach and we are done — this is the
	// "attach if one is already running" half of the ownership rule.
	if _, err := client.Ping(ctx); err == nil {
		return attachAndReturn(ctx, client, attachPID)
	} else if errors.Is(err, ErrReadUnsupported) {
		return "", ErrDaemonReadUnsupported
	}

	if os.Getenv("WIPNOTE_NO_AUTO_WRITER") != "" {
		return "", fmt.Errorf("daemon: auto-start disabled by WIPNOTE_NO_AUTO_WRITER")
	}

	// No daemon reachable. Take the O_EXCL lease to decide whether WE spawn or
	// join a racer's spawn — the same guard the write path uses, so two racing
	// launchers can never fork a pair of daemons.
	lease, lerr := AcquireLease(projectRoot)
	switch {
	case lerr == nil:
		// We hold the lease but we are not the daemon; the child must own it.
		// Release, then spawn. A child that fails to start simply leaves the
		// lease free for the next attempt.
		_ = lease.Release()
		if serr := spawnHeadlessWriter(selfExe, projectRoot); serr != nil {
			return "", fmt.Errorf("daemon: spawn: %w", serr)
		}
	case lerr == ErrLeaseHeld:
		// Another launcher is starting one; wait for its socket.
	default:
		return "", fmt.Errorf("daemon: acquire lease: %w", lerr)
	}

	if !waitForSocketPath(ctx, client.SocketPath(), ensureReadyBudget) {
		return "", fmt.Errorf("daemon: socket did not come up within %s", ensureReadyBudget)
	}
	if _, err := client.Ping(ctx); err != nil {
		if errors.Is(err, ErrReadUnsupported) {
			return "", ErrDaemonReadUnsupported
		}
		return "", fmt.Errorf("daemon: ping after spawn: %w", err)
	}
	return attachAndReturn(ctx, client, attachPID)
}

// attachAndReturn registers attachPID with the daemon and returns its socket.
// A failed attach is fatal to the guarantee: without it the daemon may
// idle-exit mid-session, and the availability policy gives hooks no way to
// recover from that except by pausing the agent.
func attachAndReturn(ctx context.Context, client *ReadClient, attachPID int) (string, error) {
	if attachPID <= 0 {
		attachPID = os.Getpid()
	}
	if _, err := client.Attach(ctx, attachPID); err != nil {
		return "", fmt.Errorf("daemon: attach: %w", err)
	}
	return client.SocketPath(), nil
}

// waitForSocketPath polls a Unix socket by DIALING until a connection succeeds,
// budget elapses, or ctx fires. A successful dial is the only proof the daemon
// is accepting frames; the probe connection closes immediately.
func waitForSocketPath(ctx context.Context, socketPath string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		conn, err := net.DialTimeout("unix", socketPath, ensureDialInterval)
		if err == nil {
			_ = conn.Close()
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(ensureDialInterval):
		}
	}
}
