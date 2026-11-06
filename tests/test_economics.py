"""Economics conformance vectors: supply, tail emission, genesis, vesting."""

from __future__ import annotations

import json
from pathlib import Path

from arne import economics

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "economics.json"


def _code(exc: BaseException) -> str:
    return exc.code if isinstance(exc, economics.EconomicsError) else "other"


def _schedule(raw: dict) -> economics.Schedule:
    return economics.Schedule(
        beneficiary=raw["beneficiary"], category=raw["category"],
        amount=raw["amount"], cliff=raw.get("cliff", 0), duration=raw.get("duration", 0),
    )


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["genesis"]:
        schedules = [_schedule(raw) for raw in case["schedules"]]
        if case.get("error"):
            try:
                economics.validate_genesis(case["cap"], schedules)
            except economics.EconomicsError as exc:
                assert exc.code == case["error"], case["name"]
            else:
                raise AssertionError(case["name"])
            continue
        assert economics.validate_genesis(case["cap"], schedules) == case["total"]

    for case in doc["supply"]:
        supply = economics.Supply(case["cap"])
        for step in case["steps"]:
            error = None
            try:
                if step["op"] == "mint":
                    supply.mint(step["amount"], step["purpose"], step["at"])
                elif step["op"] == "burn":
                    supply.burn(step["amount"], step["at"])
                elif step["op"] == "set_tail_rate":
                    supply.set_tail_rate(step["bps"])
                elif step["op"] == "tail":
                    supply.mint_tail(step["amount"], step["epoch"], step["at"])
                elif step["op"] == "tail_for":
                    supply.mint_tail_for(
                        step["amount"], step["epoch"], step["at"], step["purpose"]
                    )
                else:
                    raise AssertionError(step["op"])
            except economics.EconomicsError as exc:
                error = exc
            if step.get("error"):
                assert error is not None and _code(error) == step["error"], step
            else:
                assert error is None, step
        expect = case["expect"]
        assert supply.total == expect["total"]
        assert supply.minted == expect.get("minted", 0)
        assert supply.burned == expect.get("burned", 0)
        assert supply.tail == expect.get("tail", 0)
        if expect.get("entries"):
            assert len(supply.entries) == expect["entries"]

    for case in doc["vesting"]:
        vesting = economics.Vesting(_schedule(case["schedule"]))
        for step in case["steps"]:
            error = None
            try:
                if step["op"] == "unlocked":
                    assert vesting.unlocked(step["at"]) == step["expect"], step
                elif step["op"] == "transferable":
                    assert vesting.transferable(step["at"]) == step["expect"], step
                elif step["op"] == "transfer":
                    vesting.transfer(step["amount"], step["at"])
                elif step["op"] == "advance":
                    completed = vesting.advance(step["at"])
                    if "completed" in step:
                        assert completed == step["completed"], step
                    if "events" in step:
                        assert [
                            {"kind": e.kind, "at": e.at, "amount": e.amount}
                            for e in vesting.events
                        ] == step["events"], step
                else:
                    raise AssertionError(step["op"])
            except economics.EconomicsError as exc:
                error = exc
            if step.get("error"):
                assert error is not None and _code(error) == step["error"], step
            else:
                assert error is None, step
        assert vesting.transferred == case["expect"]["transferred"]
        assert vesting.completed == case["expect"]["completed"]
