package db_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestGetCurrentSessionResumable_SurfacesSessionWithoutWorkItem proves the
// current-session lookup bypasses the work_item_id <> "" gate that the grouped
// listing applies: a session with no work-item attribution is still returned.
func TestGetCurrentSessionResumable_SurfacesSessionWithoutWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-current", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-current"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the current session even without a work item")
	}
	if got.LastSessionID != "sess-current" {
		t.Fatalf("LastSessionID = %q, want sess-current", got.LastSessionID)
	}
	if got.WorkItemID != "" {
		t.Fatalf("WorkItemID = %q, want empty", got.WorkItemID)
	}
	if got.Harness != "claude" {
		t.Fatalf("Harness = %q, want claude", got.Harness)
	}
}

// TestGetCurrentSessionResumable_SurfacesCompletedWorkItem proves the lookup
// bypasses the item_status NOT IN ('done','completed') filter: a session whose
// work item is completed is still surfaced as the current session.
func TestGetCurrentSessionResumable_SurfacesCompletedWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-done', 'feature', 'Done', 'done')`,
	); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness, active_feature_id)
		 VALUES (?, 'claude-code', ?, 'active', 'claude', 'feat-done')`,
		"sess-current", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-current"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the current session even with a completed work item")
	}
	if got.WorkItemID != "feat-done" {
		t.Fatalf("WorkItemID = %q, want feat-done", got.WorkItemID)
	}
	if got.Title != "Done" {
		t.Fatalf("Title = %q, want Done", got.Title)
	}
}

// TestGetCurrentSessionResumable_PicksMostRecentAmongIDs verifies that when
// several candidate session IDs (the family members) are passed, the most
// recently active one is chosen.
func TestGetCurrentSessionResumable_PicksMostRecentAmongIDs(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-stub", now.Add(-3*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert stub: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-child", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, 24*time.Hour, []string{"sess-stub", "sess-child"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the most recent family member")
	}
	if got.LastSessionID != "sess-child" {
		t.Fatalf("LastSessionID = %q, want sess-child (most recent)", got.LastSessionID)
	}
}

// TestGetCurrentSessionResumable_UsesRootAgentWorkItem verifies the awi join is
// scoped to the root agent: a session carrying both a subagent claim and a root
// claim resolves to the ROOT work item (and appears once), not an arbitrary
// subagent's item.
func TestGetCurrentSessionResumable_UsesRootAgentWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-cur", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	// A subagent claim and a root claim coexist on the same session.
	if err := db.SetActiveWorkItem(database, "sess-cur", "subagent-x", "feat-sub"); err != nil {
		t.Fatalf("set subagent claim: %v", err)
	}
	if err := db.SetActiveWorkItem(database, "sess-cur", db.AgentRootSentinel, "feat-root"); err != nil {
		t.Fatalf("set root claim: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-cur"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the current session")
	}
	if got.WorkItemID != "feat-root" {
		t.Fatalf("WorkItemID = %q, want feat-root (root-agent scoped)", got.WorkItemID)
	}
}

func TestGetCurrentSessionResumable_ClaimBeatsStaleRootAttribution(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "gemini", "antigravity"} {
		t.Run(harness, func(t *testing.T) {
			database := openIsolatedDB(t)

			now := time.Now().UTC()
			if _, err := database.Exec(
				`INSERT INTO features (id, type, title, status) VALUES
					('bug-stale-current', 'bug', 'Stale current label', 'in-progress'),
					('bug-fresh-current', 'bug', 'Fresh claimed label', 'in-progress')`,
			); err != nil {
				t.Fatalf("insert features: %v", err)
			}
			if _, err := database.Exec(
				`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness, active_feature_id)
				 VALUES (?, ?, ?, 'active', ?, 'bug-stale-current')`,
				"sess-cur", harness, now.Add(-time.Minute).Format(time.RFC3339), harness,
			); err != nil {
				t.Fatalf("insert session: %v", err)
			}
			if err := db.SetActiveWorkItem(database, "sess-cur", db.AgentRootSentinel, "bug-stale-current"); err != nil {
				t.Fatalf("set root stale claim: %v", err)
			}
			if err := db.ClaimItemOrRenew(database, &models.Claim{
				ClaimID:          "claim-fresh-current",
				WorkItemID:       "bug-fresh-current",
				OwnerSessionID:   "sess-cur",
				OwnerAgent:       harness,
				ClaimedByAgentID: harness,
				Status:           models.ClaimInProgress,
			}, time.Hour); err != nil {
				t.Fatalf("ClaimItemOrRenew: %v", err)
			}

			got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-cur"})
			if err != nil {
				t.Fatalf("GetCurrentSessionResumable: %v", err)
			}
			if got == nil {
				t.Fatal("got nil, want the current session")
			}
			if got.WorkItemID != "bug-fresh-current" {
				t.Fatalf("WorkItemID = %q, want bug-fresh-current", got.WorkItemID)
			}
			if got.Title != "Fresh claimed label" {
				t.Fatalf("Title = %q, want Fresh claimed label", got.Title)
			}
			if got.Harness != harness {
				t.Fatalf("Harness = %q, want %s", got.Harness, harness)
			}
		})
	}
}

func TestGetCurrentSessionResumable_UsesFamilyClaimForNewestUnclaimedSession(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "gemini", "antigravity"} {
		t.Run(harness, func(t *testing.T) {
			database := openIsolatedDB(t)

			now := time.Now().UTC()
			if _, err := database.Exec(
				`INSERT INTO features (id, type, title, status) VALUES
					('bug-family-current', 'bug', 'Family claimed label', 'in-progress')`,
			); err != nil {
				t.Fatalf("insert features: %v", err)
			}
			if _, err := database.Exec(
				`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness, session_family_id)
				 VALUES
					('sess-claim-owner', ?, ?, 'active', ?, 'fam-current'),
					('sess-newest', ?, ?, 'active', ?, 'fam-current')`,
				harness, now.Add(-5*time.Minute).Format(time.RFC3339), harness,
				harness, now.Add(-time.Minute).Format(time.RFC3339), harness,
			); err != nil {
				t.Fatalf("insert sessions: %v", err)
			}
			if err := db.ClaimItemOrRenew(database, &models.Claim{
				ClaimID:          "claim-family-current",
				WorkItemID:       "bug-family-current",
				OwnerSessionID:   "sess-claim-owner",
				OwnerAgent:       harness,
				ClaimedByAgentID: harness,
				Status:           models.ClaimInProgress,
			}, time.Hour); err != nil {
				t.Fatalf("ClaimItemOrRenew: %v", err)
			}
			if _, err := database.Exec(
				`UPDATE claims SET leased_at = ?, last_heartbeat_at = ? WHERE claim_id = ?`,
				now.Add(-4*time.Minute).Format(time.RFC3339),
				now.Add(-4*time.Minute).Format(time.RFC3339),
				"claim-family-current",
			); err != nil {
				t.Fatalf("age claim timestamps: %v", err)
			}

			got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-claim-owner", "sess-newest"})
			if err != nil {
				t.Fatalf("GetCurrentSessionResumable: %v", err)
			}
			if got == nil {
				t.Fatal("got nil, want the current session")
			}
			if got.LastSessionID != "sess-newest" {
				t.Fatalf("LastSessionID = %q, want sess-newest", got.LastSessionID)
			}
			if got.WorkItemID != "bug-family-current" {
				t.Fatalf("WorkItemID = %q, want bug-family-current", got.WorkItemID)
			}
			if got.Title != "Family claimed label" {
				t.Fatalf("Title = %q, want Family claimed label", got.Title)
			}
			if got.Harness != harness {
				t.Fatalf("Harness = %q, want %s", got.Harness, harness)
			}
		})
	}
}

// TestGetCurrentSessionResumable_NilWhenUnknown verifies clean degradation: no
// matching session row yields (nil, nil), so the chooser simply omits the slot.
func TestGetCurrentSessionResumable_NilWhenUnknown(t *testing.T) {
	database := openIsolatedDB(t)

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-missing"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil for unknown session", got)
	}

	// Empty ID list is also a clean no-op.
	got, err = db.GetCurrentSessionResumable(database, time.Hour, nil)
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable(nil): %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil for empty id list", got)
	}
}

func TestGetLatestHarnessSessionResumable_UsesAgentAssignedFallback(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES
			('sess-claude-parent', 'claude-code', ?, 'active', 'claude'),
			('sess-codex-current', 'codex', ?, 'active', '')`,
		now.Add(-10*time.Minute).Format(time.RFC3339),
		now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	got, err := db.GetLatestHarnessSessionResumable(database, time.Hour, "codex")
	if err != nil {
		t.Fatalf("GetLatestHarnessSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want latest codex session")
	}
	if got.LastSessionID != "sess-codex-current" {
		t.Fatalf("LastSessionID = %q, want sess-codex-current", got.LastSessionID)
	}
	if got.Harness != "codex" {
		t.Fatalf("Harness = %q, want codex", got.Harness)
	}
	if got.WorkItemID != "" {
		t.Fatalf("WorkItemID = %q, want empty for claimless runtime session", got.WorkItemID)
	}
}

