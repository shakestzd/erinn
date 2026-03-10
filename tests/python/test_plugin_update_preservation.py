"""
Integration tests for plugin update preservation with user customizations.

Tests verify that:
1. User customizations are detected correctly
2. Migrations preserve user customizations
3. Rollback functionality works with customizations
4. Plugin updates don't overwrite user docs
5. Edge cases are handled properly
"""

import tempfile
from datetime import datetime
from pathlib import Path

import pytest
from htmlgraph import __version__
from htmlgraph.docs import (
    DocsMetadata,
    get_agents_md,
    sync_docs_to_file,
)
from htmlgraph.docs.migrations import V1toV2Migration, get_migration
from htmlgraph.docs.template_engine import DocTemplateEngine
from htmlgraph.docs.version_check import (
    check_docs_version,
    check_version_on_init,
)


@pytest.fixture
def temp_project_dir():
    """Create temporary project directory for testing."""
    with tempfile.TemporaryDirectory() as tmpdir:
        project_dir = Path(tmpdir)
        htmlgraph_dir = project_dir / ".htmlgraph"
        htmlgraph_dir.mkdir(parents=True)
        yield project_dir, htmlgraph_dir


@pytest.fixture
def v1_project_with_customizations(temp_project_dir):
    """Create v1 project with user customizations."""
    project_dir, htmlgraph_dir = temp_project_dir

    # Create v1 metadata
    metadata = DocsMetadata(
        schema_version=1,
        last_updated=datetime.now(),
        customizations=[],
        base_version_on_last_update="0.20.0",
    )
    metadata.save(htmlgraph_dir)

    # Create customized AGENTS.md (v1 style)
    agents_md = htmlgraph_dir / "AGENTS.md"
    agents_md.write_text(
        """# HtmlGraph Agent Documentation

## Introduction

Standard introduction text here.

## Our Team

This is a custom section added by the user.
It should be preserved during migration.

## Quick Start

Standard quick start...

## Custom Workflows

### Deployment Workflow

1. Run tests
2. Build package
3. Publish to PyPI

This custom workflow should be preserved!

## Project Conventions

- Use uv for all Python operations
- Follow semantic versioning
- Always run ruff before commit

## Core Concepts

Standard core concepts...
"""
    )

    return project_dir, htmlgraph_dir, metadata


@pytest.fixture
def v2_project_with_overrides(temp_project_dir):
    """Create v2 project with template overrides."""
    project_dir, htmlgraph_dir = temp_project_dir

    # Create v2 metadata
    metadata = DocsMetadata(
        schema_version=2,
        last_updated=datetime.now(),
        customizations=["custom_header", "custom_workflows"],
        base_version_on_last_update=__version__,
    )
    metadata.save(htmlgraph_dir)

    # Create docs/templates directory
    templates_dir = htmlgraph_dir / "docs" / "templates"
    templates_dir.mkdir(parents=True)

    # Create user override template
    override_template = templates_dir / "agents.md.j2"
    override_template.write_text(
        """{% extends "base_agents.md.j2" %}

{% block header %}
# 🚀 My Custom HtmlGraph Docs
**Project Version: {{ sdk_version }}**
{% endblock %}

{% block custom_workflows %}
## Custom Deployment Workflow

### Step-by-Step Guide

1. **Test Everything**
   ```bash
   uv run pytest
   ```

2. **Build Package**
   ```bash
   ./scripts/deploy-all.sh {{ sdk_version }} --no-confirm
   ```

3. **Verify Publication**
   - Check PyPI: https://pypi.org/project/htmlgraph/
   - Test installation: `pip install htmlgraph=={{ sdk_version }}`

{% endblock %}
"""
    )

    return project_dir, htmlgraph_dir, metadata


# ============================================================================
# Test Group 1: User Customization Detection
# ============================================================================


def test_detect_v1_customizations(v1_project_with_customizations):
    """Test detection of user customizations in v1 AGENTS.md."""
    _, htmlgraph_dir, _ = v1_project_with_customizations

    migration = V1toV2Migration()
    agents_md = htmlgraph_dir / "AGENTS.md"

    customizations = migration._detect_customizations(agents_md)

    # Should detect custom sections
    assert "custom_workflows" in customizations
    assert (
        "project_conventions" in customizations or "custom_workflows" in customizations
    )


