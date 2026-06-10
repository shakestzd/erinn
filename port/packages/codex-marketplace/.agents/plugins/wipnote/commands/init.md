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



## Instructions for Claude

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
- `wipnote.db` - SQLite read index for queries and dashboard
- `refs.json` - Project metadata references
- `styles.css` - Default stylesheet for wipnote HTML nodes

Note:
- Additional paths such as plans, events, and launch/session markers may appear later as other wipnote commands and hooks run.
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
