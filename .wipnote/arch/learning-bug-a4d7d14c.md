---
name: learning-bug-a4d7d14c
kind: decision
paths:
    - .wipnote/.active-session
    - .wipnote/.launch-mode
    - .wipnote/debug.log
    - .wipnote/logs/serve-auto.log
    - .wipnote/logs/serve-f8093eb9.log
    - .wipnote/logs/writer.log
    - .wipnote/logs/writer.log.1
    - .wipnote/logs/writer.log.2
    - .wipnote/session-families.json
verified_at: 84bce83d07d06eb8bbd455fcc6aeaf7b08f84da0
links:
    - bug-a4d7d14c
created_by: wipnote-completion
created_at: 2026-06-16T17:49:14.53236007Z
updated_at: 2026-06-16T21:20:06.764300562Z
---

git rm --cached is required to remove files from the git index that were committed before being added to .gitignore. Using grep_search (git grep) in read-only subagents to check tracking status can return false negatives if the files don't have matching search content; git ls-files is the correct way to check if a file is tracked.
