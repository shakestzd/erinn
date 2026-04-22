// Package terminal manages ttyd sidecar processes for the embedded terminal
// feature. Each Start call spawns a new ttyd process on a free localhost port
// running the selected agent in the given project directory. Stop signals the
// process; StopAll is called on graceful server shutdown.
package terminal

import (
	"crypto/rand"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// StartRequest holds the parameters for starting a terminal session.
// Zero-valued fields fall back to MVP defaults: agent=claude, mode=dev,
// cwd=server projectDir, workItem=empty.
type StartRequest struct {
	// Agent selects which AI tool to run (claude, codex, gemini, yolo).
	// Defaults to "claude" when empty.
	Agent string
	// Mode controls launch flags (dev, normal, auto).
	// Defaults to "dev" when empty.
	Mode string
	// CWD is the working directory for the session.
	// Defaults to the server's project directory when empty.
	CWD string
	// WorkItem, when non-empty, prepends "htmlgraph feature start <id>; "
	// to the shell command for attribution.
	WorkItem string
}

// SessionView is the read-only snapshot of a session returned by Sessions().
type SessionView struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Mode      string    `json:"mode"`
	CWD       string    `json:"cwd"`
	WorkItem  string    `json:"work_item"`
	Port      int       `json:"port"`
	StartedAt time.Time `json:"started_at"`
	State     string    `json:"state"`
}

// session tracks a running ttyd process.
type session struct {
	id        string
	cmd       *exec.Cmd
	port      int
	workItem  string
	agent     string
	mode      string
	cwd       string
	startedAt time.Time
	state     string // "pending" | "live" | "exited"
}

// Manager owns the lifecycle of ttyd sidecar processes.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session // keyed by UUID
}

// NewManager creates a ready-to-use Manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*session),
	}
}

// generateSessionID produces a UUID v4 string using crypto/rand.
func generateSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("crypto/rand failed: %w", err)
	}
	// Set version 4 and variant bits per RFC 4122.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
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
// connections or the timeout expires.
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

// buildShellCmd constructs the shell one-liner that ttyd will run inside bash -lc.
//
// Rules:
//   - agent=claude (default) → htmlgraph claude [--dev]
//   - agent=codex → htmlgraph codex [--dev]
//   - agent=gemini → htmlgraph gemini [--dev]
//   - agent=yolo → claude --permission-mode bypassPermissions (no htmlgraph yolo wrapper)
//   - mode=dev (default) → --dev flag appended (not applicable to yolo)
//   - mode=normal → no flag
//   - workItem non-empty → prepends "htmlgraph feature start <id>; " for ALL agents
func buildShellCmd(agent, mode, workItem string) string {
	if agent == "" {
		agent = "claude"
	}
	if mode == "" {
		mode = "dev"
	}

	var base string
	switch agent {
	case "yolo":
		// yolo uses claude directly with bypassPermissions; no htmlgraph wrapper.
		base = "claude --permission-mode bypassPermissions"
	default:
		base = "htmlgraph " + agent
		if mode == "dev" {
			base += " --dev"
		}
	}

	if workItem != "" {
		return "htmlgraph feature start " + workItem + " >/dev/null 2>&1; " + base
	}
	return base
}

// setLive flips the session state to "live" under the mutex.
func (m *Manager) setLive(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.state = "live"
	}
}

// markExited flips the session state to "exited" under the mutex.
func (m *Manager) markExited(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.state = "exited"
	}
}

// Start spawns a ttyd process on a free port running the agent described by req.
// Zero-valued fields in req fall back to MVP defaults (agent=claude, mode=dev,
// cwd=defaultDir). Returns id, port, and pid immediately with state="pending";
// a background goroutine waits for the port to bind and flips state to "live".
func (m *Manager) Start(req StartRequest, defaultDir string) (id string, port int, pid int, err error) {
	// Apply defaults for zero-valued fields.
	if req.Agent == "" {
		req.Agent = "claude"
	}
	if req.Mode == "" {
		req.Mode = "dev"
	}
	workDir := defaultDir
	if req.CWD != "" {
		workDir = req.CWD
	}

	// Ensure ttyd is available before doing anything else.
	if _, err = exec.LookPath("ttyd"); err != nil {
		return "", 0, 0, fmt.Errorf("ttyd not found on PATH — install with: brew install ttyd")
	}

	port, err = freePort()
	if err != nil {
		return "", 0, 0, fmt.Errorf("could not find free port: %w", err)
	}

	id, err = generateSessionID()
	if err != nil {
		return "", 0, 0, fmt.Errorf("could not generate session ID: %w", err)
	}

	shellCmd := buildShellCmd(req.Agent, req.Mode, req.WorkItem)

	cmd := exec.Command(
		"ttyd",
		"-p", strconv.Itoa(port),
		"-W",               // writable (allows input)
		"-i", "127.0.0.1", // bind to localhost only
		"bash", "-lc", shellCmd,
	)
	cmd.Dir = workDir

	if err = cmd.Start(); err != nil {
		return "", 0, 0, fmt.Errorf("failed to start ttyd: %w", err)
	}

	pid = cmd.Process.Pid
	s := &session{
		id:        id,
		cmd:       cmd,
		port:      port,
		workItem:  req.WorkItem,
		agent:     req.Agent,
		mode:      req.Mode,
		cwd:       workDir,
		startedAt: time.Now(),
		state:     "pending",
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	// Background goroutine: wait for ttyd to bind, then flip state to live.
	// On failure, mark exited and kill the process.
	go func() {
		if err := waitForPort(port, 10*time.Second); err != nil {
			_ = cmd.Process.Kill()
			m.markExited(id)
			return
		}
		m.setLive(id)
	}()

	// Reap the process: flip to exited, keep entry for 10s for visibility, then remove.
	go func() {
		_ = cmd.Wait()
		m.markExited(id)
		time.Sleep(10 * time.Second)
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}()

	return id, port, pid, nil
}

// Sessions returns a snapshot of all current sessions for inventory.
func (m *Manager) Sessions() []SessionView {
	m.mu.Lock()
	defer m.mu.Unlock()
	views := make([]SessionView, 0, len(m.sessions))
	for _, s := range m.sessions {
		views = append(views, SessionView{
			ID:        s.id,
			Agent:     s.agent,
			Mode:      s.mode,
			CWD:       s.cwd,
			WorkItem:  s.workItem,
			Port:      s.port,
			StartedAt: s.startedAt,
			State:     s.state,
		})
	}
	return views
}

// StopByID signals the ttyd process identified by UUID with SIGTERM, waiting
// up to 3 seconds before escalating to SIGKILL.
func (m *Manager) StopByID(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no terminal session with id %s", id)
	}
	return m.stopSession(s)
}

// StopByPID signals the ttyd process identified by pid (back-compat).
func (m *Manager) StopByPID(pid int) error {
	m.mu.Lock()
	var found *session
	for _, s := range m.sessions {
		if s.cmd.Process != nil && s.cmd.Process.Pid == pid {
			found = s
			break
		}
	}
	m.mu.Unlock()
	if found == nil {
		return fmt.Errorf("no terminal session with pid %d", pid)
	}
	return m.stopSession(found)
}

// stopSession sends SIGTERM and waits up to 3s, then SIGKILL.
func (m *Manager) stopSession(s *session) error {
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = s.cmd.Process.Kill()
	}

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
	return nil
}

// StopAll terminates all running sessions. Called on graceful server shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		_ = m.StopByID(id)
	}
}