func TestGetLatestHarnessSessionResumablePrefersPromptBearingSession(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES
			('sess-startup-only', 'codex', ?, 'active', ''),
			('sess-with-prompt', 'codex', ?, 'active', ''),
			('sess-with-newer-prompt', 'codex', ?, 'active', '')`,
		now.Add(-time.Minute).Format(time.RFC3339),
		now.Add(-10*time.Minute).Format(time.RFC3339),
		now.Add(-20*time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO otel_signals (signal_id, session_id, harness, kind, canonical, native, ts_micros, attrs_json)
		 VALUES
			('sig-prompt', 'sess-with-prompt', 'codex', 'log', 'user_prompt', 'codex.user_prompt', 1000, ?),
			('sig-newer-prompt', 'sess-with-newer-prompt', 'codex', 'log', 'user_prompt', 'codex.user_prompt', 2000, ?)`,
		`{"prompt":"review and fix this issue","event.timestamp":"2026-06-21T05:02:04Z"}`,
		`{"prompt":"latest user query","event.timestamp":"2026-06-21T07:12:31Z"}`,
	); err != nil {
		t.Fatalf("insert prompt signal: %v", err)
	}

	got, err := db.GetLatestHarnessSessionResumable(database, time.Hour, "codex")
	if err != nil {
		t.Fatalf("GetLatestHarnessSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want prompt-bearing codex session")
	}
	if got.LastSessionID != "sess-with-newer-prompt" {
		t.Fatalf("LastSessionID = %q, want sess-with-newer-prompt", got.LastSessionID)
	}
	if got.PromptLabel != "latest user query" {
		t.Fatalf("PromptLabel = %q, want latest user query", got.PromptLabel)
	}
}

