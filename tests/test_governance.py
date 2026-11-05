"""Governance lifecycle conformance vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import governance

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "governance.json"


def _code(exc: BaseException) -> str:
    if isinstance(exc, governance.GovernanceError):
        return exc.code
    return "boom" if "boom" in str(exc) else "other"


def _registry(applied: dict) -> governance.Registry:
    registry = governance.Registry()

    def set_parameter(action: governance.Action, proposal: governance.Proposal):
        name = action.params["name"]
        applied[name] = action.params["value"]
        return lambda: applied.pop(name, None)

    def fail(action: governance.Action, proposal: governance.Proposal):
        raise RuntimeError("boom")

    registry.register("set_parameter", set_parameter)
    registry.register("fail", fail)
    return registry


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["cases"]:
        submit = case["submit"]
        state = governance.new(doc["network"], governance.Params(**doc["params"]))
        proposal = governance.Proposal(
            kind=submit["kind"], proposer=submit["proposer"], nonce=submit["nonce"],
            title=submit.get("title", ""),
            actions=[governance.Action(a["type"], a.get("params", {})) for a in submit["actions"]],
            deposit=submit["deposit"], activation_height=submit["activation_height"],
        )
        if case.get("submit_error"):
            with pytest.raises(governance.GovernanceError) as caught:
                governance.submit(state, proposal, submit["total_power"], submit["at"])
            assert caught.value.code == case["submit_error"]
            continue
        governance.submit(state, proposal, submit["total_power"], submit["at"])
        applied: dict = {}
        for step in case["steps"]:
            operation = step["op"]
            if operation == "advance":
                governance.advance(state, step["at"])
                error = None
            elif operation == "vote":
                error = _call(
                    governance.vote, state, proposal.id, step["voter"],
                    step["option"], step["weight"], step["at"],
                )
            elif operation == "queue":
                error = _call(governance.queue, state, proposal.id, step["at"])
            elif operation == "execute":
                error = _call(
                    governance.execute, state, proposal.id, step["at"], _registry(applied),
                )
            elif operation == "cancel":
                error = _call(
                    governance.cancel, state, proposal.id, step["actor"], step["at"],
                )
            else:
                raise AssertionError(f"unknown op {operation}")
            if step.get("error"):
                assert error is not None and _code(error) == step["error"], (step, error)
            else:
                assert error is None, (step, error)
        expect = case["expect"]
        got = governance.get(state, proposal.id)
        if expect.get("status"):
            assert got.status == expect["status"]
        for name in (
            "yes", "no", "abstain", "timelock_end", "activation_height",
            "voting_start", "voting_end",
        ):
            if expect.get(name):
                assert getattr(got, name) == expect[name], name
        assert got.refunded is expect.get("refunded", False)
        if expect.get("execution_error"):
            assert expect["execution_error"] in got.execution_error
        if "applied" in expect:
            assert applied == expect["applied"]


def _call(function, *args) -> BaseException | None:
    try:
        function(*args)
        return None
    except BaseException as exc:  # noqa: BLE001 - parity test inspects the error
        return exc
