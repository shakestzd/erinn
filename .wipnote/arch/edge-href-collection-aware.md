---
name: edge-href-collection-aware
kind: invariant
paths:
    - core/workitem/htmlwriter.go
    - core/workitem/templates/node.gohtml
    - core/htmlparse/parser.go
verified_at: ""
links: []
created_by: architect-coder
created_at: 2026-06-11T13:05:43.766828341Z
updated_at: 2026-06-11T13:05:43.766828341Z
---

Work-item HTML edge links MUST be collection-aware: edgeHref() in core/workitem/htmlwriter.go maps the target ID prefix to its .wipnote subdir (feat->features, bug->bugs, spk->spikes, trk->tracks, pln->plans, spc->specs, UUID-shaped->sessions). A bare <id>.html href only resolves within the same collection dir and 404s cross-collection. The htmlparse parser strips any leading directory when reading hrefs back, so prefixed hrefs round-trip to the same target ID.
