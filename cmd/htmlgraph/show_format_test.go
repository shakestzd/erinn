package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupShowFormatDir creates a minimal .htmlgraph directory with one work item
// of each type and returns hgDir and a map[type]id.
func setupShowFormatDir(t *testing.T) (string, map[string]string) {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })

	ids := make(map[string]string)

	// Create a track first (needed as parent for feature and bug).
	if err := testCreate("track", "Show Format Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) == 0 {
		t.Fatal("no track file created")
	}
	trackNode, _ := parseHTMLForID(t, trackFiles[0])
	ids["track"] = trackNode

	// Create a feature.
	if err := runWiCreate("feature", "Show Format Feature", &wiCreateOpts{
		trackID:     ids["track"],
		priority:    "high",
		description: "feature description for show format test",
		start:       false,
		noLink:      false,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) == 0 {
		t.Fatal("no feature file created")
	}
	ids["feature"] = parseHTMLForIDMust(t, featFiles[0])

	// Create a bug.
	if err := runWiCreate("bug", "Show Format Bug", &wiCreateOpts{
		trackID:     ids["track"],
		priority:    "high",
		description: "bug description for show format test",
		start:       false,
		noLink:      true,
	}); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) == 0 {
		t.Fatal("no bug file created")
	}
	ids["bug"] = parseHTMLForIDMust(t, bugFiles[0])

	// Create a plan.
	if err := runWiCreate("plan", "Show Format Plan", &wiCreateOpts{
		trackID:     ids["track"],
		priority:    "medium",
		description: "plan description for show format test",
		start:       false,
		noLink:      true,
	}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	planFiles, _ := filepath.Glob(filepath.Join(hgDir, "plans", "plan-*.html"))
	if len(planFiles) == 0 {
		t.Fatal("no plan file created")
	}
	ids["plan"] = parseHTMLForIDMust(t, planFiles[0])

	return hgDir, ids
}

// parseHTMLForID parses the first article id= from a .html file.
func parseHTMLForID(t *testing.T, path string) (string, error) {
	t.Helper()
	node, err := parseHTMLWorkItemToNode(path)
	if err != nil {
		return "", err
	}
	return node.id, nil
}

// parseHTMLWorkItemToNode parses a work item html file for id.
// We use htmlparse.ParseFile in production; here we just open the file via
// the already-imported htmlparse package (indirectly via runWiShow).
type htmlNode struct{ id string }

func parseHTMLWorkItemToNode(path string) (*htmlNode, error) {
	// Re-use the existing parseHTMLWorkItem from relevant.go which does goquery parsing.
	r, err := parseHTMLWorkItem(path)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	return &htmlNode{id: r.ID}, nil
}

func parseHTMLForIDMust(t *testing.T, path string) string {
	t.Helper()
	id, err := parseHTMLForID(t, path)
	if err != nil || id == "" {
		t.Fatalf("parseHTMLForID(%q): %v id=%q", path, err, id)
	}
	return id
}

// captureOutput runs a function and captures stdout.
func captureShowOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	runErr := fn()

	w.Close()
	os.Stdout = old

	buf := make([]byte, 1<<20)
	n, _ := r.Read(buf)
	return string(buf[:n]), runErr
}

// assertValidJSON checks that out can be unmarshalled into map[string]any and
// that the given key fields are present.
func assertValidJSON(t *testing.T, label, out string, requiredKeys ...string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%s: output is not valid JSON: %v\noutput:\n%s", label, err, out)
	}
	for _, k := range requiredKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: JSON missing key %q; keys: %v", label, k, jsonKeys(m))
		}
	}
}

func jsonKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// assertNotJSON checks that out does NOT parse as a JSON object — i.e., it is
// human-readable text.
func assertNotJSON(t *testing.T, label, out string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err == nil {
		t.Errorf("%s: expected human text, but output parsed as JSON object", label)
	}
}

// --- Test: feature show --format json ---

func TestFeatureShow_FormatJSON(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["feature"]

	out, err := captureShowOutput(t, func() error {
		return runWiShowFormat(id, "json")
	})
	if err != nil {
		t.Fatalf("runWiShowFormat(feature, json): %v", err)
	}

	assertValidJSON(t, "feature show --format json", out, "id", "title", "status")
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	if m["id"] != id {
		t.Errorf("id field: got %v, want %q", m["id"], id)
	}
}

func TestFeatureShow_FormatText(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["feature"]

	out, err := captureShowOutput(t, func() error {
		return runWiShowFormat(id, "text")
	})
	if err != nil {
		t.Fatalf("runWiShowFormat(feature, text): %v", err)
	}

	assertNotJSON(t, "feature show --format text", out)
	// Text output should contain the id.
	if !strings.Contains(out, id) {
		t.Errorf("text output does not contain id %q:\n%s", id, out)
	}
}

