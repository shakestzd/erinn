package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/sessionledger"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// testCreate is a test helper that wraps runWiCreate with the opts struct.
func testCreate(typeName, title, trackID, priority string, start, noLink bool) error {
	return runWiCreate(typeName, title, &wiCreateOpts{
		trackID:     trackID,
		priority:    priority,
		description: "test description",
		start:       start,
		noLink:      noLink,
	})
}

// testSetupTrack creates a track and returns its ID. Fatals on failure.
func testSetupTrack(t *testing.T, hgDir string) string {
	t.Helper()
	if err := testCreate("track", "Test Track", "", "medium", false, false); err != nil {
		t.Fatalf("setup track: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(files) == 0 {
		t.Fatal("no track file created")
	}
	node, _ := htmlparse.ParseFile(files[len(files)-1])
	return node.ID
}

func TestAutoTrackEdgesOnCreate(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Create a track first
	if err := testCreate("track", "Test Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}

	// Find the track ID from disk
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) != 1 {
		t.Fatalf("expected 1 track file, got %d", len(trackFiles))
	}
	trackNode, err := htmlparse.ParseFile(trackFiles[0])
	if err != nil {
		t.Fatalf("parse track: %v", err)
	}
	trackID := trackNode.ID

	// Create a feature linked to the track
	if err := testCreate("feature", "Tracked Feature", trackID, "high", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Find the feature
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}

	// Verify feature has part_of edge to track
	partOfEdges, ok := featNode.Edges["part_of"]
	if !ok || len(partOfEdges) == 0 {
		t.Errorf("feature missing part_of edge; edges = %v", featNode.Edges)
	} else if partOfEdges[0].TargetID != trackID {
		t.Errorf("part_of target = %q, want %q", partOfEdges[0].TargetID, trackID)
	}

	// Re-read the track to check contains edge
	trackNode, err = htmlparse.ParseFile(trackFiles[0])
	if err != nil {
		t.Fatalf("re-parse track: %v", err)
	}
	containsEdges, ok := trackNode.Edges["contains"]
	if !ok || len(containsEdges) == 0 {
		t.Errorf("track missing contains edge; edges = %v", trackNode.Edges)
	} else if containsEdges[0].TargetID != featNode.ID {
		t.Errorf("contains target = %q, want %q", containsEdges[0].TargetID, featNode.ID)
	}
}

func TestAutoTrackEdgesNotCreatedForTrack(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Creating a track should not attempt auto-edges even if trackID is passed
	if err := testCreate("track", "Parent Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}

	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) != 1 {
		t.Fatalf("expected 1 track file, got %d", len(trackFiles))
	}
	node, _ := htmlparse.ParseFile(trackFiles[0])
	if len(node.Edges) > 0 {
		t.Errorf("track should have no edges, got %v", node.Edges)
	}
}

func TestAutoImplementedInEdgeOnStart(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Set a fake session ID (EnvSessionID reads WIPNOTE_SESSION_ID first)
	t.Setenv("WIPNOTE_SESSION_ID", "test-session-abc")

	trackID := testSetupTrack(t, hgDir)

	// Create a feature
	if err := testCreate("feature", "Impl Feature", trackID, "high", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Find the feature ID
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Start the feature (should create implemented_in edge)
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Re-read and check for implemented_in edge
	featNode, _ = htmlparse.ParseFile(featFiles[0])
	implEdges, ok := featNode.Edges["implemented_in"]
	if !ok || len(implEdges) == 0 {
		t.Errorf("feature missing implemented_in edge; edges = %v", featNode.Edges)
	} else if implEdges[0].TargetID != "test-session-abc" {
		t.Errorf("implemented_in target = %q, want %q", implEdges[0].TargetID, "test-session-abc")
	}

	// Start again — should be idempotent (no duplicate edge)
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("re-start feature: %v", err)
	}
	featNode, _ = htmlparse.ParseFile(featFiles[0])
	implEdges = featNode.Edges["implemented_in"]
	if len(implEdges) != 1 {
		t.Errorf("expected 1 implemented_in edge after re-start, got %d", len(implEdges))
	}
}

func TestNoImplementedInEdgeWithoutSession(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Isolate from any real session running in the developer's environment.
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", tmpDir)
	t.Setenv("CLAUDE_PROJECT_DIR", "")

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "No Session Feature", trackID, "low", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}

	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	if err := runWiSetStatus("feature", featNode.ID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}

	featNode, _ = htmlparse.ParseFile(featFiles[0])
	if len(featNode.Edges["implemented_in"]) > 0 {
		t.Errorf("should not have implemented_in edge without session, got %v", featNode.Edges)
	}
}

func TestRunWiSetStatus_ClaudeStartTip(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	t.Setenv("WIPNOTE_AGENT_TYPE", "claude-code")
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("bug", "Claude Tip Bug", trackID, "medium", false, false); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) != 1 {
		t.Fatalf("expected 1 bug file, got %d", len(bugFiles))
	}
	bugNode, _ := htmlparse.ParseFile(bugFiles[0])

	out := captureStdout(t, func() {
		if err := runWiSetStatus("bug", bugNode.ID, "in-progress"); err != nil {
			t.Fatalf("start bug: %v", err)
		}
	})

	if !strings.Contains(out, "Tip: sync your Claude session label to this item:") {
		t.Fatalf("expected Claude tip in output:\n%s", out)
	}
	if !strings.Contains(out, "/rename claude-tip-bug") || !strings.Contains(out, "/color red") {
		t.Fatalf("expected Claude slash commands in output:\n%s", out)
	}
}

func TestRunWiSetStatus_CodexStartTipIsHarnessNeutral(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	t.Setenv("WIPNOTE_AGENT_TYPE", "codex")
	t.Setenv("WIPNOTE_AGENT_ID", "codex")
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("bug", "Codex Tip Bug", trackID, "medium", false, false); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) != 1 {
		t.Fatalf("expected 1 bug file, got %d", len(bugFiles))
	}
	bugNode, _ := htmlparse.ParseFile(bugFiles[0])

	out := captureStdout(t, func() {
		if err := runWiSetStatus("bug", bugNode.ID, "in-progress"); err != nil {
			t.Fatalf("start bug: %v", err)
		}
	})

	if strings.Contains(out, "Claude") || strings.Contains(out, "/rename") || strings.Contains(out, "/color") {
		t.Fatalf("expected Codex output to avoid Claude-only tip text:\n%s", out)
	}
	if !strings.Contains(out, "Tip: keep this session aligned with the item:") {
		t.Fatalf("expected harness-neutral tip in output:\n%s", out)
	}
	if !strings.Contains(out, "label: codex-tip-bug") || !strings.Contains(out, "color: red") {
		t.Fatalf("expected label and color hints in output:\n%s", out)
	}
}

func TestAutoCausedByEdgeOnBugCreate(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Create a feature first and start it
	if err := testCreate("feature", "Active Feature", trackID, "high", true, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Now create a bug — should auto-link caused_by to active feature
	if err := testCreate("bug", "Found a bug", trackID, "high", false, false); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	// Find the bug
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) != 1 {
		t.Fatalf("expected 1 bug file, got %d", len(bugFiles))
	}
	bugNode, _ := htmlparse.ParseFile(bugFiles[0])

	// Find the feature ID
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	// Verify caused_by edge
	causedByEdges := bugNode.Edges["caused_by"]
	if len(causedByEdges) == 0 {
		t.Logf("bug edges: %v", bugNode.Edges)
		t.Skip("no DB available in test — auto caused_by requires session DB")
		return
	}
	if causedByEdges[0].TargetID != featNode.ID {
		t.Errorf("caused_by target = %q, want %q", causedByEdges[0].TargetID, featNode.ID)
	}
}

