//go:build sqlitelegacy

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/internal/blame"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// setupContextPackEnv creates a minimal .wipnote directory tree and opens a
// Project. Returns the temp root dir, hgDir, and the open project.
func setupContextPackEnv(t *testing.T) (tmpDir, hgDir string, proj *workitem.Project) {
	t.Helper()
	tmpDir = t.TempDir()
	hgDir = filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })

	p, err := workitem.Open(hgDir, "wipnote-cli")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	t.Cleanup(func() { p.DB.Close() })
	return tmpDir, hgDir, p
}

// seedCommit inserts a fake git commit row into the SQLite DB for a feature.
func seedCommit(t *testing.T, proj *workitem.Project, hash, featID, msg string, ts time.Time) {
	t.Helper()
	c := &models.GitCommit{
		CommitHash: hash,
		SessionID:  "sess-test",
		FeatureID:  featID,
		Message:    msg,
		Timestamp:  ts,
	}
	if err := dbpkg.InsertGitCommit(proj.DB, c); err != nil {
		t.Fatalf("InsertGitCommit %s: %v", hash, err)
	}
}

// seedPlanWithQuestion creates a plan YAML with one unanswered question.
func seedPlanWithQuestion(t *testing.T, hgDir, trackID, planID, question string) {
	t.Helper()
	py := planyaml.NewPlan(planID, "Test Plan", "desc")
	py.Meta.TrackID = trackID
	py.Questions = []planyaml.PlanQuestion{
		{ID: "q1", Text: question, Answer: nil},
	}
	yamlPath := filepath.Join(hgDir, "plans", planID+".yaml")
	if err := planyaml.Save(yamlPath, py); err != nil {
		t.Fatalf("save plan yaml: %v", err)
	}

	// Also create a minimal HTML node so Collection.List sees it.
	// We write a bare-minimum HTML file using the htmlparse format.
	htmlPath := filepath.Join(hgDir, "plans", planID+".html")
	content := `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="id" content="` + planID + `">
<meta name="type" content="plan">
<meta name="status" content="draft">
<meta name="priority" content="medium">
<meta name="track_id" content="` + trackID + `">
<title>Test Plan</title>
</head>
<body></body>
</html>`
	if err := os.WriteFile(htmlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write plan html: %v", err)
	}
}

// assertSections asserts that the output contains all section headers in the
// expected order and each with at least one expected substring.
func assertSections(t *testing.T, out string, checks []struct{ header, contains string }) {
	t.Helper()
	lastIdx := -1
	for _, c := range checks {
		idx := strings.Index(out, c.header)
		if idx == -1 {
			t.Errorf("section header %q not found in output", c.header)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("section %q appears before previous section (order violation)", c.header)
		}
		lastIdx = idx
		if c.contains != "" && !strings.Contains(out, c.contains) {
			t.Errorf("expected %q in output near section %q, not found.\nFull output:\n%s", c.contains, c.header, out)
		}
	}
}

