---
name: learning-feat-7bc6410b
kind: decision
paths:
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/cmd/wipnote/recap_list_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/cmd/wipnote/reindex_recaps_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/cmd/wipnote/recap_list.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/cmd/wipnote/recap.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/cmd/wipnote/relevant.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/cmd/wipnote/reindex.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/cmd/wipnote/reindex_recaps.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/recap/recaptmpl/templates/recap_page.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/core/db/recaps_repo.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/core/db/migrations_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/core/db/schema.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/plan/blocks/wireframe_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a1cec795c6e81fa66/core/db/migrations.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/plan/blocks/wireframe.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a6e1d2aa5ad27f888/plugin/skills/visual-recap/SKILL.md
verified_at: 65222d8aaaffd3fae582a53ecccb92e6c06b29c3
links:
    - feat-7bc6410b
created_by: wipnote-completion
created_at: 2026-06-18T09:22:40.214125833Z
updated_at: 2026-06-18T09:22:40.214125833Z
---

Recap artifacts are first-class via a dedicated recaps SQLite table (migration 017_recaps_table), NOT an extension of features. Reindex parses metadata from data-recap-* attributes on the recap HTML body element (emitted by recap/recaptmpl); recap id comes from the filename, not the HTML. recap- prefix maps to node type recap in inferNodeTypeFromID. list/show/delete read the SQLite index, refreshed from canonical HTML on each call.
