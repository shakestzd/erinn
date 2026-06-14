package main

import (
	"os/exec"

	"github.com/shakestzd/wipnote/internal/launcher"
)

// harnessResult describes how the parent launcher should terminate after
// the harness child has reaped. Exactly one of the three fields is
// meaningful per call:
//
//   - Err non-nil       → return this error to the caller (start failure or
//     other unexpected error before/around the child run).
//   - ReraiseSig non-0  → signal.Reset and re-raise this signal so the
//     parent exits with 128+signum POSIX semantics. Used for both
//     parent-received signals and child-was-signal-terminated cases.
//   - ExitCode non-0    → os.Exit(ExitCode) to propagate the child's
//     ordinary non-zero return code.
//   - All zero          → child exited 0; return nil.
type harnessResult = launcher.HarnessResult

// runHarnessWithCleanupCore is the testable core of runHarnessWithCleanup.
// It runs the child under SIGINT/SIGTERM signal handling, runs cleanup
// once the child reaps, and returns a harnessResult describing how the
// caller should terminate. Pure — no os.Exit, no syscall.Kill — so tests
// can assert on the result without crashing the test binary.
func runHarnessWithCleanupCore(c *exec.Cmd, cleanup func()) harnessResult {
	return launcher.RunHarnessWithCleanupCore(c, cleanup)
}

// runHarnessWithCleanup runs the harness child process under a signal
// handler that intercepts SIGINT and SIGTERM, ensuring cleanup runs
// before the launcher exits. It is the production entry point — for the
// testable core that returns a result struct without calling os.Exit /
// syscall.Kill, see runHarnessWithCleanupCore.
//
// Behavior:
//   - On parent-received SIGINT/SIGTERM: forward to the child, run
//     cleanup, then re-raise the signal in the parent for 128+signum
//     POSIX exit semantics.
//   - On child signal-termination (e.g. Ctrl-C reached the child via
//     the terminal foreground group): re-raise the same signal in the
//     parent so the launcher exits with the conventional 128+signum
//     code instead of -1 / 255.
//   - On child non-zero exit: os.Exit with the child's exit code.
//   - On child exit 0: return nil.
//
// cleanup may be nil — if so, no cleanup is invoked but signal handling
// still runs.
func runHarnessWithCleanup(c *exec.Cmd, cleanup func()) error {
	return launcher.RunHarnessWithCleanup(c, cleanup)
}