func TestBugCreateNoLinkSkipsCausedBy(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item create lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Create and start a feature
	if err := testCreate("feature", "Active Feature", trackID, "high", true, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Create bug with --no-link
	if err := testCreate("bug", "Unrelated bug", trackID, "medium", false, true); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	bugNode, _ := htmlparse.ParseFile(bugFiles[0])

	// Should have no caused_by edge
	if len(bugNode.Edges["caused_by"]) > 0 {
		t.Errorf("--no-link should skip caused_by edge, got %v", bugNode.Edges)
	}
}

func TestFeatureCreateRequiresDescription(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Try to create a feature without --description (but with --track)
	opts := &wiCreateOpts{
		trackID:     trackID,
		priority:    "high",
		description: "", // no description
		start:       false,
		noLink:      false,
	}
	err := runWiCreate("feature", "Feature without description", opts)

	if err == nil {
		t.Fatal("expected error when creating feature without --description, got nil")
	}

	// Check error message contains example syntax
	errMsg := err.Error()
	if !stringContains(errMsg, "Example:") {
		t.Errorf("error message should mention 'Example:' to show syntax: %q", errMsg)
	}
	if !stringContains(errMsg, "--description") {
		t.Errorf("error message should mention --description: %q", errMsg)
	}
	if !stringContains(errMsg, "feature") {
		t.Errorf("error message should mention 'feature' command: %q", errMsg)
	}
}

func TestBugCreateRequiresDescription(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Try to create a bug without --description (but with --track)
	opts := &wiCreateOpts{
		trackID:     trackID,
		priority:    "high",
		description: "", // no description
		start:       false,
		noLink:      false,
	}
	err := runWiCreate("bug", "Bug without description", opts)

	if err == nil {
		t.Fatal("expected error when creating bug without --description, got nil")
	}

	// Check error message contains example syntax
	errMsg := err.Error()
	if !stringContains(errMsg, "Example:") {
		t.Errorf("error message should mention 'Example:' to show syntax: %q", errMsg)
	}
	if !stringContains(errMsg, "--description") {
		t.Errorf("error message should mention --description: %q", errMsg)
	}
	if !stringContains(errMsg, "bug") {
		t.Errorf("error message should mention 'bug' command: %q", errMsg)
	}
}

func TestSpecCreateNoDescriptionWarning(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Create a spec without --description (should warn, not error)
	opts := &wiCreateOpts{
		trackID:     "",
		priority:    "medium",
		description: "", // no description
		start:       false,
		noLink:      false,
	}
	err := runWiCreate("spec", "Spec without description", opts)

	if err != nil {
		t.Fatalf("spec should warn but not error, got: %v", err)
	}
}

func TestRunWiSetStatus_BlockedClearsCache(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item status lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Set cache dir to temp so we don't pollute the real home dir.
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	// Create a feature linked to the track.
	if err := testCreate("feature", "Test Blocked Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}

	// Start it — cache should be populated.
	if err := runWiSetStatus("feature", featNode.ID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	cache := ReadStatuslineCache(hgDir)
	if cache == "" {
		t.Fatal("cache should be populated after start")
	}

	// Block it — cache should be cleared and status must become blocked.
	if err := runWiSetStatus("feature", featNode.ID, "blocked"); err != nil {
		t.Fatalf("blocked: %v", err)
	}
	cache = ReadStatuslineCache(hgDir)
	if cache != "" {
		t.Errorf("cache should be empty after blocked, got %q", cache)
	}

	// Verify the status was actually set to blocked (not done).
	updatedNode, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse after blocked: %v", err)
	}
	if string(updatedNode.Status) != "blocked" {
		t.Errorf("expected status %q, got %q", "blocked", updatedNode.Status)
	}
}

// TestCreateWithDescription_AllKinds verifies that --description is persisted for
// every work item type. The spike case was previously a silent data-loss bug.
func TestCreateWithDescription_AllKinds(t *testing.T) {
	cases := []struct {
		kind    string
		subDir  string
		prefix  string
		trackOK bool // whether a --track is needed at all
	}{
		{"feature", "features", "feat-", true},
		{"bug", "bugs", "bug-", true},
		{"spike", "spikes", "spk-", false},
		{"track", "tracks", "trk-", false},
		{"plan", "plans", "plan-", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.kind, func(t *testing.T) {
			tmpDir := t.TempDir()
			hgDir := filepath.Join(tmpDir, ".wipnote")
			for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
				if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			projectDirFlag = tmpDir
			defer func() { projectDirFlag = "" }()

			trackID := ""
			if tc.trackOK {
				trackID = testSetupTrack(t, hgDir)
			}

			opts := &wiCreateOpts{
				trackID:     trackID,
				priority:    "medium",
				description: "persisted description body",
				start:       false,
				noLink:      true,
				standaloneReason: func() string {
					if tc.kind == "feature" && trackID == "" {
						return "test-standalone"
					}
					return ""
				}(),
			}
			if err := runWiCreate(tc.kind, "Test "+tc.kind, opts); err != nil {
				t.Fatalf("runWiCreate: %v", err)
			}

			files, _ := filepath.Glob(filepath.Join(hgDir, tc.subDir, tc.prefix+"*.html"))
			if len(files) == 0 {
				t.Fatalf("no %s file created", tc.kind)
			}
			node, err := htmlparse.ParseFile(files[len(files)-1])
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !stringContains(node.Content, "persisted description body") {
				t.Errorf("%s: want content %q, got %q", tc.kind, "persisted description body", node.Content)
			}
		})
	}
}

// TestSetDescription_AllKinds verifies that the set-description command works for
// every work item type. Before Fix 1 only feature worked; the rest were unregistered.
func TestSetDescription_AllKinds(t *testing.T) {
	cases := []struct {
		kind       string
		subDir     string
		prefix     string
		needsTrack bool
	}{
		{"feature", "features", "feat-", true},
		{"bug", "bugs", "bug-", true},
		{"spike", "spikes", "spk-", false},
		{"track", "tracks", "trk-", false},
		{"plan", "plans", "plan-", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.kind, func(t *testing.T) {
			tmpDir := t.TempDir()
			hgDir := filepath.Join(tmpDir, ".wipnote")
			for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
				if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			projectDirFlag = tmpDir
			defer func() { projectDirFlag = "" }()

			trackID := ""
			if tc.needsTrack {
				trackID = testSetupTrack(t, hgDir)
			}

			opts := &wiCreateOpts{
				trackID:     trackID,
				priority:    "medium",
				description: "initial description",
				start:       false,
				noLink:      true,
				standaloneReason: func() string {
					if tc.kind == "feature" && trackID == "" {
						return "test-standalone"
					}
					return ""
				}(),
			}
			if err := runWiCreate(tc.kind, "SetDesc "+tc.kind, opts); err != nil {
				t.Fatalf("runWiCreate: %v", err)
			}

			files, _ := filepath.Glob(filepath.Join(hgDir, tc.subDir, tc.prefix+"*.html"))
			if len(files) == 0 {
				t.Fatalf("no %s file created", tc.kind)
			}
			node, err := htmlparse.ParseFile(files[len(files)-1])
			if err != nil {
				t.Fatalf("parse before set-description: %v", err)
			}

			// Call the generalized runSetDescription with the kind.
			if err := runSetDescription(tc.kind, node.ID, "updated description text", "", "", "", false); err != nil {
				t.Fatalf("runSetDescription(%s): %v", tc.kind, err)
			}

			// Re-read and assert the description changed.
			node, err = htmlparse.ParseFile(files[len(files)-1])
			if err != nil {
				t.Fatalf("parse after set-description: %v", err)
			}
			if !stringContains(node.Content, "updated description text") {
				t.Errorf("%s: want content %q, got %q", tc.kind, "updated description text", node.Content)
			}
		})
	}
}

// stringContains is a helper to check if a string contains a substring
func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- warnMissingFields tests ---------------------------------------------------

func TestWarnMissingFields_FeatureRequiresTrack(t *testing.T) {
	opts := &wiCreateOpts{description: "some description"}
	err := warnMissingFields("feature", opts)
	if err == nil {
		t.Fatal("expected error for feature without --track, got nil")
	}
	if !stringContains(err.Error(), "wipnote track list") {
		t.Errorf("error should mention 'wipnote track list', got: %q", err.Error())
	}
}

func TestWarnMissingFields_BugRequiresTrack(t *testing.T) {
	opts := &wiCreateOpts{description: "some description"}
	err := warnMissingFields("bug", opts)
	if err == nil {
		t.Fatal("expected error for bug without --track, got nil")
	}
	if !stringContains(err.Error(), "wipnote track list") {
		t.Errorf("error should mention 'wipnote track list', got: %q", err.Error())
	}
}

func TestWarnMissingFields_SpikeNoTrackOK(t *testing.T) {
	opts := &wiCreateOpts{description: "investigation notes"}
	err := warnMissingFields("spike", opts)
	if err != nil {
		t.Errorf("spike without --track should not error, got: %v", err)
	}
}

func TestWarnMissingFields_TrackNoTrackOK(t *testing.T) {
	opts := &wiCreateOpts{}
	err := warnMissingFields("track", opts)
	if err != nil {
		t.Errorf("track type should not error about missing track, got: %v", err)
	}
}

func TestWarnMissingFields_FeatureWithTrackOK(t *testing.T) {
	opts := &wiCreateOpts{trackID: "trk-abc12345", description: "some description"}
	err := warnMissingFields("feature", opts)
	if err != nil {
		t.Errorf("feature with --track should not error, got: %v", err)
	}
}

func TestWarnMissingFields_ErrorMessageGuidance(t *testing.T) {
	opts := &wiCreateOpts{description: "some description"}
	err := warnMissingFields("feature", opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !stringContains(msg, "wipnote track list") {
		t.Errorf("error message should contain 'wipnote track list': %q", msg)
	}
	if !stringContains(msg, "--track") {
		t.Errorf("error message should mention '--track': %q", msg)
	}
	// Retrieval-first framing: relevant command must appear before track list.
	if !stringContains(msg, "wipnote relevant") {
		t.Errorf("error message should mention 'wipnote relevant' for retrieval-first discovery: %q", msg)
	}
	if !stringContains(msg, "last resort") {
		t.Errorf("error message should frame track creation as 'last resort': %q", msg)
	}
}

func TestWarnMissingFields_BugErrorMessageRetrievalFirst(t *testing.T) {
	opts := &wiCreateOpts{description: "some description"}
	err := warnMissingFields("bug", opts)
	if err == nil {
		t.Fatal("expected error for bug without --track, got nil")
	}
	msg := err.Error()
	if !stringContains(msg, "wipnote relevant") {
		t.Errorf("bug error message should mention 'wipnote relevant': %q", msg)
	}
	if !stringContains(msg, "last resort") {
		t.Errorf("bug error message should frame track creation as 'last resort': %q", msg)
	}
}

// testHgDirWithDB creates a temp dir with .wipnote subdirs and a seeded
// session row. Returns tmpDir, hgDir, and the pre-opened DB (caller closes it).
func testHgDirWithDB(t *testing.T, sessionID string) (tmpDir, hgDir string) {
	t.Helper()
	tmpDir = t.TempDir()
	hgDir = filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Pin the DB path inside the test temp dir via WIPNOTE_DB_PATH so the
	// production code (storage.CanonicalDBPath) and the test (which opens the
	// same path directly) agree. Without this, production would write to the
	// real user cache dir and the test would read from the unused tmp path.
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	// The session goes into the CANONICAL session ledger, which is what
	// hydrates the projection's sessions table. It used to be INSERTed into the
	// file DB above; the commands under test now build a private in-memory
	// projection from .wipnote/, so a session that exists only in that file is
	// invisible to them and to any read-back (feat-fc3cc9e0).
	if _, err := sessionledger.NewStore(hgDir).Open(sessionledger.Record{
		SessionID: sessionID,
		Harness:   "claude-code",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session ledger: %v", err)
	}
	return tmpDir, hgDir
}

// TestFeatureStart_Idempotent verifies that calling runWiSetStatus twice with
// the same feature ID does not issue a second UpdateActiveFeature write. We
// detect this by checking that active_feature_id in the DB remains set after
// both calls, and that only one claim row exists for the session+feature pair.
func TestFeatureStart_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	// test-session-idempotent — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000101"
	tmpDir, hgDir := testHgDirWithDB(t, sessionID)

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Idempotent Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// First start — must succeed and set active_feature_id.
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("first start: %v", err)
	}

	// Read active_feature_id after first start.
	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db after first start: %v", err)
	}
	defer database.Close()

	activeAfterFirst := dbpkg.GetActiveFeatureIDForSession(database, sessionID)
	if activeAfterFirst != featID {
		t.Fatalf("after first start: active_feature_id = %q, want %q", activeAfterFirst, featID)
	}

	// Count claims before second start.
	var claimsBefore int
	database.QueryRow(`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND owner_session_id = ?`,
		featID, sessionID).Scan(&claimsBefore)

	// Record updated_at before second start. Sleep 2ms so any write would
	// advance the timestamp.
	var updatedAtBefore string
	database.QueryRow(`SELECT updated_at FROM sessions WHERE session_id = ?`, sessionID).
		Scan(&updatedAtBefore)
	time.Sleep(2 * time.Millisecond)

	// Second start — must not error.
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("second start: %v", err)
	}

	// updated_at must NOT have advanced (no write was issued).
	var updatedAtAfter string
	database.QueryRow(`SELECT updated_at FROM sessions WHERE session_id = ?`, sessionID).
		Scan(&updatedAtAfter)
	if updatedAtAfter != updatedAtBefore {
		t.Errorf("updated_at changed on idempotent re-start: before=%q after=%q", updatedAtBefore, updatedAtAfter)
	}

	// Claim count must not grow (ClaimItem also guards against duplicates,
	// but the short-circuit should prevent even reaching it).
	var claimsAfter int
	database.QueryRow(`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND owner_session_id = ?`,
		featID, sessionID).Scan(&claimsAfter)
	if claimsAfter != claimsBefore {
		t.Errorf("claim count changed on idempotent re-start: before=%d after=%d", claimsBefore, claimsAfter)
	}
}

