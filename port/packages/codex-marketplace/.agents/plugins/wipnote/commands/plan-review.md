# /wipnote:plan-review

Open a plan in the `wipnote serve` web dashboard for human review and approval.

The legacy marimo review notebook has been retired. Plan review now happens
entirely in the local web dashboard, which renders the plan's slice cards
(complexity, decisions, effort/risk, approval controls) directly from the
canonical YAML source.

## Usage

```
/wipnote:plan-review <plan-id>
```

## Parameters

- `plan-id` (required): The plan ID (e.g., `plan-3a88d8a9`)

## Examples

```bash
/wipnote:plan-review plan-3a88d8a9
```

## Instructions for Claude

1. Verify the plan exists:
   ```bash
   wipnote plan show <plan-id>
   ```
   If not found, suggest `/wipnote:plan-list` to see available plans.

2. Make sure the dashboard is running. In a devcontainer:
   ```bash
   wipnote serve --bind 0.0.0.0 --port 8088
   ```
   On a regular host:
   ```bash
   wipnote serve
   ```

3. Tell the user to open the plan in the Plans view:
   ```
   Plan review available at:

     devcontainer: http://127.0.0.1:8088/#plans
     host:         http://127.0.0.1:8080/#plans

   Click the plan in the sidebar to load it. Each slice card shows
   complexity (trivial/standard/complex), effort, risk, decisions
   notes, open questions, critic revisions, and an approve / request
   changes / reject control. Click Finalize when all sections are
   approved.
   ```

4. Wait for the user to finalize. Check progress with:
   ```bash
   wipnote plan status <plan-id>
   ```