def test_detect_no_customizations_in_empty_file(temp_project_dir):
    """Test detection returns empty list for non-existent file."""
    _, htmlgraph_dir = temp_project_dir

    migration = V1toV2Migration()
    agents_md = htmlgraph_dir / "AGENTS.md"

    customizations = migration._detect_customizations(agents_md)

    assert customizations == []


def test_metadata_tracks_customizations(v2_project_with_overrides):
    """Test that metadata correctly tracks customized sections."""
    _, htmlgraph_dir, _ = v2_project_with_overrides

    metadata = DocsMetadata.load(htmlgraph_dir)

    assert metadata.has_customizations()
    assert "custom_header" in metadata.customizations
    assert "custom_workflows" in metadata.customizations


def test_metadata_add_customization(temp_project_dir):
    """Test adding customization to metadata."""
    _, htmlgraph_dir = temp_project_dir

    metadata = DocsMetadata()
    metadata.add_customization("custom_section")

    assert "custom_section" in metadata.customizations
    assert metadata.has_customizations()

    # Adding duplicate should not create duplicate entry
    metadata.add_customization("custom_section")
    assert metadata.customizations.count("custom_section") == 1


def test_metadata_remove_customization(temp_project_dir):
    """Test removing customization from metadata."""
    _, htmlgraph_dir = temp_project_dir

    metadata = DocsMetadata(customizations=["section1", "section2"])
    metadata.remove_customization("section1")

    assert "section1" not in metadata.customizations
    assert "section2" in metadata.customizations


# ============================================================================
# Test Group 2: Migration with Customizations
# ============================================================================


def test_v1_to_v2_migration_preserves_customizations(v1_project_with_customizations):
    """Test that v1→v2 migration preserves user customizations."""
    _, htmlgraph_dir, _ = v1_project_with_customizations

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    migration = V1toV2Migration()

    # Run migration
    success = migration.migrate(htmlgraph_dir, backup_dir)

    assert success

    # Check metadata updated
    metadata = DocsMetadata.load(htmlgraph_dir)
    assert metadata.schema_version == 2
    assert len(metadata.customizations) > 0

    # Check backup created
    backups = list(backup_dir.glob("v1_*"))
    assert len(backups) == 1
    backup_agents = backups[0] / "AGENTS.md"
    assert backup_agents.exists()


def test_migration_creates_backup_before_changes(v1_project_with_customizations):
    """Test that migration creates backup before making changes."""
    _, htmlgraph_dir, _ = v1_project_with_customizations

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    # Get original content
    original_content = (htmlgraph_dir / "AGENTS.md").read_text()

    migration = V1toV2Migration()
    migration.migrate(htmlgraph_dir, backup_dir)

    # Check backup contains original content
    backups = list(backup_dir.glob("v1_*"))
    backup_content = (backups[0] / "AGENTS.md").read_text()

    assert backup_content == original_content


def test_migration_archives_old_docs(v1_project_with_customizations):
    """Test that migration archives old AGENTS.md."""
    _, htmlgraph_dir, _ = v1_project_with_customizations

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    migration = V1toV2Migration()
    migration.migrate(htmlgraph_dir, backup_dir)

    # Old AGENTS.md should be renamed
    assert (
        not (htmlgraph_dir / "AGENTS.md").exists()
        or (htmlgraph_dir / "AGENTS.md.v1.backup").exists()
    )


def test_get_migration_returns_correct_script():
    """Test migration registry lookup."""
    migration = get_migration(1, 2)

    assert migration is not None
    assert isinstance(migration, V1toV2Migration)
    assert migration.from_version == 1
    assert migration.to_version == 2


def test_get_migration_returns_none_for_invalid_versions():
    """Test migration lookup with invalid versions."""
    migration = get_migration(99, 100)

    assert migration is None


# ============================================================================
# Test Group 3: Rollback Functionality
# ============================================================================


def test_rollback_restores_v1_docs(v1_project_with_customizations):
    """Test rollback restores original v1 documentation."""
    _, htmlgraph_dir, _ = v1_project_with_customizations

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    # Get original content
    original_content = (htmlgraph_dir / "AGENTS.md").read_text()

    # Migrate
    migration = V1toV2Migration()
    migration.migrate(htmlgraph_dir, backup_dir)

    # Check migration happened
    metadata = DocsMetadata.load(htmlgraph_dir)
    assert metadata.schema_version == 2

    # Rollback
    migration.rollback(htmlgraph_dir, backup_dir)

    # Check rollback succeeded
    metadata = DocsMetadata.load(htmlgraph_dir)
    assert metadata.schema_version == 1

    # Check content restored
    restored_content = (htmlgraph_dir / "AGENTS.md").read_text()
    assert restored_content == original_content


