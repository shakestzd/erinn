---
name: learning-bug-e8a8f805
kind: decision
paths:
    - .wipnote/plans/plan-6d49f2df.yaml
    - cmd/wipnote/claude.go
verified_at: 79b44a29b72914554a0cfbcba0689f72f2e49b8b
links:
    - bug-e8a8f805
created_by: wipnote-completion
created_at: 2026-06-18T05:41:17.830576618Z
updated_at: 2026-06-18T05:43:36.556172663Z
---

roborev-fix on 4 failed main reviews: verification found 4/8 findings already resolved by later work (plantmpl single-escape, issue-badge excludes SUCCESS/INFO, v2 nav-link guard, badge CSS defined). Live fixes in c0af04acc: claude.go launchClaudeDev guard +!continue_; plan-6d49f2df.yaml slices 3/4/5 + risks-mitigation folded the DANGER critic_revisions in. FOLLOW-UP: launchClaudeDefault/launchClaudeYolo carry the same default-name guard pattern (unfixed — review finding was scoped to --dev --continue).
