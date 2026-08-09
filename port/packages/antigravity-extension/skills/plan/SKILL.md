---
name: wipnote:plan
description: Plan development work using a triage-gated, blocks-first interview. Classify scope as trivial/standard/complex, then run 0/3/4 staged interview rounds through the current harness native ask-user tool when available, or plain conversation when it is not. Each stage elicits the VISUAL block first (file-tree, api-endpoint, data-model, wireframe/diagram) and derives the prose slice fields (what/why/done_when) from it — blocks are authored inline as the YAML is built, not in a post-pass. Produces slice-card YAML with grounded visual blocks and visible research provenance for external claims; pauses for human review; promotes approved slices to features. Use when asked to plan, create a development plan, or build a feature with design clarity first.
---

# wipnote Plan

Treat plan creation as a **blocks-first** system design interview. You are the candidate; the user is the interviewer with requirements. Extract requirements via staged questions before producing slice YAML. Prefer the current harness native ask-user tool when one is available; otherwise ask the same questions in plain chat. Do not jump to a 9-field worksheet — earn each field through the interview, or explicitly mark fields as inferred/skipped when the user has supplied a complete spec and the harness lacks interactive tools.

**Blocks-first is the defining discipline.** In each interview stage, author the VISUAL artifact FIRST — the file-tree of files the slice touches, the api-endpoint and data-model of its contract, the wireframe/diagram of its UI/flow — then DERIVE the prose fields (`what`/`why`/`done_when`) from those blocks. Blocks are written into the slice YAML INLINE as it is built; there is no separate visual pass. The blocks drive the design. (The separate `wipnote:visual-plan` skill is now only for after-the-fact enrichment of existing/legacy plans — never the primary path.)

**Trigger keywords:** create plan, development plan, parallel plan, plan tasks, plan this feature, review before building, generate plan, scaffold plan, slice plan, crispi

---

## Step 0: Triage

Before any slice content, classify the work. The classification drives both the interview depth and the validator's mandatory-field set on the resulting slice cards.

| Complexity | Interview stages | Mandatory deltas vs default |
|---|---|---|
| `trivial` | 0 stages | `what`/`done_when`/`tests`/`decisions_notes`/`blocks` all optional |
| `standard` | 3 stages (Requirements, Scope & state, Done-when) | `what`, `decisions_notes` >=50 chars, >=1 visual block **or** an explicit `blocks_waiver:` |
| `complex`  | 4 stages (all) | `decisions_notes` >=50 chars; >=2 `done_when` entries; >=1 slice-local question with an answer; >=1 visual block **or** an explicit `blocks_waiver:` |

Set `complexity: trivial|standard|complex` on each slice card. The field is read by `plan/planyaml/validate.go` via `effectiveComplexity`; an unset value defaults to `standard` for back-compat.

Every plan you create must include `meta.schema_version: v4`. This enables strict validation including the decisions_notes requirement for standard/complex slices (even when `complexity` is omitted, which defaults to standard), **the blocks gate** (below): every standard/complex slice must carry >=1 visual block or an explicit `blocks_waiver:`, and **the research gate** (below): every standard/complex slice must carry cited `research:` or an explicit `research_waiver:`, and `design.research` must cite at least one source. `v3` plans are still accepted for back-compat and get the same strict decisions_notes/blocks enforcement, but research stays advisory-only until `v4` — new plans must be `v4` so research is enforced too, not optional.

### Triage question template

Before emitting any slice, ask this question with the native ask-user tool when available. In Codex or any harness without such a tool, render it as plain chat and wait for the user answer unless the user already supplied enough detail to classify the work:

```json
{
  "questions": [
    {
      "question": "How would you classify this work?",
      "header": "Triage",
      "multiSelect": false,
      "options": [
        {"label": "Trivial — one-shot patch, no design risk", "description": "Skip interview. Produce a minimal slice card."},
        {"label": "Standard — needs design clarity but scope is bounded", "description": "Run 3 interview stages (Requirements, Scope & state, Done-when)."},
        {"label": "Complex — non-trivial system design, multiple unknowns", "description": "Run all 4 interview stages (Requirements, Scope, Contract, Done-when)."},
        {"label": "Skip interview — I'll paste the spec", "description": "User supplies the full spec directly; agent drafts the YAML from it."}
      ]
    }
  ]
}
```

