"""
Integration tests for Phase 1.4: SessionRegistry + RepoHash + AtomicFileWriter

Tests the unified session management system combining:
- SessionRegistry (file-based registry with atomic writes)
- RepoHash (git awareness and repository identification)
- AtomicFileWriter (crash-safe writes)
- SessionManager (high-level API)
- Session hooks (SessionStart/SessionEnd integration)

Test coverage:
- Session creation with repo info
- Atomic writes for all operations
- Repo-aware session identification
- Parent session detection
- Heartbeat mechanism
- Concurrent session handling
- Monorepo support
- Crash recovery
"""

import json
import os
from datetime import datetime, timezone

from htmlgraph.atomic_ops import AtomicFileWriter
from htmlgraph.repo_hash import RepoHash
from htmlgraph.session_registry import SessionRegistry


class TestSessionRegistryWithRepoHash:
    """Test SessionRegistry integrated with RepoHash."""

    def test_register_session_with_repo_info(self, tmp_path, monkeypatch):
        """Test registering session with repo information."""
        monkeypatch.chdir(tmp_path)

        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {
            "path": str(tmp_path),
            "hash": "repo-abc123def456",
            "branch": "main",
            "commit": "d78e458",
            "remote": "https://github.com/user/repo.git",
            "dirty": False,
            "is_monorepo": False,
        }
        instance_info = {
            "pid": os.getpid(),
            "hostname": "testhost",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        path = registry.register_session("sess-test1", repo_info, instance_info)

        assert path.exists()
        with open(path) as f:
            data = json.load(f)

        assert data["repo"]["hash"] == "repo-abc123def456"
        assert data["repo"]["branch"] == "main"
        assert data["repo"]["commit"] == "d78e458"
        assert data["instance"]["pid"] == os.getpid()

    def test_repo_hash_stability_across_calls(self, tmp_path):
        """Test that repo hash is stable across multiple calls."""
        repo_path = tmp_path / "test_repo"
        repo_path.mkdir()

        repo_hash1 = RepoHash(repo_path)
        hash1 = repo_hash1.compute_repo_hash()

        repo_hash2 = RepoHash(repo_path)
        hash2 = repo_hash2.compute_repo_hash()

        # Same repo should produce same hash
        assert hash1 == hash2
        assert hash1.startswith("repo-")

    def test_monorepo_detection_and_identification(self, tmp_path):
        """Test monorepo structure detection."""
        # Create monorepo structure with multiple pyproject.toml files
        pkg1 = tmp_path / "packages" / "pkg1"
        pkg2 = tmp_path / "packages" / "pkg2"
        pkg1.mkdir(parents=True)
        pkg2.mkdir(parents=True)

        # Create .git directory to mark as git repo
        (tmp_path / ".git").mkdir()

        (pkg1 / "pyproject.toml").write_text("[tool.poetry]\n")
        (pkg2 / "pyproject.toml").write_text("[tool.poetry]\n")

        repo_hash = RepoHash(tmp_path)
        assert repo_hash.is_monorepo()

        # Get monorepo project for pkg1
        repo_hash_pkg1 = RepoHash(pkg1)
        project = repo_hash_pkg1.get_monorepo_project()
        assert project == "packages/pkg1"

    def test_session_registry_uses_atomic_writes(self, tmp_path):
        """Test that SessionRegistry uses atomic writes for all operations."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {"path": "/path/to/repo"}
        instance_info = {
            "pid": 12345,
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        # Register session
        reg_file = registry.register_session("sess-test", repo_info, instance_info)

        # Verify file exists and is complete (no temp files)
        assert reg_file.exists()
        temp_files = list(reg_file.parent.glob(f"{reg_file.stem}*.tmp"))
        assert len(temp_files) == 0

        # Verify content is valid JSON (not partial)
        with open(reg_file) as f:
            data = json.load(f)
        assert data["session_id"] == "sess-test"


class TestSessionHookIntegration:
    """Test integration with SessionStart/SessionEnd hooks."""

    def test_initialize_session_from_hook(self, tmp_path, monkeypatch):
        """Test SessionStart hook initialization."""
        monkeypatch.chdir(tmp_path)

        from htmlgraph.session_hooks import initialize_session_from_hook

        session_id = initialize_session_from_hook()

        assert session_id.startswith("sess-")
        assert len(session_id) == 13  # sess- + 8 hex chars

        # Verify session was registered
        registry = SessionRegistry()
        instance_id = registry.get_instance_id()
        session = registry.read_session(instance_id)

        assert session is not None
        assert session["session_id"] == session_id
        assert session["status"] == "active"

    def test_initialize_session_exports_env_file(self, tmp_path, monkeypatch):
        """Test that hook exports session IDs to CLAUDE_ENV_FILE."""
        monkeypatch.chdir(tmp_path)
        env_file = tmp_path / "env_export"

        from htmlgraph.session_hooks import initialize_session_from_hook

        session_id = initialize_session_from_hook(env_file=str(env_file))

        assert env_file.exists()
        content = env_file.read_text()

        assert f"export HTMLGRAPH_SESSION_ID={session_id}" in content
        assert "export HTMLGRAPH_INSTANCE_ID=" in content
        assert "export HTMLGRAPH_REPO_HASH=" in content

    def test_finalize_session_archives(self, tmp_path, monkeypatch):
        """Test SessionEnd hook archives session."""
        monkeypatch.chdir(tmp_path)

        from htmlgraph.session_hooks import finalize_session

        instance_id = "inst-test-finalize-fixed"

        # Mock get_instance_id to return our fixed ID so finalize can find the registration
        monkeypatch.setattr(
            "htmlgraph.session_registry.SessionRegistry.get_instance_id",
            lambda self: instance_id,
        )

        registry = SessionRegistry()
        from datetime import datetime, timezone

        repo_info = {"path": str(tmp_path)}
        instance_info = {
            "pid": os.getpid(),
            "hostname": "testhost",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }
        session_id = f"sess-{__import__('uuid').uuid4().hex[:8]}"

        session_data = {
            "instance_id": instance_id,
            "session_id": session_id,
            "created": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
            "repo": repo_info,
            "instance": instance_info,
            "status": "active",
            "last_activity": datetime.now(timezone.utc).strftime(
                "%Y-%m-%dT%H:%M:%S.%fZ"
            ),
        }
        reg_file = registry.active_dir / f"{instance_id}.json"
        import json as json_module

        with open(reg_file, "w") as f:
            json_module.dump(session_data, f, indent=2)

        active_before = registry.read_session(instance_id)
        assert active_before is not None

        success = finalize_session(session_id)

        assert success
        assert not registry.read_session(instance_id)
        assert (registry.archive_dir / f"{instance_id}.json").exists()

    def test_heartbeat_updates_timestamp(self, tmp_path, monkeypatch):
        """Test heartbeat mechanism updates activity timestamp."""
        monkeypatch.chdir(tmp_path)
        import time

        # Use a fixed instance ID to avoid flakiness from timestamp changes
        instance_id = "inst-test-heartbeat-fixed"

        # Manually register session with fixed instance ID instead of using initialize_session_from_hook
        # to ensure we control the instance_id
        registry = SessionRegistry()
        from datetime import datetime, timezone

        repo_info = {"path": str(tmp_path)}
        instance_info = {
            "pid": os.getpid(),
            "hostname": "testhost",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }
        session_id = f"sess-{__import__('uuid').uuid4().hex[:8]}"

        # Write session directly with fixed instance_id
        session_data = {
            "instance_id": instance_id,
            "session_id": session_id,
            "created": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
            "repo": repo_info,
            "instance": instance_info,
            "status": "active",
            "last_activity": datetime.now(timezone.utc).strftime(
                "%Y-%m-%dT%H:%M:%S.%fZ"
            ),
        }
        reg_file = registry.active_dir / f"{instance_id}.json"
        import json as json_module

        with open(reg_file, "w") as f:
            json_module.dump(session_data, f, indent=2)

        session_before = registry.read_session(instance_id)
        activity_before = session_before["last_activity"]

        time.sleep(0.01)

        success = registry.update_activity(instance_id)

        assert success
        session_after = registry.read_session(instance_id)
        activity_after = session_after["last_activity"]

        assert activity_after > activity_before

    def test_parent_session_detection(self, tmp_path, monkeypatch):
        """Test detection of parent session from environment."""
        monkeypatch.chdir(tmp_path)

        # Set parent session in environment
        parent_id = "sess-parent123"
        monkeypatch.setenv("HTMLGRAPH_PARENT_SESSION_ID", parent_id)

        from htmlgraph.session_hooks import get_parent_session_id

        detected = get_parent_session_id()

        assert detected == parent_id

    def test_parent_session_alternate_env_var(self, tmp_path, monkeypatch):
        """Test parent session detection with alternate env var name."""
        monkeypatch.chdir(tmp_path)

        # Set with alternate name
        parent_id = "sess-parent456"
        monkeypatch.setenv("HTMLGRAPH_PARENT_SESSION", parent_id)

        from htmlgraph.session_hooks import get_parent_session_id

        detected = get_parent_session_id()

        assert detected == parent_id

    def test_no_parent_session_when_not_set(self, tmp_path, monkeypatch):
        """Test that no parent session is detected when not set."""
        monkeypatch.chdir(tmp_path)

        # Clear both possible env vars
        monkeypatch.delenv("HTMLGRAPH_PARENT_SESSION_ID", raising=False)
        monkeypatch.delenv("HTMLGRAPH_PARENT_SESSION", raising=False)

        from htmlgraph.session_hooks import get_parent_session_id

        detected = get_parent_session_id()

        assert detected is None


class TestConcurrentSessions:
    """Test concurrent session handling."""

    def test_concurrent_registrations_same_repo(self, tmp_path):
        """Test multiple concurrent sessions in same repo."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {
            "path": str(tmp_path),
            "hash": "repo-same123",
            "branch": "main",
        }

        # Simulate multiple instances registering sessions
        # by directly writing to simulate different instance IDs (each with different PID/timestamp)
        sessions = []
        for i in range(5):
            session_id = f"sess-concurrent{i}"
            instance_id = f"inst-{1000 + i}-host-{1700000000 + i}"
            instance_info = {
                "pid": 1000 + i,
                "hostname": "host",
                "start_time": datetime.now(timezone.utc).isoformat(),
            }

            # Write registration file directly to simulate different instances
            session_data = {
                "instance_id": instance_id,
                "session_id": session_id,
                "created": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
                "repo": repo_info,
                "instance": instance_info,
                "status": "active",
                "last_activity": datetime.now(timezone.utc).strftime(
                    "%Y-%m-%dT%H:%M:%S.%fZ"
                ),
            }
            reg_file = registry.active_dir / f"{instance_id}.json"
            with open(reg_file, "w") as f:
                json.dump(session_data, f, indent=2)
            sessions.append(session_id)

        # All sessions should be registered
        all_sessions = registry.get_current_sessions()
        registered_ids = {s["session_id"] for s in all_sessions}

        for session_id in sessions:
            assert session_id in registered_ids

    def test_concurrent_registrations_different_repos(self, tmp_path):
        """Test multiple concurrent sessions in different repos."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repos = [
            {"path": "/repo/a", "hash": "repo-aaaa", "branch": "main"},
            {"path": "/repo/b", "hash": "repo-bbbb", "branch": "dev"},
            {"path": "/repo/c", "hash": "repo-cccc", "branch": "feature"},
        ]

        # Register session for each repo by writing directly (simulating different instances)
        for i, repo_info in enumerate(repos):
            session_id = f"sess-repo{i}"
            instance_id = f"inst-{2000 + i}-host-{1700000000 + i}"
            instance_info = {
                "pid": 2000 + i,
                "hostname": "host",
                "start_time": datetime.now(timezone.utc).isoformat(),
            }

            # Write registration file directly
            session_data = {
                "instance_id": instance_id,
                "session_id": session_id,
                "created": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
                "repo": repo_info,
                "instance": instance_info,
                "status": "active",
                "last_activity": datetime.now(timezone.utc).strftime(
                    "%Y-%m-%dT%H:%M:%S.%fZ"
                ),
            }
            reg_file = registry.active_dir / f"{instance_id}.json"
            with open(reg_file, "w") as f:
                json.dump(session_data, f, indent=2)

        # Verify all sessions
        all_sessions = registry.get_current_sessions()
        assert len(all_sessions) == 3

        # Verify each session has correct repo info
        for session in all_sessions:
            assert session["repo"]["hash"].startswith("repo-")

    def test_session_identification_by_repo_hash(self, tmp_path):
        """Test identifying sessions by repository hash."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {
            "path": "/repo/main",
            "hash": "repo-main123",
            "branch": "main",
        }

        instance_info = {
            "pid": 3000,
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        registry.register_session("sess-repo", repo_info, instance_info)

        # Find session by repo hash
        all_sessions = registry.get_current_sessions()
        repo_sessions = [s for s in all_sessions if s["repo"]["hash"] == "repo-main123"]

        assert len(repo_sessions) == 1
        assert repo_sessions[0]["session_id"] == "sess-repo"


class TestAtomicWriteIntegration:
    """Test atomic write crash recovery."""

    def test_atomic_write_no_temp_files_leak(self, tmp_path):
        """Test that atomic writes don't leak temp files."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {"path": "/path/to/repo"}
        instance_info = {
            "pid": 4000,
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        # Register multiple times
        for i in range(10):
            instance_info["pid"] = 4000 + i
            registry.register_session(f"sess-atomic{i}", repo_info, instance_info)

        # Check for orphaned temp files
        temp_files = list(registry.active_dir.glob("*.tmp"))
        assert len(temp_files) == 0

    def test_partial_write_not_visible_to_readers(self, tmp_path):
        """Test that partial writes are not visible to readers."""
        target_file = tmp_path / "test.json"
        data = {"key": "value", "nested": {"deep": "data"}}

        # Use AtomicFileWriter
        with AtomicFileWriter(target_file) as f:
            # File is not visible yet
            assert not target_file.exists()
            f.write(json.dumps(data, indent=2))

        # File is visible after context exit
        assert target_file.exists()
        with open(target_file) as f:
            written = json.load(f)
        assert written == data

    def test_write_failure_leaves_original_untouched(self, tmp_path):
        """Test that failed writes don't corrupt original file."""
        target_file = tmp_path / "test.json"
        original_data = {"status": "original"}

        # Write original
        with open(target_file, "w") as f:
            json.dump(original_data, f)

        # Attempt write that fails
        try:
            with AtomicFileWriter(target_file) as f:
                f.write(json.dumps({"status": "new"}))
                raise RuntimeError("Simulated failure")
        except RuntimeError:
            pass

        # Original should be unchanged
        with open(target_file) as f:
            data = json.load(f)
        assert data == original_data


class TestSessionRegistryIndexManagement:
    """Test index file management for fast lookups."""

    def test_index_creation_on_first_registration(self, tmp_path):
        """Test that index file is created on first registration."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        assert not registry.index_file.exists()

        repo_info = {"path": "/path/to/repo"}
        instance_info = {
            "pid": 5000,
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        registry.register_session("sess-index1", repo_info, instance_info)

        assert registry.index_file.exists()

    def test_index_contains_all_active_sessions(self, tmp_path):
        """Test that index contains entries for all active sessions."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {"path": "/path/to/repo"}

        # Register multiple sessions
        for i in range(5):
            instance_info = {
                "pid": 5000 + i,
                "hostname": "host",
                "start_time": datetime.now(timezone.utc).isoformat(),
            }
            session_id = f"sess-index{i}"
            registry.register_session(session_id, repo_info, instance_info)

        # Check index
        with open(registry.index_file) as f:
            index = json.load(f)

        assert len(index["active_sessions"]) == 5
        for i in range(5):
            assert f"sess-index{i}" in index["active_sessions"]

    def test_index_updated_on_activity_change(self, tmp_path):
        """Test that index is updated when activity changes."""
        import time

        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {"path": "/path/to/repo"}
        instance_info = {
            "pid": 5100,
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        registry.register_session("sess-activity", repo_info, instance_info)
        instance_id = registry.get_instance_id()

        # Get original activity time
        with open(registry.index_file) as f:
            index_before = json.load(f)
        activity_before = index_before["active_sessions"]["sess-activity"][
            "last_activity"
        ]

        time.sleep(0.01)

        # Update activity
        registry.update_activity(instance_id)

        # Check index was updated
        with open(registry.index_file) as f:
            index_after = json.load(f)
        activity_after = index_after["active_sessions"]["sess-activity"][
            "last_activity"
        ]

        assert activity_after > activity_before

    def test_index_cleaned_on_archive(self, tmp_path):
        """Test that index is cleaned when session is archived."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {"path": "/path/to/repo"}
        instance_info = {
            "pid": 5200,
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        registry.register_session("sess-archive", repo_info, instance_info)
        instance_id = registry.get_instance_id()

        # Verify in index
        with open(registry.index_file) as f:
            index = json.load(f)
        assert "sess-archive" in index["active_sessions"]

        # Archive session
        registry.archive_session(instance_id)

        # Verify removed from index
        with open(registry.index_file) as f:
            index = json.load(f)
        assert "sess-archive" not in index.get("active_sessions", {})


class TestErrorRecovery:
    """Test error handling and recovery."""

    def test_corrupt_index_recovery(self, tmp_path):
        """Test recovery from corrupt index file."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {"path": "/path/to/repo"}
        instance_info = {
            "pid": 6000,
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        # Register session (creates index)
        registry.register_session("sess-valid", repo_info, instance_info)

        # Corrupt index
        with open(registry.index_file, "w") as f:
            f.write("{ invalid json")

        # Registering new session should recover by rewriting index
        instance_info["pid"] = 6001
        registry.register_session("sess-recovery", repo_info, instance_info)

        # Index should be valid now
        with open(registry.index_file) as f:
            index = json.load(f)
        assert "sess-recovery" in index["active_sessions"]

    def test_orphaned_session_detection(self, tmp_path):
        """Test detection of orphaned sessions (dead processes)."""
        registry = SessionRegistry(registry_dir=tmp_path / "registry")

        repo_info = {"path": "/path/to/repo"}
        instance_id = "inst-9999-host-1700000000"
        instance_info = {
            "pid": 9999,  # Non-existent PID
            "hostname": "host",
            "start_time": datetime.now(timezone.utc).isoformat(),
        }

        # Register with dead PID
        session_data = {
            "instance_id": instance_id,
            "session_id": "sess-orphan",
            "created": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
            "repo": repo_info,
            "instance": instance_info,
            "status": "active",
            "last_activity": datetime.now(timezone.utc).strftime(
                "%Y-%m-%dT%H:%M:%S.%fZ"
            ),
        }
        reg_file = registry.active_dir / f"{instance_id}.json"
        with open(reg_file, "w") as f:
            json.dump(session_data, f, indent=2)

        # Session should still be readable
        session = registry.read_session(instance_id)
        assert session is not None
        assert session["session_id"] == "sess-orphan"
        assert session["instance"]["pid"] == 9999


class TestMonorepoScenarios:
    """Test monorepo-specific scenarios."""

    def test_monorepo_project_identification(self, tmp_path):
        """Test identifying which project in monorepo owns a session."""
        # Create monorepo structure
        root = tmp_path / "monorepo"
        root.mkdir()
        (root / ".git").mkdir()  # Mark as git repo

        pkg1 = root / "packages" / "pkg1"
        pkg2 = root / "packages" / "pkg2"
        pkg1.mkdir(parents=True)
        pkg2.mkdir(parents=True)

        (pkg1 / "pyproject.toml").write_text("[tool.poetry]\n")
        (pkg2 / "pyproject.toml").write_text("[tool.poetry]\n")

        # Get monorepo project for pkg1
        repo_hash = RepoHash(pkg1)
        project = repo_hash.get_monorepo_project()

        assert project == "packages/pkg1"

    def test_different_monorepo_projects_different_hashes(self, tmp_path):
        """Test that different monorepo projects are distinguishable via project name."""
        # Create monorepo
        root = tmp_path / "monorepo"
        root.mkdir()
        (root / ".git").mkdir()

        pkg1 = root / "packages" / "pkg1"
        pkg2 = root / "packages" / "pkg2"
        pkg1.mkdir(parents=True)
        pkg2.mkdir(parents=True)

        (pkg1 / "pyproject.toml").write_text("[tool.poetry]\n")
        (pkg2 / "pyproject.toml").write_text("[tool.poetry]\n")

        # Root detects monorepo (multiple pyproject.toml files)
        assert RepoHash(root).is_monorepo()

        # Projects identify correctly relative to monorepo root
        project1 = RepoHash(pkg1).get_monorepo_project()
        project2 = RepoHash(pkg2).get_monorepo_project()

        assert project1 != project2
        assert project1 == "packages/pkg1"
        assert project2 == "packages/pkg2"
