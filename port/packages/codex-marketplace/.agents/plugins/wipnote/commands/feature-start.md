# /wipnote:feature-start

Start working on a feature (moves it to in-progress)

## Usage

```
/wipnote:feature-start [feature-id]
```

## Parameters

- `feature-id` (optional): The feature ID to start working on. If not provided, lists available features.


## Examples

```bash
/wipnote:feature-start feature-001
```
Start working on feature-001

```bash
/wipnote:feature-start
```
List available features and prompt for selection



## Instructions for Claude

### Implementation:

**DO THIS:**

1. **Check if feature-id provided:**
   - If YES → Go to step 3
   - If NO → Go to step 2

2. **List available features:**
   ```bash
   wipnote find features --status todo
   ```
   Show available features to the user, ask which they want to start, wait for response.

3. **Start the feature:**
   ```bash
   wipnote feature start {feature_id}
   ```

4. **Get feature details:**
   ```bash
   wipnote feature show {feature_id}
   ```
   Extract: title, ID, status (should be "in-progress"), description, and any implementation steps.

5. **Show step breakdown if available:**
   - If feature has steps, show progress: "Step X/Y complete"
   - Display remaining steps clearly

6. **Present the feature context** using the output template below.

7. **Confirm readiness:**
   - Show the feature details clearly
   - Ask what the user would like to work on first

### Output Format:

## Started: {feature_title}

**ID:** {feature_id}
**Status:** in-progress

### Description
{feature_description}

### Steps
{implementation_steps}

---

All activity will now be attributed to this feature.
What would you like to work on first?