func TestFeatureShow_DefaultFormatIsText(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["feature"]

	// Default (empty string) should behave like text.
	out, err := captureShowOutput(t, func() error {
		return runWiShowFormat(id, "")
	})
	if err != nil {
		t.Fatalf("runWiShowFormat(feature, default): %v", err)
	}

	assertNotJSON(t, "feature show (default)", out)
}

// --- Test: bug show --format json ---

func TestBugShow_FormatJSON(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["bug"]

	out, err := captureShowOutput(t, func() error {
		return runWiShowFormat(id, "json")
	})
	if err != nil {
		t.Fatalf("runWiShowFormat(bug, json): %v", err)
	}

	assertValidJSON(t, "bug show --format json", out, "id", "title", "status")
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	if m["id"] != id {
		t.Errorf("id field: got %v, want %q", m["id"], id)
	}
}

func TestBugShow_FormatText(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["bug"]

	out, err := captureShowOutput(t, func() error {
		return runWiShowFormat(id, "text")
	})
	if err != nil {
		t.Fatalf("runWiShowFormat(bug, text): %v", err)
	}

	assertNotJSON(t, "bug show --format text", out)
	if !strings.Contains(out, id) {
		t.Errorf("text output does not contain id %q:\n%s", id, out)
	}
}

// --- Test: plan show --format json ---

func TestPlanShow_FormatJSON(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["plan"]

	out, err := captureShowOutput(t, func() error {
		return runPlanShowFormat(id, "json")
	})
	if err != nil {
		t.Fatalf("runPlanShowFormat(json): %v", err)
	}

	assertValidJSON(t, "plan show --format json", out, "id", "title", "status")
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	if m["id"] != id {
		t.Errorf("id field: got %v, want %q", m["id"], id)
	}
}

func TestPlanShow_FormatText(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["plan"]

	out, err := captureShowOutput(t, func() error {
		return runPlanShowFormat(id, "text")
	})
	if err != nil {
		t.Fatalf("runPlanShowFormat(text): %v", err)
	}

	assertNotJSON(t, "plan show --format text", out)
	if !strings.Contains(out, id) {
		t.Errorf("text output does not contain id %q:\n%s", id, out)
	}
}

// --- Test: track show --format json ---

func TestTrackShow_FormatJSON(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["track"]

	out, err := captureShowOutput(t, func() error {
		return runTrackShowFormat(id, false, "json")
	})
	if err != nil {
		t.Fatalf("runTrackShowFormat(json): %v", err)
	}

	assertValidJSON(t, "track show --format json", out, "id", "title", "status")
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	if m["id"] != id {
		t.Errorf("id field: got %v, want %q", m["id"], id)
	}
}

func TestTrackShow_FormatText(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["track"]

	out, err := captureShowOutput(t, func() error {
		return runTrackShowFormat(id, false, "text")
	})
	if err != nil {
		t.Fatalf("runTrackShowFormat(text): %v", err)
	}

	assertNotJSON(t, "track show --format text", out)
	if !strings.Contains(out, id) {
		t.Errorf("text output does not contain id %q:\n%s", id, out)
	}
}

func TestTrackShow_FormatDefault(t *testing.T) {
	_, ids := setupShowFormatDir(t)
	id := ids["track"]

	out, err := captureShowOutput(t, func() error {
		return runTrackShow(id, false)
	})
	if err != nil {
		t.Fatalf("runTrackShow(default): %v", err)
	}

	assertNotJSON(t, "track show (default)", out)
	if !strings.Contains(out, id) {
		t.Errorf("text output does not contain id %q:\n%s", id, out)
	}
}

// TestShowJSON_KeyFields verifies that important metadata fields appear in JSON
// for every work item type (not just the three required by the task spec).
func TestShowJSON_KeyFields(t *testing.T) {
	_, ids := setupShowFormatDir(t)

	cases := []struct {
		name   string
		fn     func() error
		wantID string
	}{
		{
			name:   "feature",
			fn:     func() error { return runWiShowFormat(ids["feature"], "json") },
			wantID: ids["feature"],
		},
		{
			name:   "bug",
			fn:     func() error { return runWiShowFormat(ids["bug"], "json") },
			wantID: ids["bug"],
		},
		{
			name:   "plan",
			fn:     func() error { return runPlanShowFormat(ids["plan"], "json") },
			wantID: ids["plan"],
		},
		{
			name:   "track",
			fn:     func() error { return runTrackShowFormat(ids["track"], false, "json") },
			wantID: ids["track"],
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureShowOutput(t, tc.fn)
			if err != nil {
				t.Fatalf("%s show --format json: %v", tc.name, err)
			}
			assertValidJSON(t, tc.name, out, "id", "title", "status", "priority")

			var m map[string]any
			_ = json.Unmarshal([]byte(out), &m)
			if m["id"] != tc.wantID {
				t.Errorf("%s: id = %v, want %q", tc.name, m["id"], tc.wantID)
			}
		})
	}
}
