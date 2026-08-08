package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// batchSpec is the YAML schema for batch work item creation.
type batchSpec struct {
	Track    batchTrack  `yaml:"track"`
	Features []batchItem `yaml:"features"`
	Links    []batchLink `yaml:"links"`
}

type batchTrack struct {
	Title    string   `yaml:"title"`
	Priority string   `yaml:"priority"`
	Steps    []string `yaml:"steps"`
}

type batchItem struct {
	Title     string   `yaml:"title"`
	Priority  string   `yaml:"priority"`
	Steps     []string `yaml:"steps"`
	BlockedBy []string `yaml:"blocked_by"`
}

type batchLink struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	Rel  string `yaml:"rel"`
}

// batchResult holds the outcome of a batch apply operation.
type batchResult struct {
	TrackID      string
	FeatureIDs   []string
	LinksCreated int
}

func batchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Batch operations on work items",
	}
	cmd.AddCommand(batchApplyCmd())
	cmd.AddCommand(batchExportCmd())
	return cmd
}

func batchApplyCmd() *cobra.Command {
	var file string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create work items from a YAML spec",
		Long: `Create a track, features, and links from a YAML specification file.

Supports file input or stdin (pipe from another command).

Examples:
  wipnote batch apply --file spec.yaml
  wipnote batch apply --file spec.yaml --dry-run
  cat spec.yaml | wipnote batch apply --file -`,
		RunE: func(_ *cobra.Command, _ []string) error {
			data, err := readBatchInput(file)
			if err != nil {
				return err
			}
			result, err := executeBatchApply(data, dryRun)
			if err != nil {
				return err
			}
			printBatchResult(result, dryRun)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "",
		"YAML spec file (use - for stdin)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"print what would be created without executing")
	return cmd
}

func readBatchInput(file string) ([]byte, error) {
	if file == "" || file == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(file)
}

func parseBatchSpec(data []byte) (*batchSpec, error) {
	var spec batchSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return &spec, nil
}

func validateBatchSpec(spec *batchSpec) error {
	if len(spec.Features) > 0 && spec.Track.Title == "" {
		return fmt.Errorf("batch spec has %d feature(s) but no track — features require a parent track to avoid orphans", len(spec.Features))
	}
	return nil
}

func executeBatchApply(data []byte, dryRun bool) (*batchResult, error) {
	spec, err := parseBatchSpec(data)
	if err != nil {
		return nil, err
	}

	if err := validateBatchSpec(spec); err != nil {
		return nil, err
	}

	if dryRun {
		return executeDryRun(spec), nil
	}

	dir, err := findWipnoteDir()
	if err != nil {
		return nil, err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return nil, fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	result := &batchResult{}

	// 1. Create track (if specified)
	var trackID string
	if spec.Track.Title != "" {
		opts := []workitem.TrackOption{
			workitem.TrackWithPriority(defaultStr(spec.Track.Priority, "medium")),
		}
		if len(spec.Track.Steps) > 0 {
			opts = append(opts, workitem.TrackWithPlanPhases(spec.Track.Steps...))
		}
		track, err := p.Tracks.Create(spec.Track.Title, opts...)
		if err != nil {
			return nil, fmt.Errorf("create track: %w", err)
		}
		trackID = track.ID
		result.TrackID = trackID
	}

	// 2. Create features in order, building a title→ID map for references
	titleToID := make(map[string]string)
	for _, fi := range spec.Features {
		opts := []workitem.FeatureOption{
			workitem.FeatWithPriority(defaultStr(fi.Priority, "medium")),
		}
		if trackID != "" {
			opts = append(opts, workitem.FeatWithTrack(trackID))
		}
		if len(fi.Steps) > 0 {
			opts = append(opts, workitem.FeatWithSteps(fi.Steps...))
		}
		node, err := p.Features.Create(fi.Title, opts...)
		if err != nil {
			return nil, fmt.Errorf("create feature %q: %w", fi.Title, err)
		}
		titleToID[fi.Title] = node.ID
		result.FeatureIDs = append(result.FeatureIDs, node.ID)
	}

	// 3. Create blocked_by edges from inline declarations. These are a
	// genuine asserted dependency between sibling features in the batch
	// (the spec author wrote "blocked_by: [...]" deliberately) — equivalent
	// in intent to `link add`, just authored in bulk — so, unlike plan-slice
	// edges, they are not excluded from graph.FindBottlenecks. The origin
	// tag exists purely so this batch-apply provenance is distinguishable
	// from a NULL-metadata human `link add` assertion (bug-f55532ba).
	for _, fi := range spec.Features {
		fromID := titleToID[fi.Title]
		for _, blockerTitle := range fi.BlockedBy {
			blockerID, ok := titleToID[blockerTitle]
			if !ok {
				return nil, fmt.Errorf("blocked_by %q: no feature with that title in this spec", blockerTitle)
			}
			edge := models.Edge{
				TargetID:     blockerID,
				Relationship: models.RelBlockedBy,
				Title:        blockerTitle,
				Since:        time.Now().UTC(),
				Properties: map[string]string{
					"origin": graph.EdgeOriginBatchApply,
				},
			}
			if _, err := p.Features.AddEdge(fromID, edge); err != nil {
				return nil, fmt.Errorf("add blocked_by edge %s->%s: %w", fromID, blockerID, err)
			}
			result.LinksCreated++
		}
	}

	// 4. Create explicit links
	for _, link := range spec.Links {
		fromID, ok := titleToID[link.From]
		if !ok {
			return nil, fmt.Errorf("link from %q: no item with that title", link.From)
		}
		toID, ok := titleToID[link.To]
		if !ok {
			return nil, fmt.Errorf("link to %q: no item with that title", link.To)
		}
		rel := models.NormalizeRelationship(link.Rel)
		if !models.IsValidRelationship(rel) {
			return nil, fmt.Errorf("link rel %q: unknown relationship type", link.Rel)
		}
		edge := models.Edge{
			TargetID:     toID,
			Relationship: rel,
			Title:        link.To,
			Since:        time.Now().UTC(),
		}
		col := resolveCollection(p, fromID)
		if col == nil {
			col = p.Features.Collection
		}
		if _, err := col.AddEdge(fromID, edge); err != nil {
			return nil, fmt.Errorf("add link %s -[%s]-> %s: %w", fromID, link.Rel, toID, err)
		}
		result.LinksCreated++
	}

	return result, nil
}

func executeDryRun(spec *batchSpec) *batchResult {
	result := &batchResult{}
	if spec.Track.Title != "" {
		result.TrackID = "[dry-run]"
		fmt.Printf("[dry-run] Would create track: %q (priority: %s)\n",
			spec.Track.Title, defaultStr(spec.Track.Priority, "medium"))
		for _, s := range spec.Track.Steps {
			fmt.Printf("  step: %s\n", s)
		}
	}
	for _, fi := range spec.Features {
		result.FeatureIDs = append(result.FeatureIDs, "[dry-run]")
		fmt.Printf("[dry-run] Would create feature: %q (priority: %s)\n",
			fi.Title, defaultStr(fi.Priority, "medium"))
		for _, s := range fi.Steps {
			fmt.Printf("  step: %s\n", s)
		}
		for _, b := range fi.BlockedBy {
			fmt.Printf("  blocked_by: %s\n", b)
			result.LinksCreated++
		}
	}
	for _, link := range spec.Links {
		fmt.Printf("[dry-run] Would link: %s -[%s]-> %s\n", link.From, link.Rel, link.To)
		result.LinksCreated++
	}
	return result
}

func printBatchResult(result *batchResult, dryRun bool) {
	if dryRun {
		return // already printed during dry run
	}
	if result.TrackID != "" {
		fmt.Printf("Created track: %s\n", result.TrackID)
	}
	for _, fid := range result.FeatureIDs {
		fmt.Printf("Created feature: %s\n", fid)
	}
	if result.LinksCreated > 0 {
		fmt.Printf("Created %d link(s)\n", result.LinksCreated)
	}
	total := len(result.FeatureIDs)
	if result.TrackID != "" {
		total++
	}
	fmt.Printf("\nBatch complete: %d item(s), %d link(s)\n", total, result.LinksCreated)
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func batchExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export <track-id>",
		Short: "Export a track and its features as YAML",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runBatchExport(args[0])
		},
	}
}