func TestGetLatestHarnessSessionResumable_NilWhenNoHarnessMatch(t *testing.T) {
	database := openIsolatedDB(t)

	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES ('sess-claude', 'claude-code', ?, 'active', 'claude')`,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	got, err := db.GetLatestHarnessSessionResumable(database, time.Hour, "codex")
	if err != nil {
		t.Fatalf("GetLatestHarnessSessionResumable: %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil without a codex session", got)
	}
}

func TestSessionPromptLabelPrefersLastUserQueryThenMessagesThenOTel(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, last_user_query, last_user_query_at)
		 VALUES
			('sess-meta', 'codex', ?, 'active', 'review and fix this issue', ?),
			('sess-msg', 'codex', ?, 'active', '', ''),
			('sess-otel', 'codex', ?, 'active', '', '')`,
		now.Add(-2*time.Minute).Format(time.RFC3339),
		now.Add(-30*time.Second).Format(time.RFC3339),
		now.Add(-time.Minute).Format(time.RFC3339),
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO messages (session_id, ordinal, role, content, timestamp)
		 VALUES
			('sess-meta', 1, 'user', 'older ignored message', ?),
			('sess-msg', 1, 'user', 'first user message', ?),
			('sess-msg', 2, 'assistant', 'assistant text', ?),
			('sess-msg', 3, 'user', 'last user message', ?)`,
		now.Add(-4*time.Minute).Format(time.RFC3339),
		now.Add(-3*time.Minute).Format(time.RFC3339),
		now.Add(-2*time.Minute).Format(time.RFC3339),
		now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO otel_signals (signal_id, session_id, harness, kind, canonical, native, ts_micros, attrs_json)
		 VALUES
			('sig-old', 'sess-otel', 'codex', 'log', 'user_prompt', 'codex.user_prompt', 1000, ?),
			('sig-new', 'sess-otel', 'codex', 'log', 'user_prompt', 'codex.user_prompt', 2000, ?)`,
		`{"prompt":"old otel prompt","event.timestamp":"2026-06-21T05:01:00Z"}`,
		`{"prompt":"new otel prompt","event.timestamp":"2026-06-21T05:02:00Z"}`,
	); err != nil {
		t.Fatalf("insert otel prompts: %v", err)
	}

	if got := db.SessionPromptLabel(database, "sess-meta"); got != "review and fix this issue" {
		t.Fatalf("metadata label = %q, want last_user_query", got)
	}
	if got := db.SessionPromptLabel(database, "sess-msg"); got != "last user message" {
		t.Fatalf("message label = %q, want latest user message", got)
	}
	if got := db.SessionPromptLabel(database, "sess-otel"); got != "new otel prompt" {
		t.Fatalf("otel label = %q, want newest otel prompt", got)
	}
}

func TestGetLatestHarnessSessionResumableIncludesPromptLabel(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, last_user_query)
		 VALUES ('sess-codex-current', 'codex', ?, 'active', 'resume me by prompt')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	got, err := db.GetLatestHarnessSessionResumable(database, time.Hour, "codex")
	if err != nil {
		t.Fatalf("GetLatestHarnessSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want latest codex session")
	}
	if got.PromptLabel != "resume me by prompt" {
		t.Fatalf("PromptLabel = %q, want resume me by prompt", got.PromptLabel)
	}
}
