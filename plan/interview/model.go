// Package interview defines the harness-agnostic question model for the
// plan-mode interview, decoupled from any single renderer.
//
// The same model drives every renderer: the wipnote web-form command (the
// portable canonical path that works on Claude Code, Codex CLI, and Gemini
// CLI alike), and — as an optional inline fast-path — a harness's native
// ask-user tool (Claude `AskUserQuestion`, Gemini `ask_user`). The answer
// types below (choice / text / yesno) are the intersection of those native
// tools so a single definition maps cleanly onto each.
//
// Renderers collect answers into a flat map and call Compose, which routes
// them into the Scope/Decisions/Context buckets that back a slice's
// decisions_notes — the same three-section Markdown the cross-harness
// `wipnote plan elicit-decisions` command persists.
package interview

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AnswerType is the input kind for a question. The set is the common
// denominator of Claude `AskUserQuestion` and Gemini `ask_user`.
type AnswerType string

const (
	Choice AnswerType = "choice"
	Text   AnswerType = "text"
	YesNo  AnswerType = "yesno"
)

// Bucket is one of the three decisions_notes subsections a stage feeds.
type Bucket string

const (
	BucketScope     Bucket = "Scope"
	BucketDecisions Bucket = "Decisions"
	BucketContext   Bucket = "Context"
)

// Option is one selectable choice for a Choice question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is a single prompt within a stage. ID is stable and is what a
// renderer posts answers back under.
type Question struct {
	ID          string     `json:"id"`
	Prompt      string     `json:"prompt"`
	Header      string     `json:"header"`
	Type        AnswerType `json:"type"`
	MultiSelect bool       `json:"multiSelect,omitempty"`
	Options     []Option   `json:"options,omitempty"`
	Placeholder string     `json:"placeholder,omitempty"`
}

// BlockPrompt is a blocks-first authoring instruction attached to a stage. It
// names a visual block type (a key from planyaml.BlockCatalog) the agent should
// author INLINE during that stage, before deriving prose. Description is the
// catalog's one-line summary of the type; Prompt is the stage-specific nudge for
// what to capture in it. Renderers (web form, native ask-user, plain chat) show
// these so block elicitation drives the interview rather than being a post-pass.
type BlockPrompt struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
}

// Stage is one interview round. Bucket says which decisions_notes section its
// answers (and the stage's free-text note) are composed into. Blocks, when
// present, are the visual artifacts to author FIRST in that stage; prose answers
// are then derived from them (blocks-first planning).
type Stage struct {
	Key       string        `json:"key"`
	Title     string        `json:"title"`
	Bucket    Bucket        `json:"bucket"`
	Questions []Question    `json:"questions"`
	Blocks    []BlockPrompt `json:"blocks,omitempty"`
}

// Definition is an agent-supplied interview: the staged question set the
// orchestrating skill composes for one round and hands to a renderer (the web
// form, or a native ask-user tool). Decoupling the model from ForComplexity is
// what lets the skill ask plan/slice-specific and adaptive follow-up questions
// rather than a fixed template.
type Definition struct {
	Stages []Stage `json:"stages"`
}

// ParseDefinition decodes an agent-supplied question set (JSON) and validates
// it enough to render and to compose answers back: every stage needs a key,
// a known bucket, and at least one question; every question needs a stable id,
// a prompt, and (for choice) at least one option. Question IDs must be unique
// so posted answers map unambiguously.
func ParseDefinition(data []byte) ([]Stage, error) {
	var def Definition
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("parse interview definition: %w", err)
	}
	if len(def.Stages) == 0 {
		return nil, errors.New("interview definition has no stages")
	}
	seen := map[string]bool{}
	for si := range def.Stages {
		st := &def.Stages[si]
		if strings.TrimSpace(st.Key) == "" {
			return nil, fmt.Errorf("stage %d: missing key", si)
		}
		switch st.Bucket {
		case BucketScope, BucketDecisions, BucketContext:
		default:
			return nil, fmt.Errorf("stage %q: invalid bucket %q (want Scope|Decisions|Context)", st.Key, st.Bucket)
		}
		if len(st.Questions) == 0 {
			return nil, fmt.Errorf("stage %q: no questions", st.Key)
		}
		for qi := range st.Questions {
			q := &st.Questions[qi]
			if strings.TrimSpace(q.ID) == "" {
				return nil, fmt.Errorf("stage %q question %d: missing id", st.Key, qi)
			}
			if seen[q.ID] {
				return nil, fmt.Errorf("duplicate question id %q", q.ID)
			}
			seen[q.ID] = true
			if strings.TrimSpace(q.Prompt) == "" {
				return nil, fmt.Errorf("question %q: missing prompt", q.ID)
			}
			if q.Type == "" {
				q.Type = Choice
			}
			switch q.Type {
			case Choice, Text, YesNo:
			default:
				return nil, fmt.Errorf("question %q: invalid type %q (want choice|text|yesno)", q.ID, q.Type)
			}
			if q.Type == Choice && len(q.Options) == 0 {
				return nil, fmt.Errorf("question %q: choice type needs at least one option", q.ID)
			}
		}
	}
	return def.Stages, nil
}

