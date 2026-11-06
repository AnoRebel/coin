"""Auto-conversion reserve vectors: proof, coverage, and disablement."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import economics

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "reserve.json"


def _code(exc: BaseException) -> str:
    return exc.code if isinstance(exc, economics.EconomicsError) else "other"


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["invalid"]:
        with pytest.raises(economics.EconomicsError) as caught:
            economics.Reserve(case["asset"], case["rate"])
        assert caught.value.code == case["error"], case["name"]

    route = economics.Reserve(doc["asset"], doc["rate"])
    for step in doc["steps"]:
        error = None
        arce = 0
        try:
            if step["op"] == "set_reserves":
                route.set_reserves(step["amount"])
            elif step["op"] == "set_liabilities":
                route.set_liabilities(step["amount"])
            elif step["op"] == "recapitalize":
                route.recapitalize(step["amount"])
            elif step["op"] == "quote":
                arce = route.quote(step["asset_amount"], step["min_arce"])
            elif step["op"] == "verify":
                proof = route.commit(step["at"])
                if step.get("digest"):
                    proof.digest = step["digest"]
                proof.verify(step["observed_reserves"], step["observed_liabilities"])
            else:
                raise AssertionError(f"unknown op {step['op']}")
        except economics.EconomicsError as exc:
            error = exc
        if step.get("error"):
            assert error is not None and _code(error) == step["error"], step
            continue
        assert error is None, step
        expect = step.get("expect", {})
        if expect.get("arce"):
            assert arce == expect["arce"], step
        if expect.get("reserves"):
            assert route.reserves == expect["reserves"], step
        if "covered" in expect:
            assert route.covered is expect["covered"], step
        if "disabled" in expect:
            assert route.disabled is expect["disabled"], step
        if "spare" in expect:
            assert route.spare == expect["spare"], step
