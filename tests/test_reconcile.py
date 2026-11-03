"""Reconciliation: duplicate, missing, and conflicting confirmations with totals."""

from __future__ import annotations

import dataclasses
import json
from pathlib import Path

from arne import reconcile

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "reconcile.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def test_shared_vectors() -> None:
    doc = _load()
    ledger = reconcile.Ledger(reconcile.Policy(**doc["policy"]))

    for case in doc["cases"]:
        expect = case["expect"]
        kind = case["kind"]
        if kind == "submit":
            state, outcome = ledger.submit(reconcile.Operation(**case["operation"]))
            assert outcome == expect["outcome"], case["name"]
            if "status" in expect:
                assert state.status == expect["status"], case["name"]
            if expect.get("amount"):
                assert state.operation.amount == expect["amount"], case["name"]
            if expect.get("correlation"):
                assert state.operation.correlation == expect["correlation"], case["name"]
        elif kind == "confirm":
            state, outcome = ledger.confirm(
                reconcile.Confirmation(**case["confirmation"]), case.get("now", 0)
            )
            assert outcome == expect["outcome"], case["name"]
            if "status" in expect:
                assert state.status == expect["status"], case["name"]
            if expect.get("reference"):
                assert state.reference == expect["reference"], case["name"]
            if expect.get("detail_contains"):
                assert expect["detail_contains"] in state.conflict_detail, case["name"]
        elif kind == "reconcile":
            report = ledger.reconcile(case["now"])
            assert report.matched == expect["matched"], case["name"]
            assert report.unmatched == expect["unmatched"], case["name"]
            assert report.conflicting == expect["conflicting"], case["name"]
            assert [dataclasses.asdict(t) for t in report.totals] == expect["totals"], case["name"]
            assert [e.kind for e in report.exceptions] == expect["exceptions"], case["name"]
        elif kind == "state":
            state = ledger.state(case["correlation"])
            assert (state is not None) is expect["found"], case["name"]
            if state is not None:
                assert state.status == expect["status"], case["name"]
                assert state.operation.amount == expect["amount"], case["name"]
                assert state.reference == expect["reference"], case["name"]
        else:
            raise AssertionError(kind)


def test_policy_can_keep_an_operation_pending() -> None:
    ledger = reconcile.Ledger(reconcile.Policy(fail_on_timeout=False))
    operation = reconcile.Operation(
        key="k", correlation="c", asset="arce:testnet/arce",
        amount=10, created_at=100, timeout=10,
    )
    assert ledger.submit(operation)[1] == reconcile.ACCEPTED
    report = ledger.reconcile(200)
    assert (report.matched, report.unmatched, report.conflicting) == (0, 1, 0)
    assert report.totals[0].pending == 10
    assert ledger.state("c").status == reconcile.PENDING
    state, outcome = ledger.confirm(
        reconcile.Confirmation("c", "arce:testnet/arce", 10, "0xaa", 201), 201
    )
    assert outcome == reconcile.ACCEPTED
    assert state.status == reconcile.SETTLED
