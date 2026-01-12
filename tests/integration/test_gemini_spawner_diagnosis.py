#!/usr/bin/env python3
"""Test GeminiSpawner to diagnose failures."""

import sys
from pathlib import Path

# Add project root to path
project_root = Path(__file__).parent
sys.path.insert(0, str(project_root / "src" / "python"))

from htmlgraph.orchestration.spawners import GeminiSpawner


def test_basic_gemini():
    """Test basic Gemini spawner invocation."""
    print("=" * 80)
    print("GEMINI SPAWNER DIAGNOSTIC TEST")
    print("=" * 80)

    spawner = GeminiSpawner()

    # Test 1: Simple prompt without tracking
    print("\n[TEST 1] Simple prompt without tracking")
    print("-" * 80)
    result = spawner.spawn(
        prompt="What is 2+2? Answer in one word.",
        output_format="json",
        model="gemini-2.0-flash",
        track_in_htmlgraph=False,
        timeout=30,
    )

    print(f"Success: {result.success}")
    print(f"Response: {result.response}")
    print(f"Error: {result.error}")
    print(f"Tokens: {result.tokens_used}")

    if not result.success:
        print("\n❌ FAILURE DETECTED")
        print(f"Error details: {result.error}")
        print(f"Raw output: {result.raw_output}")
        return False

    # Test 2: Stream JSON format
    print("\n[TEST 2] Stream JSON format")
    print("-" * 80)
    result2 = spawner.spawn(
        prompt="List 3 prime numbers",
        output_format="stream-json",
        model="gemini-2.0-flash",
        track_in_htmlgraph=False,
        timeout=30,
    )

    print(f"Success: {result2.success}")
    print(f"Response: {result2.response[:200] if result2.response else 'None'}...")
    print(f"Error: {result2.error}")

    if not result2.success:
        print("\n❌ FAILURE DETECTED")
        print(f"Error details: {result2.error}")
        return False

    print("\n" + "=" * 80)
    print("✅ ALL TESTS PASSED")
    print("=" * 80)
    return True


if __name__ == "__main__":
    success = test_basic_gemini()
    sys.exit(0 if success else 1)