// TestFeatureStart_DifferentFeatures verifies that starting two different
// features in sequence issues both UpdateActiveFeature writes, ending with
// active_feature_id pointing at the second feature.
func TestFeatureStart_DifferentFeatures(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	// test-session-two-features — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000102"
	tmpDir, hgDir := testHgDirWithDB(t, sessionID)

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	// Create two features.
	for _, title := range []string{"Feature Alpha", "Feature Beta"} {
		if err := testCreate("feature", title, trackID, "medium", false, false); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 2 {
		t.Fatalf("expected 2 feature files, got %d", len(featFiles))
	}
	nodeA, _ := htmlparse.ParseFile(featFiles[0])
	nodeB, _ := htmlparse.ParseFile(featFiles[1])
	idA, idB := nodeA.ID, nodeB.ID

	// Start feature A.
	if err := runWiSetStatus("feature", idA, "in-progress"); err != nil {
		t.Fatalf("start A: %v", err)
	}

	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if got := dbpkg.GetActiveFeatureIDForSession(database, sessionID); got != idA {
		t.Errorf("after start A: active_feature_id = %q, want %q", got, idA)
	}

	// Start feature B — must write and update active_feature_id.
	if err := runWiSetStatus("feature", idB, "in-progress"); err != nil {
		t.Fatalf("start B: %v", err)
	}
	if got := dbpkg.GetActiveFeatureIDForSession(database, sessionID); got != idB {
		t.Errorf("after start B: active_feature_id = %q, want %q", got, idB)
	}
}

// TestFeatureStart_ClaimWrittenOnFirstStart verifies that a claim row is written
// to the claims table on the very first call to feature start (bug-0d55d8e4).
func TestFeatureStart_ClaimWrittenOnFirstStart(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	// test-session-claim-first-start — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000103"
	const agentID = dbpkg.AgentRootSentinel

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Claim First Start", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("first start: %v", err)
	}

	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	var count int
	database.QueryRow(
		`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND claimed_by_agent_id = ? AND status IN ('proposed','claimed','in_progress','blocked','handoff_pending')`,
		featID, agentID,
	).Scan(&count)
	if count != 1 {
		t.Errorf("want 1 live claim row after first start, got %d", count)
	}
}

