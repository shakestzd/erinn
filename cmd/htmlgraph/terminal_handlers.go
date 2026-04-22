package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/shakestzd/htmlgraph/internal/terminal"
)

// terminalManager is the interface used by terminal HTTP handlers.
// Defined as an interface to allow mocking in tests.
type terminalManager interface {
	Start(req terminal.StartRequest, defaultDir string) (id string, port, pid int, err error)
	StopByID(id string) error
	StopByPID(pid int) error
	StopAll()
	Sessions() []terminal.SessionView
}

// terminalMgr is the package-level manager for ttyd sidecar processes.
// It is initialised once and shared across all requests.
var terminalMgr terminalManager = terminal.NewManager()

// terminalStartRequest is the JSON body for POST /api/terminal/start.
// All fields are optional; zero values fall back to MVP defaults.
type terminalStartRequest struct {
	Agent    string `json:"agent"`
	Mode     string `json:"mode"`
	CWD      string `json:"cwd"`
	WorkItem string `json:"work_item"`
	// CwdKind controls worktree resolution for the terminal session.
	// "main" or "" — use projectDir as-is (no worktree).
	// "feature-worktree" — resolve CWD via worktree.EnsureForFeature(work_item, projectDir).
	// "track-worktree"   — resolve CWD via worktree.EnsureForTrack(work_item, projectDir).
	// Any other value returns 400 Bad Request.
	CwdKind string `json:"cwd_kind"`
}

// terminalStartResponse is the JSON body returned on success.
type terminalStartResponse struct {
	ID       string `json:"id"`
	Port     int    `json:"port"`
	Pid      int    `json:"pid"`
	State    string `json:"state"`
	URL      string `json:"url"`
	Agent    string `json:"agent,omitempty"`
	Mode     string `json:"mode,omitempty"`
	WorkItem string `json:"work_item,omitempty"`
}

// terminalStopRequest is the JSON body for POST /api/terminal/stop.
// Accepts id (preferred) or pid (back-compat).
type terminalStopRequest struct {
	ID  string `json:"id"`
	Pid int    `json:"pid"`
}

// resolveManager returns the injected mock manager if provided, else the global.
func resolveManager(mgr []terminalManager) terminalManager {
	if len(mgr) > 0 && mgr[0] != nil {
		return mgr[0]
	}
	return terminalMgr
}

// handleTerminalStart handles POST /api/terminal/start.
// It spawns a ttyd sidecar on a free port and returns {id, port, pid, state:"pending"}.
// The optional mgr parameter allows injection of a mock for testing.
func handleTerminalStart(projectDir string, mgr ...terminalManager) http.HandlerFunc {
	m := resolveManager(mgr)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req terminalStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		// Resolve CWD from cwd_kind + work_item when an explicit CWD is not provided.
		cwd := req.CWD
		if cwd == "" && req.CwdKind != "" && req.WorkItem != "" {
			var resolveErr error
			switch req.CwdKind {
			case "feature-worktree":
				cwd, resolveErr = EnsureForFeature(req.WorkItem, projectDir, io.Discard)
			case "track-worktree":
				cwd, resolveErr = EnsureForTrack(req.WorkItem, projectDir, io.Discard)
			case "main", "":
				// no resolution needed
			default:
				http.Error(w, "invalid cwd_kind: must be 'main', 'feature-worktree', or 'track-worktree'", http.StatusBadRequest)
				return
			}
			if resolveErr != nil {
				http.Error(w, "worktree resolution failed: "+resolveErr.Error(), http.StatusInternalServerError)
				return
			}
		} else if cwd == "" && req.CwdKind != "" && req.WorkItem == "" {
			// cwd_kind provided but no work_item — validate kind is known, then ignore.
			switch req.CwdKind {
			case "feature-worktree", "track-worktree", "main", "":
				// valid kind; fall through (no resolution without work_item)
			default:
				http.Error(w, "invalid cwd_kind: must be 'main', 'feature-worktree', or 'track-worktree'", http.StatusBadRequest)
				return
			}
		}

		startReq := terminal.StartRequest{
			Agent:    req.Agent,
			Mode:     req.Mode,
			CWD:      cwd,
			WorkItem: req.WorkItem,
		}
		id, port, pid, err := m.Start(startReq, projectDir)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		// Echo back effective agent/mode so callers can verify what launched.
		agent := req.Agent
		if agent == "" {
			agent = "claude"
		}
		mode := req.Mode
		if mode == "" {
			mode = "dev"
		}

		respondJSON(w, terminalStartResponse{
			ID:       id,
			Port:     port,
			Pid:      pid,
			State:    "pending",
			URL:      fmt.Sprintf("http://127.0.0.1:%d", port),
			Agent:    agent,
			Mode:     mode,
			WorkItem: req.WorkItem,
		})
	}
}

// handleTerminalSessions handles GET /api/terminal/sessions.
// Returns a JSON array of all current sessions with their state.
func handleTerminalSessions(mgr ...terminalManager) http.HandlerFunc {
	m := resolveManager(mgr)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessions := m.Sessions()
		if sessions == nil {
			sessions = []terminal.SessionView{}
		}
		respondJSON(w, sessions)
	}
}

// handleTerminalStop handles POST /api/terminal/stop.
// Accepts {id:"<uuid>"} (preferred) or {pid:<int>} (back-compat).
func handleTerminalStop(mgr ...terminalManager) http.HandlerFunc {
	m := resolveManager(mgr)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req terminalStopRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		var stopErr error
		if req.ID != "" {
			stopErr = m.StopByID(req.ID)
		} else if req.Pid != 0 {
			stopErr = m.StopByPID(req.Pid)
		} else {
			http.Error(w, "id or pid required", http.StatusBadRequest)
			return
		}

		if stopErr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": stopErr.Error()})
			return
		}

		respondJSON(w, map[string]bool{"ok": true})
	}
}

// handleTerminalStopAll handles POST /api/terminal/stop-all.
// Terminates all live sessions. Accepts an empty body for navigator.sendBeacon compat.
func handleTerminalStopAll(mgr ...terminalManager) http.HandlerFunc {
	m := resolveManager(mgr)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m.StopAll()
		respondJSON(w, map[string]bool{"ok": true})
	}
}
