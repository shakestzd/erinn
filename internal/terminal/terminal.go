// Package terminal manages ttyd sidecar processes for the embedded terminal
// feature. Each Start call spawns a new ttyd process on a free localhost port
// running a launcher shell (htmlgraph claude/codex/gemini, or claude with
// bypass-permissions for yolo). Stop signals the process; StopAll is called on
// graceful server shutdown.
package terminal

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// StartRequest describes one ttyd session to spawn. Zero-valued fields fall
// back to MVP defaults: Agent=claude, Mode=dev, CWD=caller-supplied default,
// WorkItem=unset (no attribution prefix). The struct form is used instead of
// positional parameters so new fields can be added without breaking callers.
type StartRequest struct {
	Agent    string
	Mode     string
	CWD      string
	WorkItem string
}

// buildShellCmd returns the bash one-liner that ttyd will execute for the
// given agent/mode/work-item combination. Zero-valued agent/mode fall back to
// claude/dev. When workItem is set, the command is prefixed with an
// attribution step so every spawned session is linked to its work item in the
// lineage graph.
//
// Agent routing:
//   - claude/codex/gemini: invoked via their `htmlgraph <agent>` wrapper so
//     HTMLGRAPH_AGENT env injection flows correctly.
//   - yolo: invoked directly as `claude --permission-mode bypassPermissions`
//     (not the `htmlgraph yolo` wrapper) to avoid nested-process chains and
//     duplicate wrapper side effects.
func buildShellCmd(agent, mode, workItem string) string {
	if agent == "" {
		agent = "claude"
	}
	if mode == "" {
		mode = "dev"
	}

	var cmd string
	switch agent {
	case "yolo":
		cmd = "claude --permission-mode bypassPermissions"
	default:
		cmd = "htmlgraph " + agent
		if mode == "dev" {
			cmd += " --dev"
		}
	}

	if workItem != "" {
		cmd = "htmlgraph feature start " + workItem + " >/dev/null 2>&1; " + cmd
	}
	return cmd
}

// session tracks a running ttyd process.
type session struct {
	cmd      *exec.Cmd
	port     int
	agent    string
	mode     string
	cwd      string
	workItem string
}

// Manager owns the lifecycle of ttyd sidecar processes.
type Manager struct {
	mu       sync.Mutex
	sessions map[int]*session // keyed by pid
}

// NewManager creates a ready-to-use Manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[int]*session),
	}
}

// freePort binds to 127.0.0.1:0, reads the assigned port, and releases the
// listener. There is a small TOCTOU window, but it is acceptable for an MVP
// sidecar tool where collisions are rare.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// waitForPort polls 127.0.0.1:<port> with TCP dials until the port accepts
// connections or the timeout expires. Used to ensure ttyd has actually bound
// the listening socket before returning from Start.
func waitForPort(port int, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("ttyd did not bind %s within %s", addr, timeout)
}

// Start spawns a ttyd process on a free port running the shell command
// produced by buildShellCmd. Zero-valued StartRequest fields fall back to
// MVP defaults (claude, dev, defaultDir). Returns the port and pid on success.
func (m *Manager) Start(req StartRequest, defaultDir string) (port int, pid int, err error) {
	// Ensure ttyd is available before doing anything else.
	if _, err = exec.LookPath("ttyd"); err != nil {
		return 0, 0, fmt.Errorf("ttyd not found on PATH — install with: brew install ttyd")
	}

	port, err = freePort()
	if err != nil {
		return 0, 0, fmt.Errorf("could not find free port: %w", err)
	}

	cwd := req.CWD
	if cwd == "" {
		cwd = defaultDir
	}

	shellCmd := buildShellCmd(req.Agent, req.Mode, req.WorkItem)

	cmd := exec.Command(
		"ttyd",
		"-p", strconv.Itoa(port),
		"-W",              // writable (allows input)
		"-i", "127.0.0.1", // bind to localhost only
		"bash", "-lc", shellCmd,
	)
	cmd.Dir = cwd

	if err = cmd.Start(); err != nil {
		return 0, 0, fmt.Errorf("failed to start ttyd: %w", err)
	}

	pid = cmd.Process.Pid
	s := &session{
		cmd:      cmd,
		port:     port,
		agent:    req.Agent,
		mode:     req.Mode,
		cwd:      cwd,
		workItem: req.WorkItem,
	}

	m.mu.Lock()
	m.sessions[pid] = s
	m.mu.Unlock()

	// Reap the process and remove from map when it exits.
	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		delete(m.sessions, pid)
		m.mu.Unlock()
	}()

	// Wait for ttyd to actually bind the listening socket. If the deadline
	// expires, kill the process and return an error. This prevents the race
	// condition where we return the port before ttyd is ready, causing the
	// frontend to get ERR_CONNECTION_REFUSED and cache a broken state.
	if err = waitForPort(port, 3*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return 0, 0, err
	}

	return port, pid, nil
}

// Stop signals the ttyd process identified by pid with SIGTERM, waiting up
// to 3 seconds before escalating to SIGKILL.
func (m *Manager) Stop(pid int) error {
	m.mu.Lock()
	s, ok := m.sessions[pid]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no terminal session with pid %d", pid)
	}

	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = s.cmd.Process.Kill()
	}

	// Give the process a moment to exit cleanly; force-kill if it lingers.
	done := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = s.cmd.Process.Kill()
	}

	m.mu.Lock()
	delete(m.sessions, pid)
	m.mu.Unlock()
	return nil
}

// StopAll terminates all running sessions. Called on graceful server shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	pids := make([]int, 0, len(m.sessions))
	for pid := range m.sessions {
		pids = append(pids, pid)
	}
	m.mu.Unlock()

	for _, pid := range pids {
		_ = m.Stop(pid)
	}
}
