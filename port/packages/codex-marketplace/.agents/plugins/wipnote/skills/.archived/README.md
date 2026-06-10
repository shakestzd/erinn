# Archived Skills

This directory contains skills that have been consolidated or deprecated.

## parallel-orchestrator (Archived 2025-12-31)

**Reason:** Consolidated into `wipnote-tracker` skill.

**Why?** The parallel-orchestrator skill provided orchestration and delegation patterns, but this created confusion:
- Two skills with overlapping responsibilities
- Users didn't know which to activate
- Orchestration is a core part of the wipnote workflow, not a separate concern

**Solution:** All orchestration directives are now in `wipnote-tracker` as "Orchestrator Mode" (Section 1 of Core Responsibilities).

**Migration:** The `wipnote-tracker` skill now includes:
- Orchestrator mode directives (delegation patterns)
- 6-phase parallel workflow
- Task ID coordination helpers
- All orchestration anti-patterns and best practices

**What was moved:**
- Delegation philosophy and decision framework
- Git delegation requirements (ALWAYS DELEGATE)
- Parallel workflow coordination
- Task ID pattern for result retrieval
- SDK orchestration methods

**See:** `wipnote-tracker/SKILL.md` for the consolidated skill.
