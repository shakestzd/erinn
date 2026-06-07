package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/migrate"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// migrateTracksTestEnv builds a temp project directory and seeds:
//   - tracks: trk-old, trk-yolo, trk-plan
//   - features: featClear (yolo-dominant, currently on trk-old),
//     featAmbig (split between two tracks),
//     featOrphan (no feature_files),
//     featStable (already on its dominant track).
//
// Returns the .wipnote directory path and a project root for the test.
func migrateTracksTestEnv(t *testing.T) (hgDir, rulesPath string) {
	t.Helper()
	root := t.TempDir()
	hgDir = filepath.Join(root, ".wipnote")
	if err := os.MkdirAll(hgDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Force the DB to live inside the project for test isolation.
	dbPath := filepath.Join(hgDir, "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	// Open the project — workitem.Open will use WIPNOTE_DB_PATH and create
	// the DB file. We also use it to write canonical HTML so feature update
	// works end-to-end.
	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	tOld, err := p.Tracks.Create("Old Track")
	if err != nil {
		t.Fatal(err)
	}
	tYolo, err := p.Tracks.Create("Yolo Track")
	if err != nil {
		t.Fatal(err)
	}
	tPlan, err := p.Tracks.Create("Plan Track")
	if err != nil {
		t.Fatal(err)
	}

	featClear, err := p.Features.Create("Clear yolo feature", workitem.FeatWithTrack(tOld.ID))
	if err != nil {
		t.Fatal(err)
	}
	featAmbig, err := p.Features.Create("Ambiguous feature", workitem.FeatWithTrack(tOld.ID))
	if err != nil {
		t.Fatal(err)
	}
	featOrphan, err := p.Features.Create("Orphan feature", workitem.FeatWithTrack(tOld.ID))
	if err != nil {
		t.Fatal(err)
	}
	featStable, err := p.Features.Create("Already correct", workitem.FeatWithTrack(tYolo.ID))
	if err != nil {
		t.Fatal(err)
	}

	// Seed feature_files in the SQLite read index. We use the same DB the
	// project uses (env var override), so direct UpsertFeatureFile is fine.
	now := time.Now().UTC()
	_ = now
	upsert := func(featureID, path string) {
		if err := db.UpsertFeatureFile(p.DB, &models.FeatureFile{
			ID: "ff-" + featureID + "-" + path, FeatureID: featureID, FilePath: path, Operation: "edit",
		}); err != nil {
			t.Fatalf("upsert feature_file: %v", err)
		}
	}

	// featClear: 4 yolo files, 0 plan
	for _, f := range []string{"cmd/wipnote/yolo.go", "cmd/wipnote/tmux.go", "cmd/wipnote/budget.go", "internal/worktree/manager.go"} {
		upsert(featClear.ID, f)
	}
	// featAmbig: 2 plan, 2 yolo (50/50 split)
	for _, f := range []string{"cmd/wipnote/plan_create.go", "cmd/wipnote/plan_show.go", "cmd/wipnote/yolo.go", "cmd/wipnote/tmux.go"} {
		upsert(featAmbig.ID, f)
	}
	// featOrphan: zero feature_files
	_ = featOrphan
	// featStable: 3 yolo files; current track is already trk-yolo.
	for _, f := range []string{"cmd/wipnote/yolo.go", "cmd/wipnote/tmux.go", "cmd/wipnote/launch_run.go"} {
		upsert(featStable.ID, f)
	}

	// Save IDs for later assertions.
	t.Setenv("MTT_FEAT_CLEAR", featClear.ID)
	t.Setenv("MTT_FEAT_AMBIG", featAmbig.ID)
	t.Setenv("MTT_FEAT_ORPHAN", featOrphan.ID)
	t.Setenv("MTT_FEAT_STABLE", featStable.ID)
	t.Setenv("MTT_TRK_OLD", tOld.ID)
	t.Setenv("MTT_TRK_YOLO", tYolo.ID)
	t.Setenv("MTT_TRK_PLAN", tPlan.ID)

	// Write a rules file using the actual track IDs we just created.
	rulesPath = filepath.Join(root, "rules.yaml")
	rulesYAML := "rules:\n" +
		"  - { glob: \"cmd/wipnote/yolo.go\",        track_id: \"" + tYolo.ID + "\", priority: 110 }\n" +
		"  - { glob: \"cmd/wipnote/tmux.go\",        track_id: \"" + tYolo.ID + "\", priority: 110 }\n" +
		"  - { glob: \"cmd/wipnote/budget.go\",      track_id: \"" + tYolo.ID + "\", priority: 110 }\n" +
		"  - { glob: \"cmd/wipnote/launch_run.go\",  track_id: \"" + tYolo.ID + "\", priority: 110 }\n" +
		"  - { glob: \"internal/worktree/**\",         track_id: \"" + tYolo.ID + "\", priority: 100 }\n" +
		"  - { glob: \"cmd/wipnote/plan_*.go\",      track_id: \"" + tPlan.ID + "\", priority: 100 }\n"
	if err := os.WriteFile(rulesPath, []byte(rulesYAML), 0o644); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	return hgDir, rulesPath
}

func TestMigrateTracksDryRunOutput(t *testing.T) {
	hgDir, rulesPath := migrateTracksTestEnv(t)

	var buf bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: rulesPath,
		dryRun:    true,
		types:     "features",
		threshold: 0.6,
		format:    "text",
	}
	if err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard); err != nil {
		t.Fatalf("runMigrateTracks: %v", err)
	}

	out := buf.String()
	// Headline summary line should be present.
	if !strings.Contains(out, "features classified:") {
		t.Errorf("missing summary line:\n%s", out)
	}
	// Confident move should be there.
	if !strings.Contains(out, "confident") {
		t.Errorf("expected at least one 'confident' decision:\n%s", out)
	}
	// Ambiguous label should appear (50/50 case is below 0.6 threshold).
	if !strings.Contains(strings.ToLower(out), "ambiguous") {
		t.Errorf("expected an ambiguous decision in output:\n%s", out)
	}

	// Dry-run must NOT have written a manifest.
	matches, _ := filepath.Glob(filepath.Join(hgDir, "migrations", "track-backfill-*.json"))
	if len(matches) != 0 {
		t.Errorf("dry-run should not write manifests, found: %v", matches)
	}
}

