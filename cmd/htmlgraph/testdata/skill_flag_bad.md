# Bad Fixture — must trigger validator

This document contains an htmlgraph invocation with a non-existent flag.

```bash
htmlgraph bug create "Example bug" --this-flag-doesnt-exist
```

The validator should detect `--this-flag-doesnt-exist` on `bug create` and fail.
