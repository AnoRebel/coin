"""Fee-market conformance vectors: congestion, split, priority, sponsorship."""

from __future__ import annotations

import json
from pathlib import Path

from arne import economics

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "fee.json"


def _code(exc: BaseException) -> str:
    return exc.code if isinstance(exc, economics.EconomicsError) else "other"


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    market = doc["fee_market"]
    for case in market["cases"]:
        fee_market = economics.FeeMarket(
            case.get("base") or market["base"],
            market["min"],
            market["max"],
            market["target"],
            market["max_change_bps"],
        )
        assert fee_market.next(case["used"]) == case["next"], case["name"]

    for case in doc["fee"]:
        try:
            fee = economics.effective_fee(
                case["base"], case["priority"], case["max_priority"], case["min"]
            )
        except economics.EconomicsError as exc:
            assert _code(exc) == case.get("error"), case["name"]
            continue
        assert case.get("error") is None, case["name"]
        assert fee == case["fee"], case["name"]

    for case in doc["split"]:
        try:
            split = economics.split_fee(
                case["fee"], case["burn_bps"], case["validator_bps"]
            )
        except economics.EconomicsError as exc:
            assert _code(exc) == case.get("error"), case["name"]
            continue
        assert case.get("error") is None, case["name"]
        assert (split.burn, split.validators, split.treasury) == (
            case["burn"],
            case["validators"],
            case["treasury"],
        ), case["name"]

    for case in doc["sponsorship"]:
        try:
            sponsorship = economics.Sponsorship(
                case["sponsor"],
                case["sponsored"],
                case["asset"],
                case["budget"],
                case["expires_at"],
            )
        except economics.EconomicsError as exc:
            assert _code(exc) == case.get("error"), case["name"]
            continue
        assert case.get("error") is None, case["name"]
        for step in case["steps"]:
            error = None
            try:
                if step["op"] == "pay":
                    sponsorship.pay(step["amount"], step["at"])
                elif step["op"] == "remaining":
                    assert sponsorship.remaining(step["at"]) == step["expect"], case["name"]
                else:
                    raise AssertionError(f"unknown op {step['op']}")
            except economics.EconomicsError as exc:
                error = exc
            if step.get("error"):
                assert error is not None and _code(error) == step["error"], step
            else:
                assert error is None, step
        assert sponsorship.spent == case["expect"]["spent"], case["name"]
