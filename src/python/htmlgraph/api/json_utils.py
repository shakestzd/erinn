"""
JSON Handling Utilities - Backward compatibility re-exports.

DEPRECATED: This module is deprecated. Import from htmlgraph.json_utils instead.

The JSON utilities have been promoted to the top-level htmlgraph.json_utils module
to make them available to all code (hooks, CLI, core).

Old imports:
    from htmlgraph.api.json_utils import JSONHandler

New imports (recommended):
    from htmlgraph.json_utils import JSONHandler
"""

# noqa: F401 - Re-export for backward compatibility
from htmlgraph.json_utils import (
    JSONHandler,
    JSONParseError,
    parse_json,
    serialize_json,
    validate_json,
)

__all__ = [
    "JSONHandler",
    "JSONParseError",
    "parse_json",
    "serialize_json",
    "validate_json",
]
