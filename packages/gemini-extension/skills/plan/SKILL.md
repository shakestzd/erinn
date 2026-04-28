---
name: htmlgraph:plan
description: Plan development work with interactive dashboard review before any code is written. Generates a YAML plan, populates slices and questions, runs dual-agent critique, opens dashboard for human review (approvals persist to SQLite), then reads finalized decisions to dispatch tasks. Use when asked to plan, create a development plan, or build a feature with design clarity first.
---

# HtmlGraph Plan

Use this skill when asked to plan development work, create a parallel execution plan, organize tasks for multi-agent execution, or build a feature with human review before implementation.

**Trigger keywords:** create plan, development plan, parallel plan, plan tasks, parallelize work, organize work, task breakdown, crispi, interactive plan, plan with review, design and build, plan this feature, review before building, generate plan, scaffold plan

---

## What This Skill Does

Generates a structured YAML plan, populates it with vertical slices, design questions with recommendations, and dual-agent critique. Opens the dashboard for interactive human review — approvals persist to SQLite on every click. After finalization, reads approved slices and design decisions to dispatch tasks via `/htmlgraph:execute`.

**Architecture:**
- **YAML** = plan content (agent-written, read by dashboard)
- **SQLite `plan_feedback`** = human approvals (persisted on every interaction)
- **Dashboard** = interactive review UI (reads both, writes approvals to SQLite)
- **Static HTML** = archival export (on finalize)

---

## Step 0: Work Item Attribution (MANDATORY)

Before anything else:

1. Check: `htmlgraph status` — is there an active feature/track for this work?
2. If yes: `htmlgraph feature start <id>`
3. If no: `htmlgraph feature create "<title>" --track <trk-id>` then `htmlgraph feature start <id>`

Plans without attribution produce untracked work.

---

## Step 0.5: Check for Already-Finalized Plan

If the user's request mentions a specific plan ID (e.g. "dispatch plan-a1b2c3d4" or "run step 7 for plan-xxxx"), check its status first:

```bash
htmlgraph plan show <plan-id> | grep -i status
```

**If status is `finalized`:** skip Steps 1-6 entirely and go straight to Step 7. The approvals and question answers are already persisted in `plan_feedback`. The plan has already been locked (read-only) — no further revisions are possible without reopening. Hand off directly to `/htmlgraph:execute <plan-id>` or the `htmlgraph yolo` command from the plan details view.

**Do NOT re-run research, critique, or human review on a finalized plan.** If the plan needs revision, ask the user explicitly before reopening.

If the plan status is `draft` or `review`, proceed to Step 1.

---

## Step 1b: Check for Existing Plan from Plan Mode

If the user just exited Claude Code's plan mode, the `ExitPlanMode` hook may have already created a skeleton YAML:

```bash
# Check for recently-created plan YAMLs (within last 5 minutes)
find .htmlgraph/plans/ -name "plan-*.yaml" -mmin -5 2>/dev/null
```

If a recent YAML exists:
1. Read the skeleton YAML — it will have slices with `what` partially populated but `why`, `done_when`, `tests`, `files` mostly empty. The slices may not map to proper delivery slices.
2. **Do NOT create a new plan** — use this plan ID as the base
3. Also locate the original `.md` plan file (same directory, most recent `.md` file)
4. Proceed to Step 1 (research) as normal — the research output will inform the enrichment
5. In Step 2, **launch enrichment agents instead of writing YAML from scratch** (see Step 2 below)

If no recent YAML exists, proceed to Step 1 (research) and Step 2 as normal.

---

## Step 1: Research (Parallel Agents)

Spawn research agents in a single message — do not proceed until both complete:

```
Agent(description="Understand current codebase state", subagent_type="htmlgraph:researcher",
      prompt="Investigate [area]. Answer: current state, key files, existing patterns, constraints.")

Agent(description="Check for prior work", subagent_type="Explore",
      prompt="Search .htmlgraph/ for any features or spikes related to [area]. Report feature IDs and status.")
```

Research must answer:
- Current state of the codebase in this area
- Desired end state (from the request)
- Open design questions (choices that affect the architecture)
- Candidate vertical slices (end-to-end, not horizontal layers)
- Real dependencies between slices
- Prior art or existing patterns to leverage

**Skip research only for:** trivial changes (<10 lines), bug fixes with known root cause, documentation-only changes.

---

