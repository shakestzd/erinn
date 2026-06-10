# /wipnote:feature-complete

Mark a feature as complete

## Usage

```
/wipnote:feature-complete [feature-id]
```

## Parameters

- `feature-id` (optional): The feature ID to complete. If not provided, completes the current active feature.


## Examples

```bash
/wipnote:feature-complete feature-001
```
Complete a specific feature

```bash
/wipnote:feature-complete
```
Complete the current active feature



## Instructions for Claude

### Implementation:

**DO THIS:**

1. **Get current feature if not specified:**
   ```bash
   wipnote find features --status in-progress
   ```
   If no feature_id given, use the first in-progress feature from the list.

2. **Complete the feature:**
   ```bash
   wipnote feature complete {feature_id}
   ```

3. **Get updated project status:**
   ```bash
   wipnote status
   ```

4. **Present summary** using the output template below.

5. **Recommend next steps:**
   ```bash
   wipnote analytics summary
   ```
   - If pending features exist → Suggest starting the next feature
   - If all features done → Congratulate on completion
   - Offer to run `/wipnote:plan` for new work

### Output Format:

## Feature Completed

**ID:** {feature_id}
**Title:** {title}
**Status:** done

### Progress Update
{progress from `wipnote status` output}

### What's Next?
{top recommendation from `wipnote analytics summary`}

**DELEGATION**: Delegate implementation based on complexity:
- Simple fixes (1-2 files) → `call spawn_agent with agent_type "wipnote-patch-coder"`
- Features (3-8 files) → `call spawn_agent with agent_type "wipnote-feature-coder"`
- Architecture (10+ files) → `call spawn_agent with agent_type "wipnote-architect-coder"`
