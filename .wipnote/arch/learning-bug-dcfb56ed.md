---
name: learning-bug-dcfb56ed
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/claude_chooser_current_test.go
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/claude_chooser.go
verified_at: eeead62d95d6810c4b392a4dc9efc77677c01ea0
links:
    - bug-dcfb56ed
created_by: wipnote-completion
created_at: 2026-06-19T06:17:57.82914923Z
updated_at: 2026-06-19T06:17:57.82914923Z
---

roborev caught a real gap my unit tests missed: I tested promptLaunchIntent (the renderer) directly but not chooseLaunchIntent's upstream early-return gate, which only checked SameHarness/CrossHarness emptiness and dropped grouped.Current when it was the sole resumable option. Lesson: when a new field bypasses an existing filter, audit EVERY short-circuit/gate that derives emptiness from the old fields — extract a single predicate (hasResumableOptions) so the gate and the renderer agree and the rule is testable in one place.