## Step 2: Generate Plan YAML

### Path A: Enriching a skeleton from plan mode (Step 1b found a YAML)

The hook-generated skeleton has structural issues: headings may not map to delivery slices, mandatory fields are empty, and the design section is sparse. **This requires agent reasoning, not just a CLI command.**

Launch parallel enrichment agents — each reads the skeleton YAML + original `.md` + research output, and produces one section of the complete CRISPI YAML:

```
Agent(description="Enrich design section", subagent_type="htmlgraph:sonnet-coder",
      prompt="Read the skeleton YAML at .htmlgraph/plans/<plan-id>.yaml and the original
      plan at .htmlgraph/plans/<name>.md. Also use these research findings: [paste findings].
      
      Populate the design section:
      - design.problem: evidence-based description from the markdown's context/background
      - design.goals: measurable outcomes extracted from the plan's objectives
      - design.constraints: real limitations from research findings
      
      Write ONLY the design section as YAML to /tmp/design-section.yaml")

Agent(description="Restructure slices", subagent_type="htmlgraph:sonnet-coder",
      prompt="Read the skeleton YAML at .htmlgraph/plans/<plan-id>.yaml and the original
      plan at .htmlgraph/plans/<name>.md. Also use these research findings: [paste findings].
      
      The skeleton slices may be document sections (Context, Testing, Files Changed),
      not delivery slices. Restructure them into proper vertical slices:
      - Each slice = one end-to-end deliverable, not a horizontal layer
      - Populate ALL mandatory fields: what, why, files, done_when, tests, effort, risk
      - Set real dependency order in deps
      - Use slice-N IDs (e.g. slice-1, slice-2) — real feat- IDs are issued at finalize (Step 7b)
      - Collapse structural sections into slice metadata (testing → done_when, files → files)
      
      Write ONLY the slices section as YAML to /tmp/slices-section.yaml")

Agent(description="Generate design questions", subagent_type="htmlgraph:haiku-coder",
      prompt="Read the skeleton YAML at .htmlgraph/plans/<plan-id>.yaml and the original
      plan at .htmlgraph/plans/<name>.md. Also use these research findings: [paste findings].
      
      Identify 2-5 open design questions where the plan has implicit choices.
      Each question needs: text, description (context + tradeoffs), recommended option,
      and 2+ options with descriptive labels.
      
      Write ONLY the questions section as YAML to /tmp/questions-section.yaml")
```

After all agents return, the orchestrator assembles the complete YAML from the three sections, preserving the existing `meta` from the skeleton. Write the assembled plan via:
```bash
htmlgraph plan rewrite-yaml <plan-id> --file /tmp/assembled-plan.yaml
```

### Path B: Creating a new plan from scratch (no skeleton)

Generate the plan ID:
```bash
htmlgraph plan create "<title>" --description "<description>"
```
Note the returned plan ID. Then create the YAML file at `.htmlgraph/plans/<plan-id>.yaml`.

The orchestrator constructs the full YAML structure and writes it directly, or launches the same parallel agents as Path A but without a skeleton to start from (agents use research findings only).

### YAML Schema

Every plan YAML MUST follow this exact structure:

```yaml
meta:
  id: plan-<hex8>          # from plan create
  track_id: <trk-id>       # if retroactive, empty if plan-first
  title: "<plan title>"
  description: >
    One paragraph describing what this plan designs and why.
  created_at: "YYYY-MM-DD"
  status: draft             # draft | review | finalized
  created_by: claude-opus   # agent that generated it

design:
  problem: >
    What is the current state? What's wrong? Why does this matter?
    Include measurements, evidence, or user impact.
  goals:
    - "**Goal 1** — measurable outcome"
    - "**Goal 2** — measurable outcome"
  constraints:
    - "Constraint 1 — why it exists"
    - "Constraint 2 — why it exists"
  approved: false
  comment: ""

slices:
  - id: slice-1             # slice-N until finalize; real feat- IDs issued at Step 7b
    num: 1
    title: "<slice title>"
    what: >
      What exactly will be implemented. Be specific — name functions,
      files, APIs. An agent reading this should know what to build.
    why: >
      Why this slice exists. What problem does it solve? What breaks
      without it? Link to the design goals.
    files:
      - path/to/file1.go
      - path/to/file2.go
    deps: []                 # slice numbers this depends on, e.g. [1, 3]
    done_when:
      - "Acceptance criterion 1 — testable, concrete"
      - "Acceptance criterion 2 — testable, concrete"
    effort: S                # S | M | L
    risk: Low                # Low | Med | High
    tests: |
      Unit: specific test description with expected input/output
      Integration: specific integration test
      Regression: what existing tests must still pass
    approved: false
    comment: ""

questions:
  - id: q-<kebab-name>
    text: "The design question in plain English?"
    description: >
      Context paragraph explaining WHY this question matters, what
      tradeoffs are involved, and what evidence exists for each option.
      The reviewer needs this to make an informed decision.
    recommended: <option-key>  # agent's best judgment
    options:
      - key: option-a
        label: "A: Short name — Full description of what this option means and its implications"
      - key: option-b
        label: "B: Short name — Full description of what this option means and its implications"
    answer: null               # null = unanswered, set by human

critique: null                 # null = not yet run, populated by Step 4
```