---

## Mandatory Research (ENFORCED for v4 plans)

Before drafting slices, run live web research using your web search / web fetch tools — do not rely on training-data knowledge for version-sensitive details:

1. **Latest official docs and standards** — for every technology the plan touches (libraries, protocols, APIs), fetch or search current documentation.
2. **Existing OSS packages and tools** — before designing a custom implementation for any non-trivial component, search for well-maintained packages that already solve the problem (pkg.go.dev, npm, PyPI, crates.io). Capture the adopt-vs-build decision: if adopting, cite the package; if building custom, cite what you evaluated and why it was rejected. The goal is **no reinventing the wheel**.
3. **Provider docs for harness integration** — for agent/hook/skill/plugin work against Claude Code, Codex CLI, or Antigravity, check the provider docs for existing primitives that may already cover the requirement.

### The research gate (schema-enforced)

Research is **not optional discipline — the validator enforces it** for `v4` plans (`plan/planyaml/validate.go`):

- **`design.research`** must cite ≥1 source (the plan-wide research basis).
- **Every standard/complex slice** must carry ≥1 `research:` source **or** an explicit `research_waiver:`. Trivial slices are exempt.
- Every source `url` must be `http(s)`.

A plan that violates this fails `wipnote plan validate-yaml` (and finalize). Record research in the **structured** `research:` field — not buried in prose:

```yaml
design:
  research:
    - url: https://example.com/docs
      claim: Confirms the v2 API shape used in slice 3.
      accessed: 2026-06-18
slices:
  - num: 3
    complexity: standard
    research:
      - url: https://pkg.go.dev/github.com/x/y
        claim: Battle-tested parser — adopted instead of hand-rolling (slice avoids reinventing).
        accessed: 2026-06-18
  - num: 4
    complexity: standard
    research_waiver: stdlib net/http only — no external dependency or standard applies.
```

If research genuinely doesn't apply to a slice, use `research_waiver:` with the reason — never leave it silently empty. Mark any unverifiable external assumption as `unverified`; do not present it as a confirmed fact.

---

## The Interview — Blocks-First (standard = stages 1, 2, 4; complex = all 4)

Each stage elicits the VISUAL block FIRST, then derives prose from it. The block is authored INLINE into the slice's `blocks:` field as the YAML is built — never as a later pass.

| Stage | Author this block FIRST | Then derive these prose fields | Typical AUQ shape |
|---|---|---|---|
| 1. Requirements | `wireframe`/`diagram` (UI/flow work only; skip for non-visual slices) | `why`, `what` (from the sketched surface/flow), `decisions_notes` (rationale half) | "Sketch the user-visible surface/flow. What problem does it solve, for whom?" |
| 2. Scope & state | `file-tree` of the real files this slice touches (new + edited) | `files`, `what` (scope half) — read straight off the tree | "List the files. Where does the state live? Any cross-slice ordering?" |
| 3. API / contract | `api-endpoint` (method+path+params) and/or `data-model` (typed columns) | `what` (contract half), `done_when` (per route/entity), `decisions_notes` (interface picks) | "Author the route/entity. What's the firing rule? What does the response carry?" |
| 4. Done-when | (no new block — anchor acceptance criteria to the blocks above) | `done_when`, `tests`, `effort`, `risk` | "How will you tell each block's behavior works? Which existing tests must still pass?" |

**Derivation, not duplication:** the prose fields restate the block in narrative form for the slice card; they must stay consistent with the block. If a stage has no natural visual artifact (e.g. a pure non-visual standard slice), say so and proceed to prose directly — do not invent a block to satisfy the form.

