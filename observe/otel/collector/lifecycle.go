// Package collector provides the CollectorLifecycle interface and its
// ProcessCollector implementation for spawning, monitoring, and cleaning up
// wipnote otel-collect child processes.
//
// Future launchers (Codex, Gemini) call Spawn directly without duplicating
// retry/watchdog/cleanup machinery.
package collector

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Lifecycle is the minimal interface for spawning a per-session OTel collector.
type Lifecycle interface {
	// Spawn starts an otel-collect process for the given session and returns
	// the port it is listening on plus a cleanup function. The cleanup function
	// stops the watchdog goroutine, SIGTERMs the current process (waits up to
	// 3s, then SIGKILLs), and removes the .collector-pid file.
	Spawn(binPath, sessionID, projectDir string) (port int, cleanup func(), err error)
}

// SpawnFn is the function signature used by RetrySpawn and the watchdog to
// start a single collector child process. requestedPort=0 lets the kernel
// auto-assign a port; non-zero reuses the given port (used by the watchdog
// to keep the harness's exporter endpoint stable across respawns).
type SpawnFn func(binPath, sessionID, projectDir string, requestedPort int) (int, *os.Process, error)

// ProcessCollectorOpts configures a ProcessCollector.
type ProcessCollectorOpts struct {
	// Stderr is where warning/info/FATAL lines are written. Defaults to os.Stderr.
	Stderr io.Writer

	// StrictMode is reserved for callers that want Spawn errors to be fatal;
	// the ProcessCollector itself does not call os.Exit — that decision belongs
	// to the caller.
	StrictMode bool

	// SpawnFn overrides the default spawn function. Nil means use DefaultSpawnFn.
	// Primarily for tests.
	SpawnFn SpawnFn

	// WatchdogIntervalEnv is the env-var name used to override the watchdog
	// poll interval. Empty string defaults to "WIPNOTE_OTEL_WATCHDOG_INTERVAL".
	WatchdogIntervalEnv string
}

// ProcessCollector implements Lifecycle by managing a real os.Process.
type ProcessCollector struct {
	opts ProcessCollectorOpts
}

// NewProcessCollector returns a new ProcessCollector configured by opts.
func NewProcessCollector(opts ProcessCollectorOpts) *ProcessCollector {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.WatchdogIntervalEnv == "" {
		opts.WatchdogIntervalEnv = "WIPNOTE_OTEL_WATCHDOG_INTERVAL"
	}
	return &ProcessCollector{opts: opts}
}

// Spawn starts the collector, retries up to 3 times with backoff, writes the
// PID file, starts the watchdog, and returns the port and cleanup func. The
// watchdog respawns on the same port so the harness's OTLP exporter endpoint
// remains valid across collector restarts.
//
// On failure it writes a FATAL line to Stderr and returns a non-nil error.
func (c *ProcessCollector) Spawn(binPath, sessionID, projectDir string) (int, func(), error) {
	spawnFn := c.opts.SpawnFn
	if spawnFn == nil {
		spawnFn = DefaultSpawnFn
	}

	port, proc, attempts, err := RetrySpawn(binPath, sessionID, projectDir, 0, 3, spawnFn, c.opts.Stderr)
	if err != nil {
		fmt.Fprintf(c.opts.Stderr, "wipnote: FATAL: collector spawn failed after %d attempts: %v\n", attempts, err)
		return 0, nil, err
	}

	WriteCollectorPID(projectDir, sessionID, proc.Pid)
	procPtr := newProcPointer(proc)
	startReaper(procPtr.Load(), projectDir, sessionID)

	stopWatchdog := StartWatchdog(procPtr, port, binPath, sessionID, projectDir, c.opts.Stderr, spawnFn, c.opts.WatchdogIntervalEnv)
	cleanup := makeCleanup(procPtr, projectDir, sessionID, stopWatchdog)
	return port, cleanup, nil
}

