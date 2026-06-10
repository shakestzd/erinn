package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	corearch "github.com/shakestzd/wipnote/core/arch"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/storage"
	iarch "github.com/shakestzd/wipnote/internal/arch"
	"github.com/spf13/cobra"
)

// archCmd builds the top-level "wipnote arch" command group.
func archCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "arch",
		Short: "Manage architectural memory cards",
	}
	cmd.AddCommand(archAddCmd())
	cmd.AddCommand(archEditCmd())
	cmd.AddCommand(archListCmd())
	cmd.AddCommand(archShowCmd())
	cmd.AddCommand(archValidateCmd())
	cmd.AddCommand(archDeprecateCmd())
	cmd.AddCommand(archResolveCmd())
	return cmd
}

// archAddCmd creates a new arch card.
func archAddCmd() *cobra.Command {
	var (
		kind        string
		paths       []string
		verifiedAt  string
		links       []string
		createdBy   string
		body        string
	)
	cmd := &cobra.Command{
		Use:   "add <slug>",
		Short: "Create a new architectural memory card",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runArchAdd(args[0], kind, paths, verifiedAt, links, createdBy, body)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Card kind: subsystem-map, invariant, hazard, decision (required)")
	cmd.Flags().StringSliceVar(&paths, "paths", nil, "Glob patterns for affected paths (repeatable)")
	cmd.Flags().StringVar(&verifiedAt, "verified-at", "", "Git SHA at which this card was last verified")
	cmd.Flags().StringSliceVar(&links, "links", nil, "Work item IDs this card is linked to (repeatable)")
	cmd.Flags().StringVar(&createdBy, "created-by", "", "Author identifier (required)")
	cmd.Flags().StringVar(&body, "body", "", "Card body (markdown, max 120 words, required)")
	return cmd
}

func runArchAdd(slug, kind string, paths []string, verifiedAt string, links []string, createdBy, body string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	card := &corearch.Card{
		Name:       slug,
		Kind:       corearch.Kind(kind),
		Paths:      paths,
		VerifiedAt: verifiedAt,
		Links:      links,
		CreatedBy:  createdBy,
		Body:       body,
	}
	if err := store.Create(card); err != nil {
		return err
	}
	fmt.Printf("Created arch card: %s\n", slug)
	return nil
}

// archEditCmd updates an existing arch card.
func archEditCmd() *cobra.Command {
	var (
		kind       string
		paths      []string
		verifiedAt string
		links      []string
		body       string
	)
	cmd := &cobra.Command{
		Use:   "edit <slug>",
		Short: "Update an existing architectural memory card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runArchEdit(cmd, args[0], kind, paths, verifiedAt, links, body)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "New card kind")
	cmd.Flags().StringSliceVar(&paths, "paths", nil, "New glob patterns (replaces existing; use --paths= to clear)")
	cmd.Flags().StringVar(&verifiedAt, "verified-at", "", "New verified-at git SHA (use --verified-at= to clear)")
	cmd.Flags().StringSliceVar(&links, "links", nil, "New linked work item IDs (replaces existing; use --links= to clear)")
	cmd.Flags().StringVar(&body, "body", "", "New card body")
	return cmd
}

func runArchEdit(cmd *cobra.Command, slug, kind string, paths []string, verifiedAt string, links []string, body string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	card, err := store.Get(slug)
	if err != nil {
		return err
	}
	// Use Changed() to allow explicit clearing of optional fields (e.g. --paths= clears paths).
	if cmd.Flags().Changed("kind") {
		card.Kind = corearch.Kind(kind)
	}
	if cmd.Flags().Changed("paths") {
		card.Paths = paths
	}
	if cmd.Flags().Changed("verified-at") {
		card.VerifiedAt = verifiedAt
	}
	if cmd.Flags().Changed("links") {
		card.Links = links
	}
	if cmd.Flags().Changed("body") {
		card.Body = body
	}
	if err := store.Update(card); err != nil {
		return err
	}
	fmt.Printf("Updated arch card: %s\n", slug)
	return nil
}

// archListCmd lists arch cards.
func archListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List architectural memory cards",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runArchList(cmd.OutOrStdout(), all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include superseded/retired cards")
	return cmd
}

func runArchList(out io.Writer, all bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	cards, err := store.List(all)
	if err != nil {
		return err
	}
	if len(cards) == 0 {
		fmt.Fprintln(out, "No arch cards found.")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tKIND\tSTATUS\tPATHS")
	for _, c := range cards {
		status := "active"
		if c.IsRetired() {
			if c.SupersededBy != "" {
				status = "superseded:" + c.SupersededBy
			} else {
				status = "retired"
			}
		}
		pathsStr := strings.Join(c.Paths, ", ")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, c.Kind, status, pathsStr)
	}
	return w.Flush()
}

// archShowCmd prints one card.
func archShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <slug>",
		Short: "Print an architectural memory card",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runArchShow(args[0])
		},
	}
}