**Keep `what` under 56 words** as you accumulate it across stages 1–3 — it's a hard cap in `wipnote plan validate-yaml` (see Prose-Length Caps below). Read straight off the blocks and stop; push elaboration and rejected alternatives into `decisions_notes` instead of padding `what`.

Each stage = the block authoring step + 1-3 questions, in a single native ask-user call where available, or one compact chat question set where it is not.

### Block-authoring prep (read the live catalog ONCE, up front)

Block elicitation is folded into the interview — but the **vocabulary** of block types still has a single source of truth. Before the first slice, read the live catalog so you author with current tags/fields and never hardcode them:

```bash
wipnote plan blocks            # the supported block types + required fields/row keys
```

The per-slice interview question set already carries the blocks-first prompts for you — `wipnote plan interview-questions <plan-id> <slice-num>` emits, per stage, a `blocks` array (each with `type`, the catalog `description`, and a `prompt` for what to capture). Render those prompts alongside the stage questions so the authoring step is explicit in whatever surface you use.

### Grounding rule (enforced discipline)

A block is grounded when every field, route, and file path it names either ALREADY exists in the codebase or WILL exist as a direct output of this slice. Never invent schema, routes, or files to fill a block. If you cannot ground a block type for a slice without inventing data, skip that block type for that slice and note why in `decisions_notes`.

- `data-model` rows: real or will-exist field names/types.
- `api-endpoint` method/path: routes the slice will actually implement.
- `file-tree` entries: real files the slice touches.
- `wireframe` HTML: `var(--wf-*)` design tokens only — never raw hex/rgb.

### Triage gating for blocks

| Complexity | Blocks expected |
|---|---|
| `trivial` | Minimal or none — trivial slices have no design surface to visualise; skip blocks. |
| `standard` | Blocks expected when the slice is concrete enough to ground them (a real file-tree at minimum; api-endpoint/data-model where a contract exists). If the slice is genuinely underspecified, note the skip rather than invent. |
| `complex` | Blocks expected — author every block type the slice justifies (file-tree always; api-endpoint/data-model/wireframe/diagram as the design warrants). |

