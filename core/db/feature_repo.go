package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Feature is a lightweight row struct for the features table.
// The full Node model lives in internal/models; this is for DB CRUD only.
type Feature struct {
	ID             string
	Type           string
	Title          string
	Description    string
	Status         string
	Priority       string
	AssignedTo     string
	TrackID        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StepsTotal     int
	StepsCompleted int
}

// InsertFeature creates a new feature row.
func InsertFeature(db *sql.DB, f *Feature) error {
	_, err := db.Exec(`
		INSERT INTO features (id, type, title, description, status, priority,
			assigned_to, track_id, created_at, updated_at,
			steps_total, steps_completed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Type, f.Title, nullStr(f.Description),
		f.Status, f.Priority,
		nullStr(f.AssignedTo), nullStr(f.TrackID),
		f.CreatedAt.UTC().Format(time.RFC3339),
		f.UpdatedAt.UTC().Format(time.RFC3339),
		f.StepsTotal, f.StepsCompleted,
	)
	if err != nil {
		return fmt.Errorf("insert feature %s: %w", f.ID, err)
	}
	return nil
}

// GetFeature retrieves a feature by ID.
func GetFeature(db *sql.DB, id string) (*Feature, error) {
	row := db.QueryRow(`
		SELECT id, type, title, description, status, priority,
			assigned_to, track_id, created_at, updated_at,
			steps_total, steps_completed
		FROM features WHERE id = ?`, id)

	f := &Feature{}
	var desc, assignedTo, trackID sql.NullString
	var createdStr, updatedStr string

	err := row.Scan(
		&f.ID, &f.Type, &f.Title, &desc, &f.Status, &f.Priority,
		&assignedTo, &trackID, &createdStr, &updatedStr,
		&f.StepsTotal, &f.StepsCompleted,
	)
	if err != nil {
		return nil, fmt.Errorf("get feature %s: %w", id, err)
	}

	f.Description = desc.String
	f.AssignedTo = assignedTo.String
	f.TrackID = trackID.String
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)

	return f, nil
}

// trackExists returns true if the given track ID is present in the tracks table.
// An empty id always returns false to avoid a needless query.
func trackExists(db *sql.DB, trackID string) bool {
	if trackID == "" {
		return false
	}
	var exists int
	err := db.QueryRow(`SELECT 1 FROM tracks WHERE id = ? LIMIT 1`, trackID).Scan(&exists)
	return err == nil
}

// UpsertFeature inserts or updates a feature row.
// On conflict by id, all mutable fields are updated.
// If the feature's TrackID does not correspond to an existing track, it is
// coerced to empty (NULL in SQLite) so that dangling FK references don't
// cause the upsert to fail.  The HTML store remains the source of truth.
func UpsertFeature(database *sql.DB, f *Feature) error {
	trackID := f.TrackID
	if trackID != "" && !trackExists(database, trackID) {
		trackID = ""
	}

	_, err := database.Exec(`
		INSERT INTO features (id, type, title, description, status, priority,
			assigned_to, track_id, created_at, updated_at,
			steps_total, steps_completed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			status = excluded.status,
			priority = excluded.priority,
			assigned_to = excluded.assigned_to,
			track_id = excluded.track_id,
			updated_at = excluded.updated_at,
			steps_total = excluded.steps_total,
			steps_completed = excluded.steps_completed`,
		f.ID, f.Type, f.Title, nullStr(f.Description),
		f.Status, f.Priority,
		nullStr(f.AssignedTo), nullStr(trackID),
		f.CreatedAt.UTC().Format(time.RFC3339),
		f.UpdatedAt.UTC().Format(time.RFC3339),
		f.StepsTotal, f.StepsCompleted,
	)
	if err != nil {
		return fmt.Errorf("upsert feature %s: %w", f.ID, err)
	}
	return nil
}

// UpdateFeatureStatus updates a feature's status (and updated_at).
func UpdateFeatureStatus(db *sql.DB, id, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE features SET status = ?, updated_at = ? WHERE id = ?`,
		status, now, id,
	)
	return err
}

// UpdateFeatureSteps updates the steps_total and steps_completed counters
// for a feature in the SQLite read index. HTML is canonical; this is best-effort.
func UpdateFeatureSteps(db *sql.DB, featureID string, total, completed int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE features SET steps_total = ?, steps_completed = ?, updated_at = ?
		WHERE id = ?`,
		total, completed, now, featureID,
	)
	return err
}

// ListFeaturesByStatus returns features matching the given status,
// ordered by priority DESC, created_at DESC.
func ListFeaturesByStatus(db *sql.DB, status string, limit int) ([]Feature, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`
		SELECT id, type, title, status, priority, track_id,
			created_at, updated_at, steps_total, steps_completed
		FROM features
		WHERE status = ?
		ORDER BY
			CASE priority
				WHEN 'critical' THEN 0
				WHEN 'high' THEN 1
				WHEN 'medium' THEN 2
				WHEN 'low' THEN 3
			END,
			created_at DESC
		LIMIT ?`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var features []Feature
	for rows.Next() {
		var f Feature
		var trackID sql.NullString
		var createdStr, updatedStr string

		if err := rows.Scan(
			&f.ID, &f.Type, &f.Title, &f.Status, &f.Priority, &trackID,
			&createdStr, &updatedStr, &f.StepsTotal, &f.StepsCompleted,
		); err != nil {
			return nil, err
		}
		f.TrackID = trackID.String
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		features = append(features, f)
	}
	return features, rows.Err()
}

// --- Dedup-at-create similarity ----------------------------------------------
//
// Slice-6 (feat-e8879220): when a bug/feature is created we compare its
// normalized title+description against open AND recently-closed items so a
// likely duplicate can be auto-linked with a relates_to edge and flagged for
// triage. The whole feature is best-effort and stdlib-only — no new
// dependency, and it MUST NOT make `create` fail when the read index is
// missing or empty (fresh clone / CI).

// DedupSimilarityThreshold is the minimum combined similarity score (0..1) at
// which two items are treated as a strong duplicate match.
//
// Rationale for 0.72: byte-identical title+description scores exactly 1.0;
// near-duplicates that differ only by a few words / reordering stay above
// ~0.75 with the token+trigram blend below; clearly unrelated work items
// score well under 0.4 in practice. 0.72 was tuned to fire on the
// bug-7b5339cc / bug-d67e695e style restatements without producing false
// positives on unrelated items. It is intentionally a code constant (not a
// flag) so the behaviour is reproducible across harnesses; the *window* is
// the knob operators tune, not the threshold.
const DedupSimilarityThreshold = 0.72

// DedupDefaultWindowDays is how far back (by created_at) a *closed* item is
// still considered a dedup candidate. Open items are always candidates
// regardless of age. 30 days balances catching genuine re-reports of a
// recently-fixed bug against scanning unbounded history. Operators override
// via the WIPNOTE_DEDUP_WINDOW_DAYS env var (parsed by the caller, which
// passes the resolved window into ListDedupCandidates).
const DedupDefaultWindowDays = 30

// DedupCandidate is a lightweight projection of a feature/bug row used for
// similarity scoring at create time.
type DedupCandidate struct {
	ID     string
	Type   string
	Title  string
	Status string
	Score  float64 // populated by FindDuplicate; 0 from ListDedupCandidates

	// scoringText is the pre-normalized title+description corpus used for
	// similarity. Unexported so it never leaks into JSON / display paths.
	scoringText string
}

// ListDedupCandidates returns open items of any age plus closed items created
// within windowDays, restricted to the given type ("bug" or "feature").
//
// Graceful no-op contract: if the features table is absent or empty (fresh
// clone, unbuilt index, CI) this returns (nil, nil) — never an error — so the
// caller's `create` flow can proceed without dedup. Only genuine query errors
// on an existing-but-broken table propagate.
func ListDedupCandidates(db *sql.DB, itemType string, windowDays int) ([]DedupCandidate, error) {
	if db == nil {
		return nil, nil
	}
	if windowDays <= 0 {
		windowDays = DedupDefaultWindowDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -windowDays).Format(time.RFC3339)

	// Open items: any age. Closed items: only within the window. "Closed"
	// here means a terminal status in the read index schema.
	rows, err := db.Query(`
		SELECT id, type, title, COALESCE(description,''), status
		FROM features
		WHERE type = ?
		  AND (
		        status NOT IN ('done','ended')
		     OR created_at >= ?
		      )`, itemType, cutoff)
	if err != nil {
		// Missing table => index not built yet. Treat as "no candidates".
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var out []DedupCandidate
	for rows.Next() {
		var c DedupCandidate
		var desc string
		if err := rows.Scan(&c.ID, &c.Type, &c.Title, &desc, &c.Status); err != nil {
			return nil, err
		}
		// Stash description in Title-adjacent scoring text by re-using the
		// same normalization the caller will apply; keep Title clean for
		// display and carry the full corpus separately.
		c.Title = strings.TrimSpace(c.Title)
		c.Score = 0
		c.scoringText = normalizeForSimilarity(c.Title + " " + desc)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FindDuplicate scores newTitle+newDesc against every candidate and returns
// the single best match whose score is >= DedupSimilarityThreshold, or nil
// when nothing is similar enough. Deterministic: ties break by candidate ID.
func FindDuplicate(newTitle, newDesc string, candidates []DedupCandidate) *DedupCandidate {
	target := normalizeForSimilarity(newTitle + " " + newDesc)
	if target == "" {
		return nil
	}
	best := -1.0
	var bestC *DedupCandidate
	// Sort a copy by ID so tie-breaking is stable regardless of DB row order.
	cp := make([]DedupCandidate, len(candidates))
	copy(cp, candidates)
	sort.Slice(cp, func(i, j int) bool { return cp[i].ID < cp[j].ID })
	for i := range cp {
		score := similarityScore(target, cp[i].scoringText)
		if score >= DedupSimilarityThreshold && score > best {
			best = score
			cp[i].Score = score
			bestC = &cp[i]
		}
	}
	return bestC
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// commonStopwords are dropped before token scoring so boilerplate phrasing
// ("the", "a", "when") does not inflate similarity between unrelated items.
var commonStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "on": true, "for": true, "is": true, "are": true,
	"be": true, "with": true, "when": true, "that": true, "this": true,
	"it": true, "as": true, "at": true, "by": true, "from": true,
}

// normalizeForSimilarity lowercases, replaces every non-alphanumeric run with
// a single space, trims, and collapses whitespace. The result is the canonical
// form fed to both token and trigram scorers.
func normalizeForSimilarity(s string) string {
	s = strings.ToLower(s)
	s = nonWord.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// similarityScore blends token-set Jaccard with character-trigram Jaccard.
//
//	score = max(tokenJaccard, 0.6*tokenJaccard + 0.4*trigramJaccard)
//
// The max() arm guarantees byte-identical text scores 1.0 even after stopword
// removal, while the blended arm rewards near-duplicates that share most words
// but reorder or lightly edit them. Trigrams add robustness to small typos /
// inflection differences without pulling unrelated items over the threshold.
func similarityScore(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	tj := jaccard(tokenSet(a), tokenSet(b))
	gj := jaccard(trigramSet(a), trigramSet(b))
	blended := 0.6*tj + 0.4*gj
	if tj > blended {
		return tj
	}
	return blended
}

func tokenSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, tok := range strings.Fields(s) {
		if commonStopwords[tok] {
			continue
		}
		set[tok] = true
	}
	return set
}

func trigramSet(s string) map[string]bool {
	set := make(map[string]bool)
	compact := strings.ReplaceAll(s, " ", "")
	r := []rune(compact)
	if len(r) < 3 {
		if len(r) > 0 {
			set[string(r)] = true
		}
		return set
	}
	for i := 0; i+3 <= len(r); i++ {
		set[string(r[i:i+3])] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
