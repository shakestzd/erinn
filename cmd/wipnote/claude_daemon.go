package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/hooks"
)

// Launcher-side daemon guarantee (feat-f6759e37).
//
// A launcher-started session GUARANTEES the work-item daemon: this is where
// that promise is made. The launcher starts a daemon or attaches to one already
// running, proves it speaks the read protocol, and publishes its socket path
// into the child environment. Hooks then read work-item state from canonical
// state via that socket instead of from the derived index — which is the whole
// point, since a hook is a fresh process that cannot afford the canonical parse
// itself.
//
// The promise is made ONLY when it can be kept. If anything prevents it the
// launcher publishes nothing, and the session runs under the unguaranteed
// contract — identical to how every session behaved before this feature. That
// mode is ANNOUNCED, never inferred: the user is told which contract they got.

// ensureDaemonBudget bounds the launcher's whole ensure step so a wedged socket
// cannot delay a launch indefinitely. On expiry the session simply runs
// unguaranteed.
const ensureDaemonBudget = 8 * time.Second

// ensureDaemonForSession guarantees the daemon for wipnoteRoot and returns the
// environment assignments to layer into the child process.
//
// A nil/empty return means no guarantee was made; the caller must not fabricate
// one. Every failure path announces itself on stderr, because a session
// silently running under a different contract than the user expects is the
// kind of ambiguity this feature exists to remove.
func ensureDaemonForSession(wipnoteRoot string) []string {
	if wipnoteRoot == "" {
		return nil
	}
	if !hooks.IswipnoteProject(wipnoteRoot) {
		return nil
	}

	selfExe, err := os.Executable()
	if err != nil {
		announceUnguaranteed(fmt.Sprintf("cannot resolve the wipnote binary to start it (%v)", err))
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ensureDaemonBudget)
	defer cancel()

	socket, err := daemon.EnsureDaemon(ctx, wipnoteRoot, selfExe, os.Getpid())
	if err != nil {
		switch {
		case errors.Is(err, daemon.ErrDaemonReadUnsupported):
			announceUnguaranteed("a daemon from an older wipnote build is already running for this " +
				"project and does not serve reads; it belongs to another session, so wipnote will " +
				"not stop it")
		default:
			announceUnguaranteed(err.Error())
		}
		return nil
	}

	fmt.Fprintf(os.Stderr,
		"wipnote: work-item daemon guaranteed for this session (socket %s)\n", socket)
	return []string{hooks.DaemonSocketEnv + "=" + socket}
}

// announceUnguaranteed states, in the user's terminal, that this session runs
// without the daemon guarantee and why. Hooks in this session read the derived
// index exactly as they always have.
func announceUnguaranteed(reason string) {
	fmt.Fprintf(os.Stderr,
		"wipnote: running WITHOUT the work-item daemon guarantee — %s\n"+
			"wipnote: hooks will read the derived index, as they do in any session no launcher started.\n",
		reason)
}
