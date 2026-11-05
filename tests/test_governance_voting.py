"""Stake- and role-weighted voting: shared conformance vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import governance

VECTORS = (
    Path(__file__).resolve().parent.parent
    / "spec"
    / "vectors"
    / "governance-voting.json"
)


def _code(exc: BaseException | None) -> str:
    if exc is None:
        return ""
    if isinstance(exc, governance.GovernanceError):
        return exc.code
    return "other"


def _call(function, *args):
    try:
        function(*args)
        return None
    except governance.GovernanceError as exc:
        return exc


def test_shared_voting_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    params = governance.Params(**doc["params"])
    for case in doc["cases"]:
        power = governance.new_voting_power()
        power.max_voter_bps = case.get("max_voter_bps", 0)
        power.consortium = case.get("consortium", False)
        state = None
        proposal = None

        for step in case["steps"]:
            operation = step["op"]
            error = None
            if operation == "set_stake":
                error = _call(governance.set_stake, power, step["member"], step["amount"])
            elif operation == "set_delegation":
                error = _call(
                    governance.set_delegation,
                    power,
                    step["delegator"],
                    step["delegate"],
                )
            elif operation == "set_role":
                error = _call(
                    governance.set_role,
                    power,
                    step["member"],
                    step["role"],
                    step["weight"],
                )
            elif operation == "ballot":
                weight, role, delegators = governance.ballot(power, step["voter"])
                assert weight == step["weight"], step["voter"]
                assert role == step.get("role", ""), step["voter"]
                expected = step.get("delegators") or {}
                assert (delegators or {}) == expected, step["voter"]
            elif operation == "vote":
                if state is None:
                    state = governance.new(doc["network"], params)
                    proposal = governance.Proposal(
                        kind=governance.PARAMETER,
                        proposer="arce-devnet1proposer",
                        nonce=1,
                        actions=[
                            governance.Action(
                                "set_parameter", {"name": "x", "value": 1}
                            )
                        ],
                        deposit=100,
                        activation_height=500,
                    )
                    governance.submit(state, proposal, case["total_power"], 100)
                error = _call(
                    governance.cast_vote,
                    state,
                    proposal.id,
                    step["voter"],
                    step["option"],
                    power,
                    step["at"],
                )
            elif operation == "advance":
                assert state is not None
                governance.advance(state, step["at"])
            else:
                raise AssertionError(f"unknown op {operation}")

            if step.get("error"):
                assert _code(error) == step["error"], (step, error)
            else:
                assert error is None, (step, error)

        if state is None:
            continue
        got = governance.get(state, proposal.id)
        expect = case["expect"]
        if expect.get("status"):
            assert got.status == expect["status"]
        for name in ("yes", "no", "abstain"):
            assert getattr(got, name) == expect.get(name, 0), name


def test_unknown_role_has_no_weight() -> None:
    power = governance.new_voting_power()
    power.role_of["a"] = "guest"
    weight, role, delegators = governance.ballot(power, "a")
    assert (weight, role, delegators) == (0, "guest", None)


def test_casting_without_power_is_rejected() -> None:
    state = governance.new(
        "arne:devnet",
        governance.Params(
            min_deposit=100,
            deposit_period=0,
            voting_period=100,
            timelock=50,
            quorum_bps=5000,
            threshold_bps=5000,
            constitutional_threshold_bps=6667,
            constitutional_timelock=500,
            max_actions=8,
        ),
    )
    proposal = governance.Proposal(
        kind=governance.PARAMETER,
        proposer="proposer",
        nonce=1,
        actions=[governance.Action("set_parameter", {"name": "x", "value": 1})],
        deposit=100,
        activation_height=500,
    )
    governance.submit(state, proposal, 10, 100)
    with pytest.raises(governance.GovernanceError) as caught:
        governance.cast_vote(
            state, proposal.id, "ghost", governance.YES,
            governance.new_voting_power(), 110,
        )
    assert caught.value.code == "power"
