package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// TestCreateSessionHTML_ProvenanceAttrs verifies that when the Session struct
// carries provenance fields, they are emitted as data-created-by-* attributes
// on the session <article>.
func TestCreateSessionHTML_ProvenanceAttrs(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	s := &models.Session{
		SessionID:           "sess-prov-001",
		AgentAssigned:       "claude-code",
		Status:              "active",
		CreatedAt:           time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		StartCommit:         "abcdef1",
		CreatedByAgent:      "claude-code",
		CreatedByModel:      "claude-opus-4-7",
		CreatedByRole:       "architect-coder",
		CreatedByCLIVersion: "1.2.3",
	}

	CreateSessionHTML(projectDir, s)

	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", "sess-prov-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("session HTML missing: %v", err)
	}
	content := string(data)

	wantAttrs := []string{
		`data-created-by-agent="claude-code"`,
		`data-created-by-model="claude-opus-4-7"`,
		`data-created-by-role="architect-coder"`,
		`data-created-by-cli-version="1.2.3"`,
	}
	for _, attr := range wantAttrs {
		if !strings.Contains(content, attr) {
			t.Errorf("session HTML missing %s\n--- html ---\n%s", attr, content)
		}
	}
}

// TestCreateSessionHTML_NoProvenanceWhenAbsent verifies that legacy sessions
// (no provenance fields populated) round-trip without spurious attributes.
func TestCreateSessionHTML_NoProvenanceWhenAbsent(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &models.Session{
		SessionID:     "sess-prov-002",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		StartCommit:   "abc1234",
	}
	CreateSessionHTML(projectDir, s)

	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "sessions", "sess-prov-002.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, attr := range []string{"data-created-by-agent", "data-created-by-model", "data-created-by-role", "data-created-by-cli-version"} {
		if strings.Contains(string(data), attr) {
			t.Errorf("legacy session should omit %s; html:\n%s", attr, data)
		}
	}
}
