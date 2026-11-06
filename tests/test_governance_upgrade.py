"""Code-upgrade conformance vectors and lifecycle."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import governance

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "upgrade.json"

APPROVED = "aa" * 32
OTHER = "bb" * 32
ROLLBACK = "cc" * 32


def _params() -> governance.Params:
    return governance.Params(
        min_deposit=100, deposit_period=0, voting_period=100, timelock=50,
        quorum_bps=5000, threshold_bps=5000,
        constitutional_threshold_bps=6667, constitutional_timelock=500,
        max_actions=8,
    )


def _action(**overrides) -> governance.Action:
    params = {
        "target": "binary", "hash": APPROVED, "previous_hash": ROLLBACK,
        "migration": "export state and replay from height 900",
        "rollback": "run the previous release against the snapshot",
        "version": "v2.0.0",
    }
    params.update(overrides)
    return governance.Action("code_upgrade", params)


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["cases"]:
        action = governance.Action(case["action"]["type"], case["action"].get("params", {}))
        if case.get("error") == "hash":
            plan = governance.new_upgrade(action)
            with pytest.raises(governance.GovernanceError) as caught:
                governance.verify(plan, case["running"])
            assert caught.value.code == "hash", case["name"]
            continue
        if case.get("error"):
            with pytest.raises(governance.GovernanceError) as caught:
                governance.new_upgrade(action)
            assert caught.value.code == case["error"], case["name"]
            continue
        plan = governance.new_upgrade(action)
        governance.verify(plan, case["running"])
        for name, value in case.get("expect", {}).items():
            assert getattr(plan, name) == value, case["name"]


def _passed(state: governance.State) -> str:
    proposal = governance.Proposal(
        kind=governance.CODE_UPGRADE, proposer="arce-devnet1proposer", nonce=7,
        title="upgrade", actions=[_action()], deposit=100, activation_height=750,
    )
    governance.submit(state, proposal, 1000, 100)
    pid = governance.proposal_id("arne:devnet", proposal.proposer, 7)
    governance.vote(state, pid, "voter-a", governance.YES, 600, 110)
    governance.advance(state, 200)
    governance.queue(state, pid, 200)
    return pid


def test_matching_hash_records_rollback_metadata() -> None:
    state = governance.new("arne:devnet", _params())
    pid = _passed(state)
    upgrader = governance.Upgrader(lambda: APPROVED)
    registry = governance.Registry()
    upgrader.register(registry)
    governance.execute(state, pid, 250, registry)
    assert state.proposals[pid].status == governance.EXECUTED
    assert upgrader.applied is not None
    assert upgrader.applied.proposal == pid
    assert upgrader.applied.activation_height == 750
    assert upgrader.applied.previous_hash == ROLLBACK


def test_hash_mismatch_blocks_activation() -> None:
    state = governance.new("arne:devnet", _params())
    pid = _passed(state)
    upgrader = governance.Upgrader(lambda: OTHER)
    registry = governance.Registry()
    upgrader.register(registry)
    with pytest.raises(governance.GovernanceError) as caught:
        governance.execute(state, pid, 250, registry)
    assert caught.value.code == "hash"
    assert state.proposals[pid].status == governance.FAILED
    assert "hash" in state.proposals[pid].execution_error
    assert upgrader.applied is None


def test_upgrade_action_needs_upgrade_proposal() -> None:
    state = governance.new("arne:devnet", _params())
    proposal = governance.Proposal(
        kind=governance.PARAMETER, proposer="p", nonce=1,
        actions=[_action()], deposit=100, activation_height=1,
    )
    with pytest.raises(governance.GovernanceError) as caught:
        governance.submit(state, proposal, 1000, 100)
    assert caught.value.code == "invalid"