func TestMigrateTracksWriteCreatesManifest(t *testing.T) {
	hgDir, rulesPath := migrateTracksTestEnv(t)
	featClear := os.Getenv("MTT_FEAT_CLEAR")
	trkYolo := os.Getenv("MTT_TRK_YOLO")

	var buf bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: rulesPath,
		write:     true,
		types:     "features",
		threshold: 0.6,
		format:    "text",
	}
	if err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard); err != nil {
		t.Fatalf("runMigrateTracks: %v", err)
	}

	// Manifest written?
	matches, _ := filepath.Glob(filepath.Join(hgDir, "migrations", "track-backfill-*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 manifest file, got %d: %v", len(matches), matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Decisions []migrate.Decision `json:"decisions"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	// Confirm featClear was moved.
	moved := false
	for _, d := range manifest.Decisions {
		if d.FeatureID == featClear && d.ProposedTrack == trkYolo {
			moved = true
			break
		}
	}
	if !moved {
		t.Fatalf("featClear (%s) → %s not recorded in manifest:\n%s", featClear, trkYolo, data)
	}

	// Confirm the canonical store reflects the new track for featClear.
	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open after write: %v", err)
	}
	defer p.Close()
	feat, err := p.Features.Get(featClear)
	if err != nil {
		t.Fatalf("Features.Get: %v", err)
	}
	if feat.TrackID != trkYolo {
		t.Errorf("featClear track_id = %q, want %q", feat.TrackID, trkYolo)
	}
}

func TestMigrateTracksWriteRefusesOverwrite(t *testing.T) {
	hgDir, rulesPath := migrateTracksTestEnv(t)

	// Pre-create a manifest so the second write call collides.
	migDir := filepath.Join(hgDir, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(migDir, "track-backfill-1234567890.json")
	if err := os.WriteFile(existing, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: rulesPath,
		write:     true,
		types:     "features",
		threshold: 0.6,
		format:    "text",
		// force is FALSE
	}
	err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard)
	if err == nil {
		t.Fatalf("expected error on existing manifest without --force")
	}
	if !strings.Contains(err.Error(), "manifest") {
		t.Errorf("error = %q, want a manifest-collision message", err.Error())
	}

	// With --force it should succeed.
	opts.force = true
	buf.Reset()
	if err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard); err != nil {
		t.Fatalf("runMigrateTracks --force: %v", err)
	}
}

func TestMigrateTracksJSONFormat(t *testing.T) {
	hgDir, rulesPath := migrateTracksTestEnv(t)

	var buf bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: rulesPath,
		dryRun:    true,
		types:     "features",
		threshold: 0.6,
		format:    "json",
	}
	if err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard); err != nil {
		t.Fatalf("runMigrateTracks json: %v", err)
	}
	var ds []migrate.Decision
	if err := json.Unmarshal(buf.Bytes(), &ds); err != nil {
		t.Fatalf("not valid JSON Decision array: %v\n%s", err, buf.String())
	}
	if len(ds) == 0 {
		t.Errorf("expected non-empty decisions array")
	}
}

func TestMigrateTracksAmbiguousSkippedInWrite(t *testing.T) {
	hgDir, rulesPath := migrateTracksTestEnv(t)
	featAmbig := os.Getenv("MTT_FEAT_AMBIG")
	trkOld := os.Getenv("MTT_TRK_OLD")

	var buf bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: rulesPath,
		write:     true,
		types:     "features",
		threshold: 0.6,
		format:    "text",
	}
	if err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard); err != nil {
		t.Fatalf("runMigrateTracks: %v", err)
	}

	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()
	feat, err := p.Features.Get(featAmbig)
	if err != nil {
		t.Fatalf("Features.Get: %v", err)
	}
	if feat.TrackID != trkOld {
		t.Errorf("ambiguous feature should not have moved; track_id = %q, want %q", feat.TrackID, trkOld)
	}
}

func TestMigrateTracksBugsWriteEditsBugCollection(t *testing.T) {
	// Regression: roborev finding — --types bugs --write must edit p.Bugs,
	// not p.Features. We seed a bug with yolo-dominant files and assert the
	// canonical store reflects the bug's new track after --write.
	hgDir, rulesPath := migrateTracksTestEnv(t)
	trkYolo := os.Getenv("MTT_TRK_YOLO")
	trkOld := os.Getenv("MTT_TRK_OLD")

	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	bg, err := p.Bugs.Create("Bug touching yolo", workitem.BugWithTrack(trkOld))
	if err != nil {
		t.Fatalf("Bugs.Create: %v", err)
	}
	for _, f := range []string{"cmd/wipnote/yolo.go", "cmd/wipnote/tmux.go", "cmd/wipnote/budget.go", "internal/worktree/manager.go"} {
		if err := db.UpsertFeatureFile(p.DB, &models.FeatureFile{
			ID: "ff-bug-" + bg.ID + "-" + f, FeatureID: bg.ID, FilePath: f, Operation: "edit",
		}); err != nil {
			t.Fatalf("UpsertFeatureFile: %v", err)
		}
	}
	p.Close()

	var buf bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: rulesPath,
		write:     true,
		types:     "bugs",
		threshold: 0.6,
		format:    "text",
	}
	if err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard); err != nil {
		t.Fatalf("runMigrateTracks bugs: %v", err)
	}

	// Reopen and check: the BUG should be on trkYolo now.
	p2, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer p2.Close()
	got, err := p2.Bugs.Get(bg.ID)
	if err != nil {
		t.Fatalf("Bugs.Get: %v", err)
	}
	if got.TrackID != trkYolo {
		t.Errorf("bug track_id = %q, want %q", got.TrackID, trkYolo)
	}
}

func TestMigrateTracksJSONWriteIsPureJSON(t *testing.T) {
	// Regression: roborev finding — --format json --write must keep stdout
	// pure-JSON-parseable. Status (manifest path, reindex hint) goes to
	// stderr instead of being appended to stdout.
	hgDir, rulesPath := migrateTracksTestEnv(t)

	var stdout, stderr bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: rulesPath,
		write:     true,
		types:     "features",
		threshold: 0.6,
		format:    "json",
	}
	if err := runMigrateTracks(context.Background(), hgDir, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runMigrateTracks: %v", err)
	}

	// Stdout must parse as JSON without trailing junk.
	var ds []migrate.Decision
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := dec.Decode(&ds); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n--- stdout ---\n%s", err, stdout.String())
	}
	if dec.More() {
		t.Errorf("stdout has trailing content after JSON decode: %s", stdout.String())
	}
	// Stderr should carry the manifest hint.
	if !strings.Contains(stderr.String(), "Manifest written") {
		t.Errorf("stderr missing manifest message:\n%s", stderr.String())
	}
}

func TestMigrateTracksRejectsUnknownProposedTrack(t *testing.T) {
	// Regression: roborev finding — a typo'd track_id in the rules file must
	// fail before any mutation, not after partial application.
	hgDir, _ := migrateTracksTestEnv(t)

	// Replace the rules file with one that names a non-existent track.
	badRules := filepath.Join(filepath.Dir(hgDir), "bad-rules.yaml")
	if err := os.WriteFile(badRules, []byte(
		"rules:\n"+
			"  - { glob: \"cmd/wipnote/yolo.go\", track_id: \"trk-does-not-exist\", priority: 110 }\n"+
			"  - { glob: \"cmd/wipnote/tmux.go\", track_id: \"trk-does-not-exist\", priority: 110 }\n"+
			"  - { glob: \"cmd/wipnote/budget.go\", track_id: \"trk-does-not-exist\", priority: 110 }\n"+
			"  - { glob: \"internal/worktree/**\", track_id: \"trk-does-not-exist\", priority: 100 }\n"),
		0o644); err != nil {
		t.Fatalf("write bad-rules: %v", err)
	}

	// Snapshot: featClear is on trkOld before the call.
	featClear := os.Getenv("MTT_FEAT_CLEAR")
	trkOld := os.Getenv("MTT_TRK_OLD")

	var buf bytes.Buffer
	opts := migrateTracksOpts{
		rulesPath: badRules,
		write:     true,
		types:     "features",
		threshold: 0.6,
		format:    "text",
	}
	err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard)
	if err == nil {
		t.Fatalf("expected error for unknown ProposedTrack, got nil")
	}
	if !strings.Contains(err.Error(), "trk-does-not-exist") {
		t.Errorf("error doesn't name the missing track: %v", err)
	}

	// And critically: nothing was mutated.
	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()
	feat, err := p.Features.Get(featClear)
	if err != nil {
		t.Fatalf("Features.Get: %v", err)
	}
	if feat.TrackID != trkOld {
		t.Errorf("featClear was mutated despite validation failure: track_id = %q, want %q",
			feat.TrackID, trkOld)
	}
}

func TestMigrateTracksTypesValidation(t *testing.T) {
	hgDir, rulesPath := migrateTracksTestEnv(t)

	cases := []struct {
		types string
		ok    bool
	}{
		{"features", true},
		{"bugs", true},
		{"features,bugs", true},
		{"sessions", false}, // unknown type
		{"", false},
	}
	for _, c := range cases {
		opts := migrateTracksOpts{
			rulesPath: rulesPath,
			dryRun:    true,
			types:     c.types,
			threshold: 0.6,
			format:    "text",
		}
		var buf bytes.Buffer
		err := runMigrateTracks(context.Background(), hgDir, opts, &buf, io.Discard)
		if c.ok && err != nil {
			t.Errorf("types=%q: unexpected error %v", c.types, err)
		}
		if !c.ok && err == nil {
			t.Errorf("types=%q: expected error, got none", c.types)
		}
	}
}
