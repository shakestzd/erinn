package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/models"
	"github.com/shakestzd/wipnote/internal/workitem"
	"github.com/shakestzd/wipnote/internal/workowners"
)

// autoCausedByEdge creates a caused_by edge from a bug to the active feature.
func autoCausedByEdge(p *workitem.Project, bugID, featureID string) {
	edge := models.Edge{
		TargetID:     featureID,
		Relationship: models.RelCausedBy,
		Title:        featureID,
		Since:        time.Now().UTC(),
	}
	_, _ = p.Bugs.AddEdge(bugID, edge)
}

// autoImplementedInEdge creates bidirectional edges between a work item and
// a session: implemented_in (item→session in HTML+SQLite) and implements
// (session→item in SQLite; sessions also have HTML files in .wipnote/sessions/).
// Idempotent: skips if the forward edge already exists. Non-fatal on error.
func autoImplementedInEdge(col *workitem.Collection, itemID, sessionID string, database *sql.DB) {
	node, err := col.Get(itemID)
	if err != nil {
		return
	}
	// Check for existing implemented_in edge to this session.
	for _, e := range node.Edges[string(models.RelImplementedIn)] {
		if e.TargetID == sessionID {
			return // already linked
		}
	}
	edge := models.Edge{
		TargetID:     sessionID,
		Relationship: models.RelImplementedIn,
		Title:        "session " + sessionID,
		Since:        time.Now().UTC(),
	}
	_, _ = col.AddEdge(itemID, edge) // writes HTML + SQLite via dual-write

	// Reverse edge: session→item (SQLite + session HTML files in .wipnote/sessions/).
	if database != nil {
		revID := fmt.Sprintf("%s-%s-%s", sessionID, string(models.RelImplements), itemID)
		_ = dbpkg.InsertEdge(database, revID, sessionID, "session", itemID,
			inferNodeTypeFromID(itemID), string(models.RelImplements), nil)
	}
}

// autoTrackEdges creates bidirectional part_of/contains edges between a work
// item and its track. Errors are non-fatal (warn-not-block).
func autoTrackEdges(p *workitem.Project, itemID, typeName, trackID, itemTitle string) error {
	now := time.Now().UTC()

	// item → track (part_of)
	col := collectionFor(p, typeName)
	partOf := models.Edge{
		TargetID:     trackID,
		Relationship: models.RelPartOf,
		Title:        trackID,
		Since:        now,
	}
	if _, err := col.AddEdge(itemID, partOf); err != nil {
		return fmt.Errorf("part_of: %w", err)
	}

	// track → item (contains)
	contains := models.Edge{
		TargetID:     itemID,
		Relationship: models.RelContains,
		Title:        itemTitle,
		Since:        now,
	}
	if _, err := p.Tracks.AddEdge(trackID, contains); err != nil {
		return fmt.Errorf("contains: %w", err)
	}

	return nil
}

// warnMissingFields validates required and recommended fields per work item type.
// Features and bugs REQUIRE --track and --description. Spikes, tracks, plans,
// and specs are exempt from the track requirement.
func warnMissingFields(typeName string, o *wiCreateOpts) error {
	// Features and bugs require a track to link to an initiative.
	// Features with an explicit standalone_reason are exempt from the track requirement.
	if o.trackID == "" && (typeName == "feature" || typeName == "bug") && !(typeName == "feature" && o.standaloneReason != "") {
		msg := fmt.Sprintf("%s requires --track <trk-id> to link to an initiative.\n\nFind the right existing track before creating a new one:\n  1. wipnote relevant \"<topic from your work>\"   — searches by content\n  2. wipnote track list                          — enumerate all tracks\n\nOnly if no existing track fits, create one as a last resort:\n  wipnote track create \"Track Title\"", typeName)

		// For bugs with --files, try to suggest the track via file ownership.
		if typeName == "bug" && o.files != "" {
			if suggestion := suggestTrackFromFiles(o.files); suggestion != "" {
				msg += "\n\nSuggested: " + suggestion
			}
		}

		return fmt.Errorf("%s", msg)
	}

	switch typeName {
	case "bug", "feature":
		// Standalone features (no plan, no track) only need --standalone reason, not --description.
		isStandaloneFeature := typeName == "feature" && o.standaloneReason != ""
		if o.description == "" && !isStandaloneFeature {
			return fmt.Errorf("%s requires --description (captures context for future sessions)\nExample: wipnote %s create \"title\" --description \"root cause and context\"", typeName, typeName)
		}
	case "spec":
		if o.description == "" {
			fmt.Fprintf(os.Stderr, "Warning: spec created without --description.\n")
		}
	}
	return nil
}

// suggestTrackFromFiles resolves file ownership for the first affected file.
// Checks WORKOWNERS static map first, then falls back to DB heuristic.
// Returns a suggestion string like "--track trk-abc (owns cmd/foo.go)".
func suggestTrackFromFiles(files string) string {
	dir, err := findWipnoteDir()
	if err != nil {
		return ""
	}

	// Check WORKOWNERS static map first.
	wf, _ := workowners.Parse(filepath.Join(dir, "WORKOWNERS"))
	for _, f := range strings.Split(files, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if ownerID := wf.Resolve(f); ownerID != "" {
			if strings.HasPrefix(ownerID, "trk-") {
				return fmt.Sprintf("--track %s (WORKOWNERS: %s)", ownerID, f)
			}
			// Owner is a feature — resolve its track from DB if possible.
			if database := openTrackDB(dir); database != nil {
				var trackID string
				database.QueryRow("SELECT COALESCE(track_id, '') FROM features WHERE id = ?",
					ownerID).Scan(&trackID) //nolint:errcheck
				database.Close()
				if trackID != "" {
					return fmt.Sprintf("--track %s (WORKOWNERS: %s via %s)", trackID, f, ownerID)
				}
			}
			return fmt.Sprintf("feature %s owns %s (WORKOWNERS) — find its track with: wipnote feature show %s",
				ownerID, f, ownerID)
		}
	}

	// Fall back to DB heuristic.
	database := openTrackDB(dir)
	if database == nil {
		return ""
	}
	defer database.Close()

	for _, f := range strings.Split(files, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		owner := dbpkg.ResolveFileOwner(database, f)
		if owner == nil {
			continue
		}
		if owner.TrackID != "" {
			return fmt.Sprintf("--track %s (%s owns %s)", owner.TrackID, owner.Title, f)
		}
		return fmt.Sprintf("feature %s (%s) owns %s — find its track with: wipnote feature show %s",
			owner.FeatureID, owner.Title, f, owner.FeatureID)
	}
	return ""
}
