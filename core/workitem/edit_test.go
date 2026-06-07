package workitem_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// ---------------------------------------------------------------------------
// EditBuilder
// ---------------------------------------------------------------------------

func TestEditSetStatus(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Edit Status Test")

	err := p.Features.Edit(feat.ID).
		SetStatus("in-progress").
		Save()
	if err != nil {
		t.Fatalf("Edit.SetStatus.Save: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	assertEqual(t, "Status", string(got.Status), "in-progress")
}

func TestEditSetDescription(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Edit Desc Test")

	// Content must be in an element (e.g. <p>) to survive HTML round-trip,
	// because the parser reads child elements, not raw text nodes.
	err := p.Features.Edit(feat.ID).
		SetDescription("<p>New description body</p>").
		Save()
	if err != nil {
		t.Fatalf("Edit.SetDescription.Save: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	if !strings.Contains(got.Content, "New description body") {
		t.Errorf("Content: got %q", got.Content)
	}
}

func TestEditSetFindings(t *testing.T) {
	p := newTestProject(t)
	spike, _ := p.Spikes.Create("Edit Findings Test")

	err := p.Spikes.Edit(spike.ID).
		SetFindings("Investigation complete: use Redis").
		Save()
	if err != nil {
		t.Fatalf("Edit.SetFindings.Save: %v", err)
	}

	got, _ := p.Spikes.Get(spike.ID)
	if !strings.Contains(got.Content, "Investigation complete: use Redis") {
		t.Errorf("Content: got %q", got.Content)
	}
}

func TestEditAddNote(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Edit Note Test",
		workitem.FeatWithContent("<p>Original content</p>"),
	)

	err := p.Features.Edit(feat.ID).
		AddNote("First note").
		AddNote("Second note").
		Save()
	if err != nil {
		t.Fatalf("Edit.AddNote.Save: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	if !strings.Contains(got.Content, "First note") {
		t.Errorf("Content missing first note: %q", got.Content)
	}
	if !strings.Contains(got.Content, "Second note") {
		t.Errorf("Content missing second note: %q", got.Content)
	}
	if !strings.Contains(got.Content, "Original content") {
		t.Errorf("Content missing original: %q", got.Content)
	}
}

func TestEditAddNoteAfterContentSettersRegardlessOfChainOrder(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Edit Note Order Test")

	err := p.Features.Edit(feat.ID).
		AddNote("Keep this note").
		SetDescription("<p>Replacement body</p>").
		Save()
	if err != nil {
		t.Fatalf("Edit.Save: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	if !strings.Contains(got.Content, "Replacement body") {
		t.Errorf("Content missing replacement body: %q", got.Content)
	}
	if !strings.Contains(got.Content, "Keep this note") {
		t.Errorf("Content missing note: %q", got.Content)
	}
}

func TestEditChainMultipleFields(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Multi Edit Test")

	err := p.Features.Edit(feat.ID).
		SetStatus("in-progress").
		SetPriority("critical").
		SetAgent("new-agent").
		SetTrack("trk-new").
		AddNote("Started work").
		Save()
	if err != nil {
		t.Fatalf("Edit chain Save: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	assertEqual(t, "Status", string(got.Status), "in-progress")
	assertEqual(t, "Priority", string(got.Priority), "critical")
	assertEqual(t, "AgentAssigned", got.AgentAssigned, "new-agent")
	assertEqual(t, "TrackID", got.TrackID, "trk-new")
	if !strings.Contains(got.Content, "Started work") {
		t.Errorf("Content missing note: %q", got.Content)
	}
}

func TestEditNonexistentNode(t *testing.T) {
	p := newTestProject(t)

	err := p.Features.Edit("feat-nonexistent").
		SetStatus("done").
		Save()
	if err == nil {
		t.Error("expected error editing nonexistent node")
	}
}

func TestEditDeferredError(t *testing.T) {
	p := newTestProject(t)

	// All chained methods should be no-ops when initial load fails
	err := p.Features.Edit("feat-nonexistent").
		SetStatus("done").
		SetPriority("high").
		AddNote("note").
		SetDescription("desc").
		SetFindings("findings").
		SetAgent("agent").
		SetTrack("trk-x").
		Save()
	if err == nil {
		t.Error("expected error from deferred load failure")
	}
}

func TestEditOnBug(t *testing.T) {
	p := newTestProject(t)
	bug, _ := p.Bugs.Create("Bug Edit Test")

	err := p.Bugs.Edit(bug.ID).
		SetStatus("in-progress").
		AddNote("Investigating").
		Save()
	if err != nil {
		t.Fatalf("Edit bug: %v", err)
	}

	got, _ := p.Bugs.Get(bug.ID)
	assertEqual(t, "Bug status", string(got.Status), "in-progress")
	if !strings.Contains(got.Content, "Investigating") {
		t.Errorf("Bug content: %q", got.Content)
	}
}

// ---------------------------------------------------------------------------
// AddNote (standalone Collection method)
// ---------------------------------------------------------------------------

func TestAddNote(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Note Test",
		workitem.FeatWithContent("<p>Initial</p>"),
	)

	if err := p.Features.AddNote(feat.ID, "First observation"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	if !strings.Contains(got.Content, "First observation") {
		t.Errorf("Content missing note: %q", got.Content)
	}
	if !strings.Contains(got.Content, "Initial") {
		t.Errorf("Content lost original: %q", got.Content)
	}
	// Note should include agent name
	if !strings.Contains(got.Content, "test-agent") {
		t.Errorf("Note missing agent: %q", got.Content)
	}
}

func TestAddNoteAppendsMultiple(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Multi Note Test")

	_ = p.Features.AddNote(feat.ID, "Note one")
	_ = p.Features.AddNote(feat.ID, "Note two")
	_ = p.Features.AddNote(feat.ID, "Note three")

	got, _ := p.Features.Get(feat.ID)
	if !strings.Contains(got.Content, "Note one") {
		t.Errorf("Missing note one: %q", got.Content)
	}
	if !strings.Contains(got.Content, "Note two") {
		t.Errorf("Missing note two: %q", got.Content)
	}
	if !strings.Contains(got.Content, "Note three") {
		t.Errorf("Missing note three: %q", got.Content)
	}
}

func TestAddNoteNonexistent(t *testing.T) {
	p := newTestProject(t)
	err := p.Features.AddNote("feat-nonexistent", "note")
	if err == nil {
		t.Error("expected error for nonexistent feature")
	}
}

func TestAddNoteOnBug(t *testing.T) {
	p := newTestProject(t)
	bug, _ := p.Bugs.Create("Bug Note Test")

	if err := p.Bugs.AddNote(bug.ID, "Bug observation"); err != nil {
		t.Fatalf("AddNote bug: %v", err)
	}

	got, _ := p.Bugs.Get(bug.ID)
	if !strings.Contains(got.Content, "Bug observation") {
		t.Errorf("Bug content: %q", got.Content)
	}
}

func TestAddNoteOnSpike(t *testing.T) {
	p := newTestProject(t)
	spike, _ := p.Spikes.Create("Spike Note Test")

	if err := p.Spikes.AddNote(spike.ID, "Spike finding"); err != nil {
		t.Fatalf("AddNote spike: %v", err)
	}

	got, _ := p.Spikes.Get(spike.ID)
	if !strings.Contains(got.Content, "Spike finding") {
		t.Errorf("Spike content: %q", got.Content)
	}
}

// ---------------------------------------------------------------------------
// SetFindings (standalone Collection method)
// ---------------------------------------------------------------------------

func TestSetFindingsReplacesContent(t *testing.T) {
	p := newTestProject(t)
	spike, _ := p.Spikes.Create("Findings Test",
		workitem.SpikeWithFindings("Old findings"),
	)

	if _, err := p.Spikes.SetFindings(spike.ID, "New findings replace old"); err != nil {
		t.Fatalf("SetFindings: %v", err)
	}

	got, _ := p.Spikes.Get(spike.ID)
	if !strings.Contains(got.Content, "New findings replace old") {
		t.Errorf("Content missing new findings: %q", got.Content)
	}
	// Old findings should be gone (replaced, not appended)
	if strings.Contains(got.Content, "Old findings") {
		t.Errorf("Content still has old findings: %q", got.Content)
	}
}

func TestSetFindingsOnFeature(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Feature Findings Test")

	if err := p.Features.SetFindings(feat.ID, "Feature analysis complete"); err != nil {
		t.Fatalf("SetFindings: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	if !strings.Contains(got.Content, "Feature analysis complete") {
		t.Errorf("Content: %q", got.Content)
	}
}

func TestSetFindingsNonexistent(t *testing.T) {
	p := newTestProject(t)
	_, err := p.Spikes.SetFindings("spk-nonexistent", "findings")
	if err == nil {
		t.Error("expected error for nonexistent spike")
	}
}

// ---------------------------------------------------------------------------
// Claim round-trip with HTML parsing
// ---------------------------------------------------------------------------

func TestClaimRoundTrip(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Claim RT Test")

	_ = p.Features.Claim(feat.ID, "sess-rt-001")

	// Parse the HTML file directly
	path := filepath.Join(p.FeaturesDir(), feat.ID+".html")
	parsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	assertEqual(t, "parsed.AgentAssigned", parsed.AgentAssigned, "test-agent")
	assertEqual(t, "parsed.ClaimedBySession", parsed.ClaimedBySession, "sess-rt-001")
	if parsed.ClaimedAt == "" {
		t.Error("parsed.ClaimedAt should be set")
	}
}

// ---------------------------------------------------------------------------
// Edit round-trip with HTML parsing
// ---------------------------------------------------------------------------

func TestEditRoundTrip(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Edit RT Test")

	_ = p.Features.Edit(feat.ID).
		SetStatus("in-progress").
		SetPriority("high").
		AddNote("Implementation started").
		Save()

	path := filepath.Join(p.FeaturesDir(), feat.ID+".html")
	parsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	assertEqual(t, "parsed.Status", string(parsed.Status), "in-progress")
	assertEqual(t, "parsed.Priority", string(parsed.Priority), "high")
	if !strings.Contains(parsed.Content, "Implementation started") {
		t.Errorf("parsed.Content missing note: %q", parsed.Content)
	}
}

// ---------------------------------------------------------------------------
// Collection AddEdge / RemoveEdge
// ---------------------------------------------------------------------------

func TestCollectionAddEdge(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Edge Source")
	target, _ := p.Features.Create("Edge Target")

	updated, err := p.Features.AddEdge(feat.ID, models.Edge{
		TargetID:     target.ID,
		Relationship: models.RelBlocks,
		Title:        target.Title,
	})
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	edges := updated.Edges[string(models.RelBlocks)]
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	assertEqual(t, "edge target", edges[0].TargetID, target.ID)

	// Re-read from disk to confirm persistence.
	reread, _ := p.Features.Get(feat.ID)
	if len(reread.Edges[string(models.RelBlocks)]) != 1 {
		t.Error("edge not persisted to disk")
	}
}

func TestCollectionRemoveEdge(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Edge Source")

	// Add two edges, remove one.
	_, _ = p.Features.AddEdge(feat.ID, models.Edge{
		TargetID: "feat-a", Relationship: models.RelBlocks,
	})
	_, _ = p.Features.AddEdge(feat.ID, models.Edge{
		TargetID: "feat-b", Relationship: models.RelBlocks,
	})

	updated, removed, err := p.Features.RemoveEdge(feat.ID, "feat-a", models.RelBlocks)
	if err != nil {
		t.Fatalf("RemoveEdge: %v", err)
	}
	if !removed {
		t.Error("expected removed=true")
	}
	if len(updated.Edges[string(models.RelBlocks)]) != 1 {
		t.Error("expected 1 remaining edge")
	}

	// Verify disk.
	reread, _ := p.Features.Get(feat.ID)
	if len(reread.Edges[string(models.RelBlocks)]) != 1 {
		t.Error("removal not persisted")
	}

	// Remove non-existent.
	_, removed, err = p.Features.RemoveEdge(feat.ID, "feat-zzz", models.RelBlocks)
	if err != nil {
		t.Fatalf("RemoveEdge nonexistent: %v", err)
	}
	if removed {
		t.Error("expected removed=false for non-existent edge")
	}
}
