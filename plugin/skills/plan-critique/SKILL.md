---
name: wipnote:plan-critique
description: Dual-critic review pass for a drafted plan. Runs DESIGN CRITIC and FEASIBILITY CRITIC agents in parallel against the plan YAML, then writes findings as critic_revisions on the affected slices. Use when asked to critique this plan, review plan, run plan critic pass, or before human review of a multi-slice plan.
---

# wipnote Plan Critique

Invoke this skill **after** the plan is drafted via `wipnote:plan` (slices laid out, decisions_notes captured) and **before** the human review pass. It runs two critic agents in parallel against the plan YAML and bakes the findings into the affected slices as `critic_revisions`.

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
   - **DESIGN CRITIC** — Haiku model. Reads the plan as a design document. Looks for missing slices, scope gaps, unclear acceptance criteria, conflicting goals, undefined lifecycle states, and reviewer ergonomics issues (will a human actually understand and approve this?).
   - **FEASIBILITY CRITIC** — Sonnet model. Reads the plan as an implementation contract. Verifies cited files exist, line ranges match, existing patterns are reused not reinvented, gates and regex/SQL constraints accommodate the proposed sections, and that each done_when entry is testable from the listed files alone.
3. Applies findings by editing the YAML directly and re-running `wipnote plan validate <plan-id>`, or by using `wipnote plan edit <plan-id>` to write `critic_revisions` back into the affected slice cards. Before scoring any slice, the FEASIBILITY CRITIC MUST:
   - **Verify external claims via web search / web fetch.** When a slice's `what` or `decisions_notes` asserts specific library behaviour, SDK contracts, API response shapes, or harness (Claude Code / Codex CLI / Gemini CLI) plugin/hook semantics, the critic must check that assertion against current official documentation. Unverified external claims are `warn`-level findings; claims that contradict official docs are `danger`.
   - **Flag reinvented wheels.** When a slice proposes a custom solution for a problem that a well-maintained OSS package already solves, the critic MUST flag it (severity `warn`) and cite what it found (package name + source). The goal is to catch custom implementations before they are built, not after.

Both critics produce structured output: a list of findings tagged with a per-slice scope (the slice `num` they target), a severity (`success | warn | danger | info`), and a one-line summary.

---

## Output shape (critic_revisions)

Each finding lands on the affected slice as a `critic_revisions[]` entry. Schema mirrors `plan/planyaml/schema.go`:

```yaml
slices:
  - id: slice-1
    num: 1
    # ... slice content ...
    critic_revisions:
      - source: haiku           # design critic
        severity: HIGH          # free-form label; HIGH/LOW/DANGER convention
        summary: "Section-naming contract documented but validSectionRe at api_plans.go:286 rejects the proposed pattern."
      - source: sonnet          # feasibility critic
        severity: LOW
        summary: "Minor: existing TestLimiterMetric covers the regression; no new test needed."
```

The dashboard renders each `critic_revision` as a compact badge inside the slice card so reviewers see the finding alongside the spec it modifies.

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
    - "Haiku (design)"
    - "Sonnet (feasibility)"
  assumptions:
    - id: A1
      status: verified|plausible|unverified|questionable|falsified
      text: "<assumption>"
      evidence: "<file:line citation>"
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
