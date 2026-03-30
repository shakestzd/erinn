#!/usr/bin/env bash
# Wrapper: invokes the Python systematic-change checker.
# Install via: git config core.hooksPath .githooks
set -euo pipefail
exec python3 "$(dirname "$0")/pre-commit-systematic-check.py" "$@"
