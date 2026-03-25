"""Tests for orchestrator mode state management."""

from datetime import datetime, timezone

from htmlgraph.orchestrator_mode import OrchestratorMode, OrchestratorModeManager


class TestOrchestratorMode:
    """Test OrchestratorMode model."""

    def test_default_state(self):
        """Test default orchestrator mode state."""
        mode = OrchestratorMode()
        assert mode.enabled is False
        assert mode.activated_at is None
        assert mode.session_id is None
        assert mode.enforcement_level == "strict"
        assert mode.auto_activated is False
        assert mode.disabled_by_user is False

    def test_to_dict(self):
        """Test conversion to dict."""
        now = datetime.now(timezone.utc)
        mode = OrchestratorMode(
            enabled=True,
            activated_at=now,
            session_id="sess-123",
            enforcement_level="guidance",
            auto_activated=True,
            disabled_by_user=False,
        )

        data = mode.to_dict()
        assert data["enabled"] is True
        assert data["activated_at"] == now.isoformat()
        assert data["session_id"] == "sess-123"
        assert data["enforcement_level"] == "guidance"
        assert data["auto_activated"] is True
        assert data["disabled_by_user"] is False

    def test_from_dict(self):
        """Test creation from dict."""
        now = datetime.now(timezone.utc)
        data = {
            "enabled": True,
            "activated_at": now.isoformat(),
            "session_id": "sess-456",
            "enforcement_level": "strict",
            "auto_activated": False,
            "disabled_by_user": True,
        }

        mode = OrchestratorMode.from_dict(data)
        assert mode.enabled is True
        assert mode.activated_at is not None
        assert mode.session_id == "sess-456"
        assert mode.enforcement_level == "strict"
        assert mode.auto_activated is False
        assert mode.disabled_by_user is True

    def test_from_dict_missing_fields(self):
        """Test from_dict handles missing fields with defaults."""
        mode = OrchestratorMode.from_dict({})
        assert mode.enabled is False
        assert mode.activated_at is None
        assert mode.session_id is None
        assert mode.enforcement_level == "strict"
        assert mode.auto_activated is False
        assert mode.disabled_by_user is False

    def test_roundtrip(self):
        """Test to_dict -> from_dict roundtrip."""
        original = OrchestratorMode(
            enabled=True,
            activated_at=datetime.now(timezone.utc),
            session_id="sess-789",
            enforcement_level="guidance",
            auto_activated=True,
            disabled_by_user=False,
        )

        data = original.to_dict()
        restored = OrchestratorMode.from_dict(data)

        assert restored.enabled == original.enabled
        assert restored.session_id == original.session_id
        assert restored.enforcement_level == original.enforcement_level
        assert restored.auto_activated == original.auto_activated
        assert restored.disabled_by_user == original.disabled_by_user


