package hooks

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func TestExtractClosingIDs(t *testing.T) {
	tests := []struct {
		name    string
		msg     string
		wantIDs []string
	}{
		{
			name:    "closing keyword completes",
			msg:     "feat: add error hints (completes feat-598ceba4)",
			wantIDs: []string{"feat-598ceba4"},
		},
		{
			name:    "closing keyword fixes",
			msg:     "fix: resolve link error (fixes bug-1ce71599)",
			wantIDs: []string{"bug-1ce71599"},
		},
		{
			name:    "closing keyword closes",
			msg:     "closes spk-21cf4782 — audit done",
			wantIDs: []string{"spk-21cf4782"},
		},
		{
			name:    "closing keyword resolves",
			msg:     "resolves feat-05329c66",
			wantIDs: []string{"feat-05329c66"},
		},
		{
			name:    "closing keyword fix (no es)",
			msg:     "fix feat-12345678",
			wantIDs: []string{"feat-12345678"},
		},
		{
			name:    "closing keyword close (no s)",
			msg:     "close bug-abcdef01",
			wantIDs: []string{"bug-abcdef01"},
		},
		{
			name:    "closing keyword complete (no s)",
			msg:     "complete feat-aabbccdd",
			wantIDs: []string{"feat-aabbccdd"},
		},
		{
			name:    "parenthetical reference",
			msg:     "fix(errors): track branch not-found (feat-180ab53f)",
			wantIDs: []string{"feat-180ab53f"},
		},
		{
			name:    "parenthetical with spaces",
			msg:     "fix(errors): improve messages ( feat-180ab53f )",
			wantIDs: []string{"feat-180ab53f"},
		},
		{
			name:    "multiple IDs via keywords",
			msg:     "feat: Wave 1 — completes feat-598ceba4, completes feat-ebfac662",
			wantIDs: []string{"feat-598ceba4", "feat-ebfac662"},
		},
		{
			name:    "keyword and parenthetical deduplicated",
			msg:     "fixes feat-aabbccdd (feat-aabbccdd)",
			wantIDs: []string{"feat-aabbccdd"},
		},
		{
			name:    "mixed types",
			msg:     "closes feat-11111111 and fixes bug-22222222",
			wantIDs: []string{"feat-11111111", "bug-22222222"},
		},
		{
			name:    "case insensitive",
			msg:     "COMPLETES feat-aabbccdd",
			wantIDs: []string{"feat-aabbccdd"},
		},
		{
			name:    "no match — bare ID without keyword",
			msg:     "feat-598ceba4 some commit",
			wantIDs: nil,
		},
		{
			name:    "no match — no IDs",
			msg:     "fix: improve error messages",
			wantIDs: nil,
		},
		{
			name:    "no match — wrong prefix",
			msg:     "completes task-12345678",
			wantIDs: nil,
		},
		{
			name:    "no match — short hash",
			msg:     "completes feat-1234",
			wantIDs: nil,
		},
		{
			name:    "parenthetical with trailing issue number (broadened)",
			msg:     "fix(complete): unblock feature completion in non-Python projects (bug-900f6655, #114)",
			wantIDs: []string{"bug-900f6655"},
		},
		{
			name:    "parenthetical with multiple trailing tokens",
			msg:     "some message (feat-aabbccdd, foo, bar)",
			wantIDs: []string{"feat-aabbccdd"},
		},
		{
			name:    "bare ID in prose without parens or keywords",
			msg:     "checking progress on bug-900f6655 — lots to do",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractClosingIDs(tt.msg)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("extractClosingIDs(%q) = %v, want %v", tt.msg, got, tt.wantIDs)
			}
			for i := range got {
				if got[i] != tt.wantIDs[i] {
					t.Errorf("extractClosingIDs(%q)[%d] = %q, want %q", tt.msg, i, got[i], tt.wantIDs[i])
				}
			}
		})
	}
}