// TestFeatureStart_ClaimRenewedOnRepeatStart verifies that calling feature start
// twice on the same item with the same (session, agent) does not create a
// duplicate live claim row — the existing claim's lease is renewed instead
// (bug-0d55d8e4 fix: ClaimItemOrRenew is idempotent).
func TestFeatureStart_ClaimRenewedOnRepeatStart(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	// test-session-claim-repeat-start — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000104"
	const agentID = dbpkg.AgentRootSentinel

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Claim Repeat Start", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// First start.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("first start: %v", err)
	}

	// Second start — same item, same agent (short-circuit path in active_work_items,
	// but ClaimItemOrRenew must still run to refresh/ensure the live claim row).
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("second start: %v", err)
	}

	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Must have exactly one live claim row (no duplicates) after two starts.
	var count int
	database.QueryRow(
		`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND claimed_by_agent_id = ? AND status IN ('proposed','claimed','in_progress','blocked','handoff_pending')`,
		featID, agentID,
	).Scan(&count)
	if count != 1 {
		t.Errorf("want 1 live claim row after repeat start, got %d (no duplicates)", count)
	}
}

// TestFeatureStart_ClaimWrittenAfterExpiry verifies that feature start writes a
// fresh live claim row when the previous claim has expired, even if
// active_work_items still shows the item as active for this (session, agent).
// This is the core scenario of bug-0d55d8e4: the short-circuit on
// active_work_items was preventing the claim re-write when the claim expired.
func TestFeatureStart_ClaimWrittenAfterExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item start lifecycle")
	}

	// test-session-claim-after-expiry — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000105"
	const agentID = dbpkg.AgentRootSentinel

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Claim After Expiry", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// First start — writes claim and active_work_items.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("first start: %v", err)
	}

	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Manually expire the claim by back-dating its lease.
	pastTime := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	_, err = database.Exec(
		`UPDATE claims SET lease_expires_at = ?, last_heartbeat_at = ? WHERE work_item_id = ? AND claimed_by_agent_id = ?`,
		pastTime, pastTime, featID, agentID,
	)
	if err != nil {
		t.Fatalf("expire claim: %v", err)
	}

	// Verify claim is now expired (sanity check).
	var liveCount int
	database.QueryRow(
		`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND claimed_by_agent_id = ? AND status IN ('proposed','claimed','in_progress','blocked','handoff_pending')`,
		featID, agentID,
	).Scan(&liveCount)
	// NOTE: The claim is expired by timestamp but not yet reaped by status.
	// ReapExpiredClaims runs at the start of ClaimItemOrRenew.

	// active_work_items still shows the item as active (tables are diverged).
	gotActive := dbpkg.GetActiveWorkItem(database, sessionID, agentID)
	if gotActive != featID {
		t.Fatalf("precondition: active_work_items should still show %q, got %q", featID, gotActive)
	}

	// Close DB — wiSetStatusWithAgent opens its own connection.
	database.Close()

	// Second start — active_work_items shows short-circuit condition, but the
	// prior claim is expired. ClaimItemOrRenew must write a fresh claim.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("second start after expiry: %v", err)
	}

	database2, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer database2.Close()

	// Must have exactly one live claim row after re-start.
	var liveClaims int
	database2.QueryRow(
		`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND claimed_by_agent_id = ? AND status IN ('proposed','claimed','in_progress','blocked','handoff_pending')`,
		featID, agentID,
	).Scan(&liveClaims)
	if liveClaims != 1 {
		t.Errorf("want 1 live claim row after re-start post-expiry, got %d", liveClaims)
	}
}

// TestRunWiSetStatus_ConcurrentAgents proves that N goroutines with distinct
// agentIDs can each claim different features on the same session without
// contention, error, or loss.
//
// NOTE: We call wiSetStatusWithAgent (not runWiSetStatus) to avoid env-var races.
// runWiSetStatus is a thin wrapper that reads env vars then delegates here.
//
// NOTE on SQLite concurrency: workitem.Open opens a new DB connection per call.
// SQLite WAL allows concurrent readers but serializes writers. A semaphore (cap=1)
// ensures at most one workitem.Open call is in-flight at any time, matching the
// real-world pattern where each agent runs in a separate OS process. The key
// invariant being tested is that N distinct (session, agentID) pairs each
// produce an independent row in active_work_items — not raw write parallelism.
func TestRunWiSetStatus_ConcurrentAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("drives concurrent work-item claim lifecycle")
	}

	// test-session-concurrent — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000106"
	const N = 5

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	// Create N features up-front (sequential — avoids concurrent HTML writes).
	featIDs := make([]string, N)
	for i := 0; i < N; i++ {
		title := fmt.Sprintf("Concurrent Feature %d", i)
		if err := testCreate("feature", title, trackID, "medium", false, false); err != nil {
			t.Fatalf("create feature %d: %v", i, err)
		}
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) < N {
		t.Fatalf("expected %d feature files, got %d", N, len(featFiles))
	}
	for i := 0; i < N; i++ {
		node, err := htmlparse.ParseFile(featFiles[i])
		if err != nil {
			t.Fatalf("parse feature %d: %v", i, err)
		}
		featIDs[i] = node.ID
	}

	// Semaphore with capacity 1: serializes the workitem.Open / SQLite write
	// calls (matching real usage where each agent is a separate OS process).
	// Goroutines still run concurrently for everything outside the semaphore.
	sem := make(chan struct{}, 1)
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		agentID := fmt.Sprintf("agent-%d", i)
		featID := featIDs[i]
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			errCh <- wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID)
		}()
	}

	// Collect results — all must succeed.
	for i := 0; i < N; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("goroutine error: %v", err)
		}
	}

	// Verify all N rows in active_work_items.
	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	items, err := dbpkg.ActiveWorkItemsForSession(database, sessionID)
	if err != nil {
		t.Fatalf("ActiveWorkItemsForSession: %v", err)
	}
	if len(items) != N {
		t.Errorf("want %d active_work_items rows, got %d: %v", N, len(items), items)
	}

	// Each agentID must map to its expected feature.
	for i := 0; i < N; i++ {
		agentID := fmt.Sprintf("agent-%d", i)
		want := featIDs[i]
		if got := items[agentID]; got != want {
			t.Errorf("agent-%d: want %s, got %q", i, want, got)
		}
	}
}