// DefaultSpawnFn starts an otel-collect child process and waits for its
// handshake line ("wipnote-otel-ready port=<N>"). When requestedPort is 0,
// the kernel auto-assigns a port; when non-zero, the collector binds the
// requested port (used by the watchdog to preserve endpoint identity across
// respawns). The child is started in its own process group (Setpgid) so it
// can be independently signalled.
func DefaultSpawnFn(binPath, sessionID, projectDir string, requestedPort int) (int, *os.Process, error) {
	listen := "127.0.0.1:0"
	if requestedPort > 0 {
		listen = fmt.Sprintf("127.0.0.1:%d", requestedPort)
	}
	// --no-idle-timeout: keep the collector alive for the full interactive
	// session. Without this flag, the collector self-exits after 5 min of no
	// OTLP traffic — causing the harness to POST to a dead port for the rest
	// of the session, which the watchdog then respawns in a tight cycle.
	// Bug-28a9d7a7 Part A.
	cmd := exec.Command(binPath, "otel-collect",
		"--session-id", sessionID,
		"--project-dir", projectDir,
		"--listen", listen,
		"--no-idle-timeout",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 0, nil, fmt.Errorf("start otel-collect: %w", err)
	}

	port, err := readHandshake(bufio.NewScanner(stdout))
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return 0, nil, err
	}
	return port, cmd.Process, nil
}

// readHandshake scans stdout for the handshake line within 3s.
func readHandshake(scanner *bufio.Scanner) (int, error) {
	type result struct {
		port int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			var p int
			if _, err := fmt.Sscanf(line, "wipnote-otel-ready port=%d", &p); err == nil {
				ch <- result{port: p}
				return
			}
		}
		ch <- result{err: fmt.Errorf("otel-collect: handshake not found (stdout closed)")}
	}()

	select {
	case r := <-ch:
		return r.port, r.err
	case <-time.After(3 * time.Second):
		return 0, fmt.Errorf("otel-collect: handshake timeout (3s)")
	}
}

// RetrySpawn attempts to spawn the collector up to maxAttempts times.
// requestedPort is forwarded to spawnFn (0 = auto-assign). Backoff delays
// between attempts: 100ms, 300ms, 700ms. Writes a warning line to warnW
// after each non-final failure. Returns port, process, attempts, error.
func RetrySpawn(
	binPath, sessionID, projectDir string,
	requestedPort, maxAttempts int,
	spawnFn SpawnFn,
	warnW io.Writer,
) (int, *os.Process, int, error) {
	backoff := []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, 700 * time.Millisecond}
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		port, proc, err := spawnFn(binPath, sessionID, projectDir, requestedPort)
		if err == nil {
			return port, proc, i + 1, nil
		}
		lastErr = err
		if i < maxAttempts-1 {
			fmt.Fprintf(warnW, "wipnote: warning: collector spawn attempt %d/%d failed: %v\n", i+1, maxAttempts, err)
			if i < len(backoff) {
				time.Sleep(backoff[i])
			}
		}
	}
	return 0, nil, maxAttempts, lastErr
}

// WatchdogInterval returns the polling interval for the collector watchdog.
// The env var name is configurable.
func WatchdogInterval(envKey string) time.Duration {
	if v := os.Getenv(envKey); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Second
}

// StartWatchdog launches a goroutine that polls procPtr.Load() every
// WatchdogInterval(envKey). On process death it calls RetrySpawn with
// originalPort to keep the harness's exporter endpoint stable, updates
// procPtr to the new process, writes the new PID file, and starts a reaper
// for the new process.
//
// The returned stop function is blocking: it closes the done channel and
// waits for the goroutine to exit before returning. Callers can therefore
// rely on procPtr being stable after stop() returns.
func StartWatchdog(
	procPtr *atomic.Pointer[os.Process],
	originalPort int,
	binPath, sessionID, projectDir string,
	warnW io.Writer,
	spawnFn SpawnFn,
	envKey string,
) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(WatchdogInterval(envKey))
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				current := procPtr.Load()
				if current == nil {
					continue
				}
				if err := current.Signal(syscall.Signal(0)); err == nil {
					continue
				}
				fmt.Fprintf(warnW, "wipnote: warning: collector died (pid=%d), respawning on port %d...\n", current.Pid, originalPort)
				_, newProc, _, spawnErr := RetrySpawn(binPath, sessionID, projectDir, originalPort, 3, spawnFn, warnW)
				if spawnErr != nil {
					fmt.Fprintf(warnW, "wipnote: FATAL: collector respawn failed: %v\n", spawnErr)
					return
				}
				WriteCollectorPID(projectDir, sessionID, newProc.Pid)
				startReaper(newProc, projectDir, sessionID)
				procPtr.Store(newProc)
				fmt.Fprintf(warnW, "wipnote: info: collector respawned (pid=%d port=%d)\n", newProc.Pid, originalPort)
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
	}
}

