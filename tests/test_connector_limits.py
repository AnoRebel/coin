"""Connector exposure caps, rate windows, releases, and governed changes."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import connector_limits as limits

VECTORS = (
    Path(__file__).resolve().parent.parent / "spec" / "vectors" / "connector_limits.json"
)


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _policy(raw: dict) -> limits.Policy:
    return limits.Policy(**raw)


def _run(book: limits.Book, case: dict) -> dict:
    kind = case["kind"]
    if kind == "govern":
        change = book.govern(
            _policy(case["policy"]), case["actor"], case["reason"], case["now"]
        )
        return {
            "asset": change.asset,
            "actor": change.actor,
            "reason": change.reason,
            "at": change.at,
            "has_prior": change.prior is not None,
            "prior_exposure": change.prior.max_exposure if change.prior else 0,
            "next_exposure": change.next.max_exposure,
        }
    if kind == "submit":
        reservation = book.submit(
            case["asset"], case["counterparty"], case["amount"], case["now"]
        )
        return {
            "amount": reservation.amount,
            "exposure": book.exposure(case["asset"]),
            "counterparty_exposure": book.held_for(case["asset"], case["counterparty"]),
        }
    if kind == "release":
        released = book.release(case["id"])
        return {"released": released.released, "amount": released.amount}
    if kind == "exposure":
        return {"exposure": book.exposure(case["asset"])}
    if kind == "counterparty_exposure":
        return {"counterparty_exposure": book.held_for(case["asset"], case["counterparty"])}
    if kind == "history":
        return {
            "changes": [
                {
                    "actor": change.actor,
                    "reason": change.reason,
                    "at": change.at,
                    "has_prior": change.prior is not None,
                    "prior_exposure": change.prior.max_exposure if change.prior else 0,
                    "next_exposure": change.next.max_exposure,
                }
                for change in book.history()
            ]
        }
    raise AssertionError(kind)


def test_shared_vectors() -> None:
    doc = _load()
    book = limits.Book()

    for case in doc["cases"]:
        expect = case["expect"]
        if expect.get("error"):
            with pytest.raises(limits.LimitsError, match=expect["error"]):
                _run(book, case)
            continue
        got = _run(book, case)
        for key, value in expect.items():
            if key == "ordinal":
                continue
            assert got.get(key) == value, (case["name"], key, got.get(key), value)


def test_rejected_submission_consumes_no_budget() -> None:
    book = limits.Book()
    policy = limits.Policy(
        asset="arce:testnet/arce", max_exposure=1000, max_counterparty_exposure=1000,
        max_transfer=100, rate_count=1, rate_window=10,
    )
    book.govern(policy, "ops", "initial", 0)
    book.submit(policy.asset, "peer", 100, 100)

    with pytest.raises(limits.LimitsError, match="rate limit exceeded"):
        book.submit(policy.asset, "peer", 100, 105)
    assert book.exposure(policy.asset) == 100

    assert book.submit(policy.asset, "peer", 100, 110).amount == 100


def test_lowered_cap_only_stops_new_reservations() -> None:
    book = limits.Book()
    policy = limits.Policy(
        asset="arce:testnet/arce", max_exposure=1000, max_counterparty_exposure=1000,
        max_transfer=1000, rate_count=10, rate_window=60,
    )
    book.govern(policy, "ops", "initial", 0)
    reservation = book.submit(policy.asset, "peer", 800, 0)

    lower = limits.Policy(
        asset=policy.asset, max_exposure=500, max_counterparty_exposure=500,
        max_transfer=500, rate_count=10, rate_window=60,
    )
    book.govern(lower, "risk", "review", 1)

    with pytest.raises(limits.LimitsError, match="exposure cap reached"):
        book.submit(policy.asset, "other", 100, 2)
    assert book.release(reservation.id).released
    assert book.exposure(policy.asset) == 0
