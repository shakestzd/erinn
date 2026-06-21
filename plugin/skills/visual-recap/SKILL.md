---
name: wipnote:visual-recap
description: Generate a grounded visual recap for a work item, git range, or session. Runs wipnote recap to produce a self-contained HTML artifact grounded in the real diff, reads the artifact, writes an outcome narrative (secrets redacted), and links to the dashboard recap URL. Use when asked to recap, summarize, or visualize completed work.
---

# wipnote Visual Recap

Use this skill to generate a grounded, diff-backed recap for a work item, git range,
or session — then surface the key outcome narrative and a direct link to the HTML artifact.

**Trigger keywords:** recap, summarize work, visual recap, show what was done, recap this feature,
recap this session, show recap, diff recap

---

## Step 1: Identify the scope

Determine what to recap from the user's request:

| User says | Scope | Command |
|-----------|-------|---------|
| `recap feat-<id>` / feature ID | Work item | `wipnote recap feat-<id>` |
| `recap bug-<id>` / bug ID | Work item | `wipnote recap bug-<id>` |
| `recap spk-<id>` / spike ID | Work item | `wipnote recap spk-<id>` |
| `recap main..HEAD` / git range | Git range | `wipnote recap --range main..HEAD` |
| `recap this session` / session ID | Session | `wipnote recap --session <session-id>` |

If the scope is ambiguous, ask for clarification before running anything.

---

## Step 2: Run wipnote recap

Run the command determined in Step 1:

```bash
wipnote recap <scope>
```

The command writes `.wipnote/recaps/<recap-id>.html` and prints:

```
Recap written: .wipnote/recaps/<recap-id>.html
```

Capture the `<recap-id>` from the output line. If the command fails, report the error
verbatim — do not attempt to construct the artifact manually.

---

## Step 3: Read the artifact and extract the narrative

Read the produced HTML artifact to gather the ground-truth diff data:

```bash
wipnote sh "cat .wipnote/recaps/<recap-id>.html" --max-lines 300
```

From the artifact extract:
- **What changed** — files touched, key additions / deletions (from the embedded diff)
- **Why** — work item title and description (from the embedded lineage metadata)
- **Outcome** — tests passing, artifacts committed, quality gates

**Redact before displaying:** API keys, tokens, passwords, `.env` values, private paths,
and any content that looks like a secret. Replace with `[REDACTED]`.

---

## Step 4: Write the outcome narrative

Present a concise, grounded summary to the user:

```markdown
## Recap: <title or scope>

**Artifact:** `.wipnote/recaps/<recap-id>.html`
**Dashboard:** `wipnote serve` then open http://127.0.0.1:8080 (or http://127.0.0.1:8088 in devcontainer)

### What changed
<bullet list of key file changes and additions, grounded in the real diff>

### Outcome
<1-3 sentences: tests passed, gates clean, artifacts committed — based on the artifact content>

### Lineage
<work item ID + title if available, git range if range mode, session ID if session mode>
```

Keep the narrative tight — 5-10 lines. The full artifact is in the dashboard for deep review.

---

## Notes

- **Local-first.** No data leaves `.wipnote/`. The recap artifact is a self-contained HTML
  file committed to the repository — no hosted service is involved.
- **Idempotent.** Re-running `wipnote recap <same-id>` overwrites the artifact in place.
- **Dashboard URL:** default port is `8080`; devcontainer port is `8088` (see AGENTS.md).
  Tell the user which applies based on their environment.
- **Secrets:** always redact before displaying any diff content in the narrative.

---

## Auto-triggers

Recap generation fires automatically at two points in the lifecycle — no manual invocation needed.

### 1. Plan finalize auto-rollup

`wipnote plan finalize <plan-id>` automatically generates a consolidated rollup recap after
locking the plan. The artifact ID is `recap-pln-<plan-id>` and the git range spans from the
first commit that introduced the plan YAML (`<first-commit>..HEAD`), covering the full
planning-to-finalize arc. The recap is written to `.wipnote/recaps/recap-pln-<plan-id>.html`
and committed. A `relates_to` lineage edge is wired from the plan to the recap so it appears
in `wipnote lineage <plan-id>`.

This step is non-fatal: if the plan YAML has no git history yet (untracked) or any other
error occurs, a warning is printed to stderr and finalize continues normally.

### 2. Feature/bug complete recap nudge

`wipnote feature complete <id>` (and `wipnote bug complete <id>`) print a one-line nudge to
stderr after a successful code-bearing completion:

```
  ! recap: wipnote recap <id>   (grounded diff recap of this work)
```

This is a reminder only — no recap is generated automatically. Run the suggested command to
produce a grounded diff recap grounded in the real committed diff. The nudge is consistent
with the adjacent drift nudge and is non-fatal.
