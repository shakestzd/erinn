---
name: serve-child-lifecycle
kind: subsystem-map
paths:
    - cmd/wipnote/serve_child.go
    - internal/childproc/**
verified_at: e56c49a13549da970df6ea78c4dd4453b1ece6e9
links: []
created_by: feat-359312ab
created_at: 2026-06-10T22:08:16.94859986Z
updated_at: 2026-06-10T22:08:16.94859986Z
---

Child server lifecycle: started on-demand by Supervisor when the first request arrives for a project. Owns OTEL receiver, indexer, retention, daemon apply loop, and writequeue sink. Shuts down cleanly on SIGTERM via context cancellation. The _serve-child hidden cobra subcommand is the child entry point — never invoke directly; the parent spawns it via childproc.Supervisor.
