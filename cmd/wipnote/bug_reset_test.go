package main

import (
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

func TestBugReset_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item reset lifecycle")
	}

	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("bug", "Reset Bug", trackID, "high", false, true); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) != 1 {
		t.Fatalf("expected 1 bug file, got %d", len(bugFiles))
	}
	node, _ := htmlparse.ParseFile(bugFiles[0])
	bugID := node.ID

	if err := runWiSetStatus("bug", bugID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}

	title, err := executeReset("bug", bugID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if title != "Reset Bug" {
		t.Errorf("title: want %q, got %q", "Reset Bug", title)
	}

	node, _ = htmlparse.ParseFile(bugFiles[0])
	if node.Status != models.StatusTodo {
		t.Errorf("status: want todo, got %q", node.Status)
	}
	if node.AgentAssigned != "" {
		t.Errorf("AgentAssigned: want empty, got %q", node.AgentAssigned)
	}
}

func TestBugReset_ErrorOnTodo(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("bug", "Todo Bug", trackID, "medium", false, true); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	node, _ := htmlparse.ParseFile(bugFiles[0])

	_, err := executeReset("bug", node.ID)
	if err == nil {
		t.Fatal("expected error when resetting todo bug, got nil")
	}
	if !stringContains(err.Error(), "not in-progress") {
		t.Errorf("error should mention 'not in-progress': %v", err)
	}
}

func TestBugReset_ErrorOnDone(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item reset lifecycle")
	}

	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("bug", "Done Bug", trackID, "medium", false, true); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	node, _ := htmlparse.ParseFile(bugFiles[0])
	bugID := node.ID

	if err := runWiSetStatus("bug", bugID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := runWiSetStatus("bug", bugID, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	_, err := executeReset("bug", bugID)
	if err == nil {
		t.Fatal("expected error when resetting done bug, got nil")
	}
	if !stringContains(err.Error(), "not in-progress") {
		t.Errorf("error should mention 'not in-progress': %v", err)
	}
}

func TestBugReset_PreservesDescription(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item reset lifecycle")
	}

	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("bug", "Bug With Desc", trackID, "critical", false, true); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	node, _ := htmlparse.ParseFile(bugFiles[0])
	bugID := node.ID

	if err := runSetDescription("bug", bugID, "reproduction steps here", "", "", "", false); err != nil {
		t.Fatalf("set description: %v", err)
	}

	if err := runWiSetStatus("bug", bugID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := executeReset("bug", bugID); err != nil {
		t.Fatalf("reset: %v", err)
	}

	node, _ = htmlparse.ParseFile(bugFiles[0])
	if !stringContains(node.Content, "reproduction steps here") {
		t.Errorf("description not preserved: got %q", node.Content)
	}
}
