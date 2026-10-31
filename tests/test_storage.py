"""On-disk schema versioning and migrations for the Python FileStore."""

from __future__ import annotations

import json

import pytest
from arne import ledger, node


def _store(tmp_path) -> node.FileStore:
    return node.FileStore(tmp_path / "data")


def test_fresh_store_migrates_to_current_and_is_idempotent(tmp_path) -> None:
    store = _store(tmp_path)
    assert store.schema_version() == node.SCHEMA_VERSION
    store.save_block(
        ledger.Block(height=1, time=1, proposer="", prev_root="", state_root="", tx_root="")
    )
    store.close()

    reopened = _store(tmp_path)
    assert reopened.schema_version() == node.SCHEMA_VERSION
    assert reopened.block(1) is not None


def test_status_read_does_not_migrate(tmp_path) -> None:
    data = tmp_path / "data"
    (data / "blocks").mkdir(parents=True)
    assert node.read_schema_version(data) == 0
    store = node.FileStore(data, auto_migrate=False)
    assert store.schema_version() == 0
    store.migrate(1)
    assert node.read_schema_version(data) == node.SCHEMA_VERSION


def test_rollback_to_zero_and_reopen_remigrates(tmp_path) -> None:
    store = _store(tmp_path)
    store.migrate(0)
    assert store.schema_version() == 0
    store.migrate(0)  # second rollback is a no-op
    opened = _store(tmp_path)
    assert opened.schema_version() == node.SCHEMA_VERSION


def test_newer_schema_is_refused(tmp_path) -> None:
    data = tmp_path / "data"
    (data / "blocks").mkdir(parents=True)
    (data / "schema.json").write_text(json.dumps({"version": node.SCHEMA_VERSION + 1}))
    with pytest.raises(ledger.InvariantError):
        node.FileStore(data)


def test_unsupported_target_is_refused(tmp_path) -> None:
    store = _store(tmp_path)
    with pytest.raises(ValueError):
        store.migrate(node.SCHEMA_VERSION + 1)


def test_failing_step_leaves_version_unchanged(tmp_path, monkeypatch) -> None:
    store = _store(tmp_path)
    store.migrate(0)
    monkeypatch.setitem(
        node._MIGRATIONS,
        1,
        ("boom", lambda: (_ for _ in ()).throw(RuntimeError("boom")), lambda: None),
    )
    with pytest.raises(RuntimeError):
        store.migrate(1)
    assert store.schema_version() == 0


def test_missing_step_is_refused(tmp_path, monkeypatch) -> None:
    store = _store(tmp_path)
    store.migrate(0)
    monkeypatch.delitem(node._MIGRATIONS, 1)
    with pytest.raises(ValueError):
        store.migrate(1)
    assert store.schema_version() == 0
