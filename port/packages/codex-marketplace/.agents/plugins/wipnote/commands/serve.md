# /wipnote:serve

Start the dashboard server

## Usage

```
/wipnote:serve [port]
```

## Parameters

- `port` (optional) (default: 8080): Port number for the dashboard server


## Examples

```bash
/wipnote:serve
```
Start dashboard on default port 8080

```bash
/wipnote:serve 3000
```
Start dashboard on port 3000



## Instructions for Claude

### Implementation:

**DO THIS:**

1. **Start the dashboard server in the background:**
   ```bash
   wipnote serve --port {port} &
   ```
   Default port is 8080 if not specified.

   Note: `wipnote serve` blocks the terminal if run in the foreground. Use `&` to background it, or run it in a separate terminal if you need to see its output.

2. **Present dashboard information** using the output template below.

3. **Explain dashboard features:**
   - Real-time feature progress tracking
   - Kanban board for task organization
   - Session activity logs
   - Dependency graph visualization

4. **Provide stop instructions:**
   - If backgrounded: `kill %1` or `kill $(lsof -ti :{port})`
   - If running in a separate terminal: press Ctrl+C in that terminal

### Output Format:

## Dashboard Running

**URL:** http://localhost:{port}

The dashboard shows:
- Feature progress and kanban board
- Session history with activity logs
- Graph visualization of dependencies

Server is running in the background. To stop: `kill %1` or `kill $(lsof -ti :{port})`.
