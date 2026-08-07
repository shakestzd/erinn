---
name: wipnote:spec-from-slice
description: Elicit Scope/Decisions/Context for a plan slice and generate its OpenSpec-formatted feature spec. Use after a slice is approved but before promote-slice, or any time a slice needs decisions captured. Wraps the cross-harness `wipnote plan elicit-decisions` CLI plus `wipnote spec generate --insert`.
user_invocable: true
---

# Spec from Slice — interview + generate

Use this skill to capture Scope, Decisions, and Context for one slice of an
active plan, then materialise the resulting feature's OpenSpec-formatted spec
into its HTML — in a single guided pass.

This skill is a Claude-only convenience layer. The canonical interface is the
cross-harness CLI command `wipnote plan elicit-decisions`, which works on
Codex CLI and Antigravity without any of the steps below.

## When to invoke

- Plan slice has just been approved and you are about to call
  `wipnote plan promote-slice`.
- Slice has been promoted and the resulting feature has no
  `<section class="spec">` yet, or the section is empty.
- Decisions changed and you want to re-elicit and re-generate the spec.

## Inputs you need

- `<plan-id>` — the active plan that owns the slice.
- `<slice-num>` — the integer slice number within that plan.

If the slice already has `feature_id` set, the skill will reuse it for the
generate step. Otherwise it stops after writing the decisions and points the
user at `wipnote plan promote-slice` first.

## Procedure

1. **Read the slice card.** Run `wipnote plan show <plan-id>` (or read
   `.wipnote/plans/<plan-id>.yaml` directly) to find the slice with the given
   `num`. Capture `title`, `what`, `why`, `done_when`, `tests`, and current
   `decisions_notes` (if any).

2. **Re-elicitation guard.** If `decisions_notes` is non-empty, ask the user
   via `AskUserQuestion`:
   - **Re-elicit** — overwrite previous notes with new answers.
   - **Edit in place** — print the existing notes for the user to edit, then
     re-write verbatim.
   - **Skip** — leave notes unchanged; jump to step 5 (generate spec).

3. **Three-question interview.** Capture Scope / Decisions / Context. Where any decision involves an external technology choice (library, SDK, API, harness primitive), first ground the presented options in current official documentation via web search / web fetch. If research uncovers an existing library or tool that covers the requirement, surface it as an option with a brief note on why it may be preferable to a custom build.
   - **Scope** — what is and is not in this slice? List the boundaries.
   - **Decisions** — what design choices were made and why? Reference any
     plan questions answered.
   - **Context** — what else does the implementer need to know? Pre-existing
     constraints, related work, file ownership boundaries.

   Render with the lowest-friction mechanism the harness has (all write the
   same `decisions_notes`) — prefer staying in place over a context switch:
   - **Native ask-user tool** — Claude `AskUserQuestion` / Antigravity `ask_user`,
     one grouped block.
   - **No such tool (e.g. Codex)** — just ask the three questions as text;
     the user answers in their reply.
   - **Web form (optional)** — only for a richer surface or on request:
     `wipnote plan interview <plan-id> <slice-num>` (blocks, writes
     `decisions_notes`, commits, returns — then skip step 4). See
     [/wipnote:plan](/wipnote:plan) → "Rendering the interview".

   The user may answer each in free-form prose. Empty answers are allowed —
   the field is free text.

4. **Write decisions.** Run the cross-harness CLI:
   ```bash
   wipnote plan elicit-decisions <plan-id> <slice-num> \
     --scope "<scope answer>" \
     --decisions "<decisions answer>" \
     --context "<context answer>"
   ```
   This combines the three answers into a single Markdown blob and writes it
   to `slice.decisions_notes` atomically.

5. **Generate the spec.** If the slice has a `feature_id` (i.e., it has already
   been promoted), run:
   ```bash
   wipnote spec generate <feature-id> --insert
   ```
   The spec section is written non-destructively: if the feature already has
   non-empty spec content, the command prints a diff and refuses. Pass
   `--force` only when the user explicitly accepts the overwrite.

   If the slice has no `feature_id` yet, tell the user to run
   `wipnote plan promote-slice <plan-id> <slice-num>` first, then re-invoke
   this skill.

6. **Confirm.** Show a 2-3 line summary: which fields changed, where the spec
   was written, what to do next (typically `wipnote plan promote-slice` if
   not yet promoted, or `wipnote compliance <feature-id>` to verify the
   spec parses).

## Notes

- The CLI command `wipnote plan elicit-decisions` is the source of truth.
  This skill exists to make the interview ergonomic on Claude Code via
  `AskUserQuestion`. Other harnesses (Codex CLI, Antigravity) call the CLI
  directly — they do not need this skill.
- `decisions_notes` is free text, not a typed schema. The renderer in
  `wipnote spec generate` weaves it verbatim into the generated spec's
  `## Decisions` section.
- The `--allow-spec-skip` flag on `promote-slice` and `feature complete` is
  for emergency overrides only; this skill is the regular path.
