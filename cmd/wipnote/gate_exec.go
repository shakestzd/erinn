package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// gateSignalContext returns a context cancelled on SIGINT/SIGTERM, plus a stop
// func that must be called to release the signal handler. Gate runners use it so
// that an operator interrupt (Ctrl-C) cancels the context, which in turn kills
// the whole `go test`/`go build` process group via runManagedGate's watchdog
// instead of leaving an orphaned child running in the sandbox.
func gateSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// gate_exec.go hardens the quality-gate command runners for sandboxed /
// devcontainer environments. Two cross-cutting concerns live here so both the
// guard-profile runner (runGateCommand) and the legacy autodetect runner
// (runGate) share one implementation:
//
//  1. noexec /tmp (bug-58205bf3) — Go writes test binaries and the build cache
//     under TMPDIR (default /tmp). In the devcontainer /tmp is mounted noexec,
//     so `go test` fails with "permission denied" when it execs the freshly
//     linked test binary. We detect the noexec condition and redirect GOTMPDIR
//     (and GOCACHE when needed) to an exec-capable directory inside the project
//     tree, matching the existing convention documented in workitem_commit.go.
//
//  2. orphaned subprocesses + invisible progress (bug-c3c9278a) — gate children
//     ran via bare exec.Command with no process group and no context, so on
//     SIGINT the `go test` child was orphaned. We run every gate command in its
//     own process group and kill the whole group on context cancellation,
//     mirroring the managed-daemon pattern in serve_child.go. We also announce
//     each command ("running: ...") so progress is visible when stdout is
//     buffered by the harness tty.

// gateTmpDirName is the exec-capable scratch directory created inside the
// project tree when the inherited TMPDIR is noexec or empty. It reuses the
// ".test-tmp" convention recognised by isTestTmpPath in workitem_commit.go so
// it can never trigger real git mutations during gate test runs.
const gateTmpDirName = ".test-tmp"

// gateExecEnv builds the environment for a gate subprocess. It starts from the
// inherited environment and, when the effective temp dir is unusable for Go
// (empty or noexec), injects GOTMPDIR/GOCACHE pointing at an exec-capable
// directory under codeRoot. The returned bool reports whether a redirect was
// applied (used to tailor the remediation hint on failure).
func gateExecEnv(codeRoot string) (env []string, redirected bool, tmpDir string) {
	env = os.Environ()

	tmp := effectiveTmpDir(env)
	// If the inherited temp dir already exists, is set, and is exec-capable,
	// leave the environment untouched.
	if tmp != "" && dirIsExecCapable(tmp) {
		return env, false, tmp
	}

	// Pick an exec-capable scratch dir inside the project tree.
	dir := filepath.Join(codeRoot, gateTmpDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Cannot create the redirect target — fall back to the inherited env.
		// The command may still fail, but the remediation hint will fire.
		return env, false, tmp
	}
	if !dirIsExecCapable(dir) {
		// Even the project-tree scratch dir is noexec (unusual). Give up on the
		// redirect; the failure path will print the remediation command.
		return env, false, tmp
	}

	env = upsertEnv(env, "GOTMPDIR", dir)
	// Only redirect GOCACHE when the inherited one is unusable. The build cache
	// is execed during cgo/test-binary linking, so a noexec GOCACHE is just as
	// fatal as a noexec GOTMPDIR.
	if !goCacheUsable(env) {
		cache := filepath.Join(dir, "gocache")
		if err := os.MkdirAll(cache, 0o755); err == nil && dirIsExecCapable(cache) {
			env = upsertEnv(env, "GOCACHE", cache)
		}
	}
	return env, true, dir
}

// effectiveTmpDir returns the temp dir Go would use given env: GOTMPDIR wins,
// then TMPDIR, else "" (Go defaults to /tmp).
func effectiveTmpDir(env []string) string {
	if v := lookupEnv(env, "GOTMPDIR"); strings.TrimSpace(v) != "" {
		return v
	}
	if v := lookupEnv(env, "TMPDIR"); strings.TrimSpace(v) != "" {
		return v
	}
	// Go's documented default when neither is set.
	return "/tmp"
}

// goCacheUsable reports whether the GOCACHE in env (explicit or default) is
// exec-capable. An unset/"off" GOCACHE is treated as usable (Go derives a
// per-user default under the OS cache dir, which is normally exec-capable).
func goCacheUsable(env []string) bool {
	c := strings.TrimSpace(lookupEnv(env, "GOCACHE"))
	if c == "" || c == "off" {
		return true
	}
	return dirIsExecCapable(c)
}