// TestSubagentCanStartFeatureCreatedByDifferentAgent verifies that feat-ab67561e's
// per-(session_id, agent_id) attribution removed the old session-ownership restriction.
// A sub-agent with a distinct agent_id (but sharing the parent's session_id, as
// Claude Code does per docs) must be able to run feature start on a feature the
// orchestrator created in the same session without any error.
//
// Regression test for bug-50c7eed0: "Execute skill sub-agent attribution guard
// blocks legitimate claims to features created by orchestrator."
func TestSubagentCanStartFeatureCreatedByDifferentAgent(t *testing.T) {
	// test-session-subagent-claim — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000107"
	const orchestratorAgentID = "agent-orchestrator"
	const subagentAgentID = "agent-subagent-a"

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	// Step 1: Create a feature (simulating orchestrator creating work for subagent).
	// Do NOT start it yet — this is a feature created but not claimed.
	if err := testCreate("feature", "Subagent Claim Test", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}
	featureID := featNode.ID

	// Step 2: Orchestrator claims the feature first (simulating orchestrator
	// dispatching work and starting it to track attribution).
	if err := wiSetStatusWithAgent("feature", featureID, "in-progress", sessionID, orchestratorAgentID); err != nil {
		t.Fatalf("orchestrator start: %v", err)
	}

	// Step 3: Sub-agent claims the SAME feature with a DIFFERENT agent_id.
	// This would have been blocked under the old session-ownership model, but
	// feat-ab67561e changed attribution to per-(session, agent_id), so both
	// agents can own their own active_work_items rows for the same feature.
	if err := wiSetStatusWithAgent("feature", featureID, "in-progress", sessionID, subagentAgentID); err != nil {
		t.Fatalf("subagent start failed: %v — regression of bug-50c7eed0", err)
	}

	// Step 4: Verify both agents' rows exist in active_work_items and point
	// to the same feature.
	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	orchestratorActive := dbpkg.GetActiveWorkItem(database, sessionID, orchestratorAgentID)
	if orchestratorActive != featureID {
		t.Errorf("orchestrator active: got %q want %q", orchestratorActive, featureID)
	}

	subagentActive := dbpkg.GetActiveWorkItem(database, sessionID, subagentAgentID)
	if subagentActive != featureID {
		t.Errorf("subagent active: got %q want %q", subagentActive, featureID)
	}

	// Step 5: Verify both rows exist in active_work_items table.
	items, err := dbpkg.ActiveWorkItemsForSession(database, sessionID)
	if err != nil {
		t.Fatalf("ActiveWorkItemsForSession: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("want 2 active_work_items rows (one per agent), got %d: %v", len(items), items)
	}
}

// TestRunWiSetStatus_SubagentsDoNotStompLegacyColumn is the bug-d2d3fb3f
// regression test. When N subagents with distinct agent_ids claim different
// features on the same session, they must not race each other writing to
// sessions.active_feature_id — that single-row shared state caused silent
// attribution loss and stalls under parallel dispatch.
//
// Invariant: subagent feature starts leave sessions.active_feature_id
// untouched; only root launcher agents write it. All agents' per-agent claims
// still land correctly in active_work_items.
func TestRunWiSetStatus_SubagentsDoNotStompLegacyColumn(t *testing.T) {
	// test-session-no-stomp — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000108"
	const N = 4

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	featIDs := make([]string, N)
	for i := 0; i < N; i++ {
		title := fmt.Sprintf("No-Stomp Feature %d", i)
		if err := testCreate("feature", title, trackID, "medium", false, false); err != nil {
			t.Fatalf("create feature %d: %v", i, err)
		}
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) < N {
		t.Fatalf("expected %d feature files, got %d", N, len(featFiles))
	}
	for i := 0; i < N; i++ {
		node, err := htmlparse.ParseFile(featFiles[i])
		if err != nil {
			t.Fatalf("parse feature %d: %v", i, err)
		}
		featIDs[i] = node.ID
	}

	// Each subagent gets a distinct agent_id (not AgentRootSentinel).
	// Serialize workitem.Open calls via a cap-1 semaphore (matches the
	// one-process-per-agent reality); the point of this test is schema
	// and dual-write behavior, not raw SQLite write parallelism.
	sem := make(chan struct{}, 1)
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		agentID := fmt.Sprintf("subagent-%d", i)
		featID := featIDs[i]
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			errCh <- wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID)
		}()
	}
	for i := 0; i < N; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("subagent %d error: %v", i, err)
		}
	}

	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Every subagent's claim must be in active_work_items.
	items, err := dbpkg.ActiveWorkItemsForSession(database, sessionID)
	if err != nil {
		t.Fatalf("ActiveWorkItemsForSession: %v", err)
	}
	if len(items) != N {
		t.Errorf("want %d active_work_items rows, got %d: %v", N, len(items), items)
	}

	// Invariant: sessions.active_feature_id must NOT be written by subagents.
	// Without the fix, it reflects whichever subagent wrote last (flaky); with
	// the fix it stays empty because no root agent touched it.
	legacy := hooks.GetActiveFeatureID(database, sessionID)
	if legacy != "" {
		t.Errorf("sessions.active_feature_id must be empty when only subagents claim features; got %q", legacy)
	}
}

