# /wipnote:init

Initialize wipnote in a project

## Usage

```
/wipnote:init
```

## Parameters



## Examples

```bash
/wipnote:init
```
Set up wipnote directory structure in project



## Instructions

### Implementation:

**DO THIS:**

1. **Initialize project:**
   ```bash
   wipnote init
   ```
   The command will report whether `.wipnote/` was created or already exists.

2. **Present next steps** using the output template below.

3. **Guide the user:**
   - How to plan work: `/wipnote:plan "title"`
   - How to start session: `/wipnote:start`
   - How to view dashboard: `/wipnote:serve`

4. **Highlight key points:**
   - All subsequent work will be tracked automatically
   - Use CLI/slash commands for all operations
   - Access dashboard to view progress visually

### Output Format:

## wipnote Initialized

Created `.wipnote/` directory with:
- `features/` - Feature work items
- `sessions/` - Session activity logs
- `tracks/` - Multi-feature tracks
- `spikes/` - Research and investigation
- `bugs/` - Bug tracking
- `refs.json` - Project metadata references
- `styles.css` - Default stylesheet for wipnote HTML nodes

Note:
- Additional paths such as plans, events, and launch/session markers may appear later as other wipnote commands and hooks run.
- Nothing wipnote stores lives outside the project. The canonical record is the files under `.wipnote/` — work-item and architecture HTML, plan YAML, the session/claim/gate ledgers, per-session NDJSON — together with git history, from which commit and file attribution is derived. SQLite is used only as a process-local in-memory query engine, rebuilt inside a single command and discarded when it exits; `wipnote init` creates no database, cache directory, or per-user index.
- Current `wipnote init` does not create legacy analytics directories like `insights/`, `metrics/`, or `cigs/`.

### Next Steps
1. Plan new work: `/wipnote:plan "Feature title"`
2. Start session: `/wipnote:start`
3. View dashboard: `/wipnote:serve`

### Quick Start
```bash
# Start planning
/wipnote:plan "Add user authentication"

# Begin work
/wipnote:start

# View progress
/wipnote:serve
# Open http://localhost:8080
```
