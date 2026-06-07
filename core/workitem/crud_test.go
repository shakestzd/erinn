package workitem_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/workitem"
)

// ---------------------------------------------------------------------------
// Feature CRUD
// ---------------------------------------------------------------------------

func TestFeatureCreate(t *testing.T) {
	p := newTestProject(t)

	feat, err := p.Features.Create("User Authentication",
		workitem.FeatWithPriority("high"),
		workitem.FeatWithTrack("trk-test"),
		workitem.FeatWithSteps("Design schema", "Implement API", "Add tests"),
		workitem.FeatWithContent("<p>Auth feature for multi-tenant</p>"),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify returned node
	if !strings.HasPrefix(feat.ID, "feat-") {
		t.Errorf("ID prefix: got %q, want feat-*", feat.ID)
	}
	if feat.Title != "User Authentication" {
		t.Errorf("Title: got %q", feat.Title)
	}
	if feat.Type != "feature" {
		t.Errorf("Type: got %q", feat.Type)
	}
	if string(feat.Priority) != "high" {
		t.Errorf("Priority: got %q", feat.Priority)
	}
	if string(feat.Status) != "todo" {
		t.Errorf("Status: got %q", feat.Status)
	}
	if feat.TrackID != "trk-test" {
		t.Errorf("TrackID: got %q", feat.TrackID)
	}
	if feat.AgentAssigned != "test-agent" {
		t.Errorf("AgentAssigned: got %q", feat.AgentAssigned)
	}
	if len(feat.Steps) != 3 {
		t.Fatalf("Steps count: got %d, want 3", len(feat.Steps))
	}
	if feat.Steps[0].Description != "Design schema" {
		t.Errorf("Step[0]: got %q", feat.Steps[0].Description)
	}

	// Verify HTML file exists on disk
	htmlPath := filepath.Join(p.FeaturesDir(), feat.ID+".html")
	if _, err := os.Stat(htmlPath); err != nil {
		t.Fatalf("HTML file not found: %v", err)
	}
}

func TestFeatureCreateEmptyTitle(t *testing.T) {
	p := newTestProject(t)
	_, err := p.Features.Create("")
	if err == nil {
		t.Error("expected error for empty title")
	}
}

func TestFeatureGet(t *testing.T) {
	p := newTestProject(t)

	created, err := p.Features.Create("Get Test Feature",
		workitem.FeatWithPriority("low"),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := p.Features.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != created.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, created.ID)
	}
	if got.Title != "Get Test Feature" {
		t.Errorf("Title: got %q", got.Title)
	}
	if string(got.Priority) != "low" {
		t.Errorf("Priority: got %q", got.Priority)
	}
}

func TestFeatureList(t *testing.T) {
	p := newTestProject(t)

	_, _ = p.Features.Create("Feat A", workitem.FeatWithPriority("high"))
	_, _ = p.Features.Create("Feat B", workitem.FeatWithPriority("low"))
	_, _ = p.Features.Create("Feat C", workitem.FeatWithPriority("high"))

	// List all
	all, err := p.Features.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List all: got %d, want 3", len(all))
	}

	// Filter by priority
	high, err := p.Features.List(workitem.WithPriority("high"))
	if err != nil {
		t.Fatalf("List high: %v", err)
	}
	if len(high) != 2 {
		t.Errorf("List high: got %d, want 2", len(high))
	}
}