func runBatchExport(trackID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	// Load track
	trackPath := resolveNodePath(dir, trackID)
	if trackPath == "" {
		return fmt.Errorf("track %q not found", trackID)
	}
	trackNode, err := htmlparse.ParseFile(trackPath)
	if err != nil {
		return fmt.Errorf("load track: %w", err)
	}

	// Build export spec
	spec := batchSpec{
		Track: batchTrack{
			Title:    trackNode.Title,
			Priority: string(trackNode.Priority),
		},
	}
	for _, step := range trackNode.Steps {
		spec.Track.Steps = append(spec.Track.Steps, step.Description)
	}

	// Load linked features
	features := loadLinkedByType(dir, "features", trackID)
	idToTitle := make(map[string]string)
	for _, f := range features {
		idToTitle[f.ID] = f.Title
	}

	for _, f := range features {
		item := batchItem{
			Title:    f.Title,
			Priority: string(f.Priority),
		}
		for _, s := range f.Steps {
			item.Steps = append(item.Steps, s.Description)
		}
		if edges, ok := f.Edges[string(models.RelBlockedBy)]; ok {
			for _, e := range edges {
				if title, ok := idToTitle[e.TargetID]; ok {
					item.BlockedBy = append(item.BlockedBy, title)
				}
			}
		}
		spec.Features = append(spec.Features, item)
	}

	// Export non-blocked_by edges as explicit links
	for _, f := range features {
		for rel, edges := range f.Edges {
			if rel == string(models.RelBlockedBy) {
				continue
			}
			for _, e := range edges {
				toTitle := idToTitle[e.TargetID]
				if toTitle == "" {
					toTitle = string(e.TargetID)
				}
				spec.Links = append(spec.Links, batchLink{
					From: f.Title,
					To:   toTitle,
					Rel:  string(rel),
				})
			}
		}
	}

	out, err := yaml.Marshal(&spec)
	if err != nil {
		return fmt.Errorf("marshal YAML: %w", err)
	}
	fmt.Print(string(out))
	return nil
}