// dirIsExecCapable reports whether a small file written into dir can be made
// executable and executed. It first attempts a cheap statfs MS_NOEXEC check and
// falls back to (or confirms with) a write+exec probe. Results are memoised per
// resolved directory because gate runs invoke several commands in a row.
func dirIsExecCapable(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}

	execCapableCacheMu.Lock()
	if v, ok := execCapableCache[abs]; ok {
		execCapableCacheMu.Unlock()
		return v
	}
	execCapableCacheMu.Unlock()

	result := probeExecCapable(abs)

	execCapableCacheMu.Lock()
	execCapableCache[abs] = result
	execCapableCacheMu.Unlock()
	return result
}

var (
	execCapableCache   = map[string]bool{}
	execCapableCacheMu sync.Mutex
)

// probeExecCapable performs the actual detection: a statfs MS_NOEXEC fast-path
// (Linux) plus a write+exec probe. A directory is exec-capable only when BOTH
// agree it is (the statfs check is a fast reject; the probe is authoritative).
func probeExecCapable(dir string) bool {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	// Fast reject via statfs flags (no-op on platforms without MS_NOEXEC).
	if mountIsNoexec(dir) {
		return false
	}
	// Authoritative probe: write a tiny script, chmod +x, exec it.
	f, err := os.CreateTemp(dir, ".wipnote-execprobe-*.sh")
	if err != nil {
		return false
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.WriteString("#!/bin/sh\nexit 0\n"); err != nil {
		_ = f.Close()
		return false
	}
	if err := f.Close(); err != nil {
		return false
	}
	if err := os.Chmod(name, 0o755); err != nil {
		return false
	}
	if err := exec.Command(name).Run(); err != nil {
		return false
	}
	return true
}

// gateTmpRemediation returns a copy-paste command the operator can run to give
// the gate an exec-capable temp dir, tailored to codeRoot.
func gateTmpRemediation(codeRoot string) string {
	dir := filepath.Join(codeRoot, gateTmpDirName)
	return fmt.Sprintf("mkdir -p %q && TMPDIR=%q GOTMPDIR=%q wipnote check --gate", dir, dir, dir)
}

// isLikelyNoexecFailure heuristically detects the signature of a noexec /tmp
// failure in captured command output.
func isLikelyNoexecFailure(output string) bool {
	o := strings.ToLower(output)
	if !strings.Contains(o, "permission denied") {
		return false
	}
	// Go test/build emit the failing path; the marker is a noexec exec attempt.
	return strings.Contains(o, "/tmp/") ||
		strings.Contains(o, "go-build") ||
		strings.Contains(o, "fork/exec") ||
		strings.Contains(o, "exec format") ||
		strings.Contains(o, "text file busy")
}

// runManagedGate runs name's command (argv) in dir with env, streaming combined
// output to stdout/stderr and capturing it into the returned string. The child
// runs in its own process group; if ctx is cancelled the whole group is killed
// (SIGKILL) so no `go test`/`go build` descendant is orphaned. A "running: ..."
// line is emitted before execution so progress is visible under buffered ttys.
func runManagedGate(ctx context.Context, name, dir string, env []string, stdout, stderr io.Writer, argv ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fmt.Fprintf(stderr, "running: %s (%s)\n", name, strings.Join(argv, " "))

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	// Own process group so a cancel can reap the entire `go test` subtree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var combined strings.Builder
	cmd.Stdout = io.MultiWriter(stdout, &combined)
	cmd.Stderr = io.MultiWriter(stderr, &combined)

	if err := cmd.Start(); err != nil {
		return combined.String(), err
	}

	// Watchdog: on ctx cancellation, SIGKILL the child's process group.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-done:
		}
	}()

	err := cmd.Wait()
	close(done)
	return combined.String(), err
}

// killProcessGroup sends SIGKILL to the negative pgid of cmd's process (the
// whole group created by Setpgid), falling back to the single process if the
// pgid lookup fails. Mirrors the managed-daemon teardown in serve_child.go.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pgid, err := syscall.Getpgid(pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

// lookupEnv returns the value of key in a KEY=VALUE slice ("" if absent).
func lookupEnv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// upsertEnv returns env with key set to val (replacing any existing entry).
func upsertEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+val)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}
