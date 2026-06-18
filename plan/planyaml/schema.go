// Package planyaml defines the YAML data model for wipnote plans.
// It provides Go structs with YAML tags matching the canonical schema
// defined in prototypes/sample_plan.yaml, plus Load/Save/NewPlan helpers.
package planyaml

// PlanYAML is the top-level plan document.
type PlanYAML struct {
	Meta      PlanMeta       `yaml:"meta"`
	Design    PlanDesign     `yaml:"design"`
	Slices    []PlanSlice    `yaml:"slices"`
	Questions []PlanQuestion `yaml:"questions"`
	Critique  *PlanCritique  `yaml:"critique,omitempty"`
}

// PlanMeta holds plan identity and lifecycle metadata.
type PlanMeta struct {
	ID          string `yaml:"id"`
	TrackID     string `yaml:"track_id,omitempty"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	CreatedAt   string `yaml:"created_at"`
	Status      string `yaml:"status"` // draft | review | finalized | active | completed
	Priority    string `yaml:"priority,omitempty"`

	// SchemaVersion identifies which plan-authoring model produced this plan.
	// Empty = legacy (pre-triage). "v3" = triage-gated interview model with
	// decisions_notes as the rationale spine.
	//
	// Empty SchemaVersion preserves back-compat: legacy plans validate under
	// pre-v3 rules. Setting SchemaVersion="v3" enables strict validation:
	// decisions_notes becomes required for all standard/complex slices, even
	// when slice.Complexity is left empty (defaulting to "standard").
	SchemaVersion string `yaml:"schema_version,omitempty"`

	CreatedBy string `yaml:"created_by,omitempty"`
	Version   int    `yaml:"version"`
}

// PlanDesign captures the problem statement, goals, constraints, and
// human approval for the design section.
type PlanDesign struct {
	Problem     string   `yaml:"problem"`
	Goals       []string `yaml:"goals"`
	Constraints []string `yaml:"constraints"`
	Approved    bool     `yaml:"approved"`
	Comment     string   `yaml:"comment"`
}

// PlanSlice is a vertical delivery slice with metadata for effort,
// risk, dependencies, and human approval. V2 adds slice-local lifecycle
// states, questions, and critic revisions so each slice is an independently
// reviewable executable spec card.
//
// Section-naming contract for plan_feedback (load-bearing — slice-4 extends
// validSectionRe to accept the new pattern):
//
//	slice-<num>                        — slice-level approval/state
//	slice-<num>-question-<question-id> — slice-local question answer
//
// Global sections ("design", "questions", existing keys) are unchanged.
// The UNIQUE(plan_id, section, action, question_id) constraint in
// internal/db/plan_feedback.go already accommodates these formats; slice-4
// extends cmd/wipnote/api_plans.go validSectionRe to accept the new pattern.
//
// Agents should write multiline what/why/tests fields using YAML literal
// blocks (|) for Markdown-capable content — see the planning skill for examples.
//
// The Complexity field (added in the triage-gated interview redesign) drives
// validator branching for the per-slice required-field checks. Empty string is
// treated as "standard" for back-compat with v2 plans written before the field
// existed — see plan/planyaml/validate.go for the branching table.
type PlanSlice struct {
	ID        string   `yaml:"id"`
	FeatureID string   `yaml:"feature_id,omitempty"` // populated after plan finalize
	Num       int      `yaml:"num"`
	Title     string   `yaml:"title"`
	What      string   `yaml:"what"`
	Why       string   `yaml:"why"`
	Files     []string `yaml:"files"`
	Deps      []int    `yaml:"deps"`
	DoneWhen  []string `yaml:"done_when"`
	Effort    string   `yaml:"effort"` // S | M | L
	Risk      string   `yaml:"risk"`   // Low | Med | High
	Tests     string   `yaml:"tests"`

	// Complexity is the triage-driven classification for a slice. Drives validator
	// branching: trivial relaxes what/done_when/tests requirements; standard and
	// complex require decisions_notes (>=50 chars) and (for complex) >=1
	// slice-local question with an answer.
	//
	// Empty string is treated as "standard" for back-compat with v2 plans written
	// before this field existed.
	Complexity string `yaml:"complexity,omitempty"`

	Approved bool   `yaml:"approved"`
	Comment  string `yaml:"comment"`

	// V2 lifecycle fields (additive — legacy plans omit these and remain valid).
	ApprovalStatus  string `yaml:"approval_status,omitempty"`  // pending | approved | rejected | changes_requested
	ExecutionStatus string `yaml:"execution_status,omitempty"` // not_started | promoted | in_progress | done | blocked | superseded

	// V2 slice-local spec fields.
	Questions       []SliceQuestion  `yaml:"questions,omitempty"`        // slice-local open questions
	CriticRevisions []CriticRevision `yaml:"critic_revisions,omitempty"` // critic feedback specific to this slice

	// DecisionsNotes is free-text Markdown captured by `wipnote plan
	// elicit-decisions` (typically Scope/Decisions/Context). Slice 1's
	// `wipnote spec generate --insert` weaves this prose verbatim into the
	// generated spec's `## Decisions` section. Free text — not a typed schema.
	// Empty/absent renders no Decisions section.
	DecisionsNotes string `yaml:"decisions_notes,omitempty"`

	// Blocks is an OPTIONAL flat list of structured visual blocks attached to
	// the slice (the native equivalent of BuilderIO's block catalog). It is
	// ADDITIVE-OPTIONAL: legacy plans omit it entirely and remain valid, and no
	// schema_version bump is required (validate.go only enumerates meta enums,
	// never the slice field set). Blocks render in declared order. Each entry is
	// keyed by Type; per-type required fields are defined by BlockCatalog (the
	// single source of truth shared with `wipnote plan blocks`). Shapes are
	// validated ONLY when a block is present — see validate.go.
	Blocks []SliceBlock `yaml:"blocks,omitempty"`
}

