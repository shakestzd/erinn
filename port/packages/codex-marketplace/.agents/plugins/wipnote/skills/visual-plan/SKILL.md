---
name: wipnote:visual-plan
description: SECONDARY enrichment path — add or revise structured visual blocks (data-model, api-endpoint, file-tree, wireframe, diagram, tabs) on an EXISTING wipnote plan. Use for legacy plans drafted before blocks-first existed, or for ad-hoc later block additions/revisions — NOT for primary planning. New plans get blocks authored inline during the wipnote:plan interview (blocks-first); only reach for this skill when a plan already exists and needs blocks added or changed after the fact. Reads the live block catalog from wipnote plan blocks, adds grounded blocks to slices, regenerates the plan HTML, and links to the dashboard.
---

# wipnote Visual Plan (after-the-fact enrichment)

**This is the SECONDARY path.** Blocks-first is now built into `wipnote:plan`: during the
plan interview, each stage authors its visual block FIRST and derives the prose from it, so
freshly planned slices already carry grounded blocks. Use THIS skill only to **enrich an
EXISTING plan** — a legacy plan drafted before blocks-first, or a slice that needs a block
added or revised after the plan was written. For new plans, run `wipnote:plan`; do not use
this skill as the entry point for planning.

Use this skill to add grounded visual blocks to existing plan slices — tables, API routes,
file trees, wireframes, diagrams — then regenerate the plan HTML and surface the dashboard link.

**Trigger keywords:** enrich existing plan, add visual blocks to existing plan, add blocks to
a plan that already exists, revise blocks on a plan, backfill blocks on a legacy plan,
visualize an existing plan slice, add wireframe to existing plan, add data model to existing
plan, add api endpoint to existing plan, add file tree to existing plan

> Routing note: if the user is asking to CREATE a plan, use `wipnote:plan` (blocks-first)
> instead — that interview authors blocks inline. Only route here for after-the-fact block
> work on a plan that already exists.

---

## Step 1: Read the live block catalog

Always start by reading the current block vocabulary from the CLI — do NOT hardcode tag
names, required fields, or row schemas (the catalog evolves):

```bash
wipnote plan blocks
```

Parse the output to learn:
- Which block types are supported
- What required fields each type expects
- What row keys each type accepts

The canonical block types and their schemas come from this command at runtime.

---

## Step 2: Identify the plan and target slices

Determine which plan and slices to enrich from the user's request:

| User says | Target |
|-----------|--------|
| `enrich plan plan-<id>` | Plan ID |
| `add blocks to slice <N> of plan-<id>` | Specific slice |
| `add a data model to this plan` | Current/most recent open plan |

If the plan ID or slice is ambiguous, list open plans:

```bash
wipnote plan list
```

Then ask the user to confirm which plan and which slices to enrich before proceeding.

---

## Step 3: Read the current plan slices

Read the existing plan slices to understand their context before suggesting blocks:

```bash
wipnote plan show <plan-id>
```

For each target slice, note:
- The slice title and goal
- Files/entities already mentioned
- Acceptance criteria (these anchor what blocks are relevant)

---

## Step 4: Author grounded blocks for each slice

For each target slice, propose `blocks:` entries grounded in the real slice content.
Use only the block types and field names returned by `wipnote plan blocks` in Step 1.

**Grounding rules (non-negotiable):**
- `data-model` rows must use field names/types that exist (or will exist) in the codebase — do not invent schema.
- `api-endpoint` method/path must match routes the slice will actually implement.
- `file-tree` entries must list real files the slice touches (use the acceptance criteria and task description as source).
- `wireframe` HTML must use `var(--wf-*)` design tokens only — never raw hex/rgb colors.

**Example wireframe token usage (correct):**
```html
<div style="background: var(--wf-bg); color: var(--wf-text); border: 1px solid var(--wf-border);">
  content
</div>
```

If the slice context does not justify a visual block, skip it — do not pad with speculative blocks.

---

## Step 5: Rewrite the plan YAML with blocks

Edit the plan YAML in a temporary file and add the authored `blocks:` entries to
the target slice objects. Preserve all existing fields and ordering unless the
block insertion requires a local change.

```bash
wipnote plan show <plan-id> --format yaml > /tmp/<plan-id>.yaml
# edit /tmp/<plan-id>.yaml
wipnote plan rewrite-yaml <plan-id> --file /tmp/<plan-id>.yaml
```

Check which subcommands are available:

```bash
wipnote plan --help
```

Use the existing `rewrite-yaml` mutation path. Do not guess or invent subcommands.

---

## Step 6: Regenerate the plan HTML

After adding blocks, regenerate the plan HTML so the dashboard reflects the changes:

```bash
wipnote plan render <plan-id>
```

If the command fails, report the error verbatim — do not attempt to construct the artifact manually.

---

## Step 7: Report and link to the dashboard

Present a concise summary:

```markdown
## Visual Plan: <plan-id>

**Plan:** `<plan-id>` — <title>
**Dashboard:** `wipnote serve` then open http://127.0.0.1:8080 (or http://127.0.0.1:8088 in devcontainer)

### Blocks added
<bullet list: slice N — block type — key fields, grounded in the real slice content>

### Next step
Open the dashboard to review the rendered blocks. Re-run this skill to enrich additional slices.
```

---

## Notes

- **Local-first.** No data leaves `.wipnote/`. The plan artifact is a self-contained HTML
  file committed to the repository — no hosted service is involved.
- **Dynamic catalog.** Always read `wipnote plan blocks` at runtime. Block tag names and
  required fields may change across wipnote versions; never hardcode them.
- **Design tokens.** Wireframe blocks must use `var(--wf-*)` tokens, not raw colors.
  The renderer injects the token palette — raw hex/rgb will produce broken or unstyled output.
- **Grounded only.** Blocks must reflect the real intent of the slice. Speculative or
  placeholder blocks add noise — omit rather than invent.
- **Dashboard URL:** default port is `8080`; devcontainer port is `8088` (see AGENTS.md).
  Tell the user which applies based on their environment.
