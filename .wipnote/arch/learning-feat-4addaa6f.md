---
name: learning-feat-4addaa6f
kind: invariant
paths:
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/core/db/migrations.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/cmd/wipnote/dashboard/js/app.js
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/cmd/wipnote/plan_yaml_extras.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/plan/planyaml/schema.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/cmd/wipnote/api_plans_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/cmd/wipnote/api_plans.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/core/db/plan_feedback_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/core/db/plan_feedback.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/core/db/migrations_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-ad2ef0ece1ccbf8c2/core/db/schema.go
verified_at: be9fd21af818e4de4017c0f22fa781d680f8f102
links:
    - feat-4addaa6f
created_by: wipnote-completion
created_at: 2026-06-18T08:43:30.357663933Z
updated_at: 2026-06-18T08:43:30.357663933Z
---

Block-level plan annotations reuse plan_feedback with action='annotation'; migration 016 added anchor/consumed/resolved/resolution_target. consumed vs resolved are independent axes; resolution_target routes agent|human. validSectionRe accepts tightly-bounded slice-N-block-<name>-<idx> (not arbitrary). Migration MUST guard table existence via sqlite_master (legacy partial DBs seeded pre-plan_feedback else fail the ALTER); step name appended to BOTH want-lists in migrations_test.go (~715,~864). Recap annotation out of scope (recap IDs not in plans table → FK-fail).