// makeCleanup returns a function that stops the watchdog (waiting for it to
// exit), then SIGTERMs the *current* process tracked by procPtr and waits up
// to 3s before SIGKILL. After the kill path completes, cleanup itself calls
// removeCollectorPIDIfMatches so the PID file is gone synchronously by
// return time — the per-process reaper still does the same removal
// asynchronously (idempotent), but cleanup callers should not have to
// observe a brief stale window.
//
// The returned function is wrapped in sync.Once so callers may safely
// invoke it multiple times — for example, both via `defer cleanup()` and
// via an explicit pre-os.Exit call (since os.Exit bypasses defers).
func makeCleanup(
	procPtr *atomic.Pointer[os.Process],
	projectDir, sessionID string,
	stopWatchdog func(),
) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			stopWatchdog() // blocks until watchdog goroutine exits
			current := procPtr.Load()
			if current == nil {
				return
			}
			_ = current.Signal(syscall.SIGTERM)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if err := current.Signal(syscall.Signal(0)); err != nil {
					removeCollectorPIDIfMatches(projectDir, sessionID, current.Pid)
					return // process exited
				}
				time.Sleep(100 * time.Millisecond)
			}
			_ = current.Kill()
			removeCollectorPIDIfMatches(projectDir, sessionID, current.Pid)
		})
	}
}

// startReaper spawns a goroutine that waits for proc to exit and then
// removes .collector-pid only if it still references this PID. This makes
// independent collector exit (idle timeout, OOM) self-cleaning while
// remaining safe across watchdog respawns: an old reaper firing after the
// watchdog has rotated the PID file will see the new PID and leave the
// file alone.
func startReaper(proc *os.Process, projectDir, sessionID string) {
	go func() {
		_, _ = proc.Wait()
		removeCollectorPIDIfMatches(projectDir, sessionID, proc.Pid)
	}()
}

// RegisterCleanup is preserved for backwards compatibility with the shim in
// cmd/wipnote/claude_otel_collect_spawn.go. It composes a single-process
// reaper and cleanup. New code should call ProcessCollector.Spawn instead.
//
// Deprecated: prefer ProcessCollector.Spawn — RegisterCleanup cannot track
// watchdog-respawned processes.
func RegisterCleanup(proc *os.Process, projectDir, sessionID string) func() {
	procPtr := newProcPointer(proc)
	startReaper(proc, projectDir, sessionID)
	return makeCleanup(procPtr, projectDir, sessionID, func() {})
}

// newProcPointer constructs an atomic.Pointer[os.Process] storing proc.
func newProcPointer(proc *os.Process) *atomic.Pointer[os.Process] {
	var p atomic.Pointer[os.Process]
	p.Store(proc)
	return &p
}

// removeCollectorPIDIfMatches removes the .collector-pid file only when its
// recorded PID matches the given pid. Used by the per-process reaper and by
// makeCleanup to avoid deleting a fresher PID written by the watchdog after
// a respawn.
func removeCollectorPIDIfMatches(projectDir, sessionID string, pid int) {
	pidPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID, ".collector-pid")
	got, _, _, err := readCollectorPIDFile(pidPath)
	if err != nil || got != pid {
		return
	}
	_ = os.Remove(pidPath)
}