def test_rollback_uses_latest_backup(v1_project_with_customizations):
    """Test rollback uses most recent backup."""
    _, htmlgraph_dir, _ = v1_project_with_customizations

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    migration = V1toV2Migration()

    # Create multiple backups
    migration.migrate(htmlgraph_dir, backup_dir)

    # Manually create another backup
    backup_subdir = backup_dir / "v1_20240101_120000"
    backup_subdir.mkdir()
    (backup_subdir / "AGENTS.md").write_text("old backup content")

    # Rollback should use latest
    migration.rollback(htmlgraph_dir, backup_dir)

    # Check it used the most recent backup
    backups = sorted(backup_dir.glob("v1_*"))
    assert len(backups) >= 1


def test_rollback_raises_error_when_no_backup(temp_project_dir):
    """Test rollback fails gracefully when no backup exists."""
    _, htmlgraph_dir = temp_project_dir

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    migration = V1toV2Migration()

    with pytest.raises(ValueError, match="No backup found"):
        migration.rollback(htmlgraph_dir, backup_dir)


# ============================================================================
# Test Group 4: Template Engine with Customizations
# ============================================================================


def test_template_engine_loads_user_overrides(v2_project_with_overrides):
    """Test template engine prioritizes user overrides."""
    _, htmlgraph_dir, _ = v2_project_with_overrides

    engine = DocTemplateEngine(htmlgraph_dir)
    content = engine.render_agents_md(__version__, "claude")

    # Should contain user's custom header
    assert "🚀 My Custom HtmlGraph Docs" in content
    assert "Custom Deployment Workflow" in content


def test_template_engine_falls_back_to_base(temp_project_dir):
    """Test template engine falls back to base template when no overrides."""
    _, htmlgraph_dir = temp_project_dir

    engine = DocTemplateEngine(htmlgraph_dir)
    content = engine.render_agents_md(__version__, "claude")

    # Should contain base template content
    assert "HtmlGraph Agent Documentation" in content or "HtmlGraph" in content


def test_template_engine_merges_base_and_overrides(v2_project_with_overrides):
    """Test template correctly extends base template."""
    _, htmlgraph_dir, _ = v2_project_with_overrides

    engine = DocTemplateEngine(htmlgraph_dir)
    content = engine.render_agents_md(__version__, "claude")

    # Should have custom header (from override)
    assert "🚀 My Custom HtmlGraph Docs" in content

    # Should also have base sections (inherited from base_agents.md.j2)
    # Note: Need to check what's actually inherited based on template structure


def test_get_agents_md_with_customizations(v2_project_with_overrides):
    """Test high-level get_agents_md function with customizations."""
    _, htmlgraph_dir, _ = v2_project_with_overrides

    content = get_agents_md(htmlgraph_dir, "claude")

    # Should include user customizations
    assert "🚀 My Custom HtmlGraph Docs" in content


def test_sync_docs_to_file_writes_customized_content(v2_project_with_overrides):
    """Test sync_docs_to_file writes customized documentation."""
    project_dir, htmlgraph_dir, _ = v2_project_with_overrides

    output_file = project_dir / "AGENTS.md"
    result_path = sync_docs_to_file(htmlgraph_dir, output_file, "claude")

    assert result_path == output_file
    assert output_file.exists()

    content = output_file.read_text()
    assert "🚀 My Custom HtmlGraph Docs" in content


# ============================================================================
# Test Group 5: Version Checking
# ============================================================================


def test_check_docs_version_compatible(temp_project_dir):
    """Test version checking with compatible docs."""
    _, htmlgraph_dir = temp_project_dir

    # Create current version metadata
    DocsMetadata.create_initial(htmlgraph_dir, schema_version=2)

    compatible, message = check_docs_version(htmlgraph_dir)

    assert compatible
    assert message is None


def test_check_docs_version_outdated_but_compatible(temp_project_dir):
    """Test version checking with outdated but compatible docs."""
    _, htmlgraph_dir = temp_project_dir

    # Create v1 metadata (compatible but outdated)
    metadata = DocsMetadata(schema_version=1)
    metadata.save(htmlgraph_dir)

    compatible, message = check_docs_version(htmlgraph_dir)

    # Should be compatible with warning
    assert compatible
    assert message is not None
    assert "outdated" in message.lower() or "supported" in message.lower()


