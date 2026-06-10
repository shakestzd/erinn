package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/shakestzd/wipnote/core/arch"
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
	store, err := arch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	card := &arch.Card{
		Name:       slug,
		Kind:       arch.Kind(kind),
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
		RunE: func(_ *cobra.Command, args []string) error {
			return runArchEdit(args[0], kind, paths, verifiedAt, links, body)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "New card kind")
	cmd.Flags().StringSliceVar(&paths, "paths", nil, "New glob patterns (replaces existing)")
	cmd.Flags().StringVar(&verifiedAt, "verified-at", "", "New verified-at git SHA")
	cmd.Flags().StringSliceVar(&links, "links", nil, "New linked work item IDs (replaces existing)")
	cmd.Flags().StringVar(&body, "body", "", "New card body")
	return cmd
}

func runArchEdit(slug, kind string, paths []string, verifiedAt string, links []string, body string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := arch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	card, err := store.Get(slug)
	if err != nil {
		return err
	}
	if kind != "" {
		card.Kind = arch.Kind(kind)
	}
	if len(paths) > 0 {
		card.Paths = paths
	}
	if verifiedAt != "" {
		card.VerifiedAt = verifiedAt
	}
	if len(links) > 0 {
		card.Links = links
	}
	if body != "" {
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
		RunE: func(_ *cobra.Command, _ []string) error {
			return runArchList(all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include superseded/retired cards")
	return cmd
}

func runArchList(all bool) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	store, err := arch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	cards, err := store.List(all)
	if err != nil {
		return err
	}
	if len(cards) == 0 {
		fmt.Println("No arch cards found.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
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
	store, err := arch.NewStore(wipnoteDir)
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
	store, err := arch.NewStore(wipnoteDir)
	if err != nil {
		return err
	}
	card, err := store.Get(slug)
	if err != nil {
		return err
	}
	if err := arch.Validate(card); err != nil {
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
	store, err := arch.NewStore(wipnoteDir)
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
	store, err := arch.NewStore(wipnoteDir)
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
