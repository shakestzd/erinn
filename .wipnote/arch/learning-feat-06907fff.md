---
name: learning-feat-06907fff
kind: decision
paths:
    - .claude/worktrees/trk-a951e3c0/cmd/wipnote/recap.go
    - .claude/worktrees/trk-a951e3c0/cmd/wipnote/recap_test.go
    - .claude/worktrees/trk-a951e3c0/plugin/skills/visual-recap/SKILL.md
    - .claude/worktrees/trk-a951e3c0/cmd/wipnote/workitem.go
    - .claude/worktrees/trk-a951e3c0/cmd/wipnote/plan_finalize.go
verified_at: 399735bbff40381957480f389158527755156df0
links:
    - feat-06907fff
created_by: wipnote-completion
created_at: 2026-06-20T22:25:41.810366074Z
updated_at: 2026-06-20T22:25:41.810366074Z
---

RunPlanRollupRecap is intentionally non-fatal end-to-end: it skips with stderr note if the plan YAML is untracked (no git history). The planFirstCommitSHA helper uses --diff-filter=A --follow to find oldest commit. write-boundary test unaffected because openReadOnlyDB (not dbpkg.Open) is used.
