---
name: serve-hub-architecture
kind: subsystem-map
paths:
    - cmd/wipnote/serve*.go
    - cmd/wipnote/dashboard/**
    - cmd/wipnote/dashboard.go
verified_at: e56c49a13549da970df6ea78c4dd4453b1ece6e9
links: []
created_by: feat-359312ab
created_at: 2026-06-10T22:08:00.645152505Z
updated_at: 2026-06-10T22:08:00.645152505Z
---

wipnote serve runs a parent+child process model: the parent (serve_parent.go) is a reverse proxy that auto-discovers registered projects and forwards /p/<id>/* traffic to per-project child processes spawned by the childproc.Supervisor. Each child (serve_child.go) owns one project's database connection, writequeue, OTEL receiver, and SSE event stream. The dashboard JS (dashboard/) connects via SSE for live updates. Parent and child communicate over local Unix/TCP sockets managed by the Supervisor.