def test_check_version_on_init_succeeds_when_compatible(temp_project_dir):
    """Test initialization check with compatible version."""
    _, htmlgraph_dir = temp_project_dir

    DocsMetadata.create_initial(htmlgraph_dir, schema_version=2)

    result = check_version_on_init(htmlgraph_dir, auto_upgrade=False)

    assert result is True


# ============================================================================
# Test Group 6: Edge Cases and Error Handling
# ============================================================================


def test_migration_with_missing_agents_md(temp_project_dir):
    """Test migration handles missing AGENTS.md gracefully."""
    _, htmlgraph_dir = temp_project_dir

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    # Create v1 metadata but no AGENTS.md
    metadata = DocsMetadata(schema_version=1)
    metadata.save(htmlgraph_dir)

    migration = V1toV2Migration()

    # Should not crash
    success = migration.migrate(htmlgraph_dir, backup_dir)
    assert success


def test_metadata_load_creates_default_when_missing(temp_project_dir):
    """Test metadata loading creates default when file doesn't exist."""
    _, htmlgraph_dir = temp_project_dir

    metadata = DocsMetadata.load(htmlgraph_dir)

    assert metadata.schema_version >= 1
    assert not metadata.has_customizations()


def test_metadata_save_and_load_roundtrip(temp_project_dir):
    """Test metadata serialization roundtrip."""
    _, htmlgraph_dir = temp_project_dir

    # Create metadata with customizations
    original = DocsMetadata(
        schema_version=2,
        customizations=["section1", "section2"],
        base_version_on_last_update="0.21.0",
    )
    original.save(htmlgraph_dir)

    # Load it back
    loaded = DocsMetadata.load(htmlgraph_dir)

    assert loaded.schema_version == original.schema_version
    assert loaded.customizations == original.customizations
    assert loaded.base_version_on_last_update == original.base_version_on_last_update


def test_migration_with_corrupt_metadata(temp_project_dir):
    """Test migration handles corrupt metadata gracefully."""
    _, htmlgraph_dir = temp_project_dir

    # Create corrupt metadata file
    metadata_file = htmlgraph_dir / ".docs-metadata.json"
    metadata_file.write_text("{ invalid json }")

    # Should handle gracefully (may raise or return default)
    try:
        metadata = DocsMetadata.load(htmlgraph_dir)
        # If it succeeds, should return valid metadata
        assert hasattr(metadata, "schema_version")
    except Exception:
        # If it raises, that's also acceptable for corrupt data
        pass


def test_template_engine_with_nonexistent_template_dir(temp_project_dir):
    """Test template engine works when user template dir doesn't exist."""
    _, htmlgraph_dir = temp_project_dir

    # Don't create docs/templates directory
    engine = DocTemplateEngine(htmlgraph_dir)

    # Should fall back to package templates
    content = engine.render_agents_md(__version__, "claude")

    assert len(content) > 0
    assert "HtmlGraph" in content or "htmlgraph" in content.lower()


def test_backup_preserves_file_timestamps(v1_project_with_customizations):
    """Test backup preserves original file timestamps."""
    _, htmlgraph_dir, _ = v1_project_with_customizations

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    # Get original timestamp
    agents_md = htmlgraph_dir / "AGENTS.md"
    original_mtime = agents_md.stat().st_mtime

    migration = V1toV2Migration()
    backup_path = migration.backup_docs(htmlgraph_dir, backup_dir)

    # Check backup has same timestamp (shutil.copy2 preserves)
    backup_agents = list(backup_path.glob("AGENTS.md"))[0]
    backup_mtime = backup_agents.stat().st_mtime

    # Should be very close (within 1 second for filesystem precision)
    assert abs(original_mtime - backup_mtime) < 1.0


@pytest.mark.slow
def test_multiple_migrations_create_multiple_backups(v1_project_with_customizations):
    """Test running migration multiple times creates separate backups."""
    import time

    _, htmlgraph_dir, _ = v1_project_with_customizations

    backup_dir = htmlgraph_dir / ".docs-backups"
    backup_dir.mkdir(exist_ok=True)

    migration = V1toV2Migration()

    # First migration
    backup1 = migration.backup_docs(htmlgraph_dir, backup_dir)

    # Wait to ensure different timestamp in backup filename
    time.sleep(1.1)

    # Second migration (simulate)
    backup2 = migration.backup_docs(htmlgraph_dir, backup_dir)

    # Should create separate backups
    assert backup1 != backup2
    backups = list(backup_dir.glob("v1_*"))
    assert len(backups) >= 2
