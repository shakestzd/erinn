package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

// planBlocksCmd returns the cobra command for "plan blocks". It prints the
// supported visual-block vocabulary (types + required fields) — wipnote's
// local-first equivalent of BuilderIO's dynamic get-plan-blocks. planyaml's
// BlockCatalog is the single source of truth, so this command never hardcodes a
// frozen tag list.
func planBlocksCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "blocks",
		Short: "List the supported plan visual-block types and required fields",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runPlanBlocks(os.Stdout, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the block catalog as JSON")
	return cmd
}

// blockCatalogEntry is the JSON shape for one catalog entry.
type blockCatalogEntry struct {
	Type            string   `json:"type"`
	Description     string   `json:"description"`
	RequiredFields  []string `json:"required_fields,omitempty"`
	RequiredRowKeys []string `json:"required_row_keys,omitempty"`
	RequiresRows    bool     `json:"requires_rows"`
	RequiresEntries bool     `json:"requires_entries"`
}

// runPlanBlocks renders the block catalog to w, either as JSON or human text.
func runPlanBlocks(w io.Writer, asJSON bool) error {
	catalog := planyaml.BlockCatalog()
	if asJSON {
		entries := make([]blockCatalogEntry, 0, len(catalog))
		for _, s := range catalog {
			entries = append(entries, blockCatalogEntry{
				Type:            s.Type,
				Description:     s.Description,
				RequiredFields:  s.Fields,
				RequiredRowKeys: s.RowKeys,
				RequiresRows:    s.RequiresRows,
				RequiresEntries: s.RequiresEntries,
			})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	return writeBlockCatalogText(w, catalog)
}

// writeBlockCatalogText prints the catalog in a human-readable form.
func writeBlockCatalogText(w io.Writer, catalog []planyaml.BlockSpec) error {
	if _, err := fmt.Fprintf(w, "Supported plan block types (%d):\n\n", len(catalog)); err != nil {
		return err
	}
	for _, s := range catalog {
		if _, err := fmt.Fprintf(w, "  %s\n    %s\n", s.Type, s.Description); err != nil {
			return err
		}
		if err := writeBlockRequirements(w, s); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

// writeBlockRequirements prints the required-field lines for one block spec.
func writeBlockRequirements(w io.Writer, s planyaml.BlockSpec) error {
	if len(s.Fields) > 0 {
		if _, err := fmt.Fprintf(w, "    required fields: %s\n", strings.Join(s.Fields, ", ")); err != nil {
			return err
		}
	}
	if s.RequiresRows || len(s.RowKeys) > 0 {
		req := "optional"
		if s.RequiresRows {
			req = "required"
		}
		if _, err := fmt.Fprintf(w, "    rows (%s), each with keys: %s\n", req, strings.Join(s.RowKeys, ", ")); err != nil {
			return err
		}
	}
	if s.RequiresEntries {
		if _, err := fmt.Fprintln(w, "    required: entries (list of strings)"); err != nil {
			return err
		}
	}
	return nil
}
