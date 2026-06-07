package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// bug-7b5339cc / bug-d67e695e style restatement: same defect, lightly reworded.
const (
	dupTitleA = "indexer skips recent orphan sessions on startup"
	dupDescA  = "the indexer drops freshly created orphan sessions during the startup scan because the quiescence check fires too early"

	dupTitleB = "indexer skips recent orphan sessions on startup"
	dupDescB  = "the indexer drops freshly created orphan sessions during the startup scan because the quiescence check fires too early"

	unrelatedTitle = "dashboard activity feed should poll every five seconds"
	unrelatedDesc  = "the web component currently refreshes once and never updates; add a setInterval refresh loop"
)

func TestFindDuplicate_ByteIdenticalMatches(t *testing.T) {
	cands := []DedupCandidate{
		{ID: "bug-7b5339cc", Type: "bug", Status: "done",
			scoringText: normalizeForSimilarity(dupTitleA + " " + dupDescA)},
	}
	got := FindDuplicate(dupTitleB, dupDescB, cands)
	if got == nil {
		t.Fatalf("expected duplicate match for byte-identical text, got nil")
	}
	if got.ID != "bug-7b5339cc" {
		t.Fatalf("matched wrong candidate: %s", got.ID)
	}
	if got.Score < DedupSimilarityThreshold {
		t.Fatalf("score %.3f below threshold %.3f", got.Score, DedupSimilarityThreshold)
	}
}

func TestFindDuplicate_NearDuplicateMatches(t *testing.T) {
	cands := []DedupCandidate{
		{ID: "bug-d67e695e", Type: "bug", Status: "in-progress",
			scoringText: normalizeForSimilarity(
				"Indexer skips recent orphan sessions on startup. " +
					"The indexer drops freshly-created orphan sessions during the startup scan " +
					"because the quiescence check fires too early.")},
	}
	// Reworded but same defect.
	got := FindDuplicate(
		"indexer skips recent orphan sessions on startup",
		"indexer drops freshly created orphan sessions during startup scan; quiescence check fires too early",
		cands)
	if got == nil {
		t.Fatalf("expected near-duplicate to match, got nil")
	}
}

func TestFindDuplicate_UnrelatedNoFalsePositive(t *testing.T) {
	cands := []DedupCandidate{
		{ID: "bug-7b5339cc", Type: "bug", Status: "done",
			scoringText: normalizeForSimilarity(dupTitleA + " " + dupDescA)},
	}
	got := FindDuplicate(unrelatedTitle, unrelatedDesc, cands)
	if got != nil {
		t.Fatalf("expected NO match for unrelated item, got %s (score %.3f)",
			got.ID, got.Score)
	}
}

func TestListDedupCandidates_NilDBGracefulNoOp(t *testing.T) {
	got, err := ListDedupCandidates(nil, "bug", 30)
	if err != nil {
		t.Fatalf("nil db should be graceful no-op, got error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidates for nil db, got %v", got)
	}
}

func TestListDedupCandidates_MissingTableGracefulNoOp(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	defer database.Close()
	// No schema created — features table absent.
	got, err := ListDedupCandidates(database, "bug", 30)
	if err != nil {
		t.Fatalf("missing table should be graceful no-op, got error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidates when table absent, got %v", got)
	}
}

func TestListDedupCandidates_OpenAnyAgeClosedWithinWindow(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()
	if err := CreateAllTables(database); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Old open bug (must still be a candidate despite age).
	if _, err := database.Exec(`INSERT INTO features
		(id,type,title,description,status,priority,created_at,updated_at)
		VALUES ('bug-old-open','bug','old open','d','in-progress','medium',
		        '2000-01-01T00:00:00Z','2000-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// Old closed bug (outside window — excluded).
	if _, err := database.Exec(`INSERT INTO features
		(id,type,title,description,status,priority,created_at,updated_at)
		VALUES ('bug-old-done','bug','old done','d','done','medium',
		        '2000-01-01T00:00:00Z','2000-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	got, err := ListDedupCandidates(database, "bug", 30)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := map[string]bool{}
	for _, c := range got {
		ids[c.ID] = true
	}
	if !ids["bug-old-open"] {
		t.Errorf("expected old OPEN bug to be a candidate")
	}
	if ids["bug-old-done"] {
		t.Errorf("old CLOSED bug outside window must be excluded")
	}
}