class TestOrchestratorModeManager:
    """Test OrchestratorModeManager."""

    def test_init_with_path(self, tmp_path):
        """Test initialization with explicit path."""
        graph_dir = tmp_path / ".htmlgraph"
        manager = OrchestratorModeManager(graph_dir)
        assert manager.graph_dir == graph_dir
        assert manager.state_file == graph_dir / "orchestrator-mode.json"

    def test_init_creates_default_path(self, tmp_path, monkeypatch):
        """Test initialization with default path (cwd)."""
        monkeypatch.chdir(tmp_path)
        manager = OrchestratorModeManager()
        assert manager.graph_dir == tmp_path / ".htmlgraph"

    def test_load_nonexistent(self, tmp_path):
        """Test loading when file doesn't exist returns default."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        mode = manager.load()
        assert mode.enabled is False

    def test_save_and_load(self, tmp_path):
        """Test save and load roundtrip."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        mode = OrchestratorMode(
            enabled=True,
            activated_at=datetime.now(timezone.utc),
            session_id="sess-abc",
            enforcement_level="strict",
        )

        manager.save(mode)
        assert manager.state_file.exists()

        loaded = manager.load()
        assert loaded.enabled is True
        assert loaded.session_id == "sess-abc"
        assert loaded.enforcement_level == "strict"

    def test_save_creates_directory(self, tmp_path):
        """Test save creates directory if it doesn't exist."""
        graph_dir = tmp_path / ".htmlgraph"
        assert not graph_dir.exists()

        manager = OrchestratorModeManager(graph_dir)
        manager.save(OrchestratorMode())

        assert graph_dir.exists()
        assert manager.state_file.exists()

    def test_is_enabled(self, tmp_path):
        """Test is_enabled check."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        assert manager.is_enabled() is False

        manager.enable()
        assert manager.is_enabled() is True

        manager.disable()
        assert manager.is_enabled() is False

    def test_get_enforcement_level(self, tmp_path):
        """Test get_enforcement_level."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        assert manager.get_enforcement_level() == "strict"  # default

        manager.enable(level="guidance")
        assert manager.get_enforcement_level() == "guidance"

    def test_enable_defaults(self, tmp_path):
        """Test enable with default parameters."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        mode = manager.enable()

        assert mode.enabled is True
        assert mode.enforcement_level == "strict"
        assert mode.auto_activated is False
        assert mode.disabled_by_user is False

    def test_enable_with_params(self, tmp_path):
        """Test enable with custom parameters."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        mode = manager.enable(session_id="sess-xyz", level="guidance", auto=True)

        assert mode.enabled is True
        assert mode.session_id == "sess-xyz"
        assert mode.enforcement_level == "guidance"
        assert mode.auto_activated is True
        assert mode.activated_at is not None

    def test_disable(self, tmp_path):
        """Test disable."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        assert manager.is_enabled() is True

        mode = manager.disable()
        assert mode.enabled is False
        assert manager.is_enabled() is False

    def test_disable_by_user_sets_flag(self, tmp_path):
        """Test disable by user sets disabled_by_user flag."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()

        mode = manager.disable(by_user=True)
        assert mode.disabled_by_user is True

        # Load and verify persistence
        loaded = manager.load()
        assert loaded.disabled_by_user is True

    def test_set_level(self, tmp_path):
        """Test set_level changes enforcement level."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable(level="strict")

        mode = manager.set_level("guidance")
        assert mode.enforcement_level == "guidance"
        assert mode.enabled is True  # Doesn't disable

    def test_can_auto_activate_allowed(self, tmp_path):
        """Test can_auto_activate when allowed."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        assert manager.can_auto_activate() is True

        manager.enable(auto=True)
        assert manager.can_auto_activate() is True

    def test_can_auto_activate_blocked_by_user_disable(self, tmp_path):
        """Test can_auto_activate blocked after user disables."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True)

        assert manager.can_auto_activate() is False

    def test_status(self, tmp_path):
        """Test status returns readable info."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        status = manager.status()

        assert status["enabled"] is False
        assert status["enforcement_level"] == "strict"
        assert status["activated_at"] is None
        assert status["auto_activated"] is False
        assert status["disabled_by_user"] is False

        manager.enable(session_id="sess-123", level="guidance", auto=True)
        status = manager.status()

        assert status["enabled"] is True
        assert status["enforcement_level"] == "guidance"
        assert status["activated_at"] is not None
        assert status["auto_activated"] is True

    def test_load_corrupted_file_returns_default(self, tmp_path):
        """Test loading corrupted file returns default state."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.graph_dir.mkdir(parents=True, exist_ok=True)

        # Write corrupted JSON
        manager.state_file.write_text("{ invalid json }")

        mode = manager.load()
        assert mode.enabled is False  # Default state

    def test_multiple_enable_updates_timestamp(self, tmp_path):
        """Test multiple enables update timestamp."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")

        mode1 = manager.enable()
        time1 = mode1.activated_at

        import time

        time.sleep(0.1)

        mode2 = manager.enable()
        time2 = mode2.activated_at

        assert time2 > time1

    def test_enable_clears_disabled_by_user_flag(self, tmp_path):
        """Test enable clears disabled_by_user flag."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True)

        assert manager.load().disabled_by_user is True

        manager.enable()
        assert manager.load().disabled_by_user is False

    def test_disable_records_disabled_at(self, tmp_path):
        """Test disable records disabled_at timestamp."""
        import time as _time

        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()

        before = _time.time()
        manager.disable(by_user=True)
        after = _time.time()

        mode = manager.load()
        assert mode.disabled_at is not None
        assert before <= mode.disabled_at <= after

    def test_disable_default_scope(self, tmp_path):
        """Test disable with no options uses session scope."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True)

        mode = manager.load()
        assert mode.disable_scope == "session"
        assert mode.auto_re_enable_after_minutes == 10
        assert mode.disable_prompt_count == 0

    def test_disable_task_scope(self, tmp_path):
        """Test disable with task scope."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True, scope="task")

        mode = manager.load()
        assert mode.disable_scope == "task"

    def test_disable_timed_scope(self, tmp_path):
        """Test disable with timed scope and custom minutes."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True, scope="timed", auto_re_enable_after_minutes=5)

        mode = manager.load()
        assert mode.disable_scope == "timed"
        assert mode.auto_re_enable_after_minutes == 5

    def test_enable_clears_disable_tracking_fields(self, tmp_path):
        """Test enable clears disabled_at and related fields."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True, scope="timed", auto_re_enable_after_minutes=5)

        manager.enable()
        mode = manager.load()
        assert mode.disabled_at is None
        assert mode.disable_scope == "session"
        assert mode.disable_prompt_count == 0

    def test_increment_prompt_count_when_disabled(self, tmp_path):
        """Test increment_prompt_count increments when disabled by user."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True)

        manager.increment_prompt_count()
        manager.increment_prompt_count()

        mode = manager.load()
        assert mode.disable_prompt_count == 2

    def test_increment_prompt_count_no_op_when_enabled(self, tmp_path):
        """Test increment_prompt_count is no-op when orchestrator is enabled."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()

        manager.increment_prompt_count()

        mode = manager.load()
        assert mode.disable_prompt_count == 0

    def test_check_auto_re_enable_already_enabled(self, tmp_path):
        """Test check_auto_re_enable returns False when already enabled."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()

        re_enabled, reason = manager.check_auto_re_enable()
        assert re_enabled is False
        assert reason == "already_enabled"

    def test_check_auto_re_enable_task_scope(self, tmp_path):
        """Test task scope re-enables after 1 prompt."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True, scope="task")

        # Not yet — prompt count is 0
        re_enabled, reason = manager.check_auto_re_enable()
        assert re_enabled is False
        assert reason == "still_disabled"

        # Increment to 1
        manager.increment_prompt_count()
        re_enabled, reason = manager.check_auto_re_enable()
        assert re_enabled is True
        assert reason == "task_scope_complete"
        assert manager.load().enabled is True

    def test_check_auto_re_enable_timeout(self, tmp_path):
        """Test timed scope re-enables after timeout."""
        import time as _time

        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True, scope="timed", auto_re_enable_after_minutes=0)

        # Simulate time passing by setting disabled_at to past
        mode = manager.load()
        mode.disabled_at = _time.time() - 61  # 61 seconds ago (>0 minutes)
        manager.save(mode)

        re_enabled, reason = manager.check_auto_re_enable()
        assert re_enabled is True
        assert "timeout" in reason
        assert manager.load().enabled is True

    def test_check_auto_re_enable_still_disabled_within_timeout(self, tmp_path):
        """Test timed scope does not re-enable before timeout."""
        manager = OrchestratorModeManager(tmp_path / ".htmlgraph")
        manager.enable()
        manager.disable(by_user=True, scope="timed", auto_re_enable_after_minutes=60)

        re_enabled, reason = manager.check_auto_re_enable()
        assert re_enabled is False
        assert reason == "still_disabled"

    def test_from_dict_backward_compat_missing_new_fields(self):
        """Test from_dict deserializes old JSON files without new fields."""
        # Old JSON without new disable-tracking fields
        old_data = {
            "enabled": False,
            "disabled_by_user": True,
            "violations": 0,
            "circuit_breaker_triggered": False,
            "violation_history": [],
        }
        mode = OrchestratorMode.from_dict(old_data)
        assert mode.disabled_at is None
        assert mode.disable_scope == "session"
        assert mode.disable_prompt_count == 0
        assert mode.auto_re_enable_after_minutes == 10
