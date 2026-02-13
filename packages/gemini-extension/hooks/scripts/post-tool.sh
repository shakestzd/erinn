#!/bin/bash
#
# HtmlGraph AfterTool Hook for Gemini CLI
# Tracks tool usage for activity attribution
#

set +e

# Find project root
PROJECT_ROOT="$(pwd)"

# Check if .htmlgraph exists
if [ ! -d "$PROJECT_ROOT/.htmlgraph" ]; then
  exit 0  # Not an HtmlGraph project
fi

# Read hook input from stdin (Gemini passes JSON)
INPUT=$(cat)

# Use jq for robust parsing if available, otherwise fall back to grep
if command -v jq &> /dev/null; then
  TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty')
  TOOL_INPUT=$(echo "$INPUT" | jq -c '.input // empty')
  TOOL_OUTPUT=$(echo "$INPUT" | jq -c '.output // empty')
  SUCCESS=$(echo "$INPUT" | jq -r '.success // true')
else
  TOOL_NAME=$(echo "$INPUT" | grep -o '"tool_name":"[^"]*"' | cut -d'"' -f4)
  TOOL_INPUT=""
  TOOL_OUTPUT=""
  SUCCESS="true"
fi

if [ -z "$TOOL_NAME" ]; then
  exit 0  # No tool name, skip
fi

# Check if htmlgraph is installed
if ! command -v htmlgraph &> /dev/null; then
  if ! command -v uv &> /dev/null; then
    exit 0
  fi
  HTMLGRAPH_CMD="uv run htmlgraph"
else
  HTMLGRAPH_CMD="htmlgraph"
fi

export HTMLGRAPH_AGENT=gemini

# Track the tool usage event with rich payload
# Use a temporary file for the payload to avoid shell escape issues
PAYLOAD_FILE=$(mktemp)
echo "{\"input\": $TOOL_INPUT, \"output\": $TOOL_OUTPUT}" > "$PAYLOAD_FILE"

$HTMLGRAPH_CMD activity "$TOOL_NAME" "Tool used: $TOOL_NAME" \
  --success "$SUCCESS" \
  --payload-file "$PAYLOAD_FILE" &> /dev/null &

# Clean up in background
(sleep 5 && rm -f "$PAYLOAD_FILE") &

exit 0
