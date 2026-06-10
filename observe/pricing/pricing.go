// Package pricing derives a USD cost estimate from per-token counts and a
// model identifier. It ships with an embedded snapshot of per-model rates
// (models.json) filtered from the LiteLLM community-maintained pricing
// table. The `wipnote pricing update` command refreshes the snapshot
// from upstream.
//
// Claude Code emits cost_usd directly on its api_request events; for that
// harness we prefer the vendor-reported value. Codex CLI and Gemini CLI do
// not emit cost, so we derive it here from token counts.
//
// Design rules:
//   - Zero dependencies beyond stdlib + internal/otel.
//   - Lookups must never return a partial Pricing (all fields populated or
//     the lookup fails). A missing cache rate is treated as 0.10 × input
//     for Anthropic models (OTel-documented rate) and 0 for others.
//   - Deriving a cost for an unknown model returns (0, false) — callers
//     record CostSourceUnknown rather than guessing.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/shakestzd/wipnote/observe/otel"
)

//go:embed models.json
var modelsJSON []byte

// Pricing holds the per-token USD rates for one model. Zero values mean
// "not reported by this pricing row" — cost derivation treats absent
// cache rates as 0, not as free tokens.
type Pricing struct {
	Provider                    string  `json:"provider"`
	InputCostPerToken           float64 `json:"input_cost_per_token"`
	OutputCostPerToken          float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost float64 `json:"cache_creation_input_token_cost"`
}

// Table is a lookup of model identifier → Pricing. Models appear by exact
// name, with a fallback normalization for dated Anthropic variants
// (claude-haiku-4-5-20251001 → claude-haiku-4-5).
type Table struct {
	models map[string]Pricing
}

var (
	defaultTable    *Table
	defaultTableErr error
	once            sync.Once
)

// Default returns the process-wide default table loaded from the embedded
// models.json. The first call parses the JSON; subsequent calls are free.
func Default() (*Table, error) {
	once.Do(func() {
		defaultTable, defaultTableErr = Load(modelsJSON)
	})
	return defaultTable, defaultTableErr
}

// Load parses a models.json-shaped byte slice into a Table. Used by the
// `wipnote pricing update` command to validate a freshly-fetched file
// before writing it to disk, and by tests that supply custom tables.
func Load(data []byte) (*Table, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse models.json: %w", err)
	}
	t := &Table{models: make(map[string]Pricing, len(raw))}
	for model, msg := range raw {
		if strings.HasPrefix(model, "_") {
			continue // metadata entries are underscore-prefixed
		}
		var p Pricing
		if err := json.Unmarshal(msg, &p); err != nil {
			return nil, fmt.Errorf("parse pricing for %q: %w", model, err)
		}
		// Validate: input + output rates are mandatory; cache rates optional.
		if p.InputCostPerToken < 0 || p.OutputCostPerToken < 0 {
			return nil, fmt.Errorf("negative rate for %q", model)
		}
		t.models[model] = p
	}
	return t, nil
}

// Lookup returns pricing for the given model identifier. Returns ok=false
// if the model is unknown, in which case callers should record the cost
// as CostSourceUnknown and leave CostUSD at 0.
//
// Normalization: dated Anthropic variants (e.g. claude-haiku-4-5-20251001)
// fall back to their undated canonical (claude-haiku-4-5) if the dated
// variant is not in the table. This avoids shipping every date stamp.
func (t *Table) Lookup(model string) (Pricing, bool) {
	if p, ok := t.models[model]; ok {
		return p, true
	}
	// Fallback: strip trailing -YYYYMMDD date stamp (Anthropic convention).
	if base := stripDateSuffix(model); base != model {
		if p, ok := t.models[base]; ok {
			return p, true
		}
	}
	return Pricing{}, false
}

// stripDateSuffix removes a trailing -YYYYMMDD from an Anthropic model ID.
// Returns the input unchanged if no such suffix is present.
func stripDateSuffix(model string) string {
	i := strings.LastIndex(model, "-")
	if i < 0 || len(model)-i-1 != 8 {
		return model
	}
	for _, r := range model[i+1:] {
		if r < '0' || r > '9' {
			return model
		}
	}
	return model[:i]
}

// Derive computes the USD cost for a token mix on the given model. Returns
// (cost, CostSourceDerived, true) on success or (0, CostSourceUnknown, false)
// if the model is unknown.
//
// Thought and reasoning tokens are billed at output rate (Gemini's thought
// tokens and Codex's reasoning tokens are charged the same as output). Tool
// tokens are billed at input rate.
//
// This is the fallback path — Claude Code emits cost_usd directly and the
// receiver should record that verbatim with CostSourceVendor. Derive is for
// Codex and Gemini, and for verification tests against Claude's
// vendor-reported values.
func (t *Table) Derive(model string, tok otel.TokenCounts) (float64, otel.CostSource, bool) {
	p, ok := t.Lookup(model)
	if !ok {
		return 0, otel.CostSourceUnknown, false
	}
	cost := float64(tok.Input)*p.InputCostPerToken +
		float64(tok.Output)*p.OutputCostPerToken +
		float64(tok.CacheRead)*p.CacheReadInputTokenCost +
		float64(tok.CacheCreation)*p.CacheCreationInputTokenCost +
		float64(tok.Thought)*p.OutputCostPerToken +
		float64(tok.Reasoning)*p.OutputCostPerToken +
		float64(tok.Tool)*p.InputCostPerToken
	return cost, otel.CostSourceDerived, true
}

// Models returns the sorted list of model identifiers the table knows
// about. Used by `wipnote pricing list` and tests.
func (t *Table) Models() []string {
	out := make([]string, 0, len(t.models))
	for k := range t.models {
		out = append(out, k)
	}
	// Simple stable order; callers sort if needed.
	return out
}