func TestFilePathHash(t *testing.T) {
	h1 := filePathHash("/path/to/file.go")
	h2 := filePathHash("/path/to/file.go")
	h3 := filePathHash("/different/path.go")

	if h1 != h2 {
		t.Errorf("same path should produce same hash: %q != %q", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("different paths should produce different hashes: %q == %q", h1, h3)
	}
	if len(h1) != 8 {
		t.Errorf("hash should be 8 hex chars, got %d: %q", len(h1), h1)
	}
}

func TestLooksLikeGitCommit(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{`git commit -m "fix: stuff"`, true},
		{`git commit --amend`, true},
		{`git-commit`, true},
		{`git log`, false},
		{`echo "not a commit"`, false},
	}
	for _, tt := range tests {
		if got := looksLikeGitCommit(tt.cmd); got != tt.want {
			t.Errorf("looksLikeGitCommit(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

func TestParseGitCommitOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantHash string
		wantMsg  string
	}{
		{
			name:     "standard output",
			output:   "[main abc1234] fix: improve errors\n 3 files changed",
			wantHash: "abc1234",
			wantMsg:  "fix: improve errors",
		},
		{
			name:     "branch with slash",
			output:   "[feat/errors 1234567] feat: add hints (feat-aabbccdd)\n",
			wantHash: "1234567",
			wantMsg:  "feat: add hints (feat-aabbccdd)",
		},
		{
			name:     "no match",
			output:   "nothing to commit, working tree clean",
			wantHash: "",
			wantMsg:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, msg := parseGitCommitOutput(tt.output)
			if hash != tt.wantHash || msg != tt.wantMsg {
				t.Errorf("parseGitCommitOutput(%q) = (%q, %q), want (%q, %q)",
					tt.output, hash, msg, tt.wantHash, tt.wantMsg)
			}
		})
	}
}

// setupGitCommitTestDB creates a temp project dir with .wipnote/ and a real SQLite DB.
// Returns the database and the project dir.
func setupGitCommitTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	projectDir := t.TempDir()
	hgDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(hgDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	database, err := db.Open(filepath.Join(hgDir, "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database, projectDir
}

// TestGitCommitFallbackLinking verifies that commits are linked to work items
// derived from the commit message when the session has no active work item.
func TestGitCommitFallbackLinking(t *testing.T) {
	database, _ := setupGitCommitTestDB(t)

	tests := []struct {
		name          string
		activeFeature string      // ctx.FeatureID
		commitMsg     string
		wantFeatureID string
	}{
		{
			name:          "no active item + parenthetical with issue number",
			activeFeature: "",
			commitMsg:     "fix(complete): unblock feature completion in non-Python projects (bug-900f6655, #114)",
			wantFeatureID: "bug-900f6655",
		},
		{
			name:          "no active item + clean parenthetical",
			activeFeature: "",
			commitMsg:     "feat: update docs (feat-abcd1234)",
			wantFeatureID: "feat-abcd1234",
		},
		{
			name:          "no active item + closing keyword",
			activeFeature: "",
			commitMsg:     "fixes bug-12345678",
			wantFeatureID: "bug-12345678",
		},
		{
			name:          "no active item + message without refs",
			activeFeature: "",
			commitMsg:     "chore: update dependencies",
			wantFeatureID: "",
		},
		{
			name:          "no active item + bare ID in prose",
			activeFeature: "",
			commitMsg:     "checking progress on bug-900f6655 — lots to do",
			wantFeatureID: "",
		},
		{
			name:          "active item overrides message ID",
			activeFeature: "feat-aaaabbbb",
			commitMsg:     "fix(complete): unblock feature completion in non-Python projects (bug-900f6655, #114)",
			wantFeatureID: "feat-aaaabbbb",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a unique hash for each test case (i+1 zero-padded to 7 chars).
			hash := fmt.Sprintf("abc%04d%d", i+1, i+1)
			commit := &models.GitCommit{
				CommitHash: hash,
				SessionID:  "test-session-001",
				FeatureID:  tt.activeFeature,
				Message:    tt.commitMsg,
			}

			// Apply fallback: when activeFeature is empty, extract from message.
			if commit.FeatureID == "" && commit.Message != "" {
				ids := extractClosingIDs(commit.Message)
				if len(ids) > 0 {
					commit.FeatureID = ids[0]
				}
			}

			// Insert the commit into the database.
			if err := db.InsertGitCommit(database, commit); err != nil {
				t.Fatalf("InsertGitCommit: %v", err)
			}

			// Query the commit back and verify the FeatureID.
			var stored sql.NullString
			err := database.QueryRow(
				`SELECT feature_id FROM git_commits WHERE commit_hash = ?`,
				commit.CommitHash,
			).Scan(&stored)
			if err != nil {
				t.Fatalf("query git_commits: %v", err)
			}

			storedFeatureID := stored.String // "" when NULL/invalid
			if storedFeatureID != tt.wantFeatureID {
				t.Errorf("stored FeatureID = %q, want %q", storedFeatureID, tt.wantFeatureID)
			}
		})
	}
}