func runArchShow(slug string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	card, err := store.Get(slug)
	if err != nil {
		return err
	}
	fmt.Printf("name:          %s\n", card.Name)
	fmt.Printf("kind:          %s\n", card.Kind)
	fmt.Printf("created_by:    %s\n", card.CreatedBy)
	fmt.Printf("verified_at:   %s\n", card.VerifiedAt)
	if len(card.Paths) > 0 {
		fmt.Printf("paths:         %s\n", strings.Join(card.Paths, ", "))
	}
	if len(card.Links) > 0 {
		fmt.Printf("links:         %s\n", strings.Join(card.Links, ", "))
	}
	if card.SupersededBy != "" {
		fmt.Printf("superseded_by: %s\n", card.SupersededBy)
	}
	if card.Retired {
		fmt.Printf("status:        retired\n")
	}
	if card.Body != "" {
		fmt.Printf("\n%s\n", card.Body)
	}
	return nil
}

// archValidateCmd validates one or all cards.
func archValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [slug]",
		Short: "Validate one or all architectural memory cards",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				return runArchValidateOne(args[0])
			}
			return runArchValidateAll()
		},
	}
}

func runArchValidateOne(slug string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	card, err := store.Get(slug)
	if err != nil {
		return err
	}
	if err := corearch.Validate(card); err != nil {
		return fmt.Errorf("card %s: %w", slug, err)
	}
	fmt.Printf("arch card %s: ok\n", slug)
	return nil
}

func runArchValidateAll() error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	errs, err := store.ValidateAll()
	if err != nil {
		return err
	}
	if len(errs) == 0 {
		fmt.Println("All arch cards are valid.")
		return nil
	}
	for slug, e := range errs {
		fmt.Fprintf(os.Stderr, "card %s: %v\n", slug, e)
	}
	return fmt.Errorf("%d card(s) failed validation", len(errs))
}

// archDeprecateCmd retires a card.
func archDeprecateCmd() *cobra.Command {
	var supersededBy string
	cmd := &cobra.Command{
		Use:   "deprecate <slug>",
		Short: "Retire an architectural memory card",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runArchDeprecate(args[0], supersededBy)
		},
	}
	cmd.Flags().StringVar(&supersededBy, "superseded-by", "", "Slug of the card that supersedes this one")
	return cmd
}

func runArchDeprecate(slug, supersededBy string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	if err := store.Deprecate(slug, supersededBy); err != nil {
		return err
	}
	if supersededBy != "" {
		fmt.Printf("Deprecated arch card %s (superseded by %s)\n", slug, supersededBy)
	} else {
		fmt.Printf("Retired arch card %s\n", slug)
	}
	return nil
}

// archResolveCmd resolves architectural memory cards for a set of paths or a
// work-item ID.
func archResolveCmd() *cobra.Command {
	var (
		forFlag string
		budget  int
	)
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve architectural memory cards for given paths or a work-item ID",
		Long: `Match arch cards whose path globs overlap with the given file paths or the
files attributed to a work-item ID. Output is plain text ordered by kind
priority (hazard > invariant > subsystem-map > decision), annotated with drift
markers when verified_at has diverged from HEAD, and truncated to --budget words
with a sentinel line when cards are omitted.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runArchResolve(forFlag, budget)
		},
	}
	cmd.Flags().StringVar(&forFlag, "for", "", "Comma-separated file paths or a single work-item ID (feat-/bug-/spk-)")
	cmd.Flags().IntVar(&budget, "budget", 450, "Word budget for output (default 450 ≈ 600 tokens)")
	_ = cmd.MarkFlagRequired("for")
	return cmd
}

func runArchResolve(forFlag string, budget int) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	cards, err := store.List(false) // excludes retired/superseded
	if err != nil {
		return err
	}

	paths, err := resolveInputPaths(forFlag, wipnoteDir)
	if err != nil {
		return err
	}

	matched := iarch.MatchCards(cards, paths)
	if len(matched) == 0 {
		fmt.Println("No arch cards matched.")
		return nil
	}

	repoRoot := filepath.Dir(wipnoteDir)
	driftMap := iarch.DetectDrift(matched, repoRoot, iarch.GitDiffNameOnly)

	out := iarch.FormatOutput(matched, budget, driftMap)
	fmt.Print(out)
	return nil
}

// resolveInputPaths derives the file paths to match against.
// If forFlag is a work-item ID, it queries the DB for attributed files.
// Otherwise, it splits forFlag on commas and treats each element as a file path.
func resolveInputPaths(forFlag, wipnoteDir string) ([]string, error) {
	if iarch.LooksLikeWorkItemID(forFlag) {
		return resolveWorkItemPaths(forFlag, wipnoteDir)
	}
	raw := strings.Split(forFlag, ",")
	paths := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// resolveWorkItemPaths looks up the files attributed to a work-item ID in the
// SQLite feature_files table. Returns the deduplicated file paths.
func resolveWorkItemPaths(workItemID, wipnoteDir string) ([]string, error) {
	projectDir := filepath.Dir(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.OpenReadOnlyMigrated(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	var files []models.FeatureFile
	if err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
		f, derr := dbpkg.ListFilesByFeature(database, workItemID)
		if derr != nil {
			return derr
		}
		files = f
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list files for %s: %w", workItemID, err)
	}

	seen := make(map[string]bool, len(files))
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if !seen[f.FilePath] {
			seen[f.FilePath] = true
			paths = append(paths, f.FilePath)
		}
	}
	return paths, nil
}