// effectiveComplexity mirrors the validator: unset/unknown -> standard.
func effectiveComplexity(c string) string {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "trivial":
		return "trivial"
	case "complex":
		return "complex"
	default:
		return "standard"
	}
}

// ForComplexity returns the staged interview for a slice complexity:
// trivial -> 0 stages, standard -> 3, complex -> 4. The stages collectively
// cover all three decisions buckets so a completed interview yields a full
// Scope/Decisions/Context note.
func ForComplexity(complexity string) []Stage {
	switch effectiveComplexity(complexity) {
	case "trivial":
		return nil
	case "complex":
		return []Stage{requirementsStage(), scopeStage(), contractStage(), doneWhenStage()}
	default: // standard
		return []Stage{requirementsStage(), scopeStage(), doneWhenStage()}
	}
}

// Plan-intake question IDs — the upfront, triage-led interview that runs BEFORE
// slices exist and writes plan Design. Distinct from the per-slice flow.
const (
	QIntakeComplexity  = "intake.complexity"
	QIntakeProblem     = "intake.problem"
	QIntakeGoals       = "intake.goals"
	QIntakeConstraints = "intake.constraints"
)

// BucketDesign / BucketTriage are display labels for intake stages; intake
// answers populate plan Design, not the Scope/Decisions/Context note buckets.
const (
	BucketDesign Bucket = "Design"
	BucketTriage Bucket = "Triage"
)

// PlanIntakeStages is the upfront interview, run before any slices exist: it
// assesses complexity (triage) and gathers the plan-level problem/goals/
// constraints from a topic with possibly limited information. Its answers
// populate plan Design and a recommended complexity; the agent then drafts
// slices. This is the "interview at the beginning of the plan" — distinct from
// the per-slice BuildForSlice flow that fills a slice's decisions_notes.
func PlanIntakeStages() []Stage {
	return []Stage{
		{Key: "triage", Title: "Triage", Bucket: BucketTriage, Questions: []Question{
			{ID: QIntakeComplexity, Header: "Complexity", Type: Choice,
				Prompt: "How would you classify this work? Drives interview depth and the slices' mandatory fields.",
				Options: []Option{
					{"Trivial — one-shot patch, no design risk", "Minimal slice card; skip the deep interview."},
					{"Standard — bounded scope, needs design clarity", "Per slice: Requirements, Scope & state, Done-when."},
					{"Complex — system design, multiple unknowns", "Per slice: all four stages."},
					{"Skip — I'll paste the spec", "You supply the spec; the agent drafts the YAML."},
				}},
		}},
		{Key: "problem", Title: "Problem & goals", Bucket: BucketDesign, Questions: []Question{
			{ID: QIntakeProblem, Header: "Problem", Type: Text,
				Prompt: "What problem does this solve, and for whom?", Placeholder: "users can't X today; they need Y"},
			{ID: QIntakeGoals, Header: "Goals", Type: Text,
				Prompt: "What does success look like? One goal per line.", Placeholder: "ship X\nkeep p99 < 50ms"},
		}},
		{Key: "constraints", Title: "Constraints", Bucket: BucketDesign, Questions: []Question{
			{ID: QIntakeConstraints, Header: "Constraints", Type: Text,
				Prompt: "Hard constraints or non-negotiables? One per line.", Placeholder: "no new runtime deps\nbackward-compatible on-disk format"},
		}},
	}
}