func TestFeatureListWithStatus(t *testing.T) {
	p := newTestProject(t)

	f1, _ := p.Features.Create("Active Feature")
	_, _ = p.Features.Create("Todo Feature")
	_, _ = p.Features.Start(f1.ID)

	inProg, err := p.Features.List(workitem.WithStatus("in-progress"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(inProg) != 1 {
		t.Errorf("in-progress count: got %d, want 1", len(inProg))
	}
}

func TestFeatureDelete(t *testing.T) {
	p := newTestProject(t)

	feat, _ := p.Features.Create("Delete Me")
	if err := p.Features.Delete(feat.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := p.Features.Get(feat.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

// ---------------------------------------------------------------------------
// Feature Lifecycle
// ---------------------------------------------------------------------------

func TestFeatureStartComplete(t *testing.T) {
	p := newTestProject(t)

	feat, _ := p.Features.Create("Lifecycle Test",
		workitem.FeatWithSteps("Step 1", "Step 2"),
	)

	// Start
	started, err := p.Features.Start(feat.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if string(started.Status) != "in-progress" {
		t.Errorf("after Start: status = %q", started.Status)
	}

	// Complete
	done, err := p.Features.Complete(feat.ID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if string(done.Status) != "done" {
		t.Errorf("after Complete: status = %q", done.Status)
	}
	for i, step := range done.Steps {
		if !step.Completed {
			t.Errorf("step %d not completed after Complete", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Round-trip: create -> write HTML -> parse -> verify
// ---------------------------------------------------------------------------

func TestFeatureRoundTrip(t *testing.T) {
	p := newTestProject(t)

	feat, err := p.Features.Create("Round Trip Feature",
		workitem.FeatWithPriority("critical"),
		workitem.FeatWithTrack("trk-roundtrip"),
		workitem.FeatWithSteps("Alpha", "Beta"),
		workitem.FeatWithContent("<p>Round trip test</p>"),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Re-parse the HTML file with the internal parser
	path := filepath.Join(p.FeaturesDir(), feat.ID+".html")
	parsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	assertEqual(t, "ID", parsed.ID, feat.ID)
	assertEqual(t, "Title", parsed.Title, "Round Trip Feature")
	assertEqual(t, "Type", parsed.Type, "feature")
	assertEqual(t, "Status", string(parsed.Status), "todo")
	assertEqual(t, "Priority", string(parsed.Priority), "critical")
	assertEqual(t, "TrackID", parsed.TrackID, "trk-roundtrip")
	assertEqual(t, "AgentAssigned", parsed.AgentAssigned, "test-agent")

	if len(parsed.Steps) != 2 {
		t.Fatalf("Steps count: got %d, want 2", len(parsed.Steps))
	}
	assertEqual(t, "Step[0]", parsed.Steps[0].Description, "Alpha")
	assertEqual(t, "Step[1]", parsed.Steps[1].Description, "Beta")

	if !strings.Contains(parsed.Content, "Round trip test") {
		t.Errorf("Content missing expected text: %q", parsed.Content)
	}
}

func TestFeatureWithEdgesRoundTrip(t *testing.T) {
	p := newTestProject(t)

	feat, err := p.Features.Create("Edge Feature",
		workitem.FeatWithEdge("blocks", "feat-other"),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	path := filepath.Join(p.FeaturesDir(), feat.ID+".html")
	parsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	edges, ok := parsed.Edges["blocks"]
	if !ok || len(edges) == 0 {
		t.Fatal("no 'blocks' edges found after round-trip")
	}
	assertEqual(t, "edge target", edges[0].TargetID, "feat-other")
}

// ---------------------------------------------------------------------------
// Bug CRUD
// ---------------------------------------------------------------------------

func TestBugCreate(t *testing.T) {
	p := newTestProject(t)

	bug, err := p.Bugs.Create("Login broken on Safari",
		workitem.BugWithPriority("critical"),
		workitem.BugWithReproSteps("Open Safari", "Click login"),
	)
	if err != nil {
		t.Fatalf("Create bug: %v", err)
	}
	if !strings.HasPrefix(bug.ID, "bug-") {
		t.Errorf("ID prefix: got %q, want bug-*", bug.ID)
	}
	if bug.Type != "bug" {
		t.Errorf("Type: got %q", bug.Type)
	}

	// Verify file
	path := filepath.Join(p.BugsDir(), bug.ID+".html")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("HTML file not found: %v", err)
	}
}

func TestBugRoundTrip(t *testing.T) {
	p := newTestProject(t)
	bug, _ := p.Bugs.Create("Bug RT", workitem.BugWithPriority("high"))

	parsed, err := htmlparse.ParseFile(filepath.Join(p.BugsDir(), bug.ID+".html"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	assertEqual(t, "Type", parsed.Type, "bug")
	assertEqual(t, "Priority", string(parsed.Priority), "high")
}

// ---------------------------------------------------------------------------
// Spike CRUD
// ---------------------------------------------------------------------------

func TestSpikeCreate(t *testing.T) {
	p := newTestProject(t)

	spike, err := p.Spikes.Create("Investigate caching",
		workitem.SpikeWithType("technical"),
		workitem.SpikeWithFindings("Redis is the best option"),
	)
	if err != nil {
		t.Fatalf("Create spike: %v", err)
	}
	if !strings.HasPrefix(spike.ID, "spk-") {
		t.Errorf("ID prefix: got %q, want spk-*", spike.ID)
	}
	if spike.Type != "spike" {
		t.Errorf("Type: got %q", spike.Type)
	}
}

func TestSpikeSetFindings(t *testing.T) {
	p := newTestProject(t)

	spike, _ := p.Spikes.Create("Investigation")
	updated, err := p.Spikes.SetFindings(spike.ID, "Found the root cause")
	if err != nil {
		t.Fatalf("SetFindings: %v", err)
	}
	if !strings.Contains(updated.Content, "Found the root cause") {
		t.Errorf("Content: got %q", updated.Content)
	}

	// Verify round-trip
	parsed, _ := htmlparse.ParseFile(filepath.Join(p.SpikesDir(), spike.ID+".html"))
	if !strings.Contains(parsed.Content, "Found the root cause") {
		t.Errorf("Parsed content missing findings: %q", parsed.Content)
	}
}

func TestSpikeGetLatest(t *testing.T) {
	p := newTestProject(t)

	_, _ = p.Spikes.Create("Spike A")
	_, _ = p.Spikes.Create("Spike B")
	_, _ = p.Spikes.Create("Spike C")

	latest, err := p.Spikes.GetLatest("", 2)
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if len(latest) != 2 {
		t.Errorf("GetLatest count: got %d, want 2", len(latest))
	}
}

// ---------------------------------------------------------------------------
// Track CRUD
// ---------------------------------------------------------------------------

func TestTrackCreate(t *testing.T) {
	p := newTestProject(t)

	track, err := p.Tracks.Create("Go SDK Port",
		workitem.TrackWithPriority("high"),
		workitem.TrackWithSpec("Port Python SDK to Go"),
		workitem.TrackWithPlanPhases("Phase 1: Models", "Phase 2: Collections"),
	)
	if err != nil {
		t.Fatalf("Create track: %v", err)
	}
	if !strings.HasPrefix(track.ID, "trk-") {
		t.Errorf("ID prefix: got %q, want trk-*", track.ID)
	}
	if len(track.Steps) != 2 {
		t.Errorf("Steps: got %d, want 2", len(track.Steps))
	}
}

func TestTrackRoundTrip(t *testing.T) {
	p := newTestProject(t)
	track, _ := p.Tracks.Create("Track RT", workitem.TrackWithPriority("medium"))

	parsed, err := htmlparse.ParseFile(filepath.Join(p.TracksDir(), track.ID+".html"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	assertEqual(t, "Type", parsed.Type, "track")
	assertEqual(t, "Title", parsed.Title, "Track RT")
}
