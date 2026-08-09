package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	corearch "github.com/shakestzd/wipnote/core/arch"
	"github.com/shakestzd/wipnote/core/paths"
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
	cmd.AddCommand(archBootstrapCmd())
	cmd.AddCommand(archEditCmd())
	cmd.AddCommand(archListCmd())
	cmd.AddCommand(archShowCmd())
	cmd.AddCommand(archValidateCmd())
	cmd.AddCommand(archDeprecateCmd())
	cmd.AddCommand(archResolveCmd())
	cmd.AddCommand(archVerifyCmd())
	cmd.AddCommand(archRepairCmd())
	return cmd
}

// archAddCmd creates a new arch card.
func archAddCmd() *cobra.Command {
	var (
		kind       string
		paths      []string
		verifiedAt string
		links      []string
		createdBy  string
		body       string
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
	// Emit path warnings to stderr — advisory, do not fail.
	for _, w := range corearch.ValidatePaths(card.Paths) {
		fmt.Fprintf(os.Stderr, "WARN card %s: %s\n", slug, w)
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
	errs, warnings, err := store.ValidateAll()
	if err != nil {
		return err
	}
	// Emit warnings to stderr — advisory only, do not fail.
	for slug, ws := range warnings {
		for _, w := range ws {
			fmt.Fprintf(os.Stderr, "WARN card %s: %s\n", slug, w)
		}
	}
	if len(errs) == 0 {
		fmt.Println("All arch cards are valid.")
		return nil
	}
	for slug, e := range errs {
		fmt.Fprintf(os.Stderr, "ERROR card %s: %v\n", slug, e)
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

	resolvedPaths, pathsAttribMsg, err := resolveInputPathsWithDiag(forFlag, wipnoteDir)
	if err != nil {
		return err
	}

	matched := iarch.MatchCards(cards, resolvedPaths)
	if len(matched) == 0 {
		nameHint := cardNameHint(forFlag, cards)
		switch {
		case pathsAttribMsg == noFilesAttributedLabel:
			// A work-item ID resolved to zero attributed files: emit ONLY the
			// specific guidance, not the generic no-match line (finding 11).
			fmt.Printf("No files attributed to %s yet; try 'wipnote reindex'.\n", forFlag)
		case pathsAttribMsg != "":
			// Paths were found but no cards matched them.
			fmt.Printf("No arch cards matched for paths attributed to %s.\n", pathsAttribMsg)
		case nameHint != "":
			// bug-6c8d4731 (#158): --for only resolves by path/work-item, so a
			// bare card name always misses. Without this hint the empty result
			// reads as "the card does not exist" and can produce a false
			// dangling-reference finding.
			fmt.Println(nameHint)
		default:
			fmt.Println("No arch cards matched.")
		}
		return nil
	}

	repoRoot := filepath.Dir(wipnoteDir)
	driftMap := iarch.DetectDrift(matched, repoRoot, iarch.GitDiffNameOnly)

	out := iarch.FormatOutput(matched, budget, driftMap)
	fmt.Print(out)
	return nil
}

// cardNameHint reports whether forFlag exactly matches an existing arch
// card's name rather than a path or work-item ID. `--for` only resolves by
// path or work-item ID; a bare card name always misses, and the resulting
// bare "No arch cards matched." can be mistaken for a dangling reference
// (bug-6c8d4731, #158). Returns an actionable remediation message, or "" when
// forFlag looks like paths (comma-separated) or does not match a card name.
func cardNameHint(forFlag string, cards []*corearch.Card) string {
	trimmed := strings.TrimSpace(forFlag)
	if trimmed == "" || strings.Contains(trimmed, ",") {
		return ""
	}
	for _, c := range cards {
		if c.Name == trimmed {
			return fmt.Sprintf(
				"--for expects a path or work-item id, not a card name. Did you mean: wipnote arch show %s",
				trimmed)
		}
	}
	return ""
}

// resolveInputPathsWithDiag derives the file paths to match against and also returns a
// diagnostic label when the forFlag is a work-item ID (used to produce
// differentiated no-match messages). The label is empty for direct path inputs.
func resolveInputPathsWithDiag(forFlag, wipnoteDir string) (filePaths []string, attribLabel string, err error) {
	if iarch.LooksLikeWorkItemID(forFlag) {
		fp, label, resolveErr := resolveWorkItemPathsWithDiag(forFlag, wipnoteDir)
		return fp, label, resolveErr
	}
	raw := strings.Split(forFlag, ",")
	result := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result, "", nil
}

// resolveWorkItemPaths looks up the files attributed to a work-item ID using a
// three-tier fallback chain. Returns the deduplicated file paths.
func resolveWorkItemPaths(workItemID, wipnoteDir string) ([]string, error) {
	fp, _, err := resolveWorkItemPathsWithDiag(workItemID, wipnoteDir)
	return fp, err
}

// resolveWorkItemPathsWithDiag resolves file paths for a work-item ID and also
// returns a diagnostic label indicating which tier produced the result (used to
// differentiate "no paths attributed" from "paths found but no cards matched").
//
// Resolution is Git-derived and single-tier: `git log --grep=<workItemID>`
// finds the commits that name the item under wipnote's commit convention, and
// diff-tree expands them into the files they touched.
//
// It used to try two derived-index tiers first — feature_files rows, then
// git_commits rows — before falling back to git. Neither table is populated any
// more (feat-fc3cc9e0: the read index is a per-process projection hydrated from
// canonical artifacts, and neither table is in the hydrate set), so both tiers
// could only ever return empty and the "fallback" was in fact the only path
// that ran. They are gone rather than left as dead branches that read as
// meaningful.
//
// KNOWN NARROWING, same as the completion gate's (see
// workitem_provenance_canonical.go): a commit that implemented the item without
// naming it in its message is invisible here. feature_files also carried live
// per-tool-call touches recorded by hooks, whose only canonical record is the
// per-session events NDJSON — too large to scan synchronously. An item whose
// work was committed without its ID therefore resolves to no files.
//
// If resolution returns empty, the label is the no-files sentinel and the
// caller prints the "no files attributed" guidance.
func resolveWorkItemPathsWithDiag(workItemID, wipnoteDir string) (filePaths []string, attribLabel string, err error) {
	projectDir := filepath.Dir(wipnoteDir)

	gitHashes := gitCommitHashesForWorkItem(projectDir, workItemID)
	if len(gitHashes) > 0 {
		expanded := expandCommitFiles(projectDir, gitHashes)
		if len(expanded) > 0 {
			return expanded, workItemID, nil
		}
	}

	// No files attributed. Do NOT print here — the caller owns the single
	// no-match message so the specific guidance and the generic "No arch cards
	// matched" line are not both emitted (bug-fddf5820, finding 11). Signal the
	// no-files condition via the sentinel label so the caller can print the
	// right message exactly once.
	return nil, noFilesAttributedLabel, nil
}

// noFilesAttributedLabel is the sentinel attribLabel returned by
// resolveWorkItemPathsWithDiag when a work-item ID resolved to zero attributed
// files. The caller maps it to a single "no files attributed; try reindex"
// message (bug-fddf5820, finding 11).
const noFilesAttributedLabel = "\x00no-files-attributed"

// gitCommitHashesForWorkItem runs git log to find commits whose messages
// reference workItemID (via parenthesized or trailer convention). This is the
// sole path-resolution source for resolveWorkItemPathsWithDiag.
func gitCommitHashesForWorkItem(projectDir, workItemID string) []string {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "--format=%H", "--grep="+workItemID, "-100",
	).Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	var hashes []string
	for _, h := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		h = strings.TrimSpace(h)
		if h != "" {
			hashes = append(hashes, h)
		}
	}
	return hashes
}

// archVerifyCmd re-pins the card's verified_at to the current HEAD SHA.
func archVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <slug>",
		Short: "Re-pin a card's verified_at to current HEAD SHA",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runArchVerify(args[0])
		},
	}
}

