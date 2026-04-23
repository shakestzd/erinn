# Good Fixture — must pass validator

This document contains only valid htmlgraph invocations.

```bash
htmlgraph bug create "Example bug" --track trk-abc12345
htmlgraph feature create "My feature" --track trk-abc12345
htmlgraph find features --status in-progress
htmlgraph snapshot --summary
htmlgraph recommend --top 5
htmlgraph plan rewrite-yaml plan-abc12345 --file /tmp/updated.yaml
```

All flags above are registered on their respective commands.
