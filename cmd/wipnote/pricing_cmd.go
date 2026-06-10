package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/observe/pricing"
	"github.com/spf13/cobra"
)

// litellmRawURL is the raw JSON feed of BerriAI/litellm's model pricing
// table. The update command fetches from here, filters to the models we
// track, and rewrites the embedded models.json.
const litellmRawURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

func pricingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pricing",
		Short: "Manage the embedded model pricing snapshot used for cost derivation",
		Long: `wipnote derives USD cost estimates for Codex and Gemini OTel signals
from token counts × per-model rates. Rates ship embedded in the binary
(internal/pricing/models.json). Use these subcommands to list the current
snapshot or refresh it from upstream (LiteLLM).`,
	}
	cmd.AddCommand(pricingListCmd())
	cmd.AddCommand(pricingUpdateCmd())
	cmd.AddCommand(pricingShowCmd())
	return cmd
}

func pricingListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every model in the embedded pricing snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			tbl, err := pricing.Default()
			if err != nil {
				return err
			}
			models := tbl.Models()
			sort.Strings(models)
			w := cmd.OutOrStdout()
			for _, m := range models {
				p, _ := tbl.Lookup(m)
				fmt.Fprintf(w, "%-36s %-10s in=$%.6f/tok out=$%.6f/tok cache_r=$%.6f cache_c=$%.6f\n",
					m, p.Provider,
					p.InputCostPerToken, p.OutputCostPerToken,
					p.CacheReadInputTokenCost, p.CacheCreationInputTokenCost)
			}
			return nil
		},
	}
}

func pricingShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [model]",
		Short: "Print the rate row for one model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tbl, err := pricing.Default()
			if err != nil {
				return err
			}
			p, ok := tbl.Lookup(args[0])
			if !ok {
				return fmt.Errorf("unknown model %q (try `wipnote pricing list`)", args[0])
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(p)
		},
	}
}

func pricingUpdateCmd() *cobra.Command {
	var dry bool
	var url string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Refresh the embedded pricing snapshot from upstream (LiteLLM)",
		Long: `Fetches the LiteLLM model pricing JSON, filters it to the models that
wipnote tracks, and rewrites internal/pricing/models.json. The embedded
snapshot does not take effect until the binary is rebuilt.

Use --dry-run to see what would change without writing the file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				url = litellmRawURL
			}
			upstream, err := fetchPricing(url)
			if err != nil {
				return err
			}
			current, err := pricing.Default()
			if err != nil {
				return err
			}
			filtered, changed, removed := filterUpstream(upstream, current.Models())
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "fetched %d models from upstream, %d tracked locally\n", len(upstream), len(current.Models()))
			fmt.Fprintf(w, "changed: %d, missing from upstream: %d\n", len(changed), len(removed))
			if dry {
				for _, m := range changed {
					fmt.Fprintf(w, "  ~ %s\n", m)
				}
				for _, m := range removed {
					fmt.Fprintf(w, "  ! %s (not found upstream — will keep existing row)\n", m)
				}
				return nil
			}

			outPath, err := resolveModelsJSONPath()
			if err != nil {
				return err
			}
			if err := writeModelsJSON(outPath, filtered); err != nil {
				return err
			}
			fmt.Fprintf(w, "wrote %s (rebuild binary for changes to take effect)\n", outPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "show what would change without writing")
	cmd.Flags().StringVar(&url, "url", "", "override upstream URL (default: LiteLLM raw JSON)")
	return cmd
}

// fetchPricing downloads the LiteLLM JSON and decodes the minimal subset
// we care about. The full upstream has many more fields; we keep only the
// ones used by pricing.Derive.
func fetchPricing(url string) (map[string]pricing.Pricing, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	// LiteLLM schema contains many per-model fields; we decode into a
	// flexible map and project to our Pricing struct.
	var raw map[string]map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse upstream JSON: %w", err)
	}
	out := make(map[string]pricing.Pricing, len(raw))
	for name, fields := range raw {
		if strings.HasPrefix(name, "_") {
			continue
		}
		out[name] = pricing.Pricing{
			Provider:                    asString(fields["litellm_provider"]),
			InputCostPerToken:           asFloat(fields["input_cost_per_token"]),
			OutputCostPerToken:          asFloat(fields["output_cost_per_token"]),
			CacheReadInputTokenCost:     asFloat(fields["cache_read_input_token_cost"]),
			CacheCreationInputTokenCost: asFloat(fields["cache_creation_input_token_cost"]),
		}
	}
	return out, nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asFloat(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// filterUpstream keeps the upstream row for every model we already track
// and returns (filtered, changed, removed). A "changed" entry is one whose
// rates differ from what we ship; "removed" is a tracked model that
// upstream no longer knows about.
func filterUpstream(upstream map[string]pricing.Pricing, tracked []string) (map[string]pricing.Pricing, []string, []string) {
	filtered := make(map[string]pricing.Pricing, len(tracked))
	current, _ := pricing.Default()
	var changed, removed []string
	for _, m := range tracked {
		if u, ok := upstream[m]; ok {
			filtered[m] = u
			if existing, _ := current.Lookup(m); !pricingEqual(existing, u) {
				changed = append(changed, m)
			}
		} else {
			// Preserve the existing row so a temporary upstream gap doesn't
			// erase pricing for a model we know about.
			if existing, ok := current.Lookup(m); ok {
				filtered[m] = existing
			}
			removed = append(removed, m)
		}
	}
	sort.Strings(changed)
	sort.Strings(removed)
	return filtered, changed, removed
}

func pricingEqual(a, b pricing.Pricing) bool {
	return a.Provider == b.Provider &&
		a.InputCostPerToken == b.InputCostPerToken &&
		a.OutputCostPerToken == b.OutputCostPerToken &&
		a.CacheReadInputTokenCost == b.CacheReadInputTokenCost &&
		a.CacheCreationInputTokenCost == b.CacheCreationInputTokenCost
}

// writeModelsJSON serializes the filtered table with a _metadata header and
// sorted model keys so diffs are legible in code review.
func writeModelsJSON(path string, table map[string]pricing.Pricing) error {
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Use json.Marshal on an ordered intermediate map built via json.RawMessage.
	// stdlib encoding/json sorts object keys lexicographically, which is fine
	// for code review — we don't need group-by-provider.
	out := make(map[string]any, len(table)+1)
	out["_metadata"] = map[string]string{
		"source":       litellmRawURL,
		"filtered_for": "wipnote OTel cost derivation",
		"last_updated": time.Now().UTC().Format("2006-01-02"),
		"notes":        "Costs are USD per token. Anthropic cache_read ≈ 0.10x input; cache_creation ≈ 1.25x input (5-min TTL).",
	}
	for _, k := range keys {
		out[k] = table[k]
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, append(buf, '\n'), 0o644)
}

// resolveModelsJSONPath locates the on-disk copy of models.json. It walks
// up from this file's directory to find the repo root (the one with go.mod)
// and returns internal/pricing/models.json. Works only when the CLI is run
// from inside the source tree — that's the only context where `pricing
// update` is useful.
func resolveModelsJSONPath() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot determine source path")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			p := filepath.Join(dir, "internal", "pricing", "models.json")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
			return "", fmt.Errorf("go.mod found at %s but models.json missing", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find repo root from %s", thisFile)
}