func runArchVerify(slug string) error {
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
	repoRoot := filepath.Dir(wipnoteDir)
	sha := currentHeadSHA(repoRoot)
	card.VerifiedAt = sha
	card.UpdatedAt = time.Now().UTC()
	if err := store.Update(card); err != nil {
		return err
	}
	if sha == "" {
		fmt.Printf("Verified arch card: %s (no git repo — verified_at cleared)\n", slug)
	} else {
		fmt.Printf("Verified arch card: %s @ %s\n", slug, firstN(sha, 7))
	}
	return nil
}

// currentHeadSHA returns the full HEAD SHA for the repo at repoRoot.
// Returns "" when git is unavailable or the directory is not a git repo.
func currentHeadSHA(repoRoot string) string {
	sha, err := gitOutputIn(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// createLearningCard validates the learning body text and creates an arch card
// linked to the given work-item ID. Slug is derived from the work-item ID.
// Returns a validation error (which should ABORT completion) or a creation error.
func createLearningCard(wipnoteDir, workItemID, body, kind string, paths []string) error {
	if err := validateLearningBody(body); err != nil {
		return fmt.Errorf("--learning validation failed: %w", err)
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return fmt.Errorf("open arch store: %w", err)
	}
	// Derive slug: "learning-<workItemID>" (e.g. "learning-feat-abc12345").
	slug := "learning-" + workItemID
	if !isValidArchSlug(slug) {
		// Fallback: strip hyphens beyond the standard format.
		slug = "learning-" + sanitizeSlug(workItemID)
	}
	repoRoot := filepath.Dir(wipnoteDir)
	sha := currentHeadSHA(repoRoot)

	cardKind := corearch.KindDecision
	if kind != "" {
		cardKind = corearch.Kind(kind)
	}
	// L2 (bug-c06a0457): normalize, deduplicate, and filter paths before writing
	// the card. This defends against garbage absolute/tmp paths that may arrive
	// from pre-fix feature_files rows or from outside-repo tool inputs.
	normalizedPaths := normalizeLearningPaths(repoRoot, paths)
	card := &corearch.Card{
		Name:       slug,
		Kind:       cardKind,
		Paths:      normalizedPaths,
		VerifiedAt: sha,
		Links:      []string{workItemID},
		CreatedBy:  "wipnote-completion",
		Body:       body,
	}
	// If a card with this slug already exists, update it instead.
	if existing, getErr := store.Get(slug); getErr == nil {
		existing.Body = body
		existing.Links = appendUnique(existing.Links, workItemID)
		existing.Paths = mergeUnique(existing.Paths, normalizedPaths)
		existing.UpdatedAt = time.Now().UTC()
		return store.Update(existing)
	}
	return store.Create(card)
}

// validateLearningKind checks if the provided kind string is a valid arch card kind.
// Valid kinds are: subsystem-map, invariant, hazard, decision.
// When kind is empty, it defaults to "decision" (no error).
func validateLearningKind(kind string) error {
	if kind == "" {
		return nil // empty defaults to decision, which is valid
	}
	cardKind := corearch.Kind(kind)
	validKinds := map[corearch.Kind]bool{
		corearch.KindSubsystemMap: true,
		corearch.KindInvariant:    true,
		corearch.KindHazard:       true,
		corearch.KindDecision:     true,
	}
	if !validKinds[cardKind] {
		return fmt.Errorf("invalid --learning-kind %q; must be one of: subsystem-map, invariant, hazard, decision", kind)
	}
	return nil
}

// validateLearningBody validates just the body text for an arch card (word cap).
func validateLearningBody(body string) error {
	dummy := &corearch.Card{
		Name:      "x",
		Kind:      corearch.KindDecision,
		CreatedBy: "x",
		Body:      body,
	}
	return corearch.Validate(dummy)
}

// isValidArchSlug is a thin wrapper matching the core/arch slug rules.
func isValidArchSlug(s string) bool {
	if s == "" || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !('a' <= r && r <= 'z') && !('0' <= r && r <= '9') && r != '-' {
			return false
		}
	}
	return true
}

// sanitizeSlug replaces non-slug characters with hyphens.
func sanitizeSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// normalizeLearningPaths normalizes, deduplicates, and filters a list of file
// paths for inclusion in an arch card (bug-c06a0457 L2). It discards:
//   - paths that normalize to "unresolved:..." (outside-repo absolute paths)
//   - empty strings
//
// Relative paths that are already repo-relative pass through unchanged.
func normalizeLearningPaths(repoRoot string, rawPaths []string) []string {
	seen := make(map[string]bool, len(rawPaths))
	var out []string
	for _, p := range rawPaths {
		if p == "" {
			continue
		}
		normalized := paths.MustNormalize(p, repoRoot)
		if normalized == "" || strings.HasPrefix(normalized, "unresolved:") {
			continue
		}
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return out
}

// appendUnique appends val to slice if not already present.
func appendUnique(slice []string, val string) []string {
	for _, v := range slice {
		if v == val {
			return slice
		}
	}
	return append(slice, val)
}

// mergeUnique merges additional paths into existing, skipping duplicates.
func mergeUnique(existing, additional []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, v := range existing {
		seen[v] = true
	}
	out := append([]string(nil), existing...)
	for _, v := range additional {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// archRepairCmd implements `wipnote arch repair [--dry-run]`.
func archRepairCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Repair arch cards that contain garbage paths (absolute, unresolved:, dead worktrees)",
		Long: `For each active card, normalize every path via core/paths MustNormalize against
the repo root. Paths that remain absolute, unresolved:, or ../-escaping are
repaired by resolving the card's linked work-item IDs via the three-tier
fallback chain (feature_files -> git diff-tree -> git log --grep). Paths that
cannot be recovered are dropped. The card file is rewritten and a per-card
change summary is printed.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runArchRepair(dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would change without writing any files")
	return cmd
}

// runArchRepair is the implementation of `wipnote arch repair`.
func runArchRepair(dryRun bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	cards, err := store.List(false) // active cards only
	if err != nil {
		return err
	}
	repoRoot := filepath.Dir(wipnoteDir)

	repaired := 0
	var updateFailures int
	for _, card := range cards {
		changed, newPaths, err := repairCardPaths(card, repoRoot, wipnoteDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN repair %s: %v\n", card.Name, err)
			continue
		}
		if !changed {
			fmt.Printf("card %-40s ok (no garbage paths)\n", card.Name)
			continue
		}
		dropped := pathSetDiff(card.Paths, newPaths)
		added := pathSetDiff(newPaths, card.Paths)
		fmt.Printf("card %s:\n", card.Name)
		for _, p := range dropped {
			fmt.Printf("  - DROP  %s\n", p)
		}
		for _, p := range added {
			fmt.Printf("  + ADD   %s\n", p)
		}
		addedSet := make(map[string]bool, len(added))
		for _, p := range added {
			addedSet[p] = true
		}
		for _, p := range newPaths {
			if !addedSet[p] {
				fmt.Printf("    keep  %s\n", p)
			}
		}
		if dryRun {
			fmt.Printf("  (dry-run: no file written)\n")
			continue
		}
		card.Paths = newPaths
		if err := store.Update(card); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR repair %s: update failed: %v\n", card.Name, err)
			updateFailures++
			continue
		}
		repaired++
	}
	if dryRun {
		return nil
	}
	fmt.Printf("\nRepair complete: %d card(s) rewritten.\n", repaired)
	// bug-fddf5820 (finding 10): a store.Update failure previously only logged
	// to stderr while the command still exited 0, so scripts and the gate saw
	// a successful repair that had silently dropped writes. Surface the failure
	// as a non-nil error so the exit code reflects reality.
	if updateFailures > 0 {
		return fmt.Errorf("arch repair: %d card(s) failed to write; see errors above", updateFailures)
	}
	return nil
}

// repairCardPaths normalizes and repairs all paths on a card.
// Returns (changed bool, newPaths []string, err error).
// changed is true when newPaths differs from card.Paths.
func repairCardPaths(card *corearch.Card, repoRoot, wipnoteDir string) (bool, []string, error) {
	// First pass: normalize each path and separate clean from garbage.
	var clean []string
	var garbageExists bool
	seen := make(map[string]bool)

	for _, p := range card.Paths {
		if isGarbagePath(p) {
			garbageExists = true
			continue
		}
		// Already-relative non-garbage paths pass through; dedupe.
		normalized := paths.MustNormalize(p, repoRoot)
		if normalized == "" || strings.HasPrefix(normalized, "unresolved:") {
			garbageExists = true
			continue
		}
		if !seen[normalized] {
			seen[normalized] = true
			clean = append(clean, normalized)
		}
	}

	if !garbageExists {
		return false, card.Paths, nil
	}

	// Second pass: attempt recovery from linked work-item IDs.
	var recovered []string
	for _, linkID := range card.Links {
		fp, err := resolveWorkItemPaths(linkID, wipnoteDir)
		if err != nil {
			continue
		}
		for _, p := range fp {
			if !seen[p] && !isGarbagePath(p) {
				seen[p] = true
				recovered = append(recovered, p)
			}
		}
	}

	newPaths := append(clean, recovered...)
	// Determine changed: length differs OR any path differs.
	changed := len(newPaths) != len(card.Paths)
	if !changed {
		origSet := make(map[string]bool, len(card.Paths))
		for _, p := range card.Paths {
			origSet[p] = true
		}
		for _, p := range newPaths {
			if !origSet[p] {
				changed = true
				break
			}
		}
	}
	return changed, newPaths, nil
}

// isGarbagePath reports whether a path is an error-class garbage path
// (absolute, unresolved:, or ../ escape).
func isGarbagePath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	if strings.HasPrefix(p, "unresolved:") {
		return true
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return true
	}
	return false
}

// pathSetDiff returns elements in a that are not in b.
func pathSetDiff(a, b []string) []string {
	bSet := make(map[string]bool, len(b))
	for _, v := range b {
		bSet[v] = true
	}
	var out []string
	for _, v := range a {
		if !bSet[v] {
			out = append(out, v)
		}
	}
	return out
}

// emitDriftNudge writes a stderr nudge listing arch cards that are drift-suspect
// and whose globs overlap the given paths. It is purely advisory; any error is
// silently swallowed. Cards already nudged for their current drift key
// (card.VerifiedAt) are suppressed on subsequent calls — a card only
// resurfaces once it is re-verified and drifts again (bug-e546fae0, #156).
func emitDriftNudge(w io.Writer, touchedPaths []string, wipnoteDir string, runner iarch.DiffRunner) {
	if len(touchedPaths) == 0 || wipnoteDir == "" {
		return
	}
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		return
	}
	cards, err := store.List(false)
	if err != nil || len(cards) == 0 {
		return
	}
	matched := iarch.MatchCards(cards, touchedPaths)
	if len(matched) == 0 {
		return
	}
	repoRoot := filepath.Dir(wipnoteDir)
	driftMap := iarch.DetectDrift(matched, repoRoot, runner)

	newlyDrifted, nextNudged := dedupDrift(matched, driftMap, loadDriftNudgeCache(wipnoteDir))
	saveDriftNudgeCache(wipnoteDir, nextNudged)
	if len(newlyDrifted) == 0 {
		return
	}
	fmt.Fprintf(w, "\nArch cards may be drift-suspect (code changed since last verify):\n")
	for _, slug := range newlyDrifted {
		fmt.Fprintf(w, "  wipnote arch verify %s   # or: wipnote arch edit %s\n", slug, slug)
	}
}

// dedupDrift splits matched cards' drift keys (from driftMap) against a
// previously-nudged cache, returning the subset that is newly drift-suspect
// (never nudged, or nudged under a different key — i.e. re-verified since)
// alongside the full cache to persist for the next call. Cards absent from
// driftMap are clean and are not carried forward into the returned cache, so
// a resolved card's stale nudge entry is pruned automatically.
func dedupDrift(matched []*corearch.Card, driftMap, prevNudged map[string]string) ([]string, map[string]string) {
	nextNudged := make(map[string]string, len(driftMap))
	var newlyDrifted []string
	for _, c := range matched {
		key, isDrifted := driftMap[c.Name]
		if !isDrifted {
			continue
		}
		nextNudged[c.Name] = key
		if prevKey, hadPrev := prevNudged[c.Name]; !hadPrev || prevKey != key {
			newlyDrifted = append(newlyDrifted, c.Name)
		}
	}
	return newlyDrifted, nextNudged
}

// driftNudgeCachePath returns the project-scoped cache file recording which
// arch cards have already been surfaced by emitDriftNudge, keyed by a hash of
// wipnoteDir (mirrors statuslineCachePath so multiple projects never
// collide). Honors WIPNOTE_CACHE_DIR for test isolation.
func driftNudgeCachePath(wipnoteDir string) string {
	cacheDir := os.Getenv("WIPNOTE_CACHE_DIR")
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cacheDir = home
	}
	if wipnoteDir == "" {
		return ""
	}
	h := sha256.Sum256([]byte(filepath.Clean(wipnoteDir)))
	suffix := hex.EncodeToString(h[:4]) // 8 hex chars
	return filepath.Join(cacheDir, ".wipnote-drift-nudge-"+suffix)
}

// loadDriftNudgeCache reads the drift-nudge cache (card name -> drift key).
// Best-effort: a missing or corrupt file yields an empty map rather than an
// error, since the nudge is advisory only.
func loadDriftNudgeCache(wipnoteDir string) map[string]string {
	path := driftNudgeCachePath(wipnoteDir)
	if path == "" {
		return map[string]string{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	cache := map[string]string{}
	if err := json.Unmarshal(data, &cache); err != nil {
		return map[string]string{}
	}
	return cache
}

// saveDriftNudgeCache writes the drift-nudge cache. Best-effort: write
// failures are silently swallowed, consistent with the advisory nature of
// the nudge.
func saveDriftNudgeCache(wipnoteDir string, cache map[string]string) {
	path := driftNudgeCachePath(wipnoteDir)
	if path == "" {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = atomicWriteFile(path, data, 0o644)
}