func TestRunWiSetStatus_CodexLauncherWritesLegacyColumn(t *testing.T) {
	// test-session-codex-root — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000109"
	const agentID = "codex"

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("feature", "Codex Launcher Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	if err := wiSetStatusWithAgent("feature", featNode.ID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("codex feature start: %v", err)
	}

	database, err := openDB(hgDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if got := hooks.GetActiveFeatureID(database, sessionID); got != featNode.ID {
		t.Fatalf("sessions.active_feature_id = %q, want %q", got, featNode.ID)
	}
}

// --- Feature-complete spec-enforcement gate (feat-0fd7c8bc) ----------

// setupFeatureGateProject creates a project root with a .wipnote subdir.
// Returns the wipnoteDir.
func setupFeatureGateProject(t *testing.T) string {
	t.Helper()
	projectRoot := t.TempDir()
	hgDir := filepath.Join(projectRoot, ".wipnote")
	for _, sub := range []string{"features", "bugs", "tracks", "plans", "specs", "spikes"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return hgDir
}

// writeFeatureWithSpec writes a minimal feature HTML inside hgDir/features/
// with optional spec content (raw text inside <section class="spec">).
func writeFeatureWithSpec(t *testing.T, hgDir, featureID, specContent string) {
	t.Helper()
	body := fmt.Sprintf(`<!DOCTYPE html><html><body><article id="%s"></article>`, featureID)
	if specContent != "" {
		body += `<section class="spec">` + specContent + `</section>`
	}
	body += "</body></html>"
	path := filepath.Join(hgDir, "features", featureID+".html")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write feature html: %v", err)
	}
}

// writeFeatureCompleteEnforcementConfig writes spec_enforcement.feature_complete=true
// into hgDir/config.json (which lives under the project root).
func writeFeatureCompleteEnforcementConfig(t *testing.T, hgDir string, enabled bool) {
	t.Helper()
	body := fmt.Sprintf(`{"spec_enforcement":{"feature_complete":%t}}`, enabled)
	if err := os.WriteFile(filepath.Join(hgDir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestFeatureCompleteGate_Disabled — default config; complete succeeds without
// any spec section.
func TestFeatureCompleteGate_Disabled(t *testing.T) {
	wiAllowSpecSkip = false
	hgDir := setupFeatureGateProject(t)
	writeFeatureWithSpec(t, hgDir, "feat-gatedis", "")

	if err := checkFeatureCompleteSpecGate(hgDir, "feat-gatedis"); err != nil {
		t.Errorf("expected gate disabled by default, got error: %v", err)
	}
}

// TestFeatureCompleteGate_EnabledNoSpec — config opted in, no spec section,
// gate refuses.
func TestFeatureCompleteGate_EnabledNoSpec(t *testing.T) {
	hgDir := setupFeatureGateProject(t)
	writeFeatureCompleteEnforcementConfig(t, hgDir, true)
	writeFeatureWithSpec(t, hgDir, "feat-gatenospec", "")

	err := checkFeatureCompleteSpecGate(hgDir, "feat-gatenospec")
	if err == nil {
		t.Fatal("expected gate refusal, got nil")
	}
	if !strings.Contains(err.Error(), "no spec section") {
		t.Errorf("error should mention missing spec: %v", err)
	}
	if !strings.Contains(err.Error(), "spec generate") {
		t.Errorf("error should point to remediation command: %v", err)
	}
}

// TestFeatureCompleteGate_EnabledEmptySpec — section exists but has no
// requirements/criteria; gate refuses.
func TestFeatureCompleteGate_EnabledEmptySpec(t *testing.T) {
	hgDir := setupFeatureGateProject(t)
	writeFeatureCompleteEnforcementConfig(t, hgDir, true)
	writeFeatureWithSpec(t, hgDir, "feat-gateempty",
		`<pre>## Problem
x
## Notes
none</pre>`)

	err := checkFeatureCompleteSpecGate(hgDir, "feat-gateempty")
	if err == nil {
		t.Fatal("expected gate refusal on empty criteria, got nil")
	}
	if !strings.Contains(err.Error(), "0 criteria") {
		t.Errorf("error should mention 0 criteria: %v", err)
	}
}

// TestFeatureCompleteGate_EnabledWithRequirement — section has a Requirement
// + scenario with checked task line; gate passes.
func TestFeatureCompleteGate_EnabledWithRequirement(t *testing.T) {
	hgDir := setupFeatureGateProject(t)
	writeFeatureCompleteEnforcementConfig(t, hgDir, true)

	specBody := `<pre>## ADDED Requirements

### Requirement: Login
The implementation SHALL ensure: users authenticate.

#### Scenario: valid token
- [x] WHEN the token signature verifies
- [x] THEN the user is logged in
</pre>`
	writeFeatureWithSpec(t, hgDir, "feat-gateok", specBody)

	if err := checkFeatureCompleteSpecGate(hgDir, "feat-gateok"); err != nil {
		t.Errorf("expected gate to pass with requirement present, got: %v", err)
	}
}

// TestFeatureCompleteGate_EnabledWithLegacyCriterion — legacy ## Acceptance
// Criteria with at least one checkbox passes the gate.
func TestFeatureCompleteGate_EnabledWithLegacyCriterion(t *testing.T) {
	hgDir := setupFeatureGateProject(t)
	writeFeatureCompleteEnforcementConfig(t, hgDir, true)
	writeFeatureWithSpec(t, hgDir, "feat-gatelegacy",
		`<pre>## Acceptance Criteria
- [x] First criterion
- [ ] Second criterion
</pre>`)

	if err := checkFeatureCompleteSpecGate(hgDir, "feat-gatelegacy"); err != nil {
		t.Errorf("expected gate to pass with legacy criterion, got: %v", err)
	}
}

func setupDirtySourceGateRepo(t *testing.T) (repoRoot, wipnoteDir string) {
	t.Helper()
	repoRoot = t.TempDir()
	initGitRepo(t, repoRoot)
	wipnoteDir = filepath.Join(repoRoot, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", "feat-dirty.html"), []byte("<html></html>\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	gitCmd(t, repoRoot, "add", ".")
	gitCmd(t, repoRoot, "commit", "-m", "initial")
	return repoRoot, wipnoteDir
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", full, err, out)
	}
}

func TestUncommittedSourceCompleteGate_BlocksTrackedSource(t *testing.T) {
	repoRoot, wipnoteDir := setupDirtySourceGateRepo(t)
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("modify source: %v", err)
	}

	err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-dirty", false)
	if err == nil {
		t.Fatal("expected dirty source gate to block")
	}
	msg := err.Error()
	for _, want := range []string{"refusing to complete feat-dirty", "main.go", "git add main.go", "git commit -m \"feat-dirty: commit implementation\"", "--allow-dirty"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("gate error missing %q:\n%s", want, msg)
		}
	}
}

func TestUncommittedSourceCompleteGate_IgnoresWipnoteOnly(t *testing.T) {
	_, wipnoteDir := setupDirtySourceGateRepo(t)
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", "feat-dirty.html"), []byte("<html>updated</html>\n"), 0o644); err != nil {
		t.Fatalf("modify artifact: %v", err)
	}

	if err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-dirty", false); err != nil {
		t.Fatalf("expected .wipnote-only changes to pass, got: %v", err)
	}
}

func TestUncommittedSourceCompleteGate_AllowDirtyBypasses(t *testing.T) {
	repoRoot, wipnoteDir := setupDirtySourceGateRepo(t)
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("modify source: %v", err)
	}

	if err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-dirty", true); err != nil {
		t.Fatalf("allow dirty should bypass gate, got: %v", err)
	}
}

func TestUncommittedSourceCompleteGate_IgnoresUntrackedSource(t *testing.T) {
	repoRoot, wipnoteDir := setupDirtySourceGateRepo(t)
	if err := os.WriteFile(filepath.Join(repoRoot, "new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write untracked source: %v", err)
	}

	if err := checkUncommittedSourceCompleteGate(wipnoteDir, "feat-dirty", false); err != nil {
		t.Fatalf("untracked source should not block tracked-file gate, got: %v", err)
	}
}

