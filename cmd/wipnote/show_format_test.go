package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
)

// sampleBugHTML is a minimal wipnote bug HTML fixture.
const sampleBugHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Test Bug</title></head>
<body>
<article id="bug-aabbccdd"
         data-type="bug"
         data-status="in-progress"
         data-priority="high"
         data-created="2026-01-01T00:00:00Z"
         data-track-id="trk-testtrack">
  <header><h1>Sample Bug Title</h1></header>
  <section data-content>
    <p>This is a bug description.</p>
  </section>
</article>
</body>
</html>`

// sampleSpikeHTML is a minimal wipnote spike HTML fixture.
const sampleSpikeHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Test Spike</title></head>
<body>
<article id="spk-aabbccdd"
         data-type="spike"
         data-status="todo"
         data-priority="medium">
  <header><h1>Sample Spike Title</h1></header>
  <section data-content>
    <p>This is a spike description.</p>
  </section>
</article>
</body>
</html>`

// sampleTrackHTML is a minimal wipnote track HTML fixture.
const sampleTrackHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Test Track</title></head>
<body>
<article id="trk-aabbccdd"
         data-type="track"
         data-status="in-progress"
         data-priority="high"
         data-created="2026-01-01T00:00:00Z">
  <header><h1>Sample Track Title</h1></header>
  <section data-content>
    <p>This is a track description.</p>
  </section>
</article>
</body>
</html>`

// samplePlanHTML is a minimal wipnote plan HTML fixture.
const samplePlanHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Plan: Test Plan</title></head>
<body>
<article id="plan-aabbccdd"
         data-type="plan"
         data-status="draft"
         data-priority="medium"
         data-created="2026-01-01T00:00:00Z">
  <header><h1>Sample Plan Title</h1></header>
  <section data-content>
    <p>This is a plan description.</p>
  </section>
</article>
</body>
</html>`

