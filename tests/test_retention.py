"""Retention, legal hold, anonymization, and tombstone integrity."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import retention

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "retention.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _policies(doc: dict) -> list[retention.Policy]:
    return [
        retention.Policy(
            cls=raw["class"], days=raw["days"], action=raw["action"],
            anonymize_fields=list(raw.get("anonymize_fields", [])),
        )
        for raw in doc["policies"]
    ]


def _records(doc: dict) -> list[retention.Record]:
    return [
        retention.Record(
            id=raw["id"], cls=raw["class"], created_at=raw["created_at"],
            on_chain=raw.get("on_chain", False), ledger_ref=raw.get("ledger_ref", ""),
            fields=dict(raw.get("fields", {})),
        )
        for raw in doc["records"]
    ]


def test_shared_vectors() -> None:
    doc = _load()
    manager = retention.Manager(_policies(doc))
    for record in _records(doc):
        manager.add(record)
    for raw in doc["holds"]:
        manager.hold(retention.Hold(
            record=raw["record"], reason=raw["reason"],
            placed_by=raw["placed_by"], placed_at=raw["placed_at"],
        ))

    for case in doc.get("hold_id_cases", []):
        hold = retention.Hold(record=case["record"], reason=case["reason"],
                              placed_by=case["placed_by"], placed_at=case["placed_at"])
        assert retention._hold_id(hold) == case["id"]

    result = manager.apply(doc["now"])
    expect = doc["expect"]
    assert result.deleted == expect["deleted"]
    assert result.anonymized == expect["anonymized"]
    assert result.retained == expect["retained"]
    assert result.held == expect["held"]

    assert len(result.tombstones) == 1
    tombstone = result.tombstones[0]
    want = expect["tombstone"]
    assert tombstone.record == want["record"]
    assert tombstone.cls == want["class"]
    assert tombstone.ledger_ref == want["ledger_ref"]
    assert tombstone.action == want["action"]
    assert tombstone.deleted_at == want["deleted_at"]
    assert tombstone.content_hash == want["content_hash"]
    assert manager.record(want["record"]) is None

    anonymized = manager.record("r-old-support")
    assert anonymized is not None
    for name, value in expect["anonymized_fields"].items():
        assert anonymized.fields[name] == value
    assert anonymized.ledger_ref == "tx-support"

    by_record = {event["record"]: event for event in manager.events()}
    for record_id, want_event in expect["events"].items():
        if record_id == "held":
            event = by_record["r-held"]
        elif record_id == "no_policy":
            event = by_record["r-no-policy"]
        elif record_id == "within":
            event = by_record["r-new-kyc"]
        else:
            event = by_record[{"delete": "r-old-kyc", "anonymize": "r-old-support"}[record_id]]
        assert event["action"] == want_event["action"], record_id
        assert event["result"] == want_event["result"], record_id


def test_release_then_delete() -> None:
    manager = retention.Manager([retention.Policy(cls="logs", days=1, action=retention.DELETE)])
    manager.add(retention.Record(id="r1", cls="logs", created_at=1000))
    hold = manager.hold(retention.Hold(record="r1", reason="case", placed_by="legal", placed_at=2000))
    assert manager.apply(1_000_000).held == ["r1"]
    manager.release(hold.id, "legal", 1_000_001)
    assert manager.apply(1_000_002).deleted == ["r1"]
    assert any(event["action"] == "release" for event in manager.events())


def test_validation() -> None:
    bad = [
        [retention.Policy(cls="", days=1, action=retention.DELETE)],
        [retention.Policy(cls="a", days=0, action=retention.DELETE)],
        [retention.Policy(cls="a", days=1, action="shred")],
        [retention.Policy(cls="a", days=1, action=retention.ANONYMIZE)],
        [retention.Policy(cls="a", days=1, action=retention.DELETE, anonymize_fields=["x"])],
        [
            retention.Policy(cls="a", days=1, action=retention.DELETE),
            retention.Policy(cls="a", days=2, action=retention.DELETE),
        ],
    ]
    for policies in bad:
        with pytest.raises(retention.InvalidPolicy):
            retention.Manager(policies)

    manager = retention.Manager([retention.Policy(cls="a", days=1, action=retention.DELETE)])
    with pytest.raises(retention.OnChainRecord):
        manager.add(retention.Record(id="r", cls="a", created_at=1, on_chain=True))
    with pytest.raises(retention.InvalidPolicy):
        manager.add(retention.Record(id="", cls="a", created_at=1))
    manager.add(retention.Record(id="r", cls="a", created_at=1))
    with pytest.raises(retention.AlreadyExists):
        manager.add(retention.Record(id="r", cls="a", created_at=1))
    with pytest.raises(retention.UnknownRecord):
        manager.hold(retention.Hold(record="missing", reason="x", placed_by="y", placed_at=1))
    with pytest.raises(retention.UnknownRecord):
        manager.release("missing", "y", 1)