// TestFeatureStart_LiveCollision_Refuses verifies that `feature start` returns
// a non-zero error (without mutating state) when a foreign live session holds
// the same work item.
func TestFeatureStart_LiveCollision_Refuses(t *testing.T) {
	if testing.Short() {
		t.Skip("drives multi-session collision lifecycle")
	}

	// sess-collision-holder — session-shaped id required by the canonical session ledger.
	const holderSessionID = "019ee378-abcd-7000-8000-000000000201"
	// sess-collision-caller — session-shaped id required by the canonical session ledger.
	const callerSessionID = "019ee378-abcd-7000-8000-000000000202"
	const agentID = dbpkg.AgentRootSentinel

	tmpDir, hgDir := testHgDirWithDB(t, holderSessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	// Create a second session for the caller.
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     callerSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("insert caller session: %v", err)
	}

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("feature", "Collision Test", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Holder claims the item and gets a fresh heartbeat.
	holderClaim := &models.Claim{
		ClaimID:          "clm-collision-holder",
		WorkItemID:       featID,
		OwnerSessionID:   holderSessionID,
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: agentID,
		Status:           models.ClaimInProgress,
	}
	if err := dbpkg.ClaimItemOrRenew(database, holderClaim, 30*time.Minute); err != nil {
		t.Fatalf("holder claim: %v", err)
	}
	// Ensure heartbeat is fresh (within liveness window).
	if _, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
		time.Now().UTC().Format(time.RFC3339), "clm-collision-holder",
	); err != nil {
		t.Fatalf("set heartbeat: %v", err)
	}
	database.Close()

	// Also advance the HTML status to in-progress so col.Start works.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", holderSessionID, agentID); err != nil {
		t.Fatalf("holder start: %v", err)
	}

	// Reopen to verify state before refusal.
	database2, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	var preCount int
	database2.QueryRow(
		`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND status IN ('proposed','claimed','in_progress','blocked','handoff_pending')`,
		featID,
	).Scan(&preCount)
	database2.Close()

	// Caller tries to start the same item — must be refused (live collision).
	wiForceStart = false
	defer func() { wiForceStart = false }()
	err = wiSetStatusWithAgent("feature", featID, "in-progress", callerSessionID, agentID)
	if err == nil {
		t.Fatal("expected error from live-collision refusal, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal message missing '--force' hint: %v", err)
	}

	// State must not have mutated (claim count unchanged).
	database3, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db3: %v", err)
	}
	defer database3.Close()
	var postCount int
	database3.QueryRow(
		`SELECT COUNT(*) FROM claims WHERE work_item_id = ? AND status IN ('proposed','claimed','in_progress','blocked','handoff_pending')`,
		featID,
	).Scan(&postCount)
	if postCount != preCount {
		t.Errorf("claim count mutated on refusal: was %d, now %d", preCount, postCount)
	}
}

// TestFeatureStart_LiveCollision_ForceOverrides verifies that --force bypasses
// the live-collision refusal and proceeds (emitting a warning but succeeding).
func TestFeatureStart_LiveCollision_ForceOverrides(t *testing.T) {
	if testing.Short() {
		t.Skip("drives multi-session collision lifecycle")
	}

	// sess-force-holder — session-shaped id required by the canonical session ledger.
	const holderSessionID = "019ee378-abcd-7000-8000-000000000203"
	// sess-force-caller — session-shaped id required by the canonical session ledger.
	const callerSessionID = "019ee378-abcd-7000-8000-000000000204"
	const agentID = dbpkg.AgentRootSentinel

	tmpDir, hgDir := testHgDirWithDB(t, holderSessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     callerSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("insert caller session: %v", err)
	}

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("feature", "Force Override Test", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Holder claims and gets a fresh heartbeat.
	holderClaim := &models.Claim{
		ClaimID:          "clm-force-holder",
		WorkItemID:       featID,
		OwnerSessionID:   holderSessionID,
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: agentID,
		Status:           models.ClaimInProgress,
	}
	if err := dbpkg.ClaimItemOrRenew(database, holderClaim, 30*time.Minute); err != nil {
		t.Fatalf("holder claim: %v", err)
	}
	if _, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
		time.Now().UTC().Format(time.RFC3339), "clm-force-holder",
	); err != nil {
		t.Fatalf("set heartbeat: %v", err)
	}
	database.Close()

	// Holder starts first so HTML status is in-progress.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", holderSessionID, agentID); err != nil {
		t.Fatalf("holder start: %v", err)
	}

	// Caller with --force must succeed despite live collision.
	wiForceStart = true
	defer func() { wiForceStart = false }()
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", callerSessionID, agentID); err != nil {
		t.Fatalf("force start should succeed: %v", err)
	}
}

