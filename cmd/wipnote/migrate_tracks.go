package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/internal/migrate"
	"github.com/spf13/cobra"
)

// migrateTracksOpts holds the parsed CLI flags for `wipnote migrate-tracks`.
type migrateTracksOpts struct {
	rulesPath string
	dryRun    bool
	write     bool
	types     string  // comma-separated: features, bugs
	threshold float64 // dominant-share floor (0..1)
	format    string  // text | json
	force     bool    // overwrite existing manifest
}

// migrateTracksCmd returns the cobra command for `wipnote migrate-tracks`.
func migrateTracksCmd() *cobra.Command {
	opts := migrateTracksOpts{
		dryRun:    true,
		types:     "features",
		threshold: 0.6,
		format:    "text",
	}
	cmd := &cobra.Command{
		Use:   "migrate-tracks",
		Short: "Backfill feature track attribution via path-glob rules",
		Long: `Walks every feature, examines its feature_files paths, and proposes
re-attribution to the value-aligned track whose code surface dominates.

Modes:
  --dry-run (default)  print proposed moves; do not modify state
  --write              apply moves and write a manifest for audit/rollback

Rules: a YAML catalog of {glob, track_id, priority}. Higher priority wins
on overlap. Globs support * (single segment) and ** (across boundaries).

Decisions are emitted in five categories:
  confident       — dominant track exceeds threshold; safe to move
  ambiguous       — files matched but no track exceeds threshold
  no-attribution  — feature has zero feature_files rows
  no-match        — feature has files but none match any rule
  no-change       — current track is already the dominant track

Examples:
  wipnote migrate-tracks --rules docs/track-attribution-rules.yaml
  wipnote migrate-tracks --rules rules.yaml --write
  wipnote migrate-tracks --rules rules.yaml --format json
  wipnote migrate-tracks --rules rules.yaml --types features,bugs`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if opts.write {
				opts.dryRun = false
			}
			hgDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			printProjectHeaderIfDifferent(hgDir)
			return runMigrateTracks(context.Background(), hgDir, opts, os.Stdout, os.Stderr)
		},
	}
	cmd.Flags().StringVar(&opts.rulesPath, "rules", "", "path to rules YAML (required)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", true, "preview without writing changes (default)")
	cmd.Flags().BoolVar(&opts.write, "write", false, "apply moves and write manifest")
	cmd.Flags().StringVar(&opts.types, "types", "features", "comma-separated work-item types: features, bugs")
	cmd.Flags().Float64Var(&opts.threshold, "ambiguity-threshold", 0.6, "minimum dominant-track share (0..1)")
	cmd.Flags().StringVar(&opts.format, "format", "text", "output format: text or json")
	cmd.Flags().BoolVar(&opts.force, "force", false, "overwrite existing manifest in --write mode")
	_ = cmd.MarkFlagRequired("rules")
	return cmd
}

// runMigrateTracks executes the classifier against the project at hgDir and
// emits text/json output to out. When opts.write is true, applies confident
// moves and records them in a manifest file under .wipnote/migrations/.
//
// `out` is the primary output stream (decision table or JSON array).
// `status` is the human status stream — sent there so JSON mode keeps `out`
// pure-JSON-parseable. Pass io.Discard to suppress status entirely.
func runMigrateTracks(_ context.Context, hgDir string, opts migrateTracksOpts, out, status io.Writer) error {
	if opts.rulesPath == "" {
		return fmt.Errorf("--rules is required")
	}
	if opts.write && opts.dryRun {
		// `--write` overrides the default dry-run.
		opts.dryRun = false
	}
	if opts.threshold < 0 || opts.threshold > 1 {
		return fmt.Errorf("--ambiguity-threshold must be between 0 and 1, got %v", opts.threshold)
	}

	types, err := parseTypesFlag(opts.types)
	if err != nil {
		return err
	}

	rules, err := migrate.LoadRules(opts.rulesPath)
	if err != nil {
		return err
	}
	if len(rules.Rules) == 0 {
		return fmt.Errorf("rules file %s contains no rules", opts.rulesPath)
	}

	p, err := workitem.Open(hgDir, "wipnote-cli")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	decisions, err := migrate.ClassifyAll(p.DB, hgDir, rules, opts.threshold, types)
	if err != nil {
		return err
	}
	migrate.SortDecisions(decisions)

	// Resolve manifest path up-front so write mode fails fast on collision.
	var manifestPath string
	if opts.write {
		manifestPath = filepath.Join(hgDir, "migrations",
			fmt.Sprintf("track-backfill-%d.json", time.Now().Unix()))
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
			return fmt.Errorf("create migrations dir: %w", err)
		}
		// Refuse overwrite without --force. Since the path is unique-by-second,
		// a same-second collision is rare; but if any track-backfill-*.json
		// exists and --force is not set, treat that as "manifest already present".
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(manifestPath), "track-backfill-*.json"))
		if len(matches) > 0 && !opts.force {
			return fmt.Errorf("existing manifest(s) present in %s — re-run with --force to overwrite",
				filepath.Dir(manifestPath))
		}
	}

	// Validate proposed tracks BEFORE writing or even printing in --write
	// mode. A typo'd track_id in the rules file should fail loudly up-front
	// rather than partially applying and leaving the store in a torn state.
	if opts.write {
		if err := validateProposedTracks(p, decisions); err != nil {
			return err
		}
	}

	// Emit decisions in requested format.
	switch opts.format {
	case "json":
		if err := writeJSON(out, decisions); err != nil {
			return err
		}
	case "text", "":
		writeText(out, decisions)
	default:
		return fmt.Errorf("unknown --format %q (use text or json)", opts.format)
	}

	if !opts.write {
		return nil
	}

	// Apply confident moves and write the manifest. Status messages go to
	// `status` (stderr) so JSON mode keeps `out` pure-JSON.
	applyErrs := applyDecisions(p, decisions)
	if writeErr := writeManifest(manifestPath, opts.rulesPath, decisions); writeErr != nil {
		return fmt.Errorf("write manifest: %w (also had %d apply errors)", writeErr, len(applyErrs))
	}
	if len(applyErrs) > 0 {
		fmt.Fprintf(status, "\nWARNING: %d apply errors:\n", len(applyErrs))
		for _, e := range applyErrs {
			fmt.Fprintf(status, "  %v\n", e)
		}
		return fmt.Errorf("%d items failed to migrate (see manifest at %s)",
			len(applyErrs), manifestPath)
	}
	fmt.Fprintf(status, "\nManifest written: %s\n", manifestPath)
	fmt.Fprintln(status, "Run `wipnote reindex --full` to refresh the SQLite read index "+
		"so blame/code-areas reflect the new attribution.")
	return nil
}

