---
name: wipnote:visual-plan
description: Enrich a wipnote plan with structured visual blocks (data-model, api-endpoint, file-tree, wireframe). Reads the live block catalog from wipnote plan blocks, guides the author to add grounded blocks to plan slices, regenerates the plan HTML, and links to the dashboard plan view. Use when asked to add visual blocks, enrich a plan, or visualize a plan slice.
---

# wipnote Visual Plan

Use this skill to add grounded visual blocks to plan slices — tables, API routes, file
trees, and wireframes — then regenerate the plan HTML and surface the dashboard link.

**Trigger keywords:** enrich plan, add visual blocks, add blocks to plan, visualize plan,
visual plan, add wireframe, add data model to plan, add api endpoint to plan, add file tree to plan

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

## Step 5: Add blocks to the plan slices

Add the authored blocks to each target slice:

```bash
wipnote plan slice add-blocks <plan-id> <slice-index> --blocks '<json-array>'
```

If the CLI does not expose `add-blocks`, update the plan slice source directly using
`wipnote plan edit` or by editing the plan file and running `wipnote plan regenerate <plan-id>`.

Check which subcommands are available:

```bash
wipnote plan --help
```

Use the available command that matches the current CLI. Do not guess or invent subcommands.

---

## Step 6: Regenerate the plan HTML

After adding blocks, regenerate the plan HTML so the dashboard reflects the changes:

```bash
wipnote plan regenerate <plan-id>
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