### Quality Requirements for Each Section

**Slices — ALL fields are MANDATORY:**
- `what`: Specific enough that an agent can implement it without asking questions
- `why`: Links to a design goal or explains the business need
- `files`: At least one file path (check codebase if unsure)
- `done_when`: At least two concrete, testable acceptance criteria
- `tests`: At least one unit test and one integration/regression test
- `effort`: S (<50 lines), M (50-200 lines), L (>200 lines)
- `risk`: Low (pure addition), Med (modifies existing), High (changes hot path or shared interfaces)

**Questions — ALL fields are MANDATORY:**
- `description`: At least 2 sentences explaining context and tradeoffs
- `recommended`: Agent MUST pick a default — the human overrides only where they disagree
- `options`: At least 2 options, each with a descriptive label (not just a key)

**Design — ALL subsections are MANDATORY:**
- `problem`: Evidence-based description of current state
- `goals`: Measurable outcomes (not vague aspirations)
- `constraints`: Real limitations that affect design choices

---

## Step 3: Validate Plan Structure

Before proceeding, validate the YAML:

1. All required fields present on every slice
2. All questions have description + recommended
3. Design has problem + goals + constraints
4. Slice deps reference valid slice numbers
5. Effort and risk values are valid (S/M/L, Low/Med/High)
6. No duplicate slice numbers or question IDs

```bash
htmlgraph plan validate-yaml <plan-id>

```

---

## Step 4: Critique (Parallel Agents)

**Trigger:** Always run critique for plans with >= 3 slices. Skip for trivial plans.

Dispatch two critique agents in parallel. Each reads the plan YAML and produces structured findings.

```
Agent(description="Design critique", subagent_type="htmlgraph:haiku-coder",
      prompt="Read [plan.yaml]. Produce structured critique. [See prompt template below]")

Agent(description="Feasibility critique", subagent_type="htmlgraph:sonnet-coder",
      prompt="Read [plan.yaml]. Verify feasibility. [See prompt template below]")
```

### What Critique Agents Must Do

**For ALL plan types:**
- **Identify gaps** — what does the plan not address? What's missing?
- **Challenge assumptions** — what might be wrong? What is the plan taking for granted?
- **Suggest prior art** — what existing solutions, patterns, libraries, or examples are relevant?
- **Assess risks** — what could go wrong? What's the worst case? What's the blast radius?
- **Propose alternatives** — is there a simpler or different approach the plan missed?

**For plans involving existing code, also:**
- Verify claims against actual code (cite file:line numbers)
- Check if proposed changes conflict with existing patterns
- Identify functions/utilities that already exist and can be leveraged

**For greenfield or new ideas, also:**
- Question user/market assumptions — is there evidence of demand?
- Identify MVP scope vs full scope — what can be cut?
- Find real-world examples of similar systems — what can we learn from them?
- Flag "unknown unknowns" — what will we only discover by building?

### Critique Output Format

Each critique agent produces text in this format:

```
ASSUMPTIONS:
A1: [VERIFIED|PLAUSIBLE|UNVERIFIED|QUESTIONABLE|FALSIFIED] text | evidence

SLICE_ASSESSMENTS:
S1: [assessment with specific concerns]

RISKS:
R1: risk description | severity: Low/Medium/High | mitigation

GAPS:
- What the plan doesn't address

ALTERNATIVES:
- Different approaches worth considering

PRIOR_ART:
- Relevant existing solutions or patterns
```

