---
name: learning-feat-2570725c
kind: decision
paths:
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/templates/recap_page.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/templates/lineage_chain.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/templates/annotated_diff.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/templates/diff_zone.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/templates/file_tree_zone.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/lineage_chain.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/annotated_diff.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/file_tree_zone.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/builder.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/recaptmpl.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/annotated_diff_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/recap/recaptmpl/recaptmpl_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/plan/blocks/templates/file_tree.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/plan/blocks/templates/api_endpoint.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/plan/blocks/templates/data_model.gohtml
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/plan/blocks/blocks.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-aadb8a6fe88a6841e/plan/blocks/blocks_test.go
verified_at: 4d2e064093714801434abcba71507c5c2bcd18f4
links:
    - feat-2570725c
created_by: wipnote-completion
created_at: 2026-06-18T08:43:44.516199253Z
updated_at: 2026-06-18T08:43:44.516199253Z
---

plan/blocks is the shared HTML block-renderer leaf package: lives in the plan module (not internal/, which the separate plan module cannot import) and depends only on stdlib, so both recap/recaptmpl (root) and plan/plantmpl (slice-7) import it without cycles. Consumers adapt domain types (recap.FileChange, planyaml.SliceBlock) into neutral blocks.{DataModel,APIEndpoint,FileTree} values. Markup mirrors dashboard CSS classes (block/block-table/file-change).
