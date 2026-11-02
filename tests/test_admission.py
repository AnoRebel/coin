"""Public and consortium validator admission vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import admission, staking

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _keys() -> dict[str, tuple[str, str]]:
    doc = json.loads((VECTORS / "bls.json").read_text())
    return {v["id"]: (v["public_key"], v["pop"]) for v in doc["validators"]}


def _policy(raw: dict) -> admission.Policy:
    return admission.Policy(
        mode=raw["mode"], approvers=tuple(raw.get("approvers", [])),
        threshold=raw.get("threshold", 0),
    )


def _register(state: staking.State, key: str, pop: str, address: str, at: int) -> None:
    staking.register(state, staking.Validator(
        address=address, consensus_key=key, pop=pop,
        commission_bps=100, self_bond=1000,
    ), at)


def test_shared_vectors() -> None:
    doc = json.loads((VECTORS / "admission.json").read_text())
    for case in doc["cases"]:
        state = admission.new(_policy(case["policy"]))
        for index, step in enumerate(case["steps"]):
            error = None
            try:
                if step["op"] == "allowed":
                    got = admission.allowed(state, step["validator"])
                    if "allowed" in step["expect"]:
                        assert got == step["expect"]["allowed"]
                else:
                    admission.approve(state, step["validator"], step["approver"], step["at"])
            except admission.AdmissionError as exc:
                error = exc
            expected = step["expect"]
            if "error" in expected:
                assert error is not None, (case["name"], index, step)
                assert error.code == expected["error"], (case["name"], index, error)
                continue
            assert error is None, (case["name"], index, error)
            if "status" in expected:
                assert admission.status(state, step["validator"]) == expected["status"]
            if "approvers" in expected:
                record = state.approvals[step["validator"]]
                assert record.approvers == expected["approvers"]
                assert record.height == expected["height"]
            if step["op"] == "approve" and "allowed" in expected:
                assert admission.allowed(state, step["validator"]) == expected["allowed"]


def test_invalid_policies() -> None:
    for policy in (
        admission.Policy(mode="open"),
        admission.Policy(mode=admission.CONSORTIUM, threshold=1),
        admission.Policy(mode=admission.CONSORTIUM, approvers=("a1",), threshold=0),
        admission.Policy(mode=admission.CONSORTIUM, approvers=("a1", "a1"), threshold=1),
        admission.Policy(mode=admission.PUBLIC, approvers=("a1",)),
    ):
        with pytest.raises(admission.AdmissionError):
            admission.new(policy)


def test_consortium_admission_gates_epoch() -> None:
    keys = _keys()
    params = staking.Params(500, 1000, 0, 10, 20)
    state = staking.new("arce:devnet", params)
    _register(state, *keys["v1"], "v1", 1)
    _register(state, *keys["v2"], "v2", 2)

    adm = admission.new(admission.Policy(admission.CONSORTIUM, ("a1", "a2"), 2))
    staking.set_admission(state, admission.approved(adm))
    staking.advance_epoch(state, 10)
    assert state.validators["v1"].status == staking.PENDING
    assert state.validators["v2"].status == staking.PENDING

    admission.approve(adm, "v1", "a1", 11)
    admission.approve(adm, "v1", "a2", 12)
    staking.set_admission(state, admission.approved(adm))
    staking.advance_epoch(state, 20)
    assert state.validators["v1"].status == staking.ACTIVE
    assert state.validators["v2"].status == staking.PENDING


def test_public_admission_activates_without_approval() -> None:
    keys = _keys()
    params = staking.Params(500, 1000, 0, 10, 20)
    state = staking.new("arce:devnet", params)
    _register(state, *keys["v1"], "v1", 1)
    adm = admission.new(admission.Policy(admission.PUBLIC))
    assert admission.allowed(adm, "v1")
    staking.advance_epoch(state, 10)
    assert state.validators["v1"].status == staking.ACTIVE
