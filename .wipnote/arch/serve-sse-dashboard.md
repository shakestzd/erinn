---
name: serve-sse-dashboard
kind: subsystem-map
paths:
    - cmd/wipnote/dashboard/**
    - cmd/wipnote/serve.go
verified_at: e56c49a13549da970df6ea78c4dd4453b1ece6e9
links: []
created_by: feat-359312ab
created_at: 2026-06-10T22:08:22.615950095Z
updated_at: 2026-06-10T22:08:22.615950095Z
---

The dashboard (cmd/wipnote/dashboard/) is a static SPA served by each child's HTTP mux. It uses SSE (Server-Sent Events) for live event streaming and polls REST endpoints for status. buildSingleProjectMux() wires all routes and is shared between single-project legacy mode and the per-project child process path. Dashboard assets are embedded in the binary via Go embed.
