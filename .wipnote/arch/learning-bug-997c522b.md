---
name: learning-bug-997c522b
kind: decision
paths:
    - .devcontainer/post-create.sh
    - .wipnote/plans/plan-149eeb44.html
    - .wipnote/plans/plan-251676c5.html
    - plan/plantmpl/plantmpl_test.go
    - plan/plantmpl/templates/plan_page.gohtml
    - plugin/commands/init.md
    - plugin/skills/diagnose/SKILL.md
    - port/packages/codex-marketplace/.agents/plugins/wipnote/commands/init.md
    - port/packages/codex-marketplace/.agents/plugins/wipnote/skills/diagnose/SKILL.md
    - port/packages/gemini-extension/commands/wipnote/init.toml
    - port/packages/gemini-extension/skills/diagnose/SKILL.md
verified_at: 796a8b16c7abd98ec26aa9de2246d6fd63b7910a
links:
    - bug-997c522b
created_by: wipnote-completion
created_at: 2026-06-14T00:04:23.190295771Z
updated_at: 2026-06-14T00:04:23.190295771Z
---

Plan HTML fixes must update both plantmpl source and committed rendered plan artifacts until the local wipnote binary embeds the rebuilt template.
