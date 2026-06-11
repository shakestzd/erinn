---
name: agent-context
description: "Shared agent context — work attribution, safety rules, and development principles. Loaded by all plugin agents via skills: frontmatter."
---

# Shared Agent Context

## Work Attribution

The orchestrator always provides the work item ID in your task prompt (e.g., "Feature: feat-580dc00b"). Use it:

```bash
wipnote feature start <id>   # or bug start / spike start
```

**Rules:**
1. Look for a feature/bug/spike ID in the task prompt first
2. If found, run `start` on it — do NOT create a new one
3. Only create a new work item if the prompt genuinely contains no ID
4. If wipnote is unavailable, proceed — attribution is not a blocker

## Work Completion

When your task is done and quality gates pass:
1. Run `wipnote feature complete <id>` (or `bug complete`, `spike complete`)
2. Do this BEFORE reporting back to the orchestrator
3. If the CLI is unavailable, report completion — the orchestrator will handle it

## Safety Rules

**FORBIDDEN:** Never edit `.wipnote/` files directly. Use the CLI:
- `wipnote feature complete <id>` not `Edit(".wipnote/features/...")`
- `wipnote bug create "title" --track <trk-id>` not `Write(".wipnote/bugs/...")`

Bugs require an owning track: use `wipnote relevant "<topic>"` or `wipnote track list` to find one before creating the bug. `--standalone` is supported for feature creation only, not bug creation.

**BATCH wipnote CLI calls.** Each Bash tool call spends one turn from the user's quota. Chain commands with `&&` into a single invocation whenever possible. Do this (1 call):
```bash
wipnote bug create "A" --track trk-xxx && \
wipnote bug create "B" --track trk-xxx && \
wipnote link add feat-aaa bug-new --rel caused_by
```
Never 3 separate Bash calls for the same thing. Only break into multiple calls when a later command must parse the output (e.g., a returned ID) of an earlier one.

### Plan YAML Updates

Plan YAML files (`.wipnote/plans/*.yaml`) are validated assets — never write them directly.
Use the CLI to ensure valid structure:

- **Create:** `wipnote plan create-yaml "<title>"`
- **Update:** `wipnote plan rewrite-yaml <plan-id> --file /tmp/updated.yaml`
- **Validate:** `wipnote plan validate-yaml <plan-id>`

The `rewrite-yaml` command validates schema, checks meta.id match, and writes atomically.
Agent workflow: read plan → modify in memory → write to temp file → call rewrite-yaml.

## Architectural Context (run once at agent start — Claude Code only)

At the start of your session, inject relevant architectural memory by running:

```bash
wipnote who --json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
wi=d.get('work_item','')
if wi: print(wi)
" 2>/dev/null
```

If the above prints a work item ID (e.g. `feat-abc12345`), run:

```bash
wipnote arch resolve --for <work-item-id> 2>/dev/null
```

Paste the output verbatim under a `## Architectural Context` heading in your working context.
If the command prints "No arch cards matched." or fails silently, skip the heading — do not
emit errors or warnings. This step is informational only and must never block attribution or
task execution.

Drift markers (`UNVERIFIED:`) in the output mean the card's verified commit pre-dates recent
changes to covered files. Treat those facts as advisory — verify assumptions in code before
relying on them.

## Research routing — where does the answer live?

Before grepping local code, route by where the answer actually lives:

- External libraries/SDKs, upstream harness behavior (Claude Code / Codex / Gemini CLI), third-party tool error messages, version/API contracts, or "is this a known/recent issue?" → use your web search / web fetch tools and the GitHub CLI (`gh search issues`, `gh api`) FIRST, or in parallel with local search. Official docs, GitHub issues, releases, and changelogs are first-class research, not a last resort.
- This repo's own code, conventions, wiring, or "where is X defined?" → use local file-read/search tools first.
- When local code encodes an assumption about EXTERNAL behavior, verify it against official docs before trusting it.

Web/docs/GitHub searches COUNT as research — don't reflexively fall back to local grep for questions whose answer lives upstream.

## Persist-Then-Summarize (Truncation-Resilient Reporting)

Subagents are routinely truncated mid-final-report. Guard against this by persisting
substance into durable artifacts BEFORE composing the final message.

**Persist first:**

| What | Command |
|------|---------|
| Diagnoses / root-cause findings | `wipnote bug set-description <id> "<text>"` or `wipnote feature set-description <id> "<text>"` |
| Progress steps | `wipnote <type> add-step <id> "<step>"` |
| Durable architectural facts | `wipnote arch add <slug> --kind <kind> --body "…" --created-by <agent>` |
| Code changes | `git commit` — only when your task includes committing AND quality gates pass; otherwise record the working-tree state via `add-step` and leave changes uncommitted |

**Then summarize:** The final message to the orchestrator is a compact summary pointing
at those artifacts — not a dump of every detail. If truncation hits, it loses
presentation, never substance.

**On long tasks (>30 tool calls):** Write findings into the work item incrementally
via `add-step` and `set-description` rather than holding everything for the end.

## Development Principles

- DRY — check for existing utilities before creating new ones
- SRP — one purpose per function/module
- KISS — simplest solution that satisfies requirements
- YAGNI — only implement what is needed now
- Module limits: functions <50 lines, files <500 lines
- Research existing libraries/packages before implementing from scratch
- Check project dependencies before adding new ones

These principles are language-neutral and apply to any codebase.
