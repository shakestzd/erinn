# /wipnote:help

Display available wipnote commands and usage

## Usage

```
/wipnote:help
```

## Parameters



## Examples

```bash
/wipnote:help
```
Show all available commands and their descriptions



## Instructions for Claude

### Implementation:

**DO THIS:**

1. **Retrieve help text:**
   ```bash
   wipnote --help
   ```

2. **Present the complete help message** using the output template above

3. **Organize by categories:**
   - Session Management - user-facing workflow commands
   - Feature Management - feature lifecycle commands
   - Utilities - setup, dashboard, and tracking
   - CLI Commands - direct CLI usage alternatives
   - Dashboard - browser-based viewing instructions

4. **Make it actionable:**
   - Each command includes a description of what it does
   - Include usage examples where applicable
   - Provide CLI equivalents for power users

5. **Highlight key information:**
   - Dashboard access: `wipnote serve` → http://localhost:8080
   - All commands start with `/wipnote:` for consistency
   - CLI is available as alternative interface
```

### Output Format:

## wipnote Commands

### Session Management
- `/wipnote:start` - Start session, see status, choose what to work on
- `/wipnote:end` - End current session gracefully
- `/wipnote:status` - Quick status check

### Feature Management
- `/wipnote:feature-add [title]` - Add a new feature
- `/wipnote:feature-start <id>` - Start working on a feature
- `/wipnote:feature-complete [id]` - Mark feature as complete
- `/wipnote:feature-primary <id>` - Set primary feature for attribution

### Utilities
- `/wipnote:init` - Initialize wipnote in project
- `/wipnote:serve [port]` - Start dashboard server
- `/wipnote:track <tool> <summary>` - Manually track activity

### CLI Commands
You can also use the CLI directly:
```bash
wipnote --help
wipnote status
wipnote feature list
wipnote session list
```

### Dashboard
View progress in browser:
```bash
wipnote serve
# Open http://localhost:8080
```
