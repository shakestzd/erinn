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

// Stage is one interview round. Bucket says which decisions_notes section its
// answers (and the stage's free-text note) are composed into.
type Stage struct {
	Key       string     `json:"key"`
	Title     string     `json:"title"`
	Bucket    Bucket     `json:"bucket"`
	Questions []Question `json:"questions"`
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
