---
name: wipnote:plan-critique
description: Dual-critic review pass for a drafted plan. Runs DESIGN CRITIC and FEASIBILITY CRITIC agents in parallel against the plan YAML, then writes findings as critic_revisions on the affected slices. Use when asked to critique this plan, review plan, run plan critic pass, or before human review of a multi-slice plan.
---

# wipnote Plan Critique

Invoke this skill **after** the plan is drafted via `wipnote:plan` (slices laid out, decisions_notes captured) and **before** the human review pass. It runs two role-based critic passes against the plan YAML and bakes the findings into the affected slices as `critic_revisions`.

**Trigger keywords:** critique this plan, review plan, plan critic pass, run critics, design critique, feasibility critique

---

## When to run

| Plan shape | Critique? |
|---|---|
| Trivial single-slice plan | Skip — overhead outweighs value |
| Standard plan with 2+ slices | Run |
| Complex plan (any slice with `complexity: complex`) | **Required** before human review |

---

## Invocation

There is no `wipnote plan critique <plan-id>` subcommand. The agent invokes this skill directly, runs both critic prompts against the plan YAML, and writes findings back into the slice cards.

The agent:

1. Loads `.wipnote/plans/<plan-id>.yaml` and reads each slice card.
2. Runs two critic passes itself (no separate CLI dispatch):
   - **DESIGN CRITIC** — Reads the plan as a design document. Looks for missing slices, scope gaps, unclear acceptance criteria, conflicting goals, undefined lifecycle states, and reviewer ergonomics issues (will a human actually understand and approve this?).
   - **FEASIBILITY CRITIC** — Reads the plan as an implementation contract. Verifies cited files exist, line ranges match, existing patterns are reused not reinvented, gates and regex/SQL constraints accommodate the proposed sections, and that each done_when entry is testable from the listed files alone.
3. Records runner metadata using the current harness, not hard-coded model aliases. Examples: `antigravity-design` / `antigravity-feasibility`, `codex-design` / `codex-feasibility`, or `local-design` / `local-feasibility`. If the harness exposes concrete model names, include them in top-level `critique.reviewers` evidence text, not as portable requirements.
4. Applies findings by editing the YAML directly and re-running `wipnote plan validate-yaml <plan-id>`, or by using the available plan rewrite/edit command to write `critic_revisions` back into the affected slice cards. Before scoring any slice, the FEASIBILITY CRITIC MUST:
   - **Verify external claims via web search / web fetch.** When a slice's `what` or `decisions_notes` asserts specific library behaviour, SDK contracts, API response shapes, or harness (Claude Code / Codex CLI / Antigravity) plugin/hook semantics, the critic must check that assertion against current official documentation. Unverified external claims are `warn`-level findings; claims that contradict official docs are `danger`.
   - **Verify the slice's cited `research:` sources.** Fetch each `research[].url` (and `design.research`): the URL must resolve and actually substantiate its stated `claim`. A dead link / 404 / non-supporting source is `warn`; a source that contradicts the claim is `danger`. Stale `accessed` dates on version-sensitive claims warrant a re-check.
   - **Enforce the research gate (v4).** A standard/complex slice carrying neither `research:` nor `research_waiver:`, or a plan with no `design.research`, is a `danger` finding — it also fails `wipnote plan validate-yaml` for v4 plans. Run `wipnote plan validate-yaml <plan-id>` and fold its research errors/advisories into the critique.
   - **Flag reinvented wheels.** When a slice proposes a custom solution for a problem that a well-maintained OSS package already solves, the critic MUST flag it (severity `warn`) and cite what it found (package name + source URL). Confirm the adopt-vs-build decision is research-backed. The goal is to catch custom implementations before they are built, not after.

Both critics produce structured output: a list of findings tagged with a per-slice scope (the slice `num` they target), a severity (`success | warn | danger | info`), a one-line summary, and evidence for any external or runtime claim. Evidence can be a source URL, `runtime: <command>`, or `unverified: <reason>`.

---


## Harness-neutral runner rules

Do not name Claude model aliases such as Haiku, Sonnet, or Opus as required critique runners in Codex, Gemini, Antigravity, or Ollama-facing instructions. Those are provider-specific model families, not portable roles. Always name the role first (`design critic`, `feasibility critic`) and record the actual runner available in the current environment as metadata.

Runtime verification requirements for backend claims:

