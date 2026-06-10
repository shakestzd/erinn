# /wipnote:end

End the current session and record work summary

## Usage

```
/wipnote:end
```

## Parameters



## Examples

```bash
/wipnote:end
```
Gracefully end the current session and show work summary



## Instructions for Claude

### Implementation:

**DO THIS:**

1. **Get current session and active work:**
   ```bash
   wipnote session list --limit 1
   wipnote status
   ```

2. **End the session:**
   ```bash
   wipnote session end
   ```

3. **Present session summary** using the output template below, including:
   - Session ID and duration
   - Features worked on during this session
   - Steps marked complete
   - Progress made

4. **Provide next-session guidance:**
   - Mention how to view dashboard: `wipnote serve`
   - Suggest next steps for the next session
   - Link to session record in `.wipnote/sessions/`

5. **CRITICAL CONSTRAINT:**
   - ONLY run `/wipnote:end` when the user explicitly requests it
   - Do NOT automatically end sessions
   - Wait for explicit user command

### Output Format:

## Session Ended

**Session ID:** {session_id}
**Duration:** {duration}
**Events:** {event_count}

### Work Summary
{features_worked_on_with_counts}

### Progress Made
- {accomplishment_summary}

---

Session recorded in `.wipnote/sessions/`
View dashboard: `wipnote serve`
