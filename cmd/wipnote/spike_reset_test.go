package main

import (
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

func TestSpikeReset_HappyPath(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	if err := testCreate("spike", "Reset Spike", "", "low", false, false); err != nil {
		t.Fatalf("create spike: %v", err)
	}
	spikeFiles, _ := filepath.Glob(filepath.Join(hgDir, "spikes", "spk-*.html"))
	if len(spikeFiles) != 1 {
		t.Fatalf("expected 1 spike file, got %d", len(spikeFiles))
	}
	node, _ := htmlparse.ParseFile(spikeFiles[0])
	spikeID := node.ID

	if err := runWiSetStatus("spike", spikeID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}

	title, err := executeReset("spike", spikeID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if title != "Reset Spike" {
		t.Errorf("title: want %q, got %q", "Reset Spike", title)
	}

	node, _ = htmlparse.ParseFile(spikeFiles[0])
	if node.Status != models.StatusTodo {
		t.Errorf("status: want todo, got %q", node.Status)
	}
	if node.AgentAssigned != "" {
		t.Errorf("AgentAssigned: want empty, got %q", node.AgentAssigned)
	}
}

func TestSpikeReset_ErrorOnTodo(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	if err := testCreate("spike", "Todo Spike", "", "medium", false, false); err != nil {
		t.Fatalf("create spike: %v", err)
	}
	spikeFiles, _ := filepath.Glob(filepath.Join(hgDir, "spikes", "spk-*.html"))
	node, _ := htmlparse.ParseFile(spikeFiles[0])

	_, err := executeReset("spike", node.ID)
	if err == nil {
		t.Fatal("expected error when resetting todo spike, got nil")
	}
	if !stringContains(err.Error(), "not in-progress") {
		t.Errorf("error should mention 'not in-progress': %v", err)
	}
}

func TestSpikeReset_ErrorOnDone(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	if err := testCreate("spike", "Done Spike", "", "medium", false, false); err != nil {
		t.Fatalf("create spike: %v", err)
	}
	spikeFiles, _ := filepath.Glob(filepath.Join(hgDir, "spikes", "spk-*.html"))
	node, _ := htmlparse.ParseFile(spikeFiles[0])
	spikeID := node.ID

	if err := runWiSetStatus("spike", spikeID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := runWiSetStatus("spike", spikeID, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	_, err := executeReset("spike", spikeID)
	if err == nil {
		t.Fatal("expected error when resetting done spike, got nil")
	}
	if !stringContains(err.Error(), "not in-progress") {
		t.Errorf("error should mention 'not in-progress': %v", err)
	}
}

func TestSpikeReset_PreservesDescription(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	if err := testCreate("spike", "Spike With Desc", "", "low", false, false); err != nil {
		t.Fatalf("create spike: %v", err)
	}
	spikeFiles, _ := filepath.Glob(filepath.Join(hgDir, "spikes", "spk-*.html"))
	node, _ := htmlparse.ParseFile(spikeFiles[0])
	spikeID := node.ID

	if err := runSetDescription("spike", spikeID, "spike investigation notes", "", "", "", false); err != nil {
		t.Fatalf("set description: %v", err)
	}

	if err := runWiSetStatus("spike", spikeID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := executeReset("spike", spikeID); err != nil {
		t.Fatalf("reset: %v", err)
	}

	node, _ = htmlparse.ParseFile(spikeFiles[0])
	if !stringContains(node.Content, "spike investigation notes") {
		t.Errorf("description not preserved: got %q", node.Content)
	}
}
