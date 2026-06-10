# /wipnote:recommend

Get smart recommendations on what to work on next, including project health, bottleneck analysis, and parallel opportunities.

## Usage

```
/wipnote:recommend [--top N]
```

## Parameters

- `--top` (optional, default: 5): Number of recommendations to show

## Examples

```bash
/wipnote:recommend
```
Get top 5 recommendations with full analysis

```bash
/wipnote:recommend --top 10
```
Get top 10 recommendations

## Output

The command displays:
- **Project Health** — Counts of features, bugs, spikes, tracks by status
- **WIP Status** — Current in-progress work against your limit
- **Bottlenecks** — Stale or overloaded items blocking progress
- **Recommended Work** — Top N items scored by priority and impact
- **Parallel Opportunities** — Features grouped by track that can run in parallel

## Instructions for Claude

This command invokes `wipnote recommend` via the CLI, which provides all analytics in one unified output.

**Key workflow:**
1. Run `wipnote recommend [--top N]` where N is user's choice or default 5
2. Present the output with light markdown formatting
3. Analyze the **recommended items** for parallel execution opportunities
4. Propose next action: either parallel launch or sequential plan
