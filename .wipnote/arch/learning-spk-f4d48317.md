---
name: learning-spk-f4d48317
kind: decision
paths:
    - .wipnote/spikes/spk-f4d48317.html
verified_at: 8b0f9fccea30f4c568be9de05635e1134fcabbba
links:
    - spk-f4d48317
created_by: wipnote-completion
created_at: 2026-06-23T20:35:52.840860213Z
updated_at: 2026-06-23T20:35:52.840860213Z
---

core/hooks event-recording test failures were caused by stale installed wipnote spawning old _serve-child writer; rebuilding ~/.local/bin/wipnote from HEAD and killing old writers makes all four tests pass.