**This is schema-enforced, not just advisory.** `wipnote plan validate-yaml` FAILS any standard/complex slice with zero blocks, unless `decisions_notes` carries an explicit `blocks_waiver: <reason>` marker line — the machine-checkable opt-out for a slice that is genuinely underspecified (never invent a block to satisfy the gate; see the grounding rule above). Trivial slices stay exempt. This gate applies whenever `meta.schema_version` is `v3` or `v4`, or the slice sets `complexity:` explicitly — i.e. any plan authored under this triage-gated model, per Step 0 above. (It does NOT retroactively apply to pre-triage legacy plans that carry neither `schema_version` nor an explicit `complexity:` — see `plan/planyaml/validate.go`'s blocks-gate comment for the measured back-compat rationale.) A superseded, purely-advisory version of this same signal remains available as `ValidateBlockAdvisories` for finalized plans, which the hard gate does not cover.

```yaml
slices:
  - num: 4
    complexity: standard
    decisions_notes: |
      Pure refactor of the retry loop's backoff constant — no new file, route,
      or schema surface to visualise.
      blocks_waiver: refactor-only slice, nothing to ground a block against.
```

If the skip reason is genuine, `blocks_waiver:` on its own line inside `decisions_notes` satisfies the gate — never leave blocks silently empty.

### Rendering the interview (cross-harness)

**One workflow, lowest-friction renderer.** The questions have a single canonical source — the binary — and you render them with whatever mechanism the *current* harness supports, preferring the one that keeps the user in place. Don't default to the web form; it's a context switch.

Get the canonical set (one source of truth, never hand-maintained):

    wipnote plan interview-questions <plan-id>            # upfront: triage + problem/goals/constraints
    wipnote plan interview-questions <plan-id> <slice-num>  # per-slice: template + the slice's open questions

Render it by capability, in this preference order:

1. **Native ask-user tool, inline** — e.g. Claude `AskUserQuestion`, Gemini/Antigravity `ask_user` when exposed. Map each stage's questions to one tool call. No context switch. Preferred whenever the harness has it.
2. **Plain conversational, non-interactive** — harnesses with no ask-user tool (e.g. Codex): just ask the questions as text in your turn (present each question + its options); the user answers in their reply. Still no context switch. **Triage is always done this way or via the tool — never a form** (it's one question).
3. **Web form (optional)** — only when a richer visual surface genuinely helps (a long complex interview) or the user asks for it: `wipnote plan interview-questions … | wipnote plan interview … --questions -`. The form blocks, persists on submit, and embeds the plan-review chat panel. It's an enhancement, not the default path.

Then **persist non-interactively** (the form does this itself; for the inline/conversational paths you write the answers):
- Upfront intake → `wipnote plan set-design-yaml <plan> --problem … --goals … --constraints …`, then draft slice cards using the assessed complexity.
- Per-slice → `wipnote plan elicit-decisions <plan> <slice> --scope … --decisions … --context …`.

**Adaptive follow-ups** stay in the same channel — after persisting, judge whether a mandatory field is unmet or something's ambiguous; if so, ask the gap questions again via the *same* mechanism (tool / conversation / another form round). You own the round-to-round adaptivity; nothing keeps state between rounds.

### Codex-compatible fallback

Codex may not expose `AskUserQuestion`, Task tools, or Claude-style agent dispatch. In Codex:

1. Ask triage and interview questions in plain chat, or infer answers from the supplied spec if the user explicitly asks you to proceed without more questions.
2. Persist inferred/skipped answers in `decisions_notes` with labels such as `**Inferred:**` and `**Skipped:**` so reviewers can see where the interview was compressed.
3. Run mandatory live web research before drafting slices, and record each finding in the structured `research:` fields (`design.research` + per-slice `research:`/`research_waiver:`).
4. Validate locally with `wipnote plan validate-yaml <plan-id>` before requesting human review — for v4 plans this fails on any standard/complex slice missing `research:`/`research_waiver:` or a `design.research` basis.

Acceptance criteria for generated plans:

- No Codex-facing plan instructions depend on Claude-only model aliases or Claude-only Task/AUQ tools.
- `meta.schema_version: v4`, and every standard/complex slice carries cited `research:` (http(s) URLs) or an explicit `research_waiver:`; `design.research` cites the plan-wide basis. Unverifiable assumptions are marked `unverified`, not asserted.
- Adopt-vs-build was researched for every non-trivial component — a battle-tested package is cited when one fits, or the rejection reason is recorded (no reinventing the wheel).
- Any skipped interview stage is named and explained in the slice's `decisions_notes`.


**`--questions` JSON schema:**

    {"stages": [
      {"key": "scope", "title": "Scope & state", "bucket": "Scope",
       "blocks": [
         {"type": "file-tree", "description": "An ordered list of file paths the slice touches.",
          "prompt": "FIRST author a file-tree block of the real files this slice touches; derive `files` and the scope half of `what` from it."}
       ],
       "questions": [
         {"id": "scope.0", "header": "State", "type": "choice",
          "prompt": "Where does the state live?",
          "options": [{"label": "In .wipnote/ canonical files", "description": "HTML / plan YAML / ledgers — committed"}]}
       ]}
    ]}

- `blocks` (optional, per stage) — the blocks-first authoring prompts: `type` (a key from `wipnote plan blocks`), the catalog `description`, and a `prompt` for what to capture. Author the block(s) FIRST, then answer the questions to sharpen the derived prose. `wipnote plan interview-questions` populates this array from the live catalog.
- `bucket` ∈ `Scope` | `Decisions` | `Context` — routes the stage's answers into that `decisions_notes` subsection.
- `type` ∈ `choice` (needs `options`, optional `multiSelect`) | `text` | `yesno`.
- Question `id`s must be unique; the form posts answers back under them and composes into the same `### Scope` / `### Decisions` / `### Context` markdown the AUQ path produces — so downstream (`validate-yaml`, `spec generate --insert`, `promote-slice`, `finalize`) is identical regardless of renderer.

### Example AUQs

In each stage, lead with the block-authoring step (per `wipnote plan interview-questions`' `blocks` array), then render these questions. The questions sharpen the prose you derive from the block.

**Stage 1 — Requirements:**

```json
{
  "questions": [
    {"question": "What's the user-visible behavior we're after?", "header": "Goal", "multiSelect": false, "options": [
      {"label": "New capability — feature add", "description": "User can do X they couldn't before."},
      {"label": "Behavior change — modify existing", "description": "X already works but in a way that's wrong/slow/incomplete."},
      {"label": "Bug fix — restore intended behavior", "description": "X should already work; root-cause and fix."}
    ]},
    {"question": "What's the hard constraint?", "header": "Constraint", "multiSelect": false, "options": [
      {"label": "Performance budget", "description": "p99, throughput, or memory ceiling."},
      {"label": "Backward compatibility", "description": "Existing on-disk data must keep working."},
      {"label": "No new runtime dependency", "description": "Stdlib + existing go.mod only."},
      {"label": "Other — I'll describe in chat", "description": ""}
    ]}
  ]
}
```

**Stage 2 — Scope & state:**

```json
{
  "questions": [
    {"question": "Where does the state live?", "header": "State", "multiSelect": false, "options": [
      {"label": "In .wipnote/<kind>/*.html", "description": "Canonical work-item / arch state — committed with the repo."},
      {"label": "In a .wipnote ledger or NDJSON", "description": "Append-only canonical record: session / claim / gate ledgers, per-session events."},
      {"label": "Derived from git", "description": "Commit and file attribution — recomputed on demand, never stored."},
      {"label": "In-memory only", "description": "No persistence; lifecycle = process. This is where the SQLite projection lives."},
      {"label": "On the filesystem outside .wipnote/", "description": "e.g., session transcripts, hook artifacts."}
    ]}
  ]
}
```

**Stage 3 — API / contract:**

```json
{
  "questions": [
    {"question": "What's the firing rule for this event?", "header": "Trigger", "multiSelect": false, "options": [
      {"label": "On every tool call", "description": "PreToolUse / PostToolUse hook."},
      {"label": "On user prompt submission", "description": "UserPromptSubmit hook."},
      {"label": "On session lifecycle", "description": "SessionStart / SessionEnd hook."},
      {"label": "On explicit CLI invocation only", "description": "Not a hook — wipnote subcommand."}
    ]}
  ]
}
```

**Stage 4 — Done-when:**

```json
{
  "questions": [
    {"question": "How will you tell it works?", "header": "Acceptance", "multiSelect": false, "options": [
      {"label": "Unit test on the smallest function", "description": "Pure function — input/output check."},
      {"label": "Integration test through the CLI", "description": "Spawn the binary, assert exit code + side effects."},
      {"label": "Manual smoke test against the dashboard", "description": "Operator-visible behavior; no auto-assertion."}
    ]}
  ]
}
```

---

## decisions_notes Discipline

Write `decisions_notes` inline as the Q&A unfolds — don't retrofit at the end. Capture both the chosen option AND the rejected ones, with a one-line reason for each rejection. Structure with `**Trigger:**` / `**State:**` / `**Payload:**` / `**Rejected:**` headings so `wipnote spec generate --insert` can weave the prose into the spec's `## Decisions` section verbatim.

For standard and complex slices, the validator requires `decisions_notes` >= 50 characters (after `TrimSpace`) when `meta.status != "finalized"`. Empty/short decisions_notes fail `wipnote plan validate-yaml`.

---

## Prose-Length Caps

A corpus audit found `what` alone accounted for 33% of all slice-prose words across every plan in `.wipnote/plans/*.yaml`, at a median of 96 words/slice — prose was inflating instead of staying scannable. `wipnote plan validate-yaml` now enforces a **56-word hard cap on `what`** (`plan/planyaml/validate.go`, the corpus's 25th percentile): over the cap fails validation with the field and actual word count in the message, unless `meta.status == "finalized"` (historical plans are exempt — the cap only ever constrains new authoring). 14-32 words is a demonstrated working range: plan-f8c02547's own blocks-first slices land there and were never flagged as unclear.

Write `what` tight: state the change and where it lands, then move rationale, rejected alternatives, and elaboration into `decisions_notes` — that field has no length cap and is exactly where that detail belongs.

`why`, `done_when`, and `tests` are **advisory only** — `wipnote plan validate-yaml` warns when they run unusually long (each field's own 75th-percentile norm in the same corpus audit: `why` > 48 words, `tests` > 53 words, `done_when` > 77 words summed across its entries) but never fails the build. Treat the warning as a prompt to trim, not a gate.

---

## Trivial Slice Example

```yaml
meta:
  id: plan-<generated>
  title: "Fix typo in serve.go log message"
  status: draft
  schema_version: v4
slices:
  - id: slice-1
    num: 1
    title: "Fix typo in serve.go log message"
    why: "User-facing log message reads 'lisening' instead of 'listening'."
    files:
      - cmd/wipnote/serve.go
    effort: S
    risk: Low
    complexity: trivial
    deps: []
```

That's it. No `what`, `done_when`, `tests`, or `decisions_notes` required.

## Complex Slice Example (abbreviated)

```yaml
meta:
  id: plan-<generated>
  title: "Rate-limit /api/ingest"
  status: draft
  schema_version: v4
slices:
  - id: slice-1
    num: 1
    title: "Rate-limit /api/ingest per client IP"
    what: |
      Add token-bucket limiter in internal/ratelimit/limiter.go keyed by
      client IP. Middleware in cmd/wipnote/serve.go returns HTTP 429 with
      JSON body when bucket empty.
    why: |
      Protects the canonical event writer from burst overload (Goal 1 — throughput cap).
    files:
      - internal/ratelimit/limiter.go
      - cmd/wipnote/serve.go
    deps: []
    blocks:
      # Authored FIRST during the Scope stage — `files` was read off this tree.
      - type: file-tree
        title: Files touched
        entries:
          - internal/ratelimit/limiter.go
          - cmd/wipnote/serve.go
      # Authored FIRST during the API/contract stage — `done_when` derived from it.
      - type: api-endpoint
        fields:
          method: POST
          path: /api/ingest
        rows:
          - name: "429 body"
            type: '{"error":"rate_limited","retry_after":1}'
    done_when:
      - "Requests above the configured limit receive HTTP 429"
      - "Requests within the limit pass through unchanged"
      - "Counter `wipnote_throttled_total` increments on each 429"
    effort: M
    risk: Med
    tests: |
      Unit: TestLimiter_Allow — 10 req/s burst, first 5 allow, next reject
      Integration: TestServeRateLimit — spin up server, assert 429s
    complexity: complex
    decisions_notes: |
      **Trigger:** middleware on /api/ingest, keyed by RemoteAddr.
      **State:** in-memory map[ip]bucket, reset on process restart.
      **Payload:** 429 + {"error":"rate_limited","retry_after":1}.
      **Rejected:** file-backed counters (write amplification under load);
        Redis (new runtime dep — violates constraint).
    questions:
      - id: q-key
        text: "Key by RemoteAddr or X-Forwarded-For?"
        answer: "RemoteAddr — wipnote runs behind localhost only; XFF would need explicit trust list."
```

---

## Step 1: Create the Plan YAML

```bash
wipnote plan create-yaml "<title>" --description "<description>" --track <trk-id>
```

Note the returned plan ID. Write the YAML to `.wipnote/plans/<plan-id>.yaml` via `wipnote plan rewrite-yaml <plan-id> --file /tmp/plan.yaml`.

## Step 2: Validate, Critique, Review, Promote

```bash
wipnote plan validate-yaml <plan-id>
```

Fix schema errors before continuing. Because blocks were authored inline during the interview (Step 0/the interview above), the YAML you wrote already carries grounded `blocks:` for qualifying slices — there is **no separate visual pass**. `validate-yaml` FAILS any standard/complex slice that still has no blocks (see "The blocks gate" above) unless `decisions_notes` carries a `blocks_waiver: <reason>` marker; treat a failure as a prompt to revisit the blocks-first interview for that slice (or, for a legacy/already-drafted plan, run `wipnote:visual-plan` to enrich it after the fact, or record the waiver if the slice is genuinely underspecified).

After the plan is drafted, run `/wipnote:plan-critique <plan-id>` for the dual role-based design/feasibility review pass. See `plugin/skills/plan-critique/SKILL.md`. If critique changes a slice's design, update its blocks-first in the same edit (re-author the block, then re-derive the prose) so blocks and prose stay consistent.

Then:

```bash
wipnote serve              # human reviews per-slice in dashboard
wipnote plan read-feedback-yaml <plan-id>   # ingest feedback
wipnote plan elicit-decisions <plan-id> <num> --scope ... --decisions ... --context ...
wipnote plan promote-slice <plan-id> <num>  # creates feat-XXX
wipnote spec generate <feat-id> --insert    # materialises spec section
wipnote plan set-status <plan-id> completed # when all slices done
```

---

## Section-Naming Contract (load-bearing)

State stored in `plan_feedback` uses these section keys — mirrored in
`plan/planyaml/schema.go:43-55` and enforced by `validSectionRe` in
`cmd/wipnote/api_plans.go`:

| Key pattern | What it stores |
|-------------|----------------|
| `slice-<num>` | Slice-level approval (`action=approve`) and execution status (`action=set_execution_status`) |
| `slice-<num>-question-<id>` | Answer to a slice-local question (`action=answer`) |
| `design` | Design section approval |
| `questions` | Plan-level question answers |

The `answer-slice-question` command maps `<slice-num>` and `<question-id>` to the section key automatically.

---

## CLI Reference

| Command | Effect |
|---------|--------|
| `wipnote plan create-yaml "<title>" --description "<desc>" --track <trk>` | Creates plan stub |
| `wipnote plan rewrite-yaml <plan-id> --file <path>` | Replaces plan YAML body |
| `wipnote plan validate-yaml <plan-id>` | Runs schema validation |
| `wipnote plan approve-slice <plan-id> <num>` | Sets `approval_status=approved` |
| `wipnote plan reject-slice <plan-id> <num> [--changes-requested]` | Sets `approval_status=rejected` / `changes_requested` |
| `wipnote plan answer-slice-question <plan-id> <num> <q-id> <answer-key>` | Records slice-local answer |
| `wipnote plan set-slice-status <plan-id> <num> <status>` | Sets `execution_status` |
| `wipnote plan elicit-decisions <plan-id> <num> --scope ... --decisions ... --context ...` | Writes `decisions_notes` |
| `wipnote plan promote-slice <plan-id> <num> [--waive-deps]` | Creates `feat-XXX`, wires edges |
| `wipnote plan read-feedback-yaml <plan-id>` | Reads human feedback from `plan_feedback` |
| `wipnote plan set-status <plan-id> <status>` | Plan lifecycle (`active`/`completed`) |

---

## Related Skills

- `plugin/skills/plan-critique/SKILL.md` — dual-critic review pass (run after the plan is drafted)
- `plugin/skills/visual-plan/SKILL.md` — SECONDARY/enrichment path: add or revise grounded visual blocks on an EXISTING plan (including legacy plans drafted before blocks-first, or ad-hoc later additions). Blocks-first is now built into THIS skill's interview; use visual-plan only for after-the-fact enrichment, not primary planning.
- `plugin/skills/plan/LEGACY.md` — v1 plan compatibility notes (referenced by validator errors when an old plan is loaded)