// ReadCollectorPIDFile parses a .collector-pid file. The file format is:
//
//	<pid>
//	<starttime>     (optional second line — Linux clock-tick start time)
//
// Returns pid, starttime, hasStart=true when both lines parsed, or
// hasStart=false for legacy single-line files. Exported so callers
// outside this package (e.g. cmd/wipnote status surfaces) can parse
// the PID file consistently with the writer.
func ReadCollectorPIDFile(pidPath string) (pid int, starttime uint64, hasStart bool, err error) {
	return readCollectorPIDFile(pidPath)
}

// readCollectorPIDFile is the internal implementation used by
// removeCollectorPIDIfMatches and IsCollectorAlive.
func readCollectorPIDFile(pidPath string) (pid int, starttime uint64, hasStart bool, err error) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, 0, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return 0, 0, false, fmt.Errorf("empty PID file: %s", pidPath)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse pid %q: %w", lines[0], err)
	}
	if len(lines) >= 2 {
		if st, perr := strconv.ParseUint(strings.TrimSpace(lines[1]), 10, 64); perr == nil {
			return pid, st, true, nil
		}
	}
	return pid, 0, false, nil
}

// readProcStartTime returns the process start time from /proc/<pid>/stat
// field 22 (clock ticks since boot). Returns ok=false on non-Linux systems
// or when the proc entry is unreadable. Used for PID-reuse detection.
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
	// Index 0 here corresponds to field 3 (state); field 22 (starttime) is
	// at index 19 of this slice.
	if len(fields) < 20 {
		return 0, false
	}
	st, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return st, true
}

// IsCollectorAlive verifies the recorded collector PID is alive AND that
// its process start time matches the value recorded at write-time, when
// available. The start-time check protects against PID reuse on
// long-running CI workers; on platforms where /proc is unavailable or
// when the PID file predates the start-time format, the check falls back
// to the PID-only Signal(0) probe.
//
// sessDir is the absolute path to the session directory (typically
// <project>/.wipnote/sessions/<sid>) — the function looks for
// .collector-pid inside it. Returns (alive=false, pid=0) when the file
// is missing or unreadable.
func IsCollectorAlive(sessDir string) (alive bool, pid int) {
	pid, recordedStart, hasStart, err := readCollectorPIDFile(filepath.Join(sessDir, ".collector-pid"))
	if err != nil {
		return false, 0
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, pid
	}
	if proc.Signal(syscall.Signal(0)) != nil {
		return false, pid
	}
	if !hasStart {
		return true, pid // legacy file or non-Linux at write-time
	}
	actualStart, ok := readProcStartTime(pid)
	if !ok {
		return true, pid // /proc unavailable at read-time (non-Linux)
	}
	return actualStart == recordedStart, pid
}

// RemoveCollectorPID removes the .collector-pid file for a session.
// Best-effort: missing file or unreadable directory is not an error.
//
// Unlike removeCollectorPIDIfMatches, this is unconditional. Used by direct
// shim callers; the reaper path uses the conditional variant.
func RemoveCollectorPID(projectDir, sessionID string) {
	pidPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID, ".collector-pid")
	_ = os.Remove(pidPath)
}

// WriteCollectorPID writes the collector PID to the session directory.
// On Linux, also appends the process start time (clock ticks from
// /proc/<pid>/stat field 22) on a second line so future liveness checks
// can detect PID reuse. On non-Linux or when /proc is unreadable, only
// the PID is written; consumers must tolerate single-line files.
//
// Best-effort: errors are silently ignored.
func WriteCollectorPID(projectDir, sessionID string, pid int) {
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	_ = os.MkdirAll(sessDir, 0o755)
	pidPath := filepath.Join(sessDir, ".collector-pid")
	content := strconv.Itoa(pid) + "\n"
	if start, ok := readProcStartTime(pid); ok {
		content += strconv.FormatUint(start, 10) + "\n"
	}
	_ = os.WriteFile(pidPath, []byte(content), 0o644)
}
