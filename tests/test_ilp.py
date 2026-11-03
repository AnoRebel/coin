"""ILPv4 and Open Payments: shared vectors and HTLC boundaries."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import ilp

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "ilp.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _escrow(doc: dict, condition: bytes) -> ilp.Escrow:
    raw = doc["escrow"]
    return ilp.Escrow(
        id=raw["id"], sender=raw["sender"], receiver=raw["receiver"],
        amount=ilp.Amount(**raw["amount"]), condition=condition,
        expires_at=raw["expires_at"],
    )


def _run(doc: dict, fulfillment: bytes, condition: bytes, case: dict) -> str:
    preimage = bytes.fromhex(case["preimage"]) if case.get("preimage") else fulfillment
    destination = case.get("destination") or doc["prepare"]["destination"]
    prepare = ilp.Prepare(
        amount=doc["prepare"]["amount"], expires_at=doc["prepare"]["expires_at"],
        condition=condition, destination=destination,
    )

    kind = case["kind"]
    if kind == "quote":
        ilp.Quote(
            id=doc["quote"]["id"], expires_at=doc["quote"]["expires_at"],
            debit=ilp.Amount(**doc["quote"]["debit"]), receive=ilp.Amount(**doc["quote"]["receive"]),
            fee=ilp.Amount(**doc["quote"]["fee"]),
        ).validate(case["now"])
        return ""
    if kind == "prepare":
        return prepare.fulfill(case["now"], preimage).hex()
    if kind == "grant":
        access = tuple(
            ilp.Access(a["type"], tuple(a["actions"])) for a in doc["grant"]["access"]
        )
        ilp.Grant(doc["grant"]["access_token"], access, doc["grant"]["expires_at"]).check(
            case["now"], case["check"][0], case["check"][1]
        )
        return ""
    if kind == "claim":
        return _escrow(doc, condition).claim(case["now"], preimage).status
    if kind == "claim_twice":
        claimed = _escrow(doc, condition).claim(case["now"], preimage)
        return claimed.claim(case["now"], preimage).status
    if kind == "refund":
        return _escrow(doc, condition).refund(case["now"]).status
    if kind == "refund_after_claim":
        claimed = _escrow(doc, condition).claim(doc["escrow"]["expires_at"] - 1, preimage)
        return claimed.refund(case["now"]).status
    if kind == "stream":
        stream = ilp.Stream(case.get("stream_id") or doc["escrow"]["id"])
        receipt_id = case.get("receipt_stream_id") or stream.id
        for receipt in case["receipts"]:
            stream.apply(
                ilp.Receipt(receipt_id, receipt["sequence"], receipt["total_received"])
            )
        return str(stream.total)
    if kind == "reject":
        ilp.new_reject(case["code"], case.get("triggered_by", ""), case.get("message", ""))
        return ""
    if kind == "address":
        ilp.validate_address(case["address"])
        return ""
    raise AssertionError(f"unknown kind {kind}")


def test_shared_vectors() -> None:
    doc = _load()
    fulfillment = bytes.fromhex(doc["fulfillment"])
    condition = bytes.fromhex(doc["condition"])
    assert ilp.condition(fulfillment) == condition

    for case in doc["cases"]:
        expect = case.get("expect", {})
        if expect.get("error"):
            with pytest.raises(ilp.ILPError, match=expect["error"]):
                _run(doc, fulfillment, condition, case)
            continue
        result = _run(doc, fulfillment, condition, case)
        if expect.get("fulfillment"):
            assert result == expect["fulfillment"], case["name"]
        if expect.get("status"):
            assert result == expect["status"], case["name"]
        if expect.get("total"):
            assert result == str(expect["total"]), case["name"]


def test_builder_helpers() -> None:
    doc = _load()
    fulfillment = bytes.fromhex(doc["fulfillment"])
    condition = bytes.fromhex(doc["condition"])
    raw = doc["escrow"]
    escrow = ilp.new_escrow(
        raw["id"], raw["sender"], raw["receiver"], ilp.Amount(**raw["amount"]),
        condition, raw["expires_at"],
    )
    claimed = escrow.claim(raw["expires_at"] - 1, fulfillment)
    assert claimed.status == ilp.CLAIMED
    with pytest.raises(ilp.ILPError, match="wrong state"):
        claimed.refund(raw["expires_at"])

    with pytest.raises(ilp.ILPError, match="sender and receiver must differ"):
        ilp.new_escrow("e", "a", "a", ilp.Amount("arce:testnet/arce", 1), condition, 10)
