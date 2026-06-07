package workitem_test

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/workitem"
)

// ---------------------------------------------------------------------------
// Claim / Release / AtomicClaim / Unclaim
// ---------------------------------------------------------------------------

func TestClaimReleaseCycle(t *testing.T) {
	p := newTestProject(t)
	feat, err := p.Features.Create("Claim Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Claim
	if err := p.Features.Claim(feat.ID, "sess-001"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	got, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("Get after claim: %v", err)
	}
	assertEqual(t, "AgentAssigned", got.AgentAssigned, "test-agent")
	assertEqual(t, "ClaimedBySession", got.ClaimedBySession, "sess-001")
	if got.ClaimedAt == "" {
		t.Error("ClaimedAt should be set after Claim")
	}

	// Release
	if err := p.Features.Release(feat.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	got, err = p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	assertEqual(t, "AgentAssigned after release", got.AgentAssigned, "")
	assertEqual(t, "ClaimedBySession after release", got.ClaimedBySession, "")
	assertEqual(t, "ClaimedAt after release", got.ClaimedAt, "")
}

func TestAtomicClaimSuccess(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Atomic Claim Test")

	// First claim should succeed
	if err := p.Features.AtomicClaim(feat.ID, "sess-001"); err != nil {
		t.Fatalf("AtomicClaim: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	assertEqual(t, "AgentAssigned", got.AgentAssigned, "test-agent")
	assertEqual(t, "ClaimedBySession", got.ClaimedBySession, "sess-001")
}

func TestAtomicClaimSameAgentSameSession(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Atomic Reclaim Test")

	// Claim once
	if err := p.Features.AtomicClaim(feat.ID, "sess-001"); err != nil {
		t.Fatalf("first claim should succeed: %v", err)
	}

	// Second claim by same session should fail (claim is exclusive)
	if err := p.Features.AtomicClaim(feat.ID, "sess-001"); err == nil {
		t.Error("expected error when reclaiming with same session")
	}
}

func TestAtomicClaimDifferentSessionFails(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Atomic Conflict Test")

	// Claim with session-001
	_ = p.Features.AtomicClaim(feat.ID, "sess-001")

	// Different session should fail
	err := p.Features.AtomicClaim(feat.ID, "sess-002")
	if err == nil {
		t.Error("expected error when claiming with different session")
	}
	if !strings.Contains(err.Error(), "already claimed") {
		t.Errorf("error should mention 'already claimed': %v", err)
	}
}

func TestAtomicClaimDifferentAgentFails(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Agent Conflict Test")

	// Atomically claim with test-agent
	if err := p.Features.AtomicClaim(feat.ID, "sess-001"); err != nil {
		t.Fatalf("first atomic claim should succeed: %v", err)
	}

	// Create second Project with different agent, same project dir
	p2, err := workitem.Open(p.ProjectDir, "other-agent")
	if err != nil {
		t.Fatalf("workitem.Open for other-agent: %v", err)
	}
	defer p2.Close()

	// Different agent should fail
	err = p2.Features.AtomicClaim(feat.ID, "sess-002")
	if err == nil {
		t.Error("expected error when claiming with different agent")
	} else if !strings.Contains(err.Error(), "already claimed") {
		t.Errorf("error should mention 'already claimed': %v", err)
	}
}

func TestUnclaim(t *testing.T) {
	p := newTestProject(t)
	feat, _ := p.Features.Create("Unclaim Test")

	// Claim then unclaim
	_ = p.Features.Claim(feat.ID, "sess-001")

	if err := p.Features.Unclaim(feat.ID); err != nil {
		t.Fatalf("Unclaim: %v", err)
	}

	got, _ := p.Features.Get(feat.ID)
	// Unclaim preserves AgentAssigned but clears claim metadata
	assertEqual(t, "AgentAssigned preserved", got.AgentAssigned, "test-agent")
	assertEqual(t, "ClaimedBySession cleared", got.ClaimedBySession, "")
	assertEqual(t, "ClaimedAt cleared", got.ClaimedAt, "")
}

func TestClaimNonexistentFails(t *testing.T) {
	p := newTestProject(t)

	if err := p.Features.Claim("feat-nonexistent", "sess-001"); err == nil {
		t.Error("expected error claiming nonexistent feature")
	}
}

func TestClaimOnBugs(t *testing.T) {
	p := newTestProject(t)
	bug, _ := p.Bugs.Create("Bug Claim Test")

	if err := p.Bugs.Claim(bug.ID, "sess-001"); err != nil {
		t.Fatalf("Claim bug: %v", err)
	}

	got, _ := p.Bugs.Get(bug.ID)
	assertEqual(t, "Bug AgentAssigned", got.AgentAssigned, "test-agent")
	assertEqual(t, "Bug ClaimedBySession", got.ClaimedBySession, "sess-001")
}

func TestClaimOnSpikes(t *testing.T) {
	p := newTestProject(t)
	spike, _ := p.Spikes.Create("Spike Claim Test")

	if err := p.Spikes.Claim(spike.ID, "sess-001"); err != nil {
		t.Fatalf("Claim spike: %v", err)
	}

	got, _ := p.Spikes.Get(spike.ID)
	assertEqual(t, "Spike AgentAssigned", got.AgentAssigned, "test-agent")
}

// ---------------------------------------------------------------------------
// GetActiveWorkItem
// ---------------------------------------------------------------------------

func TestGetActiveWorkItemNoActive(t *testing.T) {
	p := newTestProject(t)

	// Create items but leave them in todo status
	_, _ = p.Features.Create("Todo Feature")
	_, _ = p.Bugs.Create("Todo Bug")

	item, err := workitem.GetActiveWorkItem(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetActiveWorkItem: %v", err)
	}
	if item != nil {
		t.Errorf("expected nil, got %+v", item)
	}
}

func TestGetActiveWorkItemOneActive(t *testing.T) {
	p := newTestProject(t)

	feat, _ := p.Features.Create("Active Feature")
	_, _ = p.Features.Start(feat.ID)

	item, err := workitem.GetActiveWorkItem(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetActiveWorkItem: %v", err)
	}
	if item == nil {
		t.Fatal("expected active work item, got nil")
	}
	assertEqual(t, "WorkItem.ID", item.ID, feat.ID)
	assertEqual(t, "WorkItem.Type", item.Type, "feature")
	assertEqual(t, "WorkItem.Title", item.Title, "Active Feature")
	assertEqual(t, "WorkItem.Status", item.Status, "in-progress")
}

func TestGetActiveWorkItemPrefersFeatures(t *testing.T) {
	p := newTestProject(t)

	// Start a feature and a bug
	feat, _ := p.Features.Create("Active Feature")
	bug, _ := p.Bugs.Create("Active Bug")
	_, _ = p.Features.Start(feat.ID)
	_, _ = p.Bugs.Start(bug.ID)

	// Features are scanned first, so the feature should be returned
	item, err := workitem.GetActiveWorkItem(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetActiveWorkItem: %v", err)
	}
	if item == nil {
		t.Fatal("expected active work item, got nil")
	}
	assertEqual(t, "WorkItem.Type", item.Type, "feature")
}

func TestGetActiveWorkItemFindsBug(t *testing.T) {
	p := newTestProject(t)

	// Only a bug is active
	bug, _ := p.Bugs.Create("Active Bug")
	_, _ = p.Bugs.Start(bug.ID)

	item, err := workitem.GetActiveWorkItem(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetActiveWorkItem: %v", err)
	}
	if item == nil {
		t.Fatal("expected active work item, got nil")
	}
	assertEqual(t, "WorkItem.Type", item.Type, "bug")
	assertEqual(t, "WorkItem.ID", item.ID, bug.ID)
}

func TestGetActiveWorkItemFindsSpike(t *testing.T) {
	p := newTestProject(t)

	// Only a spike is active
	spike, _ := p.Spikes.Create("Active Spike")
	_, _ = p.Spikes.Start(spike.ID)

	item, err := workitem.GetActiveWorkItem(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetActiveWorkItem: %v", err)
	}
	if item == nil {
		t.Fatal("expected active work item, got nil")
	}
	assertEqual(t, "WorkItem.Type", item.Type, "spike")
	assertEqual(t, "WorkItem.ID", item.ID, spike.ID)
}

func TestGetActiveWorkItemEmptyProject(t *testing.T) {
	p := newTestProject(t)

	item, err := workitem.GetActiveWorkItem(p.ProjectDir)
	if err != nil {
		t.Fatalf("GetActiveWorkItem: %v", err)
	}
	if item != nil {
		t.Errorf("expected nil for empty project, got %+v", item)
	}
}
