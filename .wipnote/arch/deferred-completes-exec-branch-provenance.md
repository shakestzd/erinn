---
name: deferred-completes-exec-branch-provenance
kind: decision
paths: []
verified_at: ""
links:
    - trk-6a1f5362
created_by: claude-code/claude-opus-4-8
created_at: 2026-06-23T19:01:50.146456093Z
updated_at: 2026-06-23T19:01:50.146456093Z
---

Multi-slice plans run on a separate exec branch and merged back (squash/rebase) get per-item commit SHAs rewritten; code commits on main aren't work-item-ID-tagged. So 'wipnote trace <id>' shows 0 commits and 'complete' refuses with 'zero linked source commits' though the work is merged. link-commit needs a SHA that no longer exists, and files overlap across slices so per-item attribution isn't clean. Resolution: complete with --accepted-advisory recording the merge-back rationale (auditable via 'wipnote check accepted-advisory'; reversible; link-commit later if SHAs reconstructed). Don't guess SHAs — wrong links are worse than an honest advisory. Confirmed on trk-6a1f5362 / plan-2390966a: 12 items closed via advisory.