// makeShowFixture creates a temp .wipnote directory with a single HTML file
// in the given subdir with the given filename and content. Returns the tmpDir.
func makeShowFixture(t *testing.T, subdir, filename, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	path := filepath.Join(hgDir, subdir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return tmpDir
}

// --- Bug show tests ---

func TestBugShow_FormatJSON(t *testing.T) {
	tmpDir := makeShowFixture(t, "bugs", "bug-aabbccdd.html", sampleBugHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runWiShowWithFormat("bug-aabbccdd", "json"); err != nil {
			t.Fatalf("runWiShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	for _, key := range []string{"id", "title", "status"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing field %q; got: %s", key, out)
		}
	}
	if m["id"] != "bug-aabbccdd" {
		t.Errorf("id = %q, want bug-aabbccdd", m["id"])
	}
}

func TestBugShow_FormatText(t *testing.T) {
	tmpDir := makeShowFixture(t, "bugs", "bug-aabbccdd.html", sampleBugHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runWiShowWithFormat("bug-aabbccdd", "text"); err != nil {
			t.Fatalf("runWiShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		t.Error("text format output unexpectedly parsed as JSON")
	}
	if !strings.Contains(out, "bug-aabbccdd") {
		t.Errorf("text output missing ID; got: %s", out)
	}
	if !strings.Contains(out, "Sample Bug Title") {
		t.Errorf("text output missing title; got: %s", out)
	}
}

func TestBugShow_DefaultIsText(t *testing.T) {
	tmpDir := makeShowFixture(t, "bugs", "bug-aabbccdd.html", sampleBugHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Default (no format flag) should produce text, not JSON.
	out := captureStdout(t, func() {
		if err := runWiShow("bug-aabbccdd"); err != nil {
			t.Fatalf("runWiShow: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		t.Error("default output unexpectedly parsed as JSON; default should be text")
	}
}

// --- Feature show tests ---

func TestFeatureShow_FormatJSON(t *testing.T) {
	tmpDir := makeShowFixture(t, "features", "feat-aabbccdd.html", sampleFeatureHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runWiShowWithFormat("feat-aabbccdd", "json"); err != nil {
			t.Fatalf("runWiShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	for _, key := range []string{"id", "title", "status"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing field %q; got: %s", key, out)
		}
	}
	if m["id"] != "feat-aabbccdd" {
		t.Errorf("id = %q, want feat-aabbccdd", m["id"])
	}
}

func TestFeatureShow_FormatText(t *testing.T) {
	tmpDir := makeShowFixture(t, "features", "feat-aabbccdd.html", sampleFeatureHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runWiShowWithFormat("feat-aabbccdd", "text"); err != nil {
			t.Fatalf("runWiShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		t.Error("text format output unexpectedly parsed as JSON")
	}
	if !strings.Contains(out, "feat-aabbccdd") {
		t.Errorf("text output missing ID; got: %s", out)
	}
}

// --- Spike show tests ---

func TestSpikeShow_FormatJSON(t *testing.T) {
	tmpDir := makeShowFixture(t, "spikes", "spk-aabbccdd.html", sampleSpikeHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runWiShowWithFormat("spk-aabbccdd", "json"); err != nil {
			t.Fatalf("runWiShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	for _, key := range []string{"id", "title", "status"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing field %q; got: %s", key, out)
		}
	}
}

// --- Track show tests ---

func TestTrackShow_FormatJSON(t *testing.T) {
	tmpDir := makeShowFixture(t, "tracks", "trk-aabbccdd.html", sampleTrackHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runTrackShowWithFormat("trk-aabbccdd", false, "json"); err != nil {
			t.Fatalf("runTrackShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	for _, key := range []string{"id", "title", "status"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing field %q; got: %s", key, out)
		}
	}
	if m["id"] != "trk-aabbccdd" {
		t.Errorf("id = %q, want trk-aabbccdd", m["id"])
	}
}

func TestTrackShow_FormatText(t *testing.T) {
	tmpDir := makeShowFixture(t, "tracks", "trk-aabbccdd.html", sampleTrackHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runTrackShowWithFormat("trk-aabbccdd", false, "text"); err != nil {
			t.Fatalf("runTrackShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		t.Error("text format output unexpectedly parsed as JSON")
	}
	if !strings.Contains(out, "trk-aabbccdd") {
		t.Errorf("text output missing ID; got: %s", out)
	}
}

func TestTrackShow_DefaultIsText(t *testing.T) {
	tmpDir := makeShowFixture(t, "tracks", "trk-aabbccdd.html", sampleTrackHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runTrackShow("trk-aabbccdd", false); err != nil {
			t.Fatalf("runTrackShow: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		t.Error("default output unexpectedly parsed as JSON; default should be text")
	}
}

// --- Plan show tests ---

func TestPlanShow_FormatJSON(t *testing.T) {
	tmpDir := makeShowFixture(t, "plans", "plan-aabbccdd.html", samplePlanHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runPlanShowWithFormat("plan-aabbccdd", "json"); err != nil {
			t.Fatalf("runPlanShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	for _, key := range []string{"id", "title", "status"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing field %q; got: %s", key, out)
		}
	}
	if m["id"] != "plan-aabbccdd" {
		t.Errorf("id = %q, want plan-aabbccdd", m["id"])
	}
}

func TestPlanShow_FormatText(t *testing.T) {
	tmpDir := makeShowFixture(t, "plans", "plan-aabbccdd.html", samplePlanHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runPlanShowWithFormat("plan-aabbccdd", "text"); err != nil {
			t.Fatalf("runPlanShowWithFormat: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		t.Error("text format output unexpectedly parsed as JSON")
	}
	if !strings.Contains(out, "plan-aabbccdd") {
		t.Errorf("text output missing ID; got: %s", out)
	}
}

func TestPlanShow_DefaultIsText(t *testing.T) {
	tmpDir := makeShowFixture(t, "plans", "plan-aabbccdd.html", samplePlanHTML)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// planShowCmd delegates to runWiShow which uses text by default.
	out := captureStdout(t, func() {
		if err := runWiShow("plan-aabbccdd"); err != nil {
			t.Fatalf("runWiShow: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		t.Error("default output unexpectedly parsed as JSON; default should be text")
	}
}

// --- JSON shape validation ---

func TestShowJSON_ContainsAllKeyFields(t *testing.T) {
	// Verify JSON output includes all the key fields we care about.
	node, err := htmlparse.ParseString(sampleBugHTML)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}

	out := captureStdout(t, func() {
		if err := printNodeDetailJSON(node); err != nil {
			t.Fatalf("printNodeDetailJSON: %v", err)
		}
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, out)
	}

	required := []string{"id", "title", "type", "status", "priority"}
	for _, key := range required {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing required field %q; got keys: %v", key, jsonKeys(m))
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
