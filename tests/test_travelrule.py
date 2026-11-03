"""Travel Rule collection and authenticated exchange, shared vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import keys, travelrule

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "travelrule.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _transfer(raw: dict) -> travelrule.Transfer:
    return travelrule.Transfer(
        network=raw["network"],
        asset=raw["asset"],
        amount=raw["amount"],
        reference=raw["reference"],
        originator=travelrule.Party(**raw["originator"]),
        beneficiary=travelrule.Party(**raw["beneficiary"]),
        originating_vasp=travelrule.VASP(**raw["originating_vasp"]),
        beneficiary_vasp=travelrule.VASP(**raw["beneficiary_vasp"]),
    )


class Recorder:
    def __init__(self, fail: bool = False) -> None:
        self.fail = fail
        self.sent: list[travelrule.Envelope] = []

    def send(self, envelope: travelrule.Envelope) -> None:
        if self.fail:
            raise RuntimeError("timeout")
        self.sent.append(envelope)


def test_shared_vectors() -> None:
    doc = _load()
    key = keys.keypair_from_private(bytes.fromhex(doc["seed"]), doc["network"])
    for case in doc["cases"]:
        transfer = _transfer(case["transfer"])
        expect = case["expect"]
        if expect.get("error"):
            with pytest.raises(travelrule.TravelRuleError, match=expect["error"]):
                travelrule.seal(transfer, key)
            continue

        transport = Recorder(fail=case["transport"] == "unavailable")
        decision, envelope = travelrule.exchange(
            travelrule.Policy(case["threshold"]), transfer, key, transport, at=42
        )
        assert decision.required is expect["required"], case["name"]
        assert decision.status == expect["status"], case["name"]
        assert decision.reason == expect["reason"], case["name"]
        assert decision.at == 42 and decision.threshold == case["threshold"], case["name"]

        if not expect.get("sender"):
            assert transport.sent == [], case["name"]
            continue
        assert envelope is not None
        assert envelope.sender == expect["sender"], case["name"]
        assert envelope.signature == expect["signature"], case["name"]
        assert len(transport.sent) == 1, case["name"]
        assert travelrule.open_envelope(envelope) == transfer, case["name"]


def test_open_rejects_tampering() -> None:
    doc = _load()
    key = keys.keypair_from_private(bytes.fromhex(doc["seed"]), doc["network"])
    case = doc["cases"][0]
    envelope = travelrule.seal(_transfer(case["transfer"]), key)

    flipped = bytes([bytes.fromhex(envelope.signature)[0] ^ 0xFF]) + bytes.fromhex(
        envelope.signature
    )[1:]
    with pytest.raises(travelrule.TravelRuleError, match="bad signature"):
        travelrule.open_envelope(
            travelrule.Envelope(envelope.version, envelope.sender, envelope.transfer, flipped.hex())
        )

    with pytest.raises(travelrule.TravelRuleError, match="version"):
        travelrule.open_envelope(
            travelrule.Envelope("ivms999", envelope.sender, envelope.transfer, envelope.signature)
        )
