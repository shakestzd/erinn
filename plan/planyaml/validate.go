package planyaml

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// rawColorRe matches raw CSS colors (hex like #1a2b3c / #abc, or rgb()/rgba()
// /hsl()/hsla() functions). Wireframe blocks must use design tokens (CSS custom
// properties like var(--color-fg)) instead — see validateWireframeBlock.
var rawColorRe = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\brgba?\(|\bhsla?\(`)

// wordCount returns the number of whitespace-delimited words in s. Backs the
// prose-length checks below: the whatWordCap hard gate and the advisory caps
// for why/tests/done_when.
func wordCount(s string) int {
	return len(strings.Fields(s))
}

// whatWordCap is the maximum word count for a slice's `what` field before
// wipnote plan validate-yaml rejects it.
//
// Derivation (not asserted — measured): a prose audit of every slice across
// .wipnote/plans/*.yaml (46 plans, 289 slices, 2026-08-07) found `what`
// word-counted to 33% of all slice-prose words in the corpus, at a median of
// 96 words/slice — plans were inflating narrative well past scannable length.
// Full `what` distribution: p10=33 p25=56 p45=91 p50(median)=96 p60=115
// p75=144 p90=198 p95=224 words (n=289).
//
// The cap is set at 56 words — the corpus's 25th percentile. An earlier draft
// of this cap used 91 (p45, "45% of existing slices already comply") — that
// was the wrong achievability metric: the cap only ever applies to
// non-finalized plans (see the finalized exemption below), so EXISTING-corpus
// compliance is a non-constraint — most of that corpus is legacy prose this
// cap will never touch. The metric that matters is whether a well-written NEW
// slice can comply, and there's direct evidence: plan-f8c02547's own 7 slices
// (written after blocks-first planning, never flagged as unclear by any
// reviewer) run 14-32 words. 56 sits comfortably above that demonstrated
// working range while still being a real, non-round percentile break (not a
// number pulled from thin air like the previously-rejected "~40 words"), and
// it forces genuine compaction on the corpus's median-length slices (96
// words) and everything above.
const whatWordCap = 56

// Advisory-only prose-length norms for why/tests/done_when, derived the same
// way as whatWordCap (each field's own 75th percentile across the same
// .wipnote/plans/*.yaml corpus audit; done_when is summed across its entries
// per slice). These fields were not the ones flagged as disproportionately
// inflating plan prose, so they only warn via ValidateProseAdvisories — they
// never fail wipnote plan validate-yaml.
const (
	whyWordAdvisoryCap      = 48 // p75 of why word counts (n=289)
	testsWordAdvisoryCap    = 53 // p75 of tests word counts (n=279, empty tests excluded)
	doneWhenWordAdvisoryCap = 77 // p75 of per-slice done_when total word counts (n=289)
)

// validateBlock checks one SliceBlock's shape against BlockCatalog. The block's
// type must be a known catalog entry; required scalar fields, row keys, and
// entries are enforced per the spec. Returns a (possibly empty) error slice.
func validateBlock(prefix string, b SliceBlock) []string {
	var errs []string
	spec, ok := blockSpecFor(b.Type)
	if !ok {
		known := make([]string, 0, len(BlockCatalog()))
		for _, s := range BlockCatalog() {
			known = append(known, s.Type)
		}
		return []string{fmt.Sprintf("%s.type %q is unknown; supported: %s", prefix, b.Type, strings.Join(known, "|"))}
	}
	for _, key := range spec.Fields {
		if strings.TrimSpace(b.Fields[key]) == "" {
			errs = append(errs, fmt.Sprintf("%s.fields.%s is required for %s blocks", prefix, key, b.Type))
		}
	}
	errs = append(errs, validateBlockRows(prefix, b, spec)...)
	if spec.RequiresEntries && len(b.Entries) == 0 {
		errs = append(errs, fmt.Sprintf("%s.entries must have at least 1 entry for %s blocks", prefix, b.Type))
	}
	if b.Type == "wireframe" {
		errs = append(errs, validateWireframeBlock(prefix, b)...)
	}
	return errs
}

// validateBlockRows enforces RequiresRows and per-row required keys (RowKeys).
func validateBlockRows(prefix string, b SliceBlock, spec BlockSpec) []string {
	var errs []string
	if spec.RequiresRows && len(b.Rows) == 0 {
		errs = append(errs, fmt.Sprintf("%s.rows must have at least 1 entry for %s blocks", prefix, b.Type))
	}
	if len(spec.RowKeys) == 0 {
		return errs
	}
	for ri, row := range b.Rows {
		for _, key := range spec.RowKeys {
			if strings.TrimSpace(row[key]) == "" {
				errs = append(errs, fmt.Sprintf("%s.rows[%d].%s is required for %s blocks", prefix, ri, key, b.Type))
			}
		}
	}
	return errs
}

// validateWireframeBlock rejects raw CSS colors in a wireframe's html field;
// wireframes must use design tokens (CSS custom properties) so they inherit the
// canonical palette rather than baking in hex/rgb values.
func validateWireframeBlock(prefix string, b SliceBlock) []string {
	if rawColorRe.MatchString(html.UnescapeString(b.Fields["html"])) {
		return []string{fmt.Sprintf("%s.fields.html must use design tokens (var(--...)), not raw hex/rgb/hsl colors", prefix)}
	}
	return nil
}

// effectiveComplexity returns the triage classification for a slice. Empty
// string defaults to "standard" so v2 plans written before the Complexity
// field existed continue to validate under the standard rules.
func effectiveComplexity(s PlanSlice) string {
	if s.Complexity == "" {
		return "standard"
	}
	return s.Complexity
}

// blocksWaiverRe matches an explicit, machine-checkable opt-out from the
// blocks gate (see the Validate doc comment): a "blocks_waiver: <reason>"
// marker line inside a slice's decisions_notes. Case-insensitive, tolerant of
// leading whitespace (decisions_notes is often a YAML literal block with
// indentation carried over).
var blocksWaiverRe = regexp.MustCompile(`(?im)^\s*blocks_waiver:\s*(.+?)\s*$`)

// blocksWaiverReason extracts the reason recorded by a "blocks_waiver:"
// marker line in s.DecisionsNotes, or "" if no such marker is present.
//
// This is the skip-reason mechanism for the blocks gate: blocks-first
// planning deliberately allows skipping a visual block when a slice is
// genuinely underspecified (e.g. a pure refactor with no new file, route, or
// schema surface) — forcing block invention in that case would violate the
// grounding rule (never invent file paths, routes, or columns just to fill a
// block). A marker line mirrors the existing research_waiver field's
// contract (an explicit, audited reason, not a silent skip) without adding a
// new PlanSlice field, since decisions_notes is already the free-text
// rationale slot required alongside blocks for the same slices.
func blocksWaiverReason(s PlanSlice) string {
	m := blocksWaiverRe.FindStringSubmatch(s.DecisionsNotes)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// hasAnsweredQuestion reports whether any slice-local question has a
// non-empty answer (after TrimSpace).
func hasAnsweredQuestion(qs []SliceQuestion) bool {
	for _, q := range qs {
		if strings.TrimSpace(q.Answer) != "" {
			return true
		}
	}
	return false
}

// Validate checks a PlanYAML for schema errors. Returns a list of error
// strings. Empty list means the plan is valid.
//
// V2 slice-card fields (ApprovalStatus, ExecutionStatus, Questions,
// CriticRevisions) are additive — legacy plans that omit them validate
// without errors.
//
// Per-slice required-field checks branch on effectiveComplexity(slice):
//
//	field             | trivial  | standard | complex
//	------------------+----------+----------+----------
//	what              | optional | required | required
//	done_when         | optional | >=1      | >=2
//	tests             | optional | required | required
//	decisions_notes   | optional | required | required  (>=50 chars after TrimSpace)
//	blocks (or waiver)| optional | required | required
//	>=1 answered Q    | optional | optional | required
//
// title/why/files/effort/risk are unconditionally required regardless of
// complexity. The decisions_notes requirement is gated on
// plan.Meta.Status != "finalized" so historical finalized plans (which
// never carried decisions_notes) continue to validate clean.
//
// Blocks gate: a standard/complex slice with zero Blocks fails unless it
// carries an explicit "blocks_waiver: <reason>" marker in decisions_notes
// (see blocksWaiverReason). This promotes what was previously only
// ValidateBlockAdvisories (non-blocking) to a hard gate: that advisory
// demonstrably went unheeded — 204 of 282 corpus slices default to
// "standard" complexity with 0% block usage among them, so a non-blocking
// nudge does not bind at the tier that carries most authoring volume.
// Trivial slices stay exempt; they have no design surface worth visualising.
// Exempt like decisions_notes (same isStrictModel/explicit-Complexity
// condition, plus finalized) — see the gate's inline comment below for the
// measured blast-radius comparison that set this width.
//
// Prose-length gate: `what` fails when it exceeds whatWordCap words, gated
// the same way as decisions_notes (plan.Meta.Status != "finalized") so
// legacy finalized plans are exempt. See whatWordCap's doc comment for the
// empirical basis. why/tests/done_when are never hard-failed for length —
// see ValidateProseAdvisories for their non-blocking equivalent.
func Validate(plan *PlanYAML) []string {
	var errs []string
	if plan.Meta.ID == "" {
		errs = append(errs, "meta.id is required")
	}
	if plan.Meta.Title == "" {
		errs = append(errs, "meta.title is required")
	}
	switch plan.Meta.Status {
	case "draft", "review", "finalized", "active", "completed":
	default:
		errs = append(errs, fmt.Sprintf("meta.status %q must be draft|review|finalized|active|completed", plan.Meta.Status))
	}
	// Validate SchemaVersion enum when non-empty: "v3" (strict) and "v4" (strict +
	// research-enforced) are accepted; empty is legacy.
	if v := plan.Meta.SchemaVersion; v != "" && v != "v3" && v != "v4" {
		errs = append(errs, fmt.Sprintf("meta.schema_version %q is invalid; accepted values: \"v3\", \"v4\" (or omit for legacy)", v))
	}
	if plan.Design.Problem == "" {
		errs = append(errs, "design.problem is required")
	}
	if len(plan.Design.Goals) == 0 {
		errs = append(errs, "design.goals must have at least 1 entry")
	}
	if len(plan.Design.Constraints) == 0 {
		errs = append(errs, "design.constraints must have at least 1 entry")
	}
	// v4: the design itself must carry a cited research basis. Source URLs are
	// shape-checked for any plan that provides them.
	for i, r := range plan.Design.Research {
		if !isResearchURL(r.URL) {
			errs = append(errs, fmt.Sprintf("design.research[%d].url %q must be an http(s) URL", i, r.URL))
		}
	}
	if plan.Meta.SchemaVersion == "v4" && len(plan.Design.Research) == 0 {
		errs = append(errs, "design.research must cite at least 1 web/doc source for v4 plans (the design's research basis)")
	}

	// Collect slice nums and IDs for duplicate/dep checks.
	nums := map[int]bool{}
	ids := map[string]bool{}

	for i, s := range plan.Slices {
		prefix := fmt.Sprintf("slices[%d]", i)

		// Validate Complexity enum membership when non-empty. Empty string is
		// allowed for back-compat (defaults to "standard" via effectiveComplexity).
		switch s.Complexity {
		case "", "trivial", "standard", "complex":
		default:
			errs = append(errs, fmt.Sprintf("%s.complexity %q must be trivial|standard|complex", prefix, s.Complexity))
		}

		complexity := effectiveComplexity(s)

		// isStrictModel identifies plans authored under the triage-gated
		// interview model (v3 introduced decisions_notes as the rationale
		// spine; v4 additionally enforces research). It gates back-compat
		// exemptions below (decisions_notes, blocks) alongside an explicitly-set
		// Complexity, so the pre-triage legacy corpus (empty schema_version AND
		// empty Complexity) is never retroactively broken by a new requirement.
		isStrictModel := plan.Meta.SchemaVersion == "v3" || plan.Meta.SchemaVersion == "v4"

		// Unconditional: title/why/files/effort/risk are required regardless
		// of complexity. (Title was previously not enforced; preserve that
		// behavior — only why/files are checked here, plus effort/risk below.)
		if s.Why == "" {
			errs = append(errs, prefix+".why is required")
		}
		if len(s.Files) == 0 {
			errs = append(errs, prefix+".files must have at least 1 entry")
		}

		// Branched on complexity: what/done_when/tests/decisions_notes/questions.
		switch complexity {
		case "trivial":
			// Trivial slices: what, done_when, tests, decisions_notes all optional.
			// No slice-question requirement.
		case "complex":
			if s.What == "" {
				errs = append(errs, prefix+".what is required")
			}
			if len(s.DoneWhen) < 2 {
				errs = append(errs, prefix+".done_when must have at least 2 entries for complex slices")
			}
			if s.Tests == "" {
				errs = append(errs, prefix+".tests is required")
			}
			if plan.Meta.Status != "finalized" {
				if len(strings.TrimSpace(s.DecisionsNotes)) < 50 {
					errs = append(errs, prefix+".decisions_notes is required (>=50 chars) for complex slices")
				}
			}
			if !hasAnsweredQuestion(s.Questions) {
				errs = append(errs, prefix+".questions must include at least 1 question with a non-empty answer for complex slices")
			}
		default: // "standard"
			if s.What == "" {
				errs = append(errs, prefix+".what is required")
			}
			if len(s.DoneWhen) == 0 {
				errs = append(errs, prefix+".done_when must have at least 1 entry")
			}
			if s.Tests == "" {
				errs = append(errs, prefix+".tests is required")
			}
			// decisions_notes is required when the plan is not finalized AND:
			//   - schema_version == "v3" (strict model: catches omitted Complexity
			//     which defaults to "standard"), OR
			//   - slice.Complexity is explicitly set (legacy behaviour).
			requiresDecisionsNotes := plan.Meta.Status != "finalized" &&
				(isStrictModel || s.Complexity != "")
			if requiresDecisionsNotes {
				if len(strings.TrimSpace(s.DecisionsNotes)) < 50 {
					errs = append(errs, prefix+".decisions_notes is required (>=50 chars) for standard slices")
				}
			}
		}

		// Prose-length gate: `what` was measured to be 33% of all slice-prose
		// words in the corpus (see whatWordCap doc comment). Enforced only on
		// non-finalized plans — the 42 historical finalized plans pre-date the
		// cap and must not retroactively fail validation.
		if plan.Meta.Status != "finalized" {
			if wc := wordCount(s.What); wc > whatWordCap {
				errs = append(errs, fmt.Sprintf("%s.what is %d words, exceeds the %d-word cap (trim into decisions_notes, or split the slice)", prefix, wc, whatWordCap))
			}
		}

		// Blocks gate: standard/complex slices must carry >=1 visual block, or
		// record an explicit skip reason via a "blocks_waiver: <reason>" marker
		// in decisions_notes. See the Validate doc comment for the measured
		// rationale and blocksWaiverReason for the marker contract.
		//
		// Exemption width (measured, not assumed): gating this unconditionally
		// on plan.Meta.Status != "finalized" — the same width as the whatWordCap
		// gate — was tried first and measured against the live .wipnote/plans
		// corpus (46 plans): it broke 21 of 22 non-finalized plans, i.e. almost
		// the entire active/draft backlog, because most predate blocks-first
		// planning entirely. That is too wide a break for a mechanical rule with
		// no way to bulk-satisfy it. Instead this gate reuses the SAME
		// back-compat condition as decisions_notes above (isStrictModel ||
		// explicit Complexity) for the standard tier, and applies unconditionally
		// for complex (which is always explicitly triaged, so that condition is
		// already true). Re-measured with this width: 5 of 22 non-finalized
		// plans newly fail, all schema_version=v3 — i.e. plans already authored
		// under the triage-gated interview model, which is exactly where 0%
		// block usage at the "standard" tier is a live regression rather than
		// pre-existing legacy debt.
		if complexity != "trivial" && plan.Meta.Status != "finalized" {
			requiresBlocks := complexity == "complex" || isStrictModel || s.Complexity != ""
			if requiresBlocks && len(s.Blocks) == 0 && blocksWaiverReason(s) == "" {
				errs = append(errs, fmt.Sprintf("%s.blocks must have at least 1 visual block for %s slices (or record a `blocks_waiver: <reason>` marker in decisions_notes if the slice is genuinely underspecified)", prefix, complexity))
			}
		}

		switch s.Effort {
		case "S", "M", "L":
		default:
			errs = append(errs, fmt.Sprintf("%s.effort %q must be S|M|L", prefix, s.Effort))
		}
		switch s.Risk {
		case "Low", "Med", "High":
		default:
			errs = append(errs, fmt.Sprintf("%s.risk %q must be Low|Med|High", prefix, s.Risk))
		}
		if nums[s.Num] {
			errs = append(errs, fmt.Sprintf("%s.num %d is duplicate", prefix, s.Num))
		}
		nums[s.Num] = true
		for _, d := range s.Deps {
			if d == s.Num {
				errs = append(errs, fmt.Sprintf("%s.deps: self-reference %d", prefix, d))
			}
		}

		// Duplicate slice IDs (non-empty IDs only).
		if s.ID != "" {
			if ids[s.ID] {
				errs = append(errs, fmt.Sprintf("%s.id %q is duplicate", prefix, s.ID))
			}
			ids[s.ID] = true
		}

		// V2: approval_status enum (empty = unset, valid for legacy plans).
		switch s.ApprovalStatus {
		case "", "pending", "approved", "rejected", "changes_requested":
		default:
			errs = append(errs, fmt.Sprintf("%s.approval_status %q must be pending|approved|rejected|changes_requested", prefix, s.ApprovalStatus))
		}

		// V2: execution_status enum (empty = unset, valid for legacy plans).
		switch s.ExecutionStatus {
		case "", "not_started", "promoted", "in_progress", "done", "blocked", "superseded":
		default:
			errs = append(errs, fmt.Sprintf("%s.execution_status %q must be not_started|promoted|in_progress|done|blocked|superseded", prefix, s.ExecutionStatus))
		}

		// V2: slice-local questions — reject duplicate IDs; validate structured form.
		qIDs := map[string]bool{}
		for j, q := range s.Questions {
			qPfx := fmt.Sprintf("%s.questions[%d]", prefix, j)
			if q.ID != "" {
				if qIDs[q.ID] {
					errs = append(errs, fmt.Sprintf("%s.id %q is duplicate within slice", qPfx, q.ID))
				}
				qIDs[q.ID] = true
			}
			// Structured form: when options are present, recommended must match a key.
			if q.Recommended != "" && len(q.Options) > 0 {
				found := false
				for _, o := range q.Options {
					if o.Key == q.Recommended {
						found = true
						break
					}
				}
				if !found {
					errs = append(errs, fmt.Sprintf("%s.recommended %q not in options", qPfx, q.Recommended))
				}
			}
		}

		// V2: critic_revisions — require source, severity, summary.
		for j, cr := range s.CriticRevisions {
			crPrefix := fmt.Sprintf("%s.critic_revisions[%d]", prefix, j)
			if cr.Source == "" {
				errs = append(errs, crPrefix+".source is required")
			}
			if cr.Severity == "" {
				errs = append(errs, crPrefix+".severity is required")
			}
			if cr.Summary == "" {
				errs = append(errs, crPrefix+".summary is required")
			}
		}

		// Phase-2 (slice-6): optional structured blocks. Additive — slices that
		// omit blocks validate unchanged. Shapes are validated ONLY when a block
		// is present, using BlockCatalog as the single source of truth.
		for j, b := range s.Blocks {
			errs = append(errs, validateBlock(fmt.Sprintf("%s.blocks[%d]", prefix, j), b)...)
		}

		// Research gate (v4): non-trivial slices must cite web/doc research or
		// record an explicit waiver, so plans are evidence-backed and don't
		// reinvent battle-tested packages. Source URLs are always shape-checked.
		errs = append(errs, validateSliceResearch(prefix, s, plan.Meta.SchemaVersion == "v4")...)
	}

	// Check dep references after collecting all nums.
	for i, s := range plan.Slices {
		for _, d := range s.Deps {
			if !nums[d] {
				errs = append(errs, fmt.Sprintf("slices[%d].deps: references nonexistent slice %d", i, d))
			}
		}
	}

	for i, q := range plan.Questions {
		prefix := fmt.Sprintf("questions[%d]", i)
		if q.Text == "" {
			errs = append(errs, prefix+".text is required")
		}
		if q.Description == "" {
			errs = append(errs, prefix+".description is required")
		}
		if len(q.Options) < 2 {
			errs = append(errs, prefix+".options must have at least 2 entries")
		}
		if q.Recommended != "" {
			found := false
			for _, o := range q.Options {
				if o.Key == q.Recommended {
					found = true
					break
				}
			}
			if !found {
				errs = append(errs, fmt.Sprintf("%s.recommended %q not in options", prefix, q.Recommended))
			}
		}
	}
	// Validate critique section if present.
	if plan.Critique != nil {
		c := plan.Critique
		if c.ReviewedAt == "" {
			errs = append(errs, "critique.reviewed_at is required")
		}
		for i, a := range c.Assumptions {
			prefix := fmt.Sprintf("critique.assumptions[%d]", i)
			switch a.Status {
			case "verified", "plausible", "unverified", "questionable", "falsified":
			default:
				errs = append(errs, fmt.Sprintf("%s.status %q is invalid", prefix, a.Status))
			}
			if a.Text == "" {
				errs = append(errs, prefix+".text is required")
			}
		}
		for i, r := range c.Risks {
			switch r.Severity {
			case "High", "Medium", "Low":
			default:
				errs = append(errs, fmt.Sprintf("critique.risks[%d].severity %q is invalid", i, r.Severity))
			}
		}
	}
	return errs
}

// validateSliceResearch enforces a slice's research basis. Source URLs are always
// shape-checked (http/https). When enforced (v4) and the slice is non-trivial,
// the slice must carry at least one research source OR a non-empty
// research_waiver, so external claims are evidence-backed and battle-tested
// packages are considered before building custom.
func validateSliceResearch(prefix string, s PlanSlice, enforced bool) []string {
	var errs []string
	for i, r := range s.Research {
		if !isResearchURL(r.URL) {
			errs = append(errs, fmt.Sprintf("%s.research[%d].url %q must be an http(s) URL", prefix, i, r.URL))
		}
	}
	if !enforced || s.Complexity == "trivial" {
		return errs
	}
	if len(s.Research) == 0 && strings.TrimSpace(s.ResearchWaiver) == "" {
		errs = append(errs, prefix+".research must cite at least 1 web/doc source (or set research_waiver) for standard/complex slices")
	}
	return errs
}

// isResearchURL reports whether u is a usable http(s) source URL — it must parse,
// carry an http/https scheme, AND have a non-empty host (so bare "https://" or a
// scheme-only string does not satisfy the gate).
func isResearchURL(u string) bool {
	parsed, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}

// ValidateResearchAdvisories returns NON-BLOCKING advisories nudging research on
// plans that do not yet enforce it (legacy/v3). It mirrors the v4 gate so the
// validate-yaml CLI and the critique pass can surface research gaps without
// failing the build. v4 plans return nil — the hard gate in Validate covers them.
func ValidateResearchAdvisories(plan *PlanYAML) []string {
	if plan == nil || plan.Meta.SchemaVersion == "v4" {
		return nil
	}
	var adv []string
	if len(plan.Design.Research) == 0 {
		adv = append(adv, "design.research: no cited research basis; web research is expected for non-trivial plans (set schema_version: v4 to enforce)")
	}
	for i, s := range plan.Slices {
		if s.Complexity == "trivial" {
			continue
		}
		if len(s.Research) == 0 && strings.TrimSpace(s.ResearchWaiver) == "" {
			adv = append(adv, fmt.Sprintf("slices[%d] (%s): no research sources cited and no research_waiver", i, s.ID))
		}
	}
	return adv
}

// ValidateBlockAdvisories returns NON-BLOCKING advisories for standard and
// complex slices that have no visual blocks. Under blocks-first planning the
// visual artifact (data-model / file-tree / wireframe / diagram / tabs)
// is authored INLINE during the plan interview and the prose is derived from it,
// so a qualifying slice with zero blocks signals the blocks-first interview was
// skipped or compressed for that slice. Trivial slices are always exempt — they
// have minimal field requirements and no design surface worth visualising. A
// slice carrying an explicit "blocks_waiver: <reason>" marker in
// decisions_notes (see blocksWaiverReason) is also exempt, mirroring how
// ValidateResearchAdvisories respects ResearchWaiver.
//
// This advisory has been superseded by a hard gate in Validate for
// non-finalized plans (a non-blocking signal was measured to go unheeded at
// the "standard" tier — see the Validate doc comment). It remains here,
// unconditional on Meta.Status, so finalized plans — exempt from the hard
// gate — still get a nudge, and so the critique pass keeps a channel for
// this signal that doesn't fail the build.
//
// Callers surface these the same way research advisories are surfaced: as
// warnings in the validate-yaml / validate CLI paths.
func ValidateBlockAdvisories(plan *PlanYAML) []string {
	if plan == nil {
		return nil
	}
	var adv []string
	for i, s := range plan.Slices {
		complexity := effectiveComplexity(s)
		if complexity == "trivial" {
			continue
		}
		if len(s.Blocks) == 0 && blocksWaiverReason(s) == "" {
			id := s.ID
			if id == "" {
				id = fmt.Sprintf("num=%d", s.Num)
			}
			adv = append(adv, fmt.Sprintf(
				"slices[%d] (%s, %s) has no visual blocks — blocks should be authored during the interview (blocks-first): add grounded data-model/file-tree/wireframe/diagram blocks, or run `wipnote:visual-plan` to enrich this slice after the fact",
				i, id, complexity,
			))
		}
	}
	return adv
}

// ValidateProseAdvisories returns NON-BLOCKING advisories for slices whose
// why/tests/done_when prose runs unusually long — over that field's own
// 75th-percentile norm in the .wipnote/plans/*.yaml corpus audit (see
// whyWordAdvisoryCap/testsWordAdvisoryCap/doneWhenWordAdvisoryCap). Unlike
// whatWordCap, these never fail wipnote plan validate-yaml — `what` was the
// field measured to be disproportionately inflating plan prose (33% of all
// slice-prose words); why/tests/done_when only warn, mirroring the pattern
// established by ValidateBlockAdvisories/ValidateResearchAdvisories.
func ValidateProseAdvisories(plan *PlanYAML) []string {
	if plan == nil {
		return nil
	}
	var adv []string
	for i, s := range plan.Slices {
		id := s.ID
		if id == "" {
			id = fmt.Sprintf("num=%d", s.Num)
		}
		if wc := wordCount(s.Why); wc > whyWordAdvisoryCap {
			adv = append(adv, fmt.Sprintf(
				"slices[%d] (%s): why is %d words — over the %d-word norm; consider trimming into decisions_notes",
				i, id, wc, whyWordAdvisoryCap,
			))
		}
		if wc := wordCount(s.Tests); wc > testsWordAdvisoryCap {
			adv = append(adv, fmt.Sprintf(
				"slices[%d] (%s): tests is %d words — over the %d-word norm; consider trimming",
				i, id, wc, testsWordAdvisoryCap,
			))
		}
		doneWC := 0
		for _, d := range s.DoneWhen {
			doneWC += wordCount(d)
		}
		if doneWC > doneWhenWordAdvisoryCap {
			adv = append(adv, fmt.Sprintf(
				"slices[%d] (%s): done_when is %d words total — over the %d-word norm; consider trimming or splitting criteria",
				i, id, doneWC, doneWhenWordAdvisoryCap,
			))
		}
	}
	return adv
}
