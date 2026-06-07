package main

import (
	"database/sql"
	"os"
	"path/filepath"
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

// TestCheckCrossProject_FixDeletesSessions verifies that --fix deletes foreign
// sessions and their events from the database.
func TestCheckCrossProject_FixDeletesSessions(t *testing.T) {
	hgDir, database := setupCrossProjectDB(t)
	defer database.Close()

	projectRoot := filepath.Dir(hgDir)

	insertTestSession(t, database, "sess-local-001", projectRoot, "")
	insertTestSession(t, database, "sess-foreign-001", "/other/project", "")

	foreign, _, err := queryForeignSessions(database, projectRoot, "")
	if err != nil {
		t.Fatalf("queryForeignSessions: %v", err)
	}

	deleted, err := deleteForeignSessions(database, foreign)
	if err != nil {
		t.Fatalf("deleteForeignSessions: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted session, got %d", deleted)
	}

	// Verify local session still exists.
	_, total, err := queryForeignSessions(database, projectRoot, "")
	if err != nil {
		t.Fatalf("queryForeignSessions after fix: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 remaining session after fix, got %d", total)
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