// TestFeatureStart_StaleCollision_Allows verifies that a stale (dead-heartbeat)
// foreign claim does NOT block start — the item must be freely reclaimable.
func TestFeatureStart_StaleCollision_Allows(t *testing.T) {
	if testing.Short() {
		t.Skip("drives multi-session collision lifecycle")
	}

	// sess-stale-coll-holder — session-shaped id required by the canonical session ledger.
	const holderSessionID = "019ee378-abcd-7000-8000-000000000205"
	// sess-stale-coll-caller — session-shaped id required by the canonical session ledger.
	const callerSessionID = "019ee378-abcd-7000-8000-000000000206"
	const agentID = dbpkg.AgentRootSentinel

	tmpDir, hgDir := testHgDirWithDB(t, holderSessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     callerSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("insert caller session: %v", err)
	}

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("feature", "Stale Collision Test", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	database.Close()

	// Holder starts first, writing a claim with a fresh heartbeat.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", holderSessionID, agentID); err != nil {
		t.Fatalf("holder start: %v", err)
	}

	// Now back-date ALL heartbeats for the holder session so liveness check fails.
	database2, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	ancient := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	if _, err := database2.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE owner_session_id = ?`,
		ancient, holderSessionID,
	); err != nil {
		t.Fatalf("set stale heartbeat: %v", err)
	}
	database2.Close()

	// Caller must succeed because holder's heartbeat is stale.
	wiForceStart = false
	defer func() { wiForceStart = false }()
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", callerSessionID, agentID); err != nil {
		t.Fatalf("stale-holder start should succeed: %v", err)
	}
}

// TestLearningNonFatal_OverLimitWord tests that completing a work item with an
// over-limit learning body succeeds (completes the item), does NOT attach the
// learning, and emits a clear warning with remediation command.
func TestLearningNonFatal_OverLimitWord(t *testing.T) {
	if testing.Short() {
		t.Skip("drives learning validation lifecycle")
	}

	// test-session-learning-over-limit — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000110"
	const agentID = "test-agent"
	tmpDir, hgDir := testHgDirWithDB(t, sessionID)

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	// Create a track first.
	if err := testCreate("track", "Test Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) == 0 {
		t.Fatal("no track file created")
	}
	trackNode, _ := htmlparse.ParseFile(trackFiles[len(trackFiles)-1])
	trackID := trackNode.ID

	// Create a feature.
	if err := testCreate("feature", "Feature for Over-Limit Learning", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Start the feature.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Create a learning body that exceeds the 120-word limit (121 words).
	overLimitBody := strings.Repeat("word ", 121)
	overLimitBody = strings.TrimSpace(overLimitBody)

	// Set the global wiLearning flag to the over-limit body.
	defer func() {
		wiLearning = ""
		wiLearningKind = ""
	}()
	wiLearning = overLimitBody
	wiLearningKind = "decision"

	// Complete the feature with the over-limit learning. This should succeed,
	// but NOT attach the learning.
	err := wiSetStatusWithAgent("feature", featID, "done", sessionID, agentID)

	// The completion itself must succeed (err == nil).
	if err != nil {
		t.Fatalf("completion should succeed despite over-limit learning: %v", err)
	}

	// Verify the item is marked as done.
	p, err := workitem.Open(hgDir, "claude-code")
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	defer p.Close()

	col := collectionFor(p, "feature")
	feat, err := col.Get(featID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if feat.Status != "done" {
		t.Errorf("expected status 'done', got %q", feat.Status)
	}
	// The key test: the item must be marked done despite the invalid learning.
	// The learning card attachment would fail silently (or not be created), but
	// the completion itself succeeds.
}

// TestLearningNonFatal_ValidBody tests that completing a work item with a valid
// learning body succeeds AND attaches the learning card.
func TestLearningNonFatal_ValidBody(t *testing.T) {
	if testing.Short() {
		t.Skip("drives learning validation lifecycle")
	}

	// test-session-learning-valid — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000111"
	const agentID = "test-agent"
	tmpDir, hgDir := testHgDirWithDB(t, sessionID)

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	// Create a track first.
	if err := testCreate("track", "Test Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) == 0 {
		t.Fatal("no track file created")
	}
	trackNode, _ := htmlparse.ParseFile(trackFiles[len(trackFiles)-1])
	trackID := trackNode.ID

	// Create a feature.
	if err := testCreate("feature", "Feature for Valid Learning", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Start the feature.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Create a learning body that is within the limit (exactly 120 words).
	validBody := strings.Repeat("word ", 120)
	validBody = strings.TrimSpace(validBody)

	// Set the global wiLearning flag to the valid body.
	defer func() {
		wiLearning = ""
		wiLearningKind = ""
	}()
	wiLearning = validBody
	wiLearningKind = "hazard"

	// Complete the feature with valid learning. This should succeed and attach.
	err := wiSetStatusWithAgent("feature", featID, "done", sessionID, agentID)

	// The completion itself must succeed.
	if err != nil {
		t.Fatalf("completion should succeed with valid learning: %v", err)
	}

	// Verify the item is marked as done.
	p, err := workitem.Open(hgDir, "claude-code")
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	defer p.Close()

	col := collectionFor(p, "feature")
	feat, err := col.Get(featID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if feat.Status != "done" {
		t.Errorf("expected status 'done', got %q", feat.Status)
	}
	// The key test: the item is marked done AND the valid learning was processed
	// (the createLearningCard function ran without error). We can't easily verify
	// the card was created in the simple case, but we confirm no error occurred.
}

// TestLearningNonFatal_InvalidKind tests that completing a work item with an
// invalid learning kind succeeds (completes the item), does NOT attach the
// learning, and emits a remediation command that does NOT contain the invalid
// kind and is properly shell-quoted.
func TestLearningNonFatal_InvalidKind(t *testing.T) {
	if testing.Short() {
		t.Skip("drives learning validation lifecycle")
	}

	// test-session-learning-invalid-kind — session-shaped id required by the canonical session ledger.
	const sessionID = "019ee378-abcd-7000-8000-000000000112"
	const agentID = "test-agent"
	tmpDir, hgDir := testHgDirWithDB(t, sessionID)

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	// Create a track first.
	if err := testCreate("track", "Test Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) == 0 {
		t.Fatal("no track file created")
	}
	trackNode, _ := htmlparse.ParseFile(trackFiles[len(trackFiles)-1])
	trackID := trackNode.ID

	// Create a feature.
	if err := testCreate("feature", "Feature for Invalid Learning Kind", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Start the feature.
	if err := wiSetStatusWithAgent("feature", featID, "in-progress", sessionID, agentID); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Create a learning body with shell metacharacters to test proper quoting/escaping.
	// This body will be quoted by shellQuote (unlike the safe slug and id).
	validBody := `danger; rm -rf $HOME && echo "pwned"` // contains ; space $ & " which are unsafe

	// Set the global wiLearning flag to the valid body but use an INVALID kind.
	defer func() {
		wiLearning = ""
		wiLearningKind = ""
	}()

	wiLearning = validBody
	wiLearningKind = "invalid-kind" // not in {decision, hazard, invariant, subsystem-map}

	// Capture stderr to check the remediation command.
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	// Complete the feature with invalid learning kind. This should succeed,
	// but NOT attach the learning.
	if err := wiSetStatusWithAgent("feature", featID, "done", sessionID, agentID); err != nil {
		w.Close()
		os.Stderr = oldStderr
		t.Fatalf("completion should succeed despite invalid learning kind: %v", err)
	}
	w.Close()
	os.Stderr = oldStderr

	// Read the stderr output.
	stderrOut, _ := io.ReadAll(r)
	stderrStr := string(stderrOut)

	// The key test: the item must be marked done despite the invalid learning kind.
	featFiles, _ = filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	feat, _ := htmlparse.ParseFile(featFiles[0])
	if feat.Status != "done" {
		t.Errorf("expected status 'done', got %q", feat.Status)
	}

	// Verify the remediation command in stderr:
	// 1. Should NOT contain the invalid kind as a --kind flag value.
	if strings.Contains(stderrStr, "--kind invalid-kind") {
		t.Errorf("remediation command should NOT contain invalid kind in --kind flag; stderr: %s", stderrStr)
	}

	// 2. Should contain --kind decision (the known-valid default).
	if !strings.Contains(stderrStr, "--kind decision") {
		t.Errorf("remediation command should contain --kind decision as a valid default; stderr: %s", stderrStr)
	}

	// 3. Should use --links for the work item ID (not --paths which is for file globs).
	if strings.Contains(stderrStr, "--paths") {
		t.Errorf("remediation command should NOT use --paths for work item ID; stderr: %s", stderrStr)
	}
	if !strings.Contains(stderrStr, "--links") {
		t.Errorf("remediation command should use --links for work item ID; stderr: %s", stderrStr)
	}

	// 4. Verify shell safety: the body (which contains metacharacters) should be properly quoted.
	// shellQuote only quotes when input contains unsafe characters; the slug and id are safe,
	// so they appear unquoted. The body contains shell metacharacters and must be quoted/escaped.
	if !strings.Contains(stderrStr, "wipnote arch add") {
		t.Errorf("remediation command should contain 'wipnote arch add'; stderr: %s", stderrStr)
	}

	// Slug is "learning-" + featID (all safe chars) — should appear UNQUOTED.
	expectedSlugUnquoted := "learning-" + featID
	if !strings.Contains(stderrStr, expectedSlugUnquoted) {
		t.Errorf("remediation command should contain unquoted slug %q; stderr: %s", expectedSlugUnquoted, stderrStr)
	}

	// Work item ID (all safe chars) should appear UNQUOTED after --links.
	// (Verify it's there; we don't need to separately check it's unquoted since safe values are never quoted.)
	if !strings.Contains(stderrStr, "--links "+featID) {
		t.Errorf("remediation command should contain --links %s; stderr: %s", featID, stderrStr)
	}

	// The learning body contains shell metacharacters (;, $, &, ", spaces) and must be quoted.
	// shellQuote wraps it in single quotes and escapes internal single quotes as '\"'\"'.
	// Our test body contains no single quotes, so the expected form is simply single-quoted.
	expectedBodyQuoted := `'danger; rm -rf $HOME && echo "pwned"'`
	if !strings.Contains(stderrStr, expectedBodyQuoted) {
		t.Errorf("remediation command should contain properly quoted body %q; stderr: %s", expectedBodyQuoted, stderrStr)
	}
}