// validateProposedTracks checks that every confident decision targets a track
// that actually exists in the project. Returns the first missing track as an
// error so the caller can abort BEFORE mutating any state.
func validateProposedTracks(p *workitem.Project, decisions []migrate.Decision) error {
	seen := map[string]bool{}
	for _, d := range decisions {
		if d.Reason != "confident" {
			continue
		}
		if seen[d.ProposedTrack] {
			continue
		}
		seen[d.ProposedTrack] = true
		if _, err := p.Tracks.Get(d.ProposedTrack); err != nil {
			return fmt.Errorf("rules reference unknown track %q (proposed for %s): %w — "+
				"check docs/track-attribution-rules.yaml for typos",
				d.ProposedTrack, d.FeatureID, err)
		}
	}
	return nil
}

// parseTypesFlag accepts a comma-separated list and validates each entry.
func parseTypesFlag(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("--types must include at least one of: features, bugs")
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		switch t {
		case "features", "bugs":
			out = append(out, t)
		default:
			return nil, fmt.Errorf("unknown type %q in --types (use features or bugs)", t)
		}
	}
	return out, nil
}

// writeText emits a tab-aligned table plus a summary line.
func writeText(w io.Writer, decisions []migrate.Decision) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FEATURE\tCURRENT\t\tPROPOSED\tCONFIDENCE\tREASON")
	for _, d := range decisions {
		conf := fmt.Sprintf("%5.1f%%", d.DominantShare*100)
		reason := d.Reason
		if d.Ambiguous {
			reason = "AMBIGUOUS (below threshold)"
		}
		from := d.CurrentTrack
		if from == "" {
			from = "-"
		}
		to := d.ProposedTrack
		if to == "" {
			to = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t→\t%s\t%s\t%s (%d files)\n",
			d.FeatureID, from, to, conf, reason, d.FileCount)
	}
	tw.Flush()
	s := migrate.Summarize(decisions)
	fmt.Fprintf(w, "\n%d features classified: %d confident moves, %d ambiguous, %d no-change, %d no-attribution, %d no-match\n",
		s.Total, s.Confident, s.Ambiguous, s.NoChange, s.NoAttribution, s.NoMatch)
}

func writeJSON(w io.Writer, decisions []migrate.Decision) error {
	if decisions == nil {
		decisions = []migrate.Decision{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(decisions)
}

// applyDecisions performs the actual re-attribution for confident decisions.
// Skips ambiguous, no-change, no-attribution, and no-match. Dispatches to the
// correct collection (features vs bugs) based on Decision.ItemType so a
// confident bug move edits p.Bugs, not p.Features. Returns errors per item
// without aborting on first failure — partial progress is recorded in the
// manifest.
func applyDecisions(p *workitem.Project, decisions []migrate.Decision) []error {
	var errs []error
	for _, d := range decisions {
		if d.Reason != "confident" {
			continue
		}
		switch d.ItemType {
		case "feature", "bug":
		default:
			errs = append(errs, fmt.Errorf("%s: unsupported item_type %q", d.FeatureID, d.ItemType))
			continue
		}
		col := collectionFor(p, d.ItemType)
		if err := col.Edit(d.FeatureID).SetTrack(d.ProposedTrack).Save(); err != nil {
			errs = append(errs, fmt.Errorf("%s: SetTrack: %w", d.FeatureID, err))
			continue
		}
		if err := moveTrackEdges(p, d.FeatureID, d.ItemType, d.ProposedTrack); err != nil {
			errs = append(errs, fmt.Errorf("%s: moveTrackEdges: %w", d.FeatureID, err))
		}
	}
	return errs
}

// migrationManifest is the on-disk audit record for a single backfill run.
type migrationManifest struct {
	GeneratedAt time.Time          `json:"generated_at"`
	RulesPath   string             `json:"rules_path"`
	WipnoteTool string             `json:"wipnote_tool"` // "migrate-tracks"
	Decisions   []migrate.Decision `json:"decisions"`
}

func writeManifest(path, rulesPath string, decisions []migrate.Decision) error {
	m := migrationManifest{
		GeneratedAt: time.Now().UTC(),
		RulesPath:   rulesPath,
		WipnoteTool: "migrate-tracks",
		Decisions:   decisions,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
