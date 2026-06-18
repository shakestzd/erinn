---
name: learning-spk-6c9f9368
kind: decision
paths:
    - .wipnote/spikes/spk-6c9f9368.html
verified_at: 53dcd34f60b266709676f6c40b4a6dff41a24068
links:
    - spk-6c9f9368
created_by: wipnote-completion
created_at: 2026-06-18T06:11:14.545236679Z
updated_at: 2026-06-18T06:20:55.775822442Z
---

Full cmd/wipnote test profile reports package elapsed 375s; top offenders are SQLiteContentionStress plus tests that invoke nested session/completion/provenance gates, while TestFeatureStart_LiveCollision_Refuses is about 2s and not top-20.
