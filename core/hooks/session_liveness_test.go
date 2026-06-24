package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
)

// writeSessionPIDRaw writes a .session-pid file with explicit content so tests
// can construct legacy (1-line) and starttime-bearing (2-line) variants without
// depending on the production resolver.
func writeSessionPIDRaw(t *testing.T, sessDir, content string) {
	t.Helper()
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, ".session-pid"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .session-pid: %v", err)
	}
}

func TestIsSessionProcessAlive(t *testing.T) {
	self := os.Getpid()
	selfStart, ok := readProcStartTime(self)

	t.Run("self pid with real starttime is alive", func(t *testing.T) {
		sessDir := t.TempDir()
		content := strconv.Itoa(self)
		if ok {
			content += "\n" + strconv.FormatUint(selfStart, 10)
		}
		writeSessionPIDRaw(t, sessDir, content)
		if !IsSessionProcessAlive(sessDir) {
			t.Fatalf("expected self pid %d to be alive", self)
		}
	})

	t.Run("self pid only (legacy single line) is alive", func(t *testing.T) {
		sessDir := t.TempDir()
		writeSessionPIDRaw(t, sessDir, strconv.Itoa(self))
		if !IsSessionProcessAlive(sessDir) {
			t.Fatalf("expected self pid %d (single line) to be alive", self)
		}
	})

	t.Run("certainly-dead pid is dead", func(t *testing.T) {
		sessDir := t.TempDir()
		// 2147483647 = max int32; no such process on any realistic system.
		writeSessionPIDRaw(t, sessDir, "2147483647")
		if IsSessionProcessAlive(sessDir) {
			t.Fatalf("expected pid 2147483647 to be reported dead")
		}
	})

	t.Run("self pid with bogus starttime is dead (PID reuse)", func(t *testing.T) {
		if !ok {
			t.Skip("no /proc starttime available on this platform; PID-reuse path is unreachable")
		}
		sessDir := t.TempDir()
		// starttime "1" can never match the real boot-relative start tick.
		writeSessionPIDRaw(t, sessDir, strconv.Itoa(self)+"\n1")
		if IsSessionProcessAlive(sessDir) {
			t.Fatalf("expected starttime mismatch to be reported dead (PID reuse)")
		}
	})

	t.Run("missing file is alive (legacy/unresolved degrades to LIVE)", func(t *testing.T) {
		sessDir := t.TempDir() // no .session-pid written
		if !IsSessionProcessAlive(sessDir) {
			t.Fatalf("expected missing .session-pid to degrade to LIVE")
		}
	})
}

func TestSessionReapEligible(t *testing.T) {
	self := os.Getpid()
	selfStart, hasStart := readProcStartTime(self)

	aliveDir := t.TempDir()
	content := strconv.Itoa(self)
	if hasStart {
		content += "\n" + strconv.FormatUint(selfStart, 10)
	}
	writeSessionPIDRaw(t, aliveDir, content)

	deadDir := t.TempDir()
	writeSessionPIDRaw(t, deadDir, "2147483647")

	missingDir := t.TempDir() // no .session-pid

	cases := []struct {
		name           string
		heartbeatStale bool
		sessDir        string
		want           bool
	}{
		{"alive + stale -> not eligible", true, aliveDir, false},
		{"alive + fresh -> not eligible", false, aliveDir, false},
		{"dead + stale -> eligible", true, deadDir, true},
		{"dead + fresh -> not eligible", false, deadDir, false},
		{"missing + stale -> not eligible (LIVE)", true, missingDir, false},
		{"missing + fresh -> not eligible (LIVE)", false, missingDir, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SessionReapEligible(tc.heartbeatStale, tc.sessDir); got != tc.want {
				t.Fatalf("SessionReapEligible(%v, %q) = %v, want %v",
					tc.heartbeatStale, tc.sessDir, got, tc.want)
			}
		})
	}
}

func TestSessionResumeRefreshesPid(t *testing.T) {
	clearNestedSessionEnv(t)

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-resume-refresh-pid-001"
	if err := db.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "completed",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    paths.NormalizeProjectDir(projectDir),
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	// Pre-write a STALE pid that is certainly not us.
	writeSessionPIDRaw(t, sessDir, "2147483647\n999999")

	// Write .launch-mode with the live (this-process) pid, mimicking the
	// resuming launcher rewriting the marker.
	marker := launchModeFile{Mode: "claude", PID: os.Getpid(), Timestamp: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal launch-mode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".wipnote", ".launch-mode"), data, 0o644); err != nil {
		t.Fatalf("write .launch-mode: %v", err)
	}

	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	if _, err := SessionResume(event, database, projectDir); err != nil {
		t.Fatalf("SessionResume: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(sessDir, ".session-pid"))
	if err != nil {
		t.Fatalf("read refreshed .session-pid: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		t.Fatalf("parse refreshed pid %q: %v", lines[0], err)
	}
	if pid != os.Getpid() {
		t.Fatalf("expected refreshed pid %d, got %d (stale pid was carried)", os.Getpid(), pid)
	}
	// If we have a real starttime, the refreshed file must carry it on line 2.
	if selfStart, ok := readProcStartTime(os.Getpid()); ok {
		if len(lines) < 2 {
			t.Fatalf("expected 2-line .session-pid with starttime, got %q", string(got))
		}
		recorded, err := strconv.ParseUint(strings.TrimSpace(lines[1]), 10, 64)
		if err != nil {
			t.Fatalf("parse refreshed starttime %q: %v", lines[1], err)
		}
		if recorded != selfStart {
			t.Fatalf("refreshed starttime = %d, want %d", recorded, selfStart)
		}
	}
	// And the refreshed anchor must now read as ALIVE.
	if !IsSessionProcessAlive(sessDir) {
		t.Fatalf("expected refreshed session anchor to be alive")
	}
}