### Writing Critique to YAML

The orchestrator parses both agents' output and writes the `critique` section:

```yaml
critique:
  reviewed_at: "YYYY-MM-DD"
  reviewers:
    - "Haiku (design review)"
    - "Sonnet (feasibility)"
  assumptions:
    - id: A1
      status: verified       # verified|plausible|unverified|questionable|falsified
      text: "The assumption being tested"
      evidence: "file:line or reasoning"
  critics:
    - title: "DESIGN CRITIC"
      sections:
        - heading: "Slice Assessment"
          items:
            - badge: "S1"
              kind: success   # success|warn|danger|info
              text: "Assessment text"
        - heading: "Gaps & Missing Considerations"
          items:
            - badge: "Gap"
              kind: warn
              text: "What the plan doesn't address"
        - heading: "Alternative Approaches"
          items:
            - badge: "Alt"
              kind: info
              text: "A different approach worth considering"
    - title: "FEASIBILITY CRITIC"
      sections:
        - heading: "What Exists to Leverage"
          items: [...]
        - heading: "What the Plan Gets Wrong"
          items: [...]
        - heading: "Prior Art & Examples"
          items: [...]
  risks:
    - risk: "Risk description"
      severity: High          # High|Medium|Low
      mitigation: "How to mitigate"
  synthesis: >
    Summary of key findings. What must change before the plan is approved?
    Numbered action items.
```

---

## Step 4b: Address Critique Findings (MANDATORY before review)

After critique agents return, the orchestrator MUST update the plan to address findings before launching review. The human should review the critique-informed version, not the stale original.

**For each FALSIFIED assumption:**
- Update the affected slice's `what` and `done_when` to address the falsification
- Add the corrected understanding to `design.constraints` if it affects the whole plan

**For each HIGH severity risk:**
- Add the mitigation as a `done_when` criterion on the affected slice
- If no slice owns the risk, add it to design constraints

**For each missing consideration identified by critics:**
- Add as a new question (if it's a design choice) or a new constraint (if it's a fact)

**Process:**
1. Parse both critique outputs for FALSIFIED/HIGH items
2. Read the current plan YAML
3. Modify the affected sections in memory
4. Write to temp file
5. Run: `htmlgraph plan rewrite-yaml <plan-id> --file /tmp/revised.yaml`
6. Git auto-commits the revision with message: `plan(<id>): address critique findings`
7. Proceed to Step 5 (review) with the updated plan

**Skip Step 4b only if:** all assumptions are verified/plausible AND no HIGH severity risks exist.

---

## Step 5: Open for Human Review (PAUSE HERE)

Direct the human to review the plan in the dashboard:

```bash
htmlgraph serve
```

Tell the human:

```
Plan ready for review in the dashboard:

  htmlgraph serve
  Open http://localhost:8080/#plans and click the plan.

Please:
1. Read the Design Discussion — Problem, Goals, Constraints
2. Review each Slice — approve or leave unchecked
3. Answer the Open Questions — defaults are recommended, override where you disagree
4. Read the AI Critique — note any risks or gaps
5. Check the Feedback Summary — progress bar shows completion
6. Click Finalize when all sections are approved

Your approvals persist automatically — you can close and reopen without losing progress.

I will wait until you finalize the plan before writing any code.
```

**STOP. Do not proceed until the human finalizes the plan.**

### Monitoring Review Progress

Poll the dashboard's feedback state to check if the human has finalized:

```bash
# Read all feedback as JSON — approvals, answers, amendments, chat.
# Resolves the canonical DB path internally (lives in the OS user
# cache dir, never the project tree). Don't shell out to sqlite3
# directly; the path is per-user and you'd hit the wrong DB.
htmlgraph plan feedback <plan-id>
```

Parse the JSON to count approvals, list answered questions, etc.

---

## Step 6: Read Finalized Decisions

After the human clicks Finalize in the dashboard, read all feedback:

```bash
# Read all feedback — approvals, answers, amendments, chat
htmlgraph plan feedback <plan-id>
```

This outputs JSON with the complete review context:

```json
{
  "plan_id": "plan-abc123",
  "approvals": {"slice-1": {"approved": true}, "slice-2": {"approved": false, "comment": "too risky"}},
  "answers": {"q-caching": "lazy", "q-error-handling": "metric-counter"},
  "amendments": [{"field": "what", "slice": 1, "op": "set", "value": "updated description"}],
  "chat_messages": [{"role": "user", "content": "...", "timestamp": "..."}]
}
```

