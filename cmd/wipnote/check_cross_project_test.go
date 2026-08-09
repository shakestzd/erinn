package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupCrossProjectDB creates a temporary .wipnote directory with an
// initialised SQLite database, returning the wipnote dir path and the open DB.
func setupCrossProjectDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(hgDir, 0o755); err != nil {
		t.Fatalf("create .wipnote dir: %v", err)
	}
	database, err := dbpkg.Open(filepath.Join(hgDir, "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return hgDir, database
}

func insertTestSession(t *testing.T, database *sql.DB, id, projectDir, gitRemoteURL string) {
	t.Helper()
	s := &models.Session{
		SessionID:     id,
		AgentAssigned: "claude",
		CreatedAt:     time.Now().UTC(),
		Status:        "completed",
		ProjectDir:    projectDir,
		GitRemoteURL:  gitRemoteURL,
	}
	if err := dbpkg.InsertSession(database, s); err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

// TestCheckCrossProject_ReportsForForeignProjectDir verifies that a session
// with a different project_dir (and no git remote) is reported as foreign.
func TestCheckCrossProject_ReportsForForeignProjectDir(t *testing.T) {
	hgDir, database := setupCrossProjectDB(t)
	defer database.Close()

	projectRoot := filepath.Dir(hgDir)

	// Own session — matches project root.
	insertTestSession(t, database, "sess-local-001", projectRoot, "")

	// Foreign session — different directory, no remote.
	insertTestSession(t, database, "sess-foreign-001", "/some/other/project", "")

	foreign, total, err := queryForeignSessions(database, projectRoot, "")
	if err != nil {
		t.Fatalf("queryForeignSessions: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 total sessions, got %d", total)
	}
	if len(foreign) != 1 {
		t.Errorf("expected 1 foreign session, got %d", len(foreign))
	}
	if len(foreign) > 0 && foreign[0].sessionID != "sess-foreign-001" {
		t.Errorf("expected sess-foreign-001, got %s", foreign[0].sessionID)
	}
}

// TestCheckCrossProject_ReportsForForeignGitRemote verifies that git remote URL
// takes precedence over project_dir for project identification.
func TestCheckCrossProject_ReportsForForeignGitRemote(t *testing.T) {
	hgDir, database := setupCrossProjectDB(t)
	defer database.Close()

	projectRoot := filepath.Dir(hgDir)
	currentRemote := "https://github.com/owner/this-repo.git"
	foreignRemote := "https://github.com/owner/other-repo.git"

	insertTestSession(t, database, "sess-own-001", projectRoot, currentRemote)
	insertTestSession(t, database, "sess-foreign-002", projectRoot, foreignRemote)

	foreign, total, err := queryForeignSessions(database, projectRoot, currentRemote)
	if err != nil {
		t.Fatalf("queryForeignSessions: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 total sessions, got %d", total)
	}
	if len(foreign) != 1 {
		t.Errorf("expected 1 foreign session, got %d", len(foreign))
	}
	if len(foreign) > 0 && foreign[0].sessionID != "sess-foreign-002" {
		t.Errorf("expected sess-foreign-002, got %s", foreign[0].sessionID)
	}
}

// TestCheckCrossProject_ReportOnly pins the removal of --fix (feat-fc3cc9e0).
//
// The command used to delete the reported rows from `sessions` and
// `agent_events`. Those tables are now a per-process projection hydrated from
// the canonical session ledger, so the DELETE hit a throwaway database and
// every "deleted" row came back on the next openDB. Detection was never the
// broken half and is still asserted by the tests above; what this asserts is
// that no delete path remains to promise otherwise.
func TestCheckCrossProject_ReportOnly(t *testing.T) {
	data, err := os.ReadFile("check_cross_project.go")
	if err != nil {
		t.Fatalf("read check_cross_project.go: %v", err)
	}
	src := string(data)
	for _, forbidden := range []string{
		"deleteForeignSessions",
		"DELETE FROM sessions",
		"DELETE FROM agent_events",
		`"fix"`,
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("check_cross_project.go still references %q — the report must not offer a remedy it cannot deliver", forbidden)
		}
	}
}

// TestCheckCrossProject_EmptyFields treats sessions with no project info as own.
func TestCheckCrossProject_EmptyFields(t *testing.T) {
	hgDir, database := setupCrossProjectDB(t)
	defer database.Close()

	projectRoot := filepath.Dir(hgDir)

	// Session with no project_dir or git_remote_url — cannot be classified as foreign.
	insertTestSession(t, database, "sess-unknown-001", "", "")

	foreign, total, err := queryForeignSessions(database, projectRoot, "")
	if err != nil {
		t.Fatalf("queryForeignSessions: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 total session, got %d", total)
	}
	if len(foreign) != 0 {
		t.Errorf("expected 0 foreign sessions for unknown project, got %d", len(foreign))
	}
}

// TestIsForeignSession tests the classification logic directly.
func TestIsForeignSession(t *testing.T) {
	tests := []struct {
		name          string
		session       crossProjectSession
		projectRoot   string
		currentRemote string
		want          bool
	}{
		{
			name:          "matching remote",
			session:       crossProjectSession{gitRemoteURL: "https://github.com/a/b.git"},
			currentRemote: "https://github.com/a/b.git",
			want:          false,
		},
		{
			name:          "different remote",
			session:       crossProjectSession{gitRemoteURL: "https://github.com/a/c.git"},
			currentRemote: "https://github.com/a/b.git",
			want:          true,
		},
		{
			name:        "matching project_dir, no remote",
			session:     crossProjectSession{projectDir: "/home/user/project"},
			projectRoot: "/home/user/project",
			want:        false,
		},
		{
			name:        "different project_dir, no remote",
			session:     crossProjectSession{projectDir: "/home/user/other"},
			projectRoot: "/home/user/project",
			want:        true,
		},
		{
			name:    "empty session fields",
			session: crossProjectSession{},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isForeignSession(tt.session, tt.projectRoot, tt.currentRemote)
			if got != tt.want {
				t.Errorf("isForeignSession() = %v, want %v", got, tt.want)
			}
		})
	}
}
