"""Validator lifecycle, stake, delegation, and epoch conformance vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import staking

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _load() -> tuple[dict, dict]:
    bls_doc = json.loads((VECTORS / "bls.json").read_text())
    doc = json.loads((VECTORS / "staking.json").read_text())
    keys = {v["id"]: (v["public_key"], v["pop"]) for v in bls_doc["validators"]}
    return doc, keys


def _apply(state: staking.State, step: dict, keys: dict) -> None:
    op = step["op"]
    if op == "register":
        key, pop = keys[step.get("key_from", step["validator"])]
        staking.register(state, staking.Validator(
            address=step["validator"], consensus_key=key, pop=pop,
            commission_bps=step["commission_bps"], self_bond=step["self_bond"],
        ), step["at"])
    elif op == "delegate":
        staking.delegate(state, step["validator"], step["delegator"], step["amount"], step["at"])
    elif op == "undelegate":
        staking.undelegate(state, step["validator"], step["delegator"], step["amount"], step["at"])
    elif op == "begin_unbonding":
        staking.begin_unbonding(state, step["validator"], step["at"])
    elif op == "jail":
        staking.jail(state, step["validator"], step["at"], step["until"])
    elif op == "unjail":
        staking.unjail(state, step["validator"], step["at"])
    elif op == "advance_epoch":
        staking.advance_epoch(state, step["at"])
    else:
        raise AssertionError(f"unknown op {op}")


def _check(state: staking.State, expect: dict) -> None:
    snap = staking.snapshot(state)
    if "epoch" in expect:
        assert snap["epoch"] == expect["epoch"]
    if "height" in expect:
        assert snap["height"] == expect["height"]
    if "active" in expect:
        assert snap["active"] == expect["active"]
    views = {v["address"]: v for v in snap["validators"]}
    for address, wanted in expect.get("validators", {}).items():
        got = views[address]
        for field, value in wanted.items():
            assert got[field] == value, (address, field, got, wanted)
    if "unbonding" in expect:
        assert snap["unbonding"] == expect["unbonding"]


def test_shared_vectors() -> None:
    doc, keys = _load()
    assert doc["cases"]
    for case in doc["cases"]:
        state = staking.new(doc["network"], staking.Params(**case["params"]))
        for index, step in enumerate(case["steps"]):
            error = None
            try:
                _apply(state, step, keys)
            except staking.StakingError as exc:
                error = exc
            wanted = step.get("expect", {}).get("error")
            if wanted:
                assert error is not None, (case["name"], index, step)
                assert error.code == wanted, (case["name"], index, error)
                if "height" in step["expect"]:
                    assert state.height == step["expect"]["height"]
                continue
            if error is not None:
                raise AssertionError((case["name"], index, step, error)) from error
            _check(state, step.get("expect", {}))


def test_unbonded_stake_is_released() -> None:
    params = staking.Params(500, 1000, 0, 10, 20)
    state = staking.new("arce:devnet", params)
    doc, keys = _load()
    key, pop = keys["v1"]
    staking.register(state, staking.Validator("v1", key, pop, 100, 1000), 1)
    staking.advance_epoch(state, 10)
    staking.begin_unbonding(state, "v1", 15)
    staking.advance_epoch(state, 20)
    staking.advance_epoch(state, 30)
    staking.advance_epoch(state, 40)
    snap = staking.snapshot(state)
    assert snap["validators"][0]["status"] == staking.UNBONDED
    assert staking.stake(state, "v1") == 0


def test_unknown_operation_rejected() -> None:
    params = staking.Params(500, 1000, 0, 10, 20)
    state = staking.new("arce:devnet", params)
    with pytest.raises(AssertionError):
        _apply(state, {"op": "nope"}, {})