// SliceBlock is one structured visual block on a slice. The schema is
// deliberately generic so new block types can be added to BlockCatalog without
// changing this struct:
//
//   - Type    selects the block kind (data-model | api-endpoint | file-tree |
//     wireframe). It MUST be a key in BlockCatalog.
//   - Title   is an optional human-readable heading.
//   - Fields  holds scalar key/value content (e.g. wireframe html, data-model
//     name). Per-type required keys are declared by BlockCatalog[Type].Fields.
//   - Rows    holds tabular content (e.g. data-model columns, api-endpoint
//     params). Required when BlockCatalog[Type].RequiresRows is true.
//   - Entries holds an ordered string list (e.g. file-tree paths). Required
//     when BlockCatalog[Type].RequiresEntries is true.
//
// Only one of Rows/Entries is typically used per type; both are optional in the
// struct and gated by the catalog so unused fields omitempty-out of the YAML.
type SliceBlock struct {
	Type    string              `yaml:"type"`
	Title   string              `yaml:"title,omitempty"`
	Fields  map[string]string   `yaml:"fields,omitempty"`
	Rows    []map[string]string `yaml:"rows,omitempty"`
	Entries []string            `yaml:"entries,omitempty"`
}

// BlockSpec describes the required shape of one block Type. It is consumed by
// both validate.go (to reject malformed blocks when present) and `wipnote plan
// blocks` (to print the supported vocabulary), so the vocabulary has a single
// source of truth and never drifts between the validator and the catalog
// command.
type BlockSpec struct {
	// Type is the catalog key (matches SliceBlock.Type).
	Type string
	// Description is a one-line human summary shown by `wipnote plan blocks`.
	Description string
	// Fields lists the required scalar keys in SliceBlock.Fields.
	Fields []string
	// RowKeys, when non-empty, lists the keys every entry in SliceBlock.Rows
	// must carry. Implies RequiresRows.
	RowKeys []string
	// RequiresRows requires at least one entry in SliceBlock.Rows.
	RequiresRows bool
	// RequiresEntries requires at least one entry in SliceBlock.Entries.
	RequiresEntries bool
}

// BlockCatalog is the single source of truth for the supported block
// vocabulary. It is intentionally a function (not a frozen package-level slice
// referenced directly by a renderer) so the catalog command remains the one
// authoritative enumeration of types + required fields — mirroring BuilderIO's
// dynamic get-plan-blocks contract ("tags drift, do not memorize"). The
// renderer and validator must read this catalog rather than hardcoding tags.
//
// Block types (the wipnote-native vocabulary):
//
//	data-model   — an entity/table with named typed columns (Rows: name/type)
//	api-endpoint — an HTTP route with method+path and request/response params
//	file-tree    — an ordered list of file paths touched by the slice
//	wireframe    — an HTML/CSS sketch using design tokens (no raw colors)
func BlockCatalog() []BlockSpec {
	return []BlockSpec{
		{
			Type:         "data-model",
			Description:  "An entity/table with named, typed columns.",
			Fields:       []string{"name"},
			RowKeys:      []string{"name", "type"},
			RequiresRows: true,
		},
		{
			Type:         "api-endpoint",
			Description:  "An HTTP route (method + path) with request/response params.",
			Fields:       []string{"method", "path"},
			RowKeys:      []string{"name", "type"},
			RequiresRows: false,
		},
		{
			Type:            "file-tree",
			Description:     "An ordered list of file paths the slice touches.",
			RequiresEntries: true,
		},
		{
			Type:        "wireframe",
			Description: "An HTML/CSS sketch built from design tokens (no raw hex/rgb colors).",
			Fields:      []string{"html"},
		},
	}
}

