// Register in plan_cmds.go: cmd.AddCommand(planElicitDecisionsCmd())
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// planElicitDecisionsCmd adds the cobra sub-command `plan elicit-decisions`.
//
// Cross-harness: this command works on Claude Code and Codex CLI
// without modification. The Claude-only convenience wrapper at
// `plugin/skills/spec-from-slice/SKILL.md` calls into this same command.
func planElicitDecisionsCmd() *cobra.Command {
	var scope, decisions, contextStr string
	var fromStdin bool

	cmd := &cobra.Command{
		Use:   "elicit-decisions <plan-id> <slice-num>",
		Short: "Capture Scope/Decisions/Context for a plan slice",
		Long: `Write the three-question Scope/Decisions/Context interview answers
into the slice's decisions_notes field as a single Markdown blob.

Two input forms:

  Flags (programmatic / non-interactive):
    wipnote plan elicit-decisions <plan-id> <slice-num> \
      --scope "..." --decisions "..." --context "..."

  Stdin (YAML payload):
    cat <<EOF | wipnote plan elicit-decisions <plan-id> <slice-num> --from-stdin
    scope: |
      <text>
    decisions: |
      <text>
    context: |
      <text>
    EOF

The command writes to plan YAML atomically. Re-runs replace previous content
with a stderr warning. The slice's decisions_notes field is consumed by
'wipnote spec generate --insert' to populate the spec's '## Decisions' section.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			sliceNum, err := parseSliceNum(args[1])
			if err != nil {
				return err
			}
			return elicitDecisionsForSlice(wipnoteDir, args[0], sliceNum,
				elicitInput{
					scope:     scope,
					decisions: decisions,
					context:   contextStr,
					fromStdin: fromStdin,
					stdin:     os.Stdin,
				})
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "Scope answer (boundaries of this slice)")
	cmd.Flags().StringVar(&decisions, "decisions", "", "Decisions answer (design choices made)")
	cmd.Flags().StringVar(&contextStr, "context", "", "Context answer (constraints, related work)")
	cmd.Flags().BoolVar(&fromStdin, "from-stdin", false, "Read scope/decisions/context as YAML from stdin")
	return cmd
}

// elicitInput bundles the three answers plus the stdin source.
type elicitInput struct {
	scope     string
	decisions string
	context   string
	fromStdin bool
	stdin     io.Reader
}

// stdinPayload mirrors the YAML keys accepted on stdin.
type stdinPayload struct {
	Scope     string `yaml:"scope"`
	Decisions string `yaml:"decisions"`
	Context   string `yaml:"context"`
}

// elicitDecisionsForSlice is the testable implementation. It loads the plan,
// derives the combined Markdown blob from flags or stdin, writes the blob to
// the slice's decisions_notes, and saves the plan atomically.
//
// The load → modify → save window runs inside planyaml.LockPlanForWrite so
// concurrent in-process elicitations on different slices of the same plan
// can't lose each other's writes.
func elicitDecisionsForSlice(wipnoteDir, planID string, sliceNum int, in elicitInput) error {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")

	defer planyaml.LockPlanForWrite(planPath)()

	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	sliceIdx, _, err := findPlanSlice(plan, sliceNum)
	if err != nil {
		return err
	}

	scope, decisions, contextStr, err := resolveElicitInputs(in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(scope) == "" && strings.TrimSpace(decisions) == "" && strings.TrimSpace(contextStr) == "" {
		return errors.New("at least one of --scope, --decisions, --context (or stdin) must be non-empty")
	}

	if existing := strings.TrimSpace(plan.Slices[sliceIdx].DecisionsNotes); existing != "" {
		fmt.Fprintln(stderr, "elicit-decisions: replacing previous decisions_notes (use --from-stdin or flags to overwrite intentionally)")
	}

	plan.Slices[sliceIdx].DecisionsNotes = combineDecisionsMarkdown(scope, decisions, contextStr)

	if err := planyaml.SaveLocked(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	// Version every decisions write to git so the slice's decisions_notes are
	// tracked like any other plan mutation (set-status, finalize). commitPlanChange
	// re-renders the HTML and commits YAML+HTML atomically, and no-ops gracefully
	// when there is nothing to commit or the project is not a git repo. This is
	// what lets the interview form (which writes through here) be versioned.
	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): set decisions — slice %d", planID, sliceNum)); err != nil {
		return fmt.Errorf("autocommit decisions: %w", err)
	}

	fmt.Printf("Decisions written to slice %d of %s\n", sliceNum, planID)
	return nil
}

// resolveElicitInputs picks between flag-form and stdin-form input. Stdin form
// is used when --from-stdin is set; otherwise the flag values are returned
// verbatim.
func resolveElicitInputs(in elicitInput) (scope, decisions, contextStr string, err error) {
	if in.fromStdin {
		raw, rerr := io.ReadAll(in.stdin)
		if rerr != nil {
			return "", "", "", fmt.Errorf("read stdin: %w", rerr)
		}
		var p stdinPayload
		if uerr := yaml.Unmarshal(raw, &p); uerr != nil {
			return "", "", "", fmt.Errorf("parse stdin YAML: %w", uerr)
		}
		return p.Scope, p.Decisions, p.Context, nil
	}
	return in.scope, in.decisions, in.context, nil
}

// combineDecisionsMarkdown produces the canonical Markdown blob written into
// slice.decisions_notes. Empty subsections are omitted (no empty headings).
func combineDecisionsMarkdown(scope, decisions, contextStr string) string {
	var sb strings.Builder
	add := func(label, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("### ")
		sb.WriteString(label)
		sb.WriteString("\n")
		sb.WriteString(body)
	}
	add("Scope", scope)
	add("Decisions", decisions)
	add("Context", contextStr)
	return sb.String()
}