- **Antigravity / `agy`** — verify installed CLI behavior with `agy --help` and, when model labels matter, `agy models`. In the verified 2026-06-15 environment, `agy --help` exposed `--model`, `--print`, `--prompt`, `--prompt-interactive`, `--sandbox`, `--add-dir`, `--continue`, `--conversation`, `--dangerously-skip-permissions`, and plugin subcommands; `agy models` listed Gemini, Claude, and GPT-OSS options. Public docs may lag or be unavailable, so record runtime evidence explicitly.
- **Codex** — verify against official OpenAI Codex CLI docs and/or `codex exec --help`. The official command reference documents `codex exec`, `--model`, `--oss`, `--local-provider`, `--sandbox`, `--json`, and related non-interactive flags.
- **Ollama** — verify against official Ollama CLI docs and local `ollama --help` when installed. If Ollama is not installed, record that local runtime verification was unavailable and use official docs only.

A critique pass that cannot verify an external CLI flag or model capability must mark the related assumption `unverified` or `plausible`, not `verified`.

## Output shape (critic_revisions)

Each finding lands on the affected slice as a `critic_revisions[]` entry. Schema mirrors `plan/planyaml/schema.go`:

```yaml
slices:
  - id: slice-1
    num: 1
    # ... slice content ...
    critic_revisions:
      - source: antigravity-design
        severity: HIGH          # free-form label; HIGH/LOW/DANGER convention
        summary: "Section-naming contract documented but validSectionRe at api_plans.go:286 rejects the proposed pattern."
      - source: antigravity-feasibility
        severity: LOW
        summary: "Minor: existing TestLimiterMetric covers the regression; no new test needed."
```

The dashboard renders each `critic_revision` as a compact badge inside the slice card so reviewers see the finding alongside the spec it modifies. For external claims, include the source URL or runtime check in the summary if no richer evidence field is available in the schema.

---

## Workflow

1. Agent runs both critic passes against the plan YAML (see Invocation above) — there is no `wipnote plan critique` CLI command.
2. Agent reviews each finding:
   - `success` — informational, no action
   - `warn` — consider addressing before human review
   - `danger` / `HIGH` — must address before human review; rewrite the affected slice
3. Rewrite the plan YAML via `wipnote plan rewrite-yaml <plan-id> --file <path>` with the revised slice cards. Keep the `critic_revisions` entries so reviewers can see what changed and why.
4. Hand off to human review (`wipnote serve` → dashboard) only after `danger`/`HIGH` items are resolved.

---

## Output format (top-level critique section, optional)

For complex plans, the critics also populate a top-level `critique:` section in the plan YAML. Schema from `plan/planyaml/schema.go`:

```yaml
critique:
  reviewed_at: "YYYY-MM-DD"
  reviewers:
    - "antigravity-design (role: design critic; runner/model verified locally or by docs)"
    - "antigravity-feasibility (role: feasibility critic; runner/model verified locally or by docs)"
  assumptions:
    - id: A1
      status: verified|plausible|unverified|questionable|falsified
      text: "<assumption>"
      evidence: "<file:line citation, source URL, or runtime command>"
  critics:
    - title: DESIGN CRITIC
      sections:
        - heading: "<theme>"
          items:
            - badge: "<short label>"
              kind: success|warn|danger|info
              text: "<finding>"
    - title: FEASIBILITY CRITIC
      sections:
        # ... same shape ...
  risks:
    - risk: "<one-line risk>"
      severity: High|Medium|Low
      mitigation: "<concrete action>"
  synthesis: |
    One-paragraph summary: which blockers resolved, which remain, what
    the human reviewer should focus on first.
```

The synthesis paragraph is the artifact the orchestrator reads when deciding whether the plan is ready for human review. Be concrete: name specific blockers by ID (A1/B2/Wrong-3) so the next reader can map findings back to code.

---

## Section-naming contract

Critique state stored in `plan_feedback` uses these section keys (mirrored in `plugin/skills/plan/SKILL.md`):

| Key pattern | What it stores |
|---|---|
| `slice-<num>` | Per-slice critic findings (action=`add_critic_revision`) |
| `critique` | Top-level critique section approval / acknowledgment |

The agent maps `<slice-num>` to the section key when writing findings back to the YAML.

---

## Relationship to other skills

- **`wipnote:plan`** — produces the plan being critiqued. Run that skill first.
- **`wipnote:plan-critique`** (this skill) — runs after drafting, before human review.
- **`wipnote:spec-from-slice`** — runs after human approval, before promotion. Reads `decisions_notes` written during the interview; critique findings should be merged into that prose before promotion.

Do NOT invoke this skill during the interview itself — interview-stage AUQ Q&A captures requirements; critique evaluates the resulting design. Mixing the two stages confuses the reviewer model.
