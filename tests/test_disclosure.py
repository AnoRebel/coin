"""Grant-scoped disclosure: no universal path, scope, expiry, and logging."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import disclosure, memo

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "disclosure.json"


def seed(n: int) -> bytes:
    return bytes([n]) * 32


def sealed_for(recipient: str, plaintext: str) -> bytes:
    return memo.seal(
        memo.public_key(seed(ord(recipient[0]))),
        plaintext.encode(),
        ephemeral_seed=bytes([7]) * 32,
        nonce=bytes([7]) * 12,
    )


def user_grant(*scope: str) -> disclosure.Grant:
    return disclosure.Grant(
        kind=disclosure.USER, subject="alice", scope=list(scope),
        from_ts=100, until=200, purpose="invoice review",
        granted_by="alice", granted_at=100,
    )


def setup() -> disclosure.Manager:
    manager = disclosure.Manager()
    manager.add_memo("tx-a", sealed_for("alice", "memo a"))
    manager.add_memo("tx-b", sealed_for("alice", "memo b"))
    return manager


def test_no_universal_decryption_path() -> None:
    manager = setup()
    key = seed(ord("a"))

    for tx in ("tx-a", "tx-b"):
        with pytest.raises(disclosure.NoGrant):
            manager.open("operator", "", tx, key, 150)
        with pytest.raises(disclosure.NoGrant):
            manager.open("operator", "made-up", tx, key, 150)

    grant = manager.grant(user_grant("tx-a"))
    assert manager.open("alice", grant.id, "tx-a", key, 150) == b"memo a"
    with pytest.raises(disclosure.OutOfScope):
        manager.open("alice", grant.id, "tx-b", key, 150)
    with pytest.raises(disclosure.Forbidden):
        manager.open("mallory", grant.id, "tx-a", key, 150)
    with pytest.raises(disclosure.DecryptFailed):
        manager.open("alice", grant.id, "tx-a", seed(ord("z")), 150)

    denied = [event for event in manager.events() if event["result"] == "denied"]
    assert len(denied) == 7


def test_exceptional_access_is_recorded() -> None:
    manager = setup()
    grant = manager.grant(disclosure.Grant(
        kind=disclosure.EXCEPTIONAL, subject="analyst", scope=["tx-a", "tx-b"],
        from_ts=100, until=500, purpose="court order",
        approvers=["gc-1", "gc-2"], legal_process="order-2026-17",
        granted_by="gc-1", granted_at=100,
    ))
    manager.open("analyst", grant.id, "tx-a", seed(ord("a")), 150)

    granted, opened = manager.events()
    assert granted["result"] == "granted"
    assert granted["kind"] == disclosure.EXCEPTIONAL
    assert granted["scope"] == ["tx-a", "tx-b"]
    assert granted["approvers"] == ["gc-1", "gc-2"]
    assert granted["purpose"] == "court order"
    assert opened["result"] == "opened"
    assert opened["transfer"] == "tx-a"
    assert opened["actor"] == "analyst"
    assert opened["purpose"] == "court order"
    assert opened["approvers"] == ["gc-1", "gc-2"]


def test_window_and_validation() -> None:
    manager = setup()
    grant = manager.grant(user_grant("tx-a"))
    with pytest.raises(disclosure.NotActive):
        manager.open("alice", grant.id, "tx-a", seed(ord("a")), 99)
    with pytest.raises(disclosure.Expired):
        manager.open("alice", grant.id, "tx-a", seed(ord("a")), 200)
    with pytest.raises(disclosure.OutOfScope):
        manager.open("alice", grant.id, "tx-missing", seed(ord("a")), 150)

    missing = manager.grant(user_grant("tx-missing"))
    with pytest.raises(disclosure.NoMemo):
        manager.open("alice", missing.id, "tx-missing", seed(ord("a")), 150)

    bad = [
        disclosure.Grant(kind=disclosure.USER, subject="", scope=["t"], from_ts=1, until=2, purpose="p", granted_by="a", granted_at=1),
        disclosure.Grant(kind=disclosure.USER, subject="a", scope=[], from_ts=1, until=2, purpose="p", granted_by="a", granted_at=1),
        disclosure.Grant(kind=disclosure.USER, subject="a", scope=["t"], from_ts=0, until=2, purpose="p", granted_by="a", granted_at=1),
        disclosure.Grant(kind=disclosure.USER, subject="a", scope=["t"], from_ts=2, until=2, purpose="p", granted_by="a", granted_at=1),
        disclosure.Grant(kind=disclosure.USER, subject="a", scope=["t"], from_ts=1, until=2, purpose="", granted_by="a", granted_at=1),
        disclosure.Grant(kind=disclosure.USER, subject="a", scope=["t"], from_ts=1, until=2, purpose="p", granted_by="a", granted_at=0),
        disclosure.Grant(kind=disclosure.USER, subject="a", scope=["t"], from_ts=1, until=2, purpose="p", approvers=["x", "y"], granted_by="a", granted_at=1),
        disclosure.Grant(kind=disclosure.EXCEPTIONAL, subject="a", scope=["t"], from_ts=1, until=2, purpose="p", approvers=["x"], legal_process="l", granted_by="a", granted_at=1),
        disclosure.Grant(kind=disclosure.EXCEPTIONAL, subject="a", scope=["t"], from_ts=1, until=2, purpose="p", approvers=["x", "y"], granted_by="a", granted_at=1),
        disclosure.Grant(kind="other", subject="a", scope=["t"], from_ts=1, until=2, purpose="p", granted_by="a", granted_at=1),
    ]
    for grant in bad:
        with pytest.raises(disclosure.InvalidGrant):
            manager.grant(grant)

    with pytest.raises(disclosure.InvalidGrant):
        manager.add_memo("", sealed_for("alice", "x"))
    with pytest.raises(disclosure.InvalidGrant):
        manager.add_memo("tx-a", sealed_for("alice", "x"))
    with pytest.raises(disclosure.InvalidGrant):
        manager.add_memo("tx-c", b"not a memo")


def test_grant_ids_match_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["grant_id_cases"]:
        grant = disclosure.Grant(
            kind=case["kind"], subject=case["subject"], scope=list(case["scope"]),
            from_ts=case["from"], until=case["until"], purpose=case["purpose"],
            approvers=list(case["approvers"]), granted_by="x", granted_at=1,
        )
        assert disclosure._grant_id(grant) == case["id"]
