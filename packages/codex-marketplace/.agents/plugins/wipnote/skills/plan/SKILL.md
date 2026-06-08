---
name: wipnote:plan
description: Plan development work using a triage-gated interview. Classify scope as trivial/standard/complex, then run 0/2/4 staged interview rounds — rendered inline via AskUserQuestion on Claude Code, or as the cross-harness `wipnote plan interview` web form — to earn each slice field. Produces slice-card YAML; pauses for human review; promotes approved slices to features. Use when asked to plan, create a development plan, or build a feature with design clarity first.
---

# wipnote Plan

Treat plan creation as a system design interview. You are the candidate; the user is the interviewer with requirements. Extract requirements via staged `AskUserQuestion` calls before producing slice YAML. Do not jump to a 9-field worksheet — earn each field through the interview.

**Trigger keywords:** create plan, development plan, parallel plan, plan tasks, plan this feature, review before building, generate plan, scaffold plan, slice plan, crispi

---

## Step 0: Triage

Before any slice content, classify the work. The classification drives both the interview depth and the validator's mandatory-field set on the resulting slice cards.

| Complexity | Interview stages | Mandatory deltas vs default |
|---|---|---|
| `trivial` | 0 stages | `what`/`done_when`/`tests`/`decisions_notes` all optional |
| `standard` | 3 stages (Requirements, Scope & state, Done-when) | `what`, `decisions_notes` >=50 chars |
| `complex`  | 4 stages (all) | `decisions_notes` >=50 chars; >=2 `done_when` entries; >=1 slice-local question with an answer |

Set `complexity: trivial|standard|complex` on each slice card. The field is read by `plan/planyaml/validate.go` via `effectiveComplexity`; an unset value defaults to `standard` for back-compat.

Every plan you create must include `meta.schema_version: v3`. This enables strict validation including the decisions_notes requirement for standard/complex slices — even when `complexity` is omitted from a slice (which defaults to standard).

### Triage AUQ template

Before emitting any slice, run this `AskUserQuestion` (paste-ready):

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

## The Interview (standard = stages 1, 2, 4; complex = all 4)

| Stage | Goal | Slice fields it populates | Typical AUQ shape |
|---|---|---|---|
| 1. Requirements | Functional + non-functional + constraints | `why`, `decisions_notes` (rationale half) | "What problem? Who's the user? What's a hard constraint?" |
| 2. Scope & state | Where the change lives; what state it owns | `files`, `deps`, `what` (scope half) | "Which files? Where does the state live? Any cross-slice ordering?" |
| 3. API / contract | Public-facing surface, payload, return shape | `what` (contract half), `decisions_notes` (interface picks) | "What's the firing rule for this event? What does the response carry?" |
| 4. Done-when | Acceptance criteria, tests, effort, risk | `done_when`, `tests`, `effort`, `risk` | "How will you tell it works? Which existing tests must still pass?" |

Each stage = 1-3 questions in a single `AskUserQuestion` call (AUQ supports up to 4 per call).

### Rendering the interview (cross-harness)

The staged interview is render-agnostic — the same stages drive any of these. Pick by harness:

- **Claude Code** — render each stage inline with `AskUserQuestion` (the examples below). Fastest path, no context switch. This is the Claude fast-path.
- **Any harness (Codex, Gemini, or when a richer review surface helps)** — render the SAME stage(s) as a local web form, the portable canonical path:
  1. Write the round's stages as JSON to a temp file (schema below).
  2. Run `wipnote plan interview <plan-id> <slice-num> --questions <file>`. It prints a localhost URL, **blocks** until the user submits, writes the answers into the slice's `decisions_notes` (git-committed automatically), then exits. The form embeds the plan-review **chat panel**, so the user can ask clarifying questions or request plan changes mid-form.
  3. Read the result: `wipnote plan show <plan-id>` (the slice's `decisions_notes`).

**Adaptive follow-ups (each form is one independent round).** After reading the submitted `decisions_notes`, judge whether anything is still ambiguous or a mandatory field is unmet. If so, compose a NEW stage set targeting only the gaps and launch another `wipnote plan interview … --questions <file2>` round. Repeat until the slice's mandatory fields are satisfied. Do **not** keep one form alive across rounds — you (the agent) own the round-to-round adaptivity; the form is a stateless renderer that returns to the CLI on submit.

**`--questions` JSON schema:**

    {"stages": [
      {"key": "requirements", "title": "Requirements", "bucket": "Decisions",
       "questions": [
         {"id": "requirements.0", "header": "Goal", "type": "choice",
          "prompt": "What's the user-visible behavior?",
          "options": [{"label": "New capability", "description": "user can do X"}]},
         {"id": "requirements.1", "header": "Payload", "type": "text",
          "prompt": "Input/output contract?", "placeholder": "in: … out: …"}
       ]}
    ]}

- `bucket` ∈ `Scope` | `Decisions` | `Context` — routes the stage's answers into that `decisions_notes` subsection.
- `type` ∈ `choice` (needs `options`, optional `multiSelect`) | `text` | `yesno`.
- Question `id`s must be unique; the form posts answers back under them and composes into the same `### Scope` / `### Decisions` / `### Context` markdown the AUQ path produces — so downstream (`validate-yaml`, `spec generate --insert`, `promote-slice`, `finalize`) is identical regardless of renderer.

### Example AUQs

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
      {"label": "In SQLite (read index)", "description": "internal/db/ — derived, can be rebuilt."},
      {"label": "In .wipnote/<kind>/*.html", "description": "Canonical store — survives DB rebuild."},
      {"label": "In-memory only", "description": "No persistence; lifecycle = process."},
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

## Trivial Slice Example

```yaml
meta:
  id: plan-<generated>
  title: "Fix typo in serve.go log message"
  status: draft
  schema_version: v3
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
  schema_version: v3
slices:
  - id: slice-1
    num: 1
    title: "Rate-limit /api/ingest per client IP"
    what: |
      Add token-bucket limiter in internal/ratelimit/limiter.go keyed by
      client IP. Middleware in cmd/wipnote/serve.go returns HTTP 429 with
      JSON body when bucket empty.
    why: |
      Protects SQLite writer from burst overload (Goal 1 — throughput cap).
    files:
      - internal/ratelimit/limiter.go
      - cmd/wipnote/serve.go
    deps: []
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
      **Rejected:** SQLite-backed counters (write amplification under load);
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

Fix schema errors before continuing.

After the plan is drafted, run `/wipnote:plan-critique <plan-id>` for the dual-critic (Sonnet+Haiku) review pass. See `plugin/skills/plan-critique/SKILL.md`.

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
- `plugin/skills/plan/LEGACY.md` — v1 plan compatibility notes (referenced by validator errors when an old plan is loaded)