// blockSpecFor returns the BlockSpec for a given type and whether it is known.
func blockSpecFor(blockType string) (BlockSpec, bool) {
	for _, spec := range BlockCatalog() {
		if spec.Type == blockType {
			return spec, true
		}
	}
	return BlockSpec{}, false
}

// SliceQuestion is an open question scoped to a single slice. It supports two
// forms:
//
//   - Minimal form: {id, text, answer} — freeform answer, no options
//   - Structured form: {id, text, description, recommended, options[], answer} —
//     mirrors PlanQuestion; the dashboard highlights the recommended option
//
// When options are present, answer should be one of the option keys (or empty
// if unanswered). When options are absent, answer is a freeform string.
//
// The section key for plan_feedback responses is:
//
//	slice-<num>-question-<id>
type SliceQuestion struct {
	ID          string           `yaml:"id"`
	Text        string           `yaml:"text"`
	Description string           `yaml:"description,omitempty"`
	Recommended string           `yaml:"recommended,omitempty"` // must match a key in Options when Options non-empty
	Options     []QuestionOption `yaml:"options,omitempty"`
	Answer      string           `yaml:"answer,omitempty"` // option key or freeform; empty = unanswered
}

// CriticRevision records a critic's feedback item scoped to a specific slice.
// Source identifies the reviewer (e.g. "haiku", "opus"), Severity is a
// free-form label (e.g. "HIGH", "LOW", "DANGER"), and Summary is a
// one-line description of the finding.
type CriticRevision struct {
	Source   string `yaml:"source"`
	Severity string `yaml:"severity"`
	Summary  string `yaml:"summary"`
}

// PlanQuestion is an open design question with options and an optional answer.
type PlanQuestion struct {
	ID          string           `yaml:"id"`
	Text        string           `yaml:"text"`
	Description string           `yaml:"description"`
	Recommended string           `yaml:"recommended,omitempty"`
	Options     []QuestionOption `yaml:"options"`
	Answer      *string          `yaml:"answer"` // nil = unanswered → "answer: null"
}

// QuestionOption is a selectable choice for a PlanQuestion.
type QuestionOption struct {
	Key   string `yaml:"key"`
	Label string `yaml:"label"`
}

// PlanCritique holds the multi-reviewer critique section.
type PlanCritique struct {
	ReviewedAt  string               `yaml:"reviewed_at" json:"reviewed_at"`
	Reviewers   []string             `yaml:"reviewers" json:"reviewers"`
	Assumptions []CritiqueAssumption `yaml:"assumptions" json:"assumptions"`
	Critics     []CriticSection      `yaml:"critics" json:"critics"`
	Risks       []CritiqueRisk       `yaml:"risks" json:"risks"`
	Synthesis   string               `yaml:"synthesis" json:"synthesis"`
}

// CritiqueAssumption is a single assumption with verification status.
type CritiqueAssumption struct {
	ID       string `yaml:"id" json:"id"`
	Status   string `yaml:"status" json:"status"` // verified|plausible|unverified|questionable|falsified
	Text     string `yaml:"text" json:"text"`
	Evidence string `yaml:"evidence" json:"evidence"`
}

// CriticSection groups critic feedback under a titled reviewer.
type CriticSection struct {
	Title    string             `yaml:"title" json:"title"`
	Sections []CriticSubsection `yaml:"sections" json:"sections"`
}

// CriticSubsection is a heading with a list of critic items.
type CriticSubsection struct {
	Heading string       `yaml:"heading" json:"heading"`
	Items   []CriticItem `yaml:"items" json:"items"`
}

// CriticItem is a single badged feedback entry.
type CriticItem struct {
	Badge string `yaml:"badge" json:"badge"`
	Kind  string `yaml:"kind" json:"kind"` // success|warn|danger|info
	Text  string `yaml:"text" json:"text"`
}

// CritiqueRisk records a risk with severity and mitigation strategy.
type CritiqueRisk struct {
	Risk       string `yaml:"risk" json:"risk"`
	Severity   string `yaml:"severity" json:"severity"` // High|Medium|Low
	Mitigation string `yaml:"mitigation" json:"mitigation"`
}