// IntakeResult is the upfront interview's output: plan Design fields plus the
// assessed complexity ("trivial"|"standard"|"complex", or "" when skipped).
type IntakeResult struct {
	Complexity  string
	Problem     string
	Goals       []string
	Constraints []string
}

// ComposeIntake maps plan-intake answers (keyed by the QIntake* ids) into an
// IntakeResult for writing to plan Design.
func ComposeIntake(answers map[string]string) IntakeResult {
	return IntakeResult{
		Complexity:  classifyComplexity(answers[QIntakeComplexity]),
		Problem:     strings.TrimSpace(answers[QIntakeProblem]),
		Goals:       splitLines(answers[QIntakeGoals]),
		Constraints: splitLines(answers[QIntakeConstraints]),
	}
}

func classifyComplexity(label string) string {
	switch {
	case strings.HasPrefix(label, "Trivial"):
		return "trivial"
	case strings.HasPrefix(label, "Standard"):
		return "standard"
	case strings.HasPrefix(label, "Complex"):
		return "complex"
	default:
		return ""
	}
}

func splitLines(s string) []string {
	var out []string
	for ln := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// BuildForSlice returns the canonical staged interview for a slice: the
// complexity template plus, when present, a stage carrying the slice's own
// unanswered slice-local questions. This is the single source of truth both
// renderers consume — the web form and (via `plan interview-questions`) a
// harness's native ask-user tool — so the questions asked are the plan's real
// open questions, not just a fixed template.
func BuildForSlice(complexity string, openQuestions []Question) []Stage {
	stages := ForComplexity(complexity)
	if len(openQuestions) > 0 {
		stages = append(stages, Stage{
			Key:       "slice-questions",
			Title:     "Open slice questions",
			Bucket:    BucketDecisions,
			Questions: openQuestions,
		})
	}
	return stages
}

// StageBlockPlan maps a stage key to the blocks-first authoring prompts for
// that stage: the visual block type(s) to author INLINE during the stage and
// what to capture in each. The Type values are keys expected to exist in
// planyaml.BlockCatalog; callers (e.g. `wipnote plan interview-questions`)
// should cross-check each Type against the live catalog and drop/annotate any
// that the catalog no longer defines, so the catalog stays the source of truth
// for WHICH block types exist while this stays the source of truth for WHEN to
// author them. Returns nil for stages with no natural visual artifact.
func StageBlockPlan(stageKey string) []BlockPrompt {
	switch stageKey {
	case "scope":
		return []BlockPrompt{{
			Type:   "file-tree",
			Prompt: "FIRST author a file-tree block of the real files this slice touches (new + edited). Derive `files` and the scope half of `what` from it. Every path must already exist or be a direct output of this slice — never invent paths.",
		}}
	case "contract":
		return []BlockPrompt{
			{
				Type:   "api-endpoint",
				Prompt: "If the slice exposes an HTTP route or CLI/command surface, author an api-endpoint block (method + path + request/response params) FIRST, then derive the contract half of `what` from it. Only for routes the slice actually implements.",
			},
			{
				Type:   "data-model",
				Prompt: "If the slice introduces or changes a stored entity, author a data-model block (named, typed columns) FIRST, then derive `what`/`done_when` from it. Columns must be real or will-exist fields — never invented schema.",
			},
		}
	case "requirements":
		return []BlockPrompt{{
			Type:   "wireframe",
			Prompt: "For UI/flow work, sketch the user-visible surface FIRST: a wireframe block (HTML/CSS using `var(--wf-*)` tokens only, no raw colors) or a diagram block (ordered steps/arrows). Derive `why`/`what` from the sketch. Skip for non-visual slices.",
		}}
	default:
		return nil
	}
}

func requirementsStage() Stage {
	return Stage{
		Key: "requirements", Title: "Requirements", Bucket: BucketDecisions,
		Questions: []Question{
			{ID: "requirements.0", Header: "Goal", Type: Choice,
				Prompt: "What's the user-visible behavior we're after?",
				Options: []Option{
					{"New capability — feature add", "Net-new surface (command, hook, or view). Expect new files + tests, not edits to existing flows."},
					{"Behavior change — modify existing", "It exists but acts wrong, slow, or incomplete. You'll change logic and must keep current callers working."},
					{"Bug fix — restore intended behavior", "It should already work. Reproduce first, fix the root cause, leave the contract unchanged."},
				}},
			{ID: "requirements.1", Header: "Constraint", Type: Choice,
				Prompt: "What's the hard constraint?",
				Options: []Option{
					{"Performance budget", "A measurable ceiling — p99 latency, throughput, or memory — the change must not exceed."},
					{"Backward compatibility", "Existing .wipnote/ HTML and SQLite must keep loading; no migration that breaks old data."},
					{"No new runtime dependency", "Solve it with the stdlib and the current go.mod — no new third-party modules."},
					{"Other", "A different hard constraint — name it in the note below or ask in the chat."},
				}},
		},
	}
}

func scopeStage() Stage {
	return Stage{
		Key: "scope", Title: "Scope & state", Bucket: BucketScope,
		Questions: []Question{
			{ID: "scope.0", Header: "State", Type: Choice,
				Prompt: "Where does the state live?",
				Options: []Option{
					{"In SQLite (read index)", "Derived, rebuildable cache (~/.cache). Safe to drop and reindex — never the source of truth."},
					{"In .wipnote/<kind>/*.html", "The canonical, committed store. Survives a DB rebuild — this is the source of truth."},
					{"In-memory only", "Lives for the process lifetime; nothing is persisted across runs."},
					{"On the filesystem outside .wipnote/", "Files like session transcripts or hook artifacts, kept outside the canonical store."},
				}},
		},
	}
}

func contractStage() Stage {
	return Stage{
		Key: "contract", Title: "API / Contract", Bucket: BucketDecisions,
		Questions: []Question{
			{ID: "contract.0", Header: "Trigger", Type: Choice,
				Prompt: "What's the firing rule for this behavior?",
				Options: []Option{
					{"On every tool call", "A PreToolUse/PostToolUse hook — runs around each tool the agent invokes."},
					{"On user prompt submission", "A UserPromptSubmit hook — runs each time the user sends a message."},
					{"On session lifecycle", "A SessionStart/SessionEnd hook — runs when a session begins or ends."},
					{"On explicit CLI invocation only", "No hook — a wipnote subcommand a user or agent runs deliberately."},
				}},
			{ID: "contract.1", Header: "Payload", Type: Text,
				Prompt:      "What's the input/output contract (payload, return shape)?",
				Placeholder: "e.g. in: slice-num + answers map · out: Scope/Decisions/Context Markdown written to the slice"},
		},
	}
}

func doneWhenStage() Stage {
	return Stage{
		Key: "donewhen", Title: "Done-when", Bucket: BucketContext,
		Questions: []Question{
			{ID: "donewhen.0", Header: "Acceptance", Type: Choice,
				Prompt: "How will you tell it works?",
				Options: []Option{
					{"Unit test on the smallest function", "A pure input→output assertion on the core logic — the fastest, most precise signal."},
					{"Integration test through the CLI", "Spawn the binary and assert the exit code plus on-disk side effects, end to end."},
					{"Manual smoke test against the dashboard", "An operator eyeballs the UI — no automated assertion; use only when the output is visual."},
				}},
		},
	}
}

// Compose routes a flat answers map into the three decisions_notes buckets.
// Keys are question IDs (e.g. "scope.0") and per-stage free-text notes keyed
// "note:<stageKey>". Empty answers produce empty buckets so no empty Markdown
// headings are written downstream.
func Compose(stages []Stage, answers map[string]string) (scope, decisions, contextStr string) {
	buckets := map[Bucket]*strings.Builder{
		BucketScope:     {},
		BucketDecisions: {},
		BucketContext:   {},
	}
	addLine := func(b Bucket, line string) {
		sb := buckets[b]
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(line)
	}
	for _, st := range stages {
		for _, q := range st.Questions {
			if v := strings.TrimSpace(answers[q.ID]); v != "" {
				addLine(st.Bucket, "- "+q.Header+": "+v)
			}
		}
		if note := strings.TrimSpace(answers["note:"+st.Key]); note != "" {
			addLine(st.Bucket, note)
		}
	}
	return buckets[BucketScope].String(), buckets[BucketDecisions].String(), buckets[BucketContext].String()
}
