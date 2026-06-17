---
name: learning-bug-8fd0fac2
kind: decision
paths:
    - .claude/worktrees/antigravity-parity-leverage-trk-6a1f5362/cmd/wipnote/build.go
verified_at: 8efe483d8368dd367a379f788680c43420dade81
links:
    - bug-8fd0fac2
created_by: wipnote-completion
created_at: 2026-06-16T20:42:51.458859862Z
updated_at: 2026-06-16T20:42:51.458859862Z
---

wipnote build was non-atomic (os.Remove then go build -o over seconds) → ~/.local/bin/wipnote missing mid-rebuild → concurrent statusline/hook callers get exit 127. Fix: build to temp path + os.Rename (atomic, same fs, fresh inode for macOS signing).
