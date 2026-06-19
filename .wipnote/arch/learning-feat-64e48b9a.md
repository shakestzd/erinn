---
name: learning-feat-64e48b9a
kind: decision
paths:
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/plan/blocks/wireframe_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/plan/blocks/wireframe.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a8ea74edc8690c7e7/cmd/wipnote/dashboard/js/app.js
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a8ea74edc8690c7e7/cmd/wipnote/dashboard/index.html
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a8ea74edc8690c7e7/cmd/wipnote/dashboard/css/components.css
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a8ea74edc8690c7e7/cmd/wipnote/serve.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a8ea74edc8690c7e7/cmd/wipnote/api_recaps.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a8ea74edc8690c7e7/cmd/wipnote/api_recaps_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a0ed7f3f2ca3228d6/plugin/skills/visual-plan/SKILL.md
verified_at: 536b6551d9fea4f30c52eace12786bfeb6ef60c8
links:
    - feat-64e48b9a
created_by: wipnote-completion
created_at: 2026-06-18T09:54:47.75795977Z
updated_at: 2026-06-18T09:54:47.75795977Z
---

wipnote:visual-plan skill: prompt-only skills for plan enrichment must read wipnote plan blocks at runtime (never hardcode block tags) and use var(--wf-*) design tokens in wireframes; build-ports fans out to 4 targets (claude/plugin, antigravity, codex, gemini) automatically from plugin/skills/<name>/SKILL.md with no manifest.json edit needed