// TestContextPack_HappyPath verifies all 7 sections appear with expected content.
func TestContextPack_HappyPath(t *testing.T) {
	_, hgDir, proj := setupContextPackEnv(t)

	// Create track.
	if err := testCreate("track", "Happy Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	trackNode, _ := htmlparse.ParseFile(trackFiles[0])
	trackID := trackNode.ID

	// Create feature.
	if err := testCreate("feature", "Happy Feature", trackID, "high", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Seed a commit.
	seedCommit(t, proj, "abc12345def67890", featID, "feat: implement context-pack", time.Now().UTC())

	// Seed a plan with an unanswered question.
	seedPlanWithQuestion(t, hgDir, trackID, "pln-aabbccdd", "What's the rollout strategy?")

	out := renderContextPack(
		featNode,
		"feat/my-branch",
		2, 0,
		nil, // trackArea is nil because we skip WalkAreas in unit tests
		[]models.GitCommit{{
			CommitHash: "abc12345def67890",
			FeatureID:  featID,
			Message:    "feat: implement context-pack",
			Timestamp:  time.Now().UTC(),
		}},
		[]unansweredQuestion{
			{Source: "plan pln-aabbccdd", Text: "What's the rollout strategy?"},
		},
	)

	checks := []struct{ header, contains string }{
		{"## 1. Claim Command", "wipnote feature start " + featID},
		{"## 2. Branch-Sync State", "feat/my-branch"},
		{"## 3. Work Item Description", "Happy Feature"},
		{"## 4. Code-Surface Helpers", ""},
		{"## 5. File Paths with Package Qualifiers", ""},
		{"## 6. Recent Same-Track Commits", "abc12345"},
		{"## 7. Open Plan-Slice Questions", "rollout strategy"},
	}
	assertSections(t, out, checks)
}

// TestContextPack_NoTrack verifies that sections 4/5/6/7 emit "(no track attribution)".
func TestContextPack_NoTrack(t *testing.T) {
	_, hgDir, _ := setupContextPackEnv(t)

	if err := runWiCreate("feature", "Untracked Feature", &wiCreateOpts{
		priority:         "medium",
		standaloneReason: "test",
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	out := renderContextPack(featNode, "main", 0, 0, nil, nil, nil)

	for _, section := range []string{
		"## 4. Code-Surface Helpers",
		"## 5. File Paths with Package Qualifiers",
		"## 6. Recent Same-Track Commits",
		"## 7. Open Plan-Slice Questions",
	} {
		idx := strings.Index(out, section)
		if idx == -1 {
			t.Errorf("section %q missing from output", section)
			continue
		}
		// Find the snippet after the section header up to the next section.
		end := idx + 200
		if end > len(out) {
			end = len(out)
		}
		snippet := out[idx:end]
		if !strings.Contains(snippet, "(no track attribution)") {
			t.Errorf("section %q: expected '(no track attribution)', got:\n%s", section, snippet)
		}
	}
}

// TestContextPack_NoPlan verifies that section 7 emits "(none)" when there are no plans.
func TestContextPack_NoPlan(t *testing.T) {
	_, hgDir, _ := setupContextPackEnv(t)

	if err := testCreate("track", "Empty Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	trackNode, _ := htmlparse.ParseFile(trackFiles[0])
	trackID := trackNode.ID

	if err := testCreate("feature", "No Plan Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	// No plans seeded — questions slice is empty.
	out := renderContextPack(featNode, "main", 0, 0, nil, nil, []unansweredQuestion{})

	if !strings.Contains(out, "(none)") {
		t.Errorf("expected '(none)' in section 7, got:\n%s", out)
	}
}

// TestContextPack_NoCommits verifies section 6 emits "(no commits yet)".
func TestContextPack_NoCommits(t *testing.T) {
	_, hgDir, _ := setupContextPackEnv(t)

	if err := testCreate("track", "No Commit Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	trackNode, _ := htmlparse.ParseFile(trackFiles[0])
	trackID := trackNode.ID

	if err := testCreate("feature", "No Commit Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	// commits is empty slice (has track, so we reach the commits section).
	out := renderContextPack(featNode, "main", 0, 0, nil, []models.GitCommit{}, nil)

	if !strings.Contains(out, "(no commits yet)") {
		t.Errorf("expected '(no commits yet)' in section 6, got:\n%s", out)
	}
}

// TestContextPack_PartialIDResolution verifies ResolvePartialID is used correctly.
func TestContextPack_PartialIDResolution(t *testing.T) {
	_, hgDir, _ := setupContextPackEnv(t)

	if err := runWiCreate("feature", "Partial ID Feature", &wiCreateOpts{
		priority:         "medium",
		standaloneReason: "test",
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	fullID := featNode.ID

	// Resolve with first 12 chars of the full ID (e.g. "feat-abcd1234" → "feat-abcd").
	partial := fullID[:12]
	resolved, err := workitem.ResolvePartialID(hgDir, partial)
	if err != nil {
		t.Fatalf("ResolvePartialID(%q): %v", partial, err)
	}
	if resolved != fullID {
		t.Errorf("expected %q, got %q", fullID, resolved)
	}
}

// TestContextPack_CmdRegistered verifies the command appears in the root cobra tree.
func TestContextPack_CmdRegistered(t *testing.T) {
	root := buildRoot()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "context-pack" {
			found = true
			break
		}
	}
	if !found {
		t.Error("context-pack command not registered in buildRoot()")
	}
}

// TestContextPack_GoPackageQualifier validates the package inference logic.
// Non-Go files return "" so callers can render "—" in table columns.
func TestContextPack_GoPackageQualifier(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"internal/foo/bar.go", "package foo"},
		{"cmd/wipnote/main.go", "package wipnote"},
		{"internal/db/schema.go", "package db"},
		{"README.md", ""},
		{"Makefile", ""},
		{".devcontainer/Dockerfile", ""},
		{"plugin/hooks/hooks.json", ""},
		{"scripts/deploy.sh", ""},
		{"top.go", "package main"},
	}
	for _, tc := range cases {
		got := goPackageQualifier(tc.path)
		if got != tc.want {
			t.Errorf("goPackageQualifier(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestContextPack_Section4Placeholder verifies §4 shows "(no track attribution)"
// when the work item has no track (bug-546cda63).
func TestContextPack_Section4Placeholder(t *testing.T) {
	_, hgDir, _ := setupContextPackEnv(t)

	if err := runWiCreate("feature", "Untracked Feature", &wiCreateOpts{
		priority:         "medium",
		standaloneReason: "test",
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	out := renderContextPack(featNode, "main", 0, 0, nil, nil, nil)

	// §4 must contain the placeholder.
	idx4 := strings.Index(out, "## 4. Code-Surface Helpers")
	if idx4 == -1 {
		t.Fatal("§4 header not found")
	}
	snippet4 := out[idx4 : idx4+150]
	if !strings.Contains(snippet4, "(no track attribution)") {
		t.Errorf("§4: expected '(no track attribution)', got:\n%s", snippet4)
	}
}

// TestContextPack_EdgeAttributedCommits verifies that commitsByTrack finds
// features attached via graph_edges (edge-based attribution from migrate-tracks),
// not only via the track_id column (bug-680d0ee4).
func TestContextPack_EdgeAttributedCommits(t *testing.T) {
	_, _, proj := setupContextPackEnv(t)

	trackID := "trk-edgetest"
	featID := "feat-edgetest"

	// Insert a minimal track + feature into SQLite WITHOUT setting track_id column.
	// The feature belongs to the track only via a graph_edges row.
	if _, err := proj.DB.Exec(`INSERT OR IGNORE INTO tracks (id, type, title, status, created_at, updated_at) VALUES (?, 'track', 'Edge Track', 'open', datetime('now'), datetime('now'))`, trackID); err != nil {
		t.Fatalf("insert track: %v", err)
	}
	if _, err := proj.DB.Exec(`INSERT OR IGNORE INTO features (id, type, title, status, priority, created_at, updated_at) VALUES (?, 'feature', 'Edge Feature', 'open', 'medium', datetime('now'), datetime('now'))`, featID); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	// Attach via edge, not column.
	if _, err := proj.DB.Exec(`INSERT OR REPLACE INTO graph_edges (edge_id, from_node_id, from_node_type, to_node_id, to_node_type, relationship_type) VALUES ('e1', ?, 'feature', ?, 'track', 'part_of')`, featID, trackID); err != nil {
		t.Fatalf("insert edge: %v", err)
	}

	// Seed a commit for the edge-attributed feature.
	seedCommit(t, proj, "edge1234abcd5678", featID, "chore: edge-attributed commit", time.Now().UTC())

	commits, err := commitsByTrack(proj, trackID)
	if err != nil {
		t.Fatalf("commitsByTrack: %v", err)
	}
	if len(commits) == 0 {
		t.Error("expected at least one commit from edge-attributed feature, got none")
	}
}

// TestContextPack_FileCap verifies §5 is capped at contextPackFileLimit rows and
// emits a footer when truncated (bug-792a8319 symptom A).
func TestContextPack_FileCap(t *testing.T) {
	_, hgDir, _ := setupContextPackEnv(t)

	if err := testCreate("track", "Big Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	trackNode, _ := htmlparse.ParseFile(trackFiles[0])

	if err := testCreate("feature", "Big Feature", trackNode.ID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featNode.TrackID = trackNode.ID

	// Build a fake TrackArea with 30 files.
	files := make([]blame.FileEntry, 30)
	for i := range files {
		files[i] = blame.FileEntry{
			Path:     fmt.Sprintf("internal/pkg%d/file.go", i),
			Features: 1,
			Touches:  i + 1,
		}
	}
	trackArea := &blame.TrackArea{
		TrackID:    trackNode.ID,
		TrackTitle: "Big Track",
		Files:      files,
	}

	out := renderContextPack(featNode, "main", 0, 0, trackArea, nil, nil)

	// Count table rows in §5.
	lines := strings.Split(out, "\n")
	tableRows := 0
	inSection5 := false
	for _, line := range lines {
		if strings.HasPrefix(line, "## 5.") {
			inSection5 = true
			continue
		}
		if strings.HasPrefix(line, "## ") && inSection5 {
			break
		}
		if inSection5 && strings.HasPrefix(line, "| internal/") {
			tableRows++
		}
	}
	if tableRows > contextPackFileLimit {
		t.Errorf("§5 has %d table rows, want at most %d", tableRows, contextPackFileLimit)
	}
	if !strings.Contains(out, "… and 10 more files") {
		t.Errorf("expected truncation footer '… and 10 more files' in output, got:\n%s", out)
	}
}

// TestContextPack_NonGoPackageColumn verifies non-Go files render "—" in the Package column
// (bug-792a8319 symptom B).
func TestContextPack_NonGoPackageColumn(t *testing.T) {
	_, hgDir, _ := setupContextPackEnv(t)

	if err := testCreate("track", "Mixed Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	trackNode, _ := htmlparse.ParseFile(trackFiles[0])

	if err := testCreate("feature", "Mixed Feature", trackNode.ID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featNode.TrackID = trackNode.ID

	trackArea := &blame.TrackArea{
		TrackID:    trackNode.ID,
		TrackTitle: "Mixed Track",
		Files: []blame.FileEntry{
			{Path: "internal/foo/bar.go", Features: 1, Touches: 3},
			{Path: "README.md", Features: 1, Touches: 1},
			{Path: "scripts/deploy.sh", Features: 1, Touches: 2},
		},
	}

	out := renderContextPack(featNode, "main", 0, 0, trackArea, nil, nil)

	// "README.md" must NOT appear as a package column value — it should be "—".
	if strings.Contains(out, "| README.md | README.md |") {
		t.Error("non-Go file path should not appear as package column value")
	}
	if !strings.Contains(out, "| README.md | — |") {
		t.Errorf("expected '| README.md | — |' in output, got:\n%s", out)
	}
	// Go file should still show package.
	if !strings.Contains(out, "package foo") {
		t.Errorf("expected 'package foo' for Go file in output, got:\n%s", out)
	}
}

// TestContextPack_GitAheadBehind verifies the function runs without panic against
// the real repo (not mocked — integration-lite smoke test).
func TestContextPack_GitAheadBehind(t *testing.T) {
	// Find the repo root by walking up from CWD.
	repoRoot := contextPackFindRepoRoot()
	if repoRoot == "" {
		t.Skip("not inside a git repository")
	}
	ahead, behind, err := gitAheadBehind(repoRoot)
	if err != nil {
		// Non-fatal: CI may not have origin/main; just don't panic.
		t.Logf("gitAheadBehind returned error (non-fatal): %v", err)
		return
	}
	if ahead < 0 || behind < 0 {
		t.Errorf("unexpected negative values: ahead=%d behind=%d", ahead, behind)
	}
}

// contextPackFindRepoRoot walks up from CWD looking for a .git directory.
func contextPackFindRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
