package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTaskCompletionConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755)
	if readTaskCompletionConfig(dir) {
		t.Error("expected false when config.json is missing")
	}
}

func TestReadTaskCompletionConfig_FlagOff(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".wipnote")
	os.MkdirAll(cfgDir, 0o755)
	data, _ := json.Marshal(map[string]any{"block_task_completion_on_quality_failure": false})
	os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o644)

	if readTaskCompletionConfig(dir) {
		t.Error("expected false when flag is explicitly false")
	}
}

func TestReadTaskCompletionConfig_FlagOn(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".wipnote")
	os.MkdirAll(cfgDir, 0o755)
	data, _ := json.Marshal(map[string]any{"block_task_completion_on_quality_failure": true})
	os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o644)

	if !readTaskCompletionConfig(dir) {
		t.Error("expected true when flag is true")
	}
}

func TestRunTaskCompletionGate_UnknownProject(t *testing.T) {
	// Empty dir with no manifest files → passes (unknown project type).
	dir := t.TempDir()
	result := runTaskCompletionGate(dir)
	if !result.Passed {
		t.Errorf("expected pass for unknown project, got failed: %s", result.Output)
	}
}

func TestRunTaskCompletionGate_DetectsGoProject(t *testing.T) {
	dir := t.TempDir()
	// Create a go.mod so DetectProjectType returns Go.
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	result := runTaskCompletionGate(dir)
	// The gate will likely fail (no Go source), but it should detect the project.
	if result.GateName != "go test ./..." {
		t.Errorf("GateName = %q, want %q", result.GateName, "go test ./...")
	}
}

func TestTaskCompleted_FlagOff_NeverBlocks(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	// Create a project dir with a go.mod (gate will fail) but NO config.json (flag off).
	projectDir := t.TempDir()
	os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755)
	os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module test\n"), 0o644)
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       projectDir,
		TaskID:    "task-noblock",
		TaskData:  map[string]any{"subject": "Test task"},
	}

	result, err := TaskCompleted(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCompleted should not error when flag is off: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true when flag is off (warn-only)")
	}
}

func TestTaskCompleted_FlagOn_BlocksOnFailure(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	// Insert a feature and set it as the active feature for the session so the
	// quality gate guard (featureID != "") is satisfied.
	td.addTrack("trk-gate-test", "Gate test track")
	td.addFeature("feat-gate-test", "feature", "Gate test feature", "in-progress")
	if _, err := td.DB.Exec(
		`UPDATE sessions SET active_feature_id = ? WHERE session_id = ?`,
		"feat-gate-test", sessionID,
	); err != nil {
		t.Fatalf("set active_feature_id: %v", err)
	}
	// Reset the package-level cache so the updated active_feature_id is read.
	featureIDCache = featureIDCacheEntry{}

	// Create project dir with go.mod AND config with blocking enabled.
	projectDir := t.TempDir()
	cfgDir := filepath.Join(projectDir, ".wipnote")
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module test\n"), 0o644)
	data, _ := json.Marshal(map[string]any{"block_task_completion_on_quality_failure": true})
	os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o644)
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       projectDir,
		TaskID:    "task-block",
		TaskData:  map[string]any{"subject": "Blocked task"},
	}

	result, err := TaskCompleted(event, td.DB)
	// When blocking, the handler returns a BlockExit2Error.
	if err == nil {
		t.Fatal("expected BlockExit2Error when flag is on and gate fails")
	}
	blockErr, ok := err.(*BlockExit2Error)
	if !ok {
		t.Fatalf("expected *BlockExit2Error, got %T: %v", err, err)
	}
	if blockErr.Message == "" {
		t.Error("expected non-empty block message")
	}
	if result != nil {
		t.Errorf("expected nil result when blocking, got %+v", result)
	}
	// Reset cache so subsequent tests are not affected.
	featureIDCache = featureIDCacheEntry{}
}