Parse the results:
- If any slice has `approved: false`: summarize what was rejected. Ask the human — revise or proceed without?
- If revising: update the YAML, loop to Step 5.
- If proceeding: note excluded slices.

---

## Step 7: Revise Plan, Create Features, and Dispatch

Finalization is an **agentic process**. You synthesize all review feedback into a revised plan, then create properly structured features.

### 7a. Revise the Plan

Read the plan YAML and the feedback from Step 6. Produce a **revised version** of the plan that incorporates:

- **Amendments**: Apply accepted amendment changes to slice descriptions, titles, deps
- **Chat discussion**: If the chat contains decisions, clarifications, or scope changes, reflect them in the affected slices
- **Design decisions**: Bake answered questions into relevant slice descriptions under "Accepted Design Decisions"
- **Rejected slices**: Remove or mark as excluded
- **Critique insights**: If critique raised risks or assumptions that were discussed, note mitigations in affected slices

The revised YAML's `slice.what` field becomes the feature description at finalize time (via `buildSliceFeatureContent`). If you need richer per-feature content — e.g., incorporating chat discussion specific to that slice — write it into `slice.what` during revision. Do not rely on finalize to synthesize it.

Update the YAML via:

```bash
htmlgraph plan rewrite-yaml <plan-id> --file /tmp/revised.yaml
```

This validates the revised structure and auto-commits with version history.

### 7b. Finalize via the Canonical Command

Call the single, atomic finalize command:

```bash
htmlgraph plan finalize <plan-id>
```

This command:
1. Reads approvals and answers from SQLite `plan_feedback` table
2. Loops over approved slices and creates features using the slice's Title and What (or Why fallback) as content
3. Writes each created `FeatureID` back into the slice YAML
4. Emits edges: `planned_in` (feature→plan), `part_of`/`contains` (feature↔track), `implemented_in` (plan→track)
5. Sets plan status to `finalized` and locks it (subsequent calls error with "plan is locked … use 'plan reopen'")

If finalize errors with "plan is locked", the plan was previously finalized. Run `htmlgraph plan reopen <plan-id>` first, revise Step 7a, then re-finalize. The finalize command is idempotent: slices whose `feature_id` is already set and whose feature exists will be reused rather than duplicated; only new or orphaned slices create new features.

If finalize errors on missing track, description, or slices, the error message is actionable — fix the YAML and re-run.

### 7c. Announce the Finalized Plan

```
Plan finalized. Track: <track-id>

Approved slices (N of M):
  feat-XXXX  Slice 1 title       -> implement
  feat-XXXX  Slice 2 title       -> implement

Rejected slices:
  Slice 3 title                  -> excluded (not approved)

Design decisions:
  Q1: Migration caching: schema-version (recommended, accepted)
  Q2: Error handling: metric-counter (overrode recommendation: structured-log)
  Q3: SessionStart scope: git-only (recommended, accepted)
```

Then hand off to `/htmlgraph:execute`.

---

## Agent Type Selection

| Agent | When to Use |
|-------|------------|
| `htmlgraph:haiku-coder` | Critique (design review), single-file tasks, <50 lines |
| `htmlgraph:sonnet-coder` | Critique (feasibility), multi-file tasks, moderate complexity |
| `htmlgraph:opus-coder` | Architecture decisions, complex algorithms, novel design |

Default to sonnet unless the task is trivially simple (haiku) or requires deep reasoning (opus).

---

## Key Rules

- **Plans are YAML files** — agents write structured data, not HTML strings
- **All slice fields are mandatory** — What, Why, Files, DoneWhen, Tests, Effort, Risk
- **All questions need context** — description + recommended option
- **Design has three subsections** — Problem, Goals, Constraints (not a single blob)
- **Critique challenges assumptions** — not just verifies code
- **Approvals persist to SQLite** — human can close and reopen without losing state
- **Finalize is explicit** — human clicks the button, not the agent
- **TDD is mandatory** — every dispatched task includes tests before implementation
- **Only approved slices** become features on dispatch
- **Finalize is atomic** — `htmlgraph plan finalize` handles feature creation, edge wiring, and status transition in one command. Agents do not call `feature create` or `plan wire` directly during finalization.
