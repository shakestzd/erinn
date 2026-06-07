package db_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func TestDeriveSessionAdherence_HappyPath(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if err := db.InsertSession(database, &models.Session{
		SessionID:     "sess-happy",
		AgentAssigned: "codex",
		CreatedAt:     now,
		Status:        "completed",
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp) VALUES (?, ?, ?, ?, ?)`,
		"abc1234", "sess-happy", "feat-happy", "feat: happy", now.Format(time.RFC3339)); err != nil {
		t.Fatalf("insert git commit: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO feature_files (id, feature_id, file_path, operation, session_id) VALUES (?, ?, ?, 'modify', ?)`,
		"ff1", "feat-happy", "packages/plugin-core/manifest.json", "sess-happy"); err != nil {
		t.Fatalf("insert feature file source: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO feature_files (id, feature_id, file_path, operation, session_id) VALUES (?, ?, ?, 'modify', ?)`,
		"ff2", "feat-happy", "packages/codex-marketplace/.agents/plugins/wipnote/hooks.json", "sess-happy"); err != nil {
		t.Fatalf("insert feature file generated: %v", err)
	}
	record := &db.GateRecord{
		SessionID:         "sess-happy",
		WorkItemID:        "feat-happy",
		Harness:           "codex",
		ProjectType:       "go",
		GateCommand:       "go build ./... && go vet ./... && go test ./...",
		Status:            "pass",
		CheckedAt:         now,
		AllowlistHitsJSON: "[]",
	}
	record.EnsureSignature()
	if err := db.InsertGateRecord(database, record); err != nil {
		t.Fatalf("InsertGateRecord: %v", err)
	}
	writeAdherenceNode(t, filepath.Join(wipnoteDir, "features", "feat-happy.html"), adherenceNodeSpec{
		ID:               "feat-happy",
		Title:            "Happy feature",
		Status:           "done",
		ClaimedBySession: "sess-happy",
		DuplicateTarget:  "feat-existing",
	})

	nodes, err := db.LoadSessionAdherenceNodes(wipnoteDir)
	if err != nil {
		t.Fatalf("LoadSessionAdherenceNodes: %v", err)
	}
	got, err := db.DeriveSessionAdherence(database, "sess-happy", nodes)
	if err != nil {
		t.Fatalf("DeriveSessionAdherence: %v", err)
	}
	if got.Score != 100 {
		t.Fatalf("score = %d, want 100", got.Score)
	}
	assertCheckStatus(t, got, "commits_closed", models.SessionAdherencePass)
	assertCheckStatus(t, got, "gate_ran", models.SessionAdherencePass)
	assertCheckStatus(t, got, "port_regen", models.SessionAdherencePass)
	assertCheckStatus(t, got, "duplicate_links", models.SessionAdherencePass)
	assertCheckStatus(t, got, "override_accumulation", models.SessionAdherencePass)
}

func TestDeriveSessionAdherence_FlagsDriftAndAccumulation(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if err := db.InsertSession(database, &models.Session{
		SessionID:     "sess-warn",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "completed",
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO feature_files (id, feature_id, file_path, operation, session_id) VALUES (?, ?, ?, 'modify', ?)`,
		"ff3", "feat-warn", "packages/plugin-core/manifest.json", "sess-warn"); err != nil {
		t.Fatalf("insert feature file source: %v", err)
	}
	record := &db.GateRecord{
		SessionID:         "sess-warn",
		WorkItemID:        "feat-warn",
		Harness:           "claude_code",
		ProjectType:       "go",
		GateCommand:       "go build ./... && go vet ./... && go test ./...",
		Status:            "pass",
		CheckedAt:         now,
		AllowlistHitsJSON: `[{"id":"listener","command":"go test"}]`,
		AllowlistHitCount: 2,
	}
	record.EnsureSignature()
	if err := db.InsertGateRecord(database, record); err != nil {
		t.Fatalf("InsertGateRecord: %v", err)
	}
	writeAdherenceNode(t, filepath.Join(wipnoteDir, "features", "feat-warn.html"), adherenceNodeSpec{
		ID:               "feat-warn",
		Title:            "Warn feature",
		Status:           "done",
		ClaimedBySession: "sess-warn",
		AcceptedReason:   "one",
	})
	writeAdherenceNode(t, filepath.Join(wipnoteDir, "features", "feat-warn-2.html"), adherenceNodeSpec{
		ID:               "feat-warn-2",
		Title:            "Warn feature 2",
		Status:           "done",
		ClaimedBySession: "sess-warn",
		AcceptedReason:   "two",
	})

	nodes, err := db.LoadSessionAdherenceNodes(wipnoteDir)
	if err != nil {
		t.Fatalf("LoadSessionAdherenceNodes: %v", err)
	}
	got, err := db.DeriveSessionAdherence(database, "sess-warn", nodes)
	if err != nil {
		t.Fatalf("DeriveSessionAdherence: %v", err)
	}
	assertCheckStatus(t, got, "commits_closed", models.SessionAdherenceFail)
	assertCheckStatus(t, got, "port_regen", models.SessionAdherenceFail)
	assertCheckStatus(t, got, "override_accumulation", models.SessionAdherenceWarn)
	if got.Warned != 1 {
		t.Fatalf("warned = %d, want 1", got.Warned)
	}
}

type adherenceNodeSpec struct {
	ID               string
	Title            string
	Status           string
	ClaimedBySession string
	AcceptedReason   string
	DuplicateTarget  string
}

func writeAdherenceNode(t *testing.T, path string, spec adherenceNodeSpec) {
	t.Helper()
	content := "<p>Description</p>"
	if spec.AcceptedReason != "" {
		content += "<p>accepted-advisory (provenance override): " + spec.AcceptedReason + "</p>"
	}
	dupNav := ""
	if spec.DuplicateTarget != "" {
		dupNav = fmt.Sprintf(`<nav data-graph-edges><section data-edge-type="relates_to"><ul><li><a href="../features/%s.html" data-relationship="relates_to" data-tag="needs-triage-dup">needs-triage-dup: %s</a></li></ul></section></nav>`,
			spec.DuplicateTarget, spec.DuplicateTarget)
	}
	html := fmt.Sprintf(`<article id="%s" data-type="feature" data-status="%s" data-claimed-by-session="%s">
<header><h1>%s</h1></header>
%s
<section data-content>%s</section>
</article>`, spec.ID, spec.Status, spec.ClaimedBySession, spec.Title, dupNav, content)
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func assertCheckStatus(t *testing.T, adherence *models.SessionAdherence, key string, want models.SessionAdherenceStatus) {
	t.Helper()
	for _, check := range adherence.Checks {
		if check.Key == key {
			if check.Status != want {
				t.Fatalf("check %s status = %s, want %s (%s)", key, check.Status, want, check.Summary)
			}
			return
		}
	}
	var keys []string
	for _, check := range adherence.Checks {
		keys = append(keys, check.Key)
	}
	t.Fatalf("check %s not found in %s", key, strings.Join(keys, ", "))
}
