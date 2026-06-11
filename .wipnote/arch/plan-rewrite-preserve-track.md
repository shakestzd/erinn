---
name: plan-rewrite-preserve-track
kind: invariant
paths:
    - cmd/wipnote/plan_yaml_extras.go
    - plan/planyaml/schema.go
verified_at: ""
links: []
created_by: architect-coder
created_at: 2026-06-11T13:05:57.600186905Z
updated_at: 2026-06-11T13:05:57.600186905Z
---

wipnote plan rewrite-yaml MUST preserve the existing plan's meta.track_id (planyaml.PlanMeta.TrackID) when the incoming YAML omits it. The track link is structural; LLM/hand rewrites of the plan body routinely drop it, which silently severs the plan from its track and makes finalize create a NEW track instead of attaching to the existing one (root cause of plan-edeb2163 losing trk-23232c8d). preserveTrackLinkage() carries the existing track_id forward; an explicit non-empty track_id in the rewrite is respected for intentional re-targeting. The canonical meta field is 'track_id', not 'track'.
