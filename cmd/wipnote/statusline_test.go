package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
)

func TestStatuslineCmd(t *testing.T) {
	// Create temporary directory for test database
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}

	// Set up project directory
	oldCwd, _ := os.Getwd()
	defer os.Chdir(oldCwd)
	os.Chdir(tmpDir)

	// Create and populate test database
	dbPath := filepath.Join(wipnoteDir, ".db", "wipnote.db")
	db, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Insert test data
	tests := []struct {
		name        string
		data        []testWorkItem
		expectID    string
		expectTitle string
	}{
		{
			name: "no_active_items",
			data: []testWorkItem{
				{ID: "feat-123", Type: "feature", Status: "todo", Title: "Test Feature"},
			},
			expectID:    "",
			expectTitle: "",
		},
		{
			name: "single_active_feature",
			data: []testWorkItem{
				{ID: "feat-456", Type: "feature", Status: "in-progress", Title: "Active Feature"},
			},
			expectID:    "feat-456",
			expectTitle: "Active Feature",
		},
		{
			name: "single_active_bug",
			data: []testWorkItem{
				{ID: "bug-789", Type: "bug", Status: "in-progress", Title: "Critical Bug"},
			},
			expectID:    "bug-789",
			expectTitle: "Critical Bug",
		},
		{
			name: "bug_prioritized_over_feature",
			data: []testWorkItem{
				{ID: "feat-111", Type: "feature", Status: "in-progress", Title: "Feature"},
				{ID: "bug-222", Type: "bug", Status: "in-progress", Title: "Bug Fix"},
			},
			expectID:    "bug-222",
			expectTitle: "Bug Fix",
		},
		{
			name: "truncates_long_title",
			data: []testWorkItem{
				{ID: "feat-333", Type: "feature", Status: "in-progress", Title: "This is a very long feature title that should be truncated"},
			},
			expectID:    "feat-333",
			expectTitle: "This is a very long feat…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up previous test data
			db.Exec("DELETE FROM features")

			// Insert test data
			for _, item := range tt.data {
				_, err := db.Exec(`
					INSERT INTO features (id, type, title, status)
					VALUES (?, ?, ?, ?)
				`, item.ID, item.Type, item.Title, item.Status)
				if err != nil {
					t.Fatalf("failed to insert test data: %v", err)
				}
			}

			// Query for active item
			var workItemID, title string
			err := db.QueryRow(`
				SELECT id, title
				FROM features
				WHERE status = 'in-progress'
				ORDER BY CASE type WHEN 'bug' THEN 0 WHEN 'feature' THEN 1 ELSE 2 END
				LIMIT 1
			`).Scan(&workItemID, &title)

			if tt.expectID == "" {
				if err != sql.ErrNoRows {
					t.Errorf("expected no rows, got %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("query failed: %v", err)
			}

			if workItemID != tt.expectID {
				t.Errorf("expected ID %q, got %q", tt.expectID, workItemID)
			}

			truncatedTitle := truncate(title, 25)
			if truncatedTitle != tt.expectTitle {
				t.Errorf("expected truncated title %q, got %q", tt.expectTitle, truncatedTitle)
			}
		})
	}
}

type testWorkItem struct {
	ID     string
	Type   string
	Status string
	Title  string
}

// --- WriteStatuslineCache tests ---

func TestWriteStatuslineCache_WritesFile(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("WIPNOTE_CACHE_DIR", cacheDir)

	// Set up a minimal .wipnote with a feature.
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("feature", "Cache Test Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	WriteStatuslineCache(hgDir, featNode.ID)

	cachePath := statuslineCachePath(hgDir)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if len(data) == 0 {
		t.Error("cache file should not be empty after write")
	}
}

func TestWriteStatuslineCache_ClearsOnComplete(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("WIPNOTE_CACHE_DIR", cacheDir)

	hgDir := filepath.Join(t.TempDir(), ".wipnote")
	cachePath := statuslineCachePath(hgDir)
	os.WriteFile(cachePath, []byte("old data"), 0o644)

	// Clear cache (empty featureID = complete)
	WriteStatuslineCache(hgDir, "")

	data, _ := os.ReadFile(cachePath)
	if len(data) != 0 {
		t.Errorf("cache should be empty after clear, got %q", data)
	}
}

func TestReadStatuslineCache_ReadsFile(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("WIPNOTE_CACHE_DIR", cacheDir)

	hgDir := filepath.Join(t.TempDir(), ".wipnote")
	// Write to the project-scoped path.
	cachePath := statuslineCachePath(hgDir)
	os.WriteFile(cachePath, []byte("test content"), 0o644)

	got := ReadStatuslineCache(hgDir)
	if got != "test content" {
		t.Errorf("expected 'test content', got %q", got)
	}
}

func TestReadStatuslineCache_EmptyOnMissing(t *testing.T) {
	t.Setenv("WIPNOTE_CACHE_DIR", t.TempDir())

	got := ReadStatuslineCache(filepath.Join(t.TempDir(), "nonexistent", ".wipnote"))
	if got != "" {
		t.Errorf("expected empty for missing cache, got %q", got)
	}
}

// TestRunStatuslineEmptySession verifies that runStatusline with an empty session ID
// returns nil and produces no output, even when in-progress work items exist on disk.
// This prevents cross-session state leakage (bug-33476dbf).
func TestRunStatuslineEmptySession(t *testing.T) {
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	// Create the standard subdirectories so workitem.Open succeeds if it were called.
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Write a synthetic in-progress feature HTML so statuslineFromHTML *would* return
	// output if it were invoked.
	featureHTML := `<!DOCTYPE html><html><head>
<meta name="id" content="feat-leak1">
<meta name="type" content="feature">
<meta name="status" content="in-progress">
<meta name="title" content="Leaked Feature">
</head><body></body></html>`
	featurePath := filepath.Join(wipnoteDir, "features", "feat-leak1.html")
	if err := os.WriteFile(featurePath, []byte(featureHTML), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}

	// Point the project directory at tmpDir so findWipnoteDir can resolve it.
	oldProjectDir := projectDirFlag
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = oldProjectDir }()

	// Capture stdout to ensure nothing is printed.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	gotErr := runStatusline("") // empty session

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if gotErr != nil {
		t.Errorf("runStatusline(\"\") returned error: %v", gotErr)
	}
	if output != "" {
		t.Errorf("runStatusline(\"\") should produce no output, got %q", output)
	}
}

func TestStatuslineCacheProjectIsolation(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("WIPNOTE_CACHE_DIR", cacheDir)

	dirA := filepath.Join(t.TempDir(), "project-a", ".wipnote")
	dirB := filepath.Join(t.TempDir(), "project-b", ".wipnote")

	// Write cache for project A.
	pathA := statuslineCachePath(dirA)
	os.WriteFile(pathA, []byte("project A item"), 0o644)

	// Project B should not see project A's cache.
	got := ReadStatuslineCache(dirB)
	if got != "" {
		t.Errorf("project B should not see project A cache, got %q", got)
	}

	// Project A should see its own cache.
	got = ReadStatuslineCache(dirA)
	if got != "project A item" {
		t.Errorf("project A should see its own cache, got %q", got)
	}
}
