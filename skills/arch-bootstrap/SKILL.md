---
name: wipnote:arch-bootstrap
description: Bootstrap architectural memory for a repo — generates starter cards from a structured brief
---

# Architectural Memory Bootstrap

Use this skill to populate a project's architectural memory from scratch. It runs the bootstrap command, reads the brief, and authors starter arch cards via the validated add path.

**Trigger keywords:** bootstrap arch, bootstrap architectural memory, starter cards, arch bootstrap, initialize arch cards

---

## Step 1: Generate the Brief

```bash
wipnote arch bootstrap
```

Read the full output. It contains:
- **Repo Layout** — top-level directories and Go module names
- **Existing Docs** — CLAUDE.md / AGENTS.md content (instructions, not architecture)
- **Lineage Hotspots** — files touched most in the last 90 days (signals where arch cards are most valuable)
- **Authoring Instructions** — card kinds, constraints, command template

---

## Step 2: Author Cards

For each major subsystem, invariant, or hazard you identify from the brief, run:

```bash
wipnote arch add <slug> \
  --kind <kind> \
  --created-by agent \
  --paths "<glob1>,<glob2>" \
  --body "<body up to 120 words>"
```

**Card kinds:**
- `subsystem-map` — what a subsystem does and its boundaries
- `invariant` — a constraint that must always hold
- `hazard` — a known failure mode or sharp edge
- `decision` — a recorded architectural decision

**Rules:**
- Body: max 120 words
- Slug: lowercase letters, digits, hyphens only
- Globs: use `**` for recursive match (e.g. `internal/**`, `cmd/wipnote/*.go`)
- Name and path-glob set must each be unique across all cards

Aim for 3–8 cards covering the top subsystems visible in the hotspot list.

Do NOT mechanically import CLAUDE.md content — those are instructions, not architecture. Focus on structural facts: what systems exist, what invariants must hold, what hazards to avoid.

---

## Step 3: Validate

```bash
wipnote arch validate
```

Fix any reported errors (body too long, missing required fields, invalid kind).

---

## Step 4: Review

```bash
wipnote arch list
wipnote arch show <slug>
```

Verify the cards describe real structural facts about the codebase, not process instructions.
