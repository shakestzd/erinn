---
name: learning-feat-e668de65
kind: decision
paths:
    - .wipnote/features/feat-e668de65.html
verified_at: 96bf4acaf20e575281657e40cdc3a015589ef9fc
links:
    - feat-e668de65
created_by: wipnote-completion
created_at: 2026-06-19T02:48:48.31454182Z
updated_at: 2026-06-19T02:48:48.31454182Z
---

huh v1.0.0 bubbletea backend hangs (does not error) when given non-TTY input — always gate runSelectTUI behind isTTYWriter(out) check before calling form.Run(); use runSelectTUIFn package-var as test seam; mapIndexToIntent is the pure mapping function for both TUI and numeric paths
