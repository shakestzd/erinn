---
name: serve-parent-proxy-invariant
kind: invariant
paths:
    - cmd/wipnote/serve_parent.go
verified_at: e56c49a13549da970df6ea78c4dd4453b1ece6e9
links: []
created_by: feat-359312ab
created_at: 2026-06-10T22:08:11.494199576Z
updated_at: 2026-06-10T22:08:11.494199576Z
---

All /p/<project-id>/* routes are validated against validProjectIDRE (hex chars only) before registry lookup — defense-in-depth against path traversal. Project IDs must never contain path separators, null bytes, or non-hex characters. autoDetectCurrentProject() uses the registry to find the current project from CWD; it returns nil silently when no match exists (not an error).
