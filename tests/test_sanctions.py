"""Sanctions screening: fail-closed decisions and shared vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import sanctions

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "sanctions.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _list_provider(doc: dict) -> sanctions.ListProvider:
    raw = doc["list"]
    return sanctions.ListProvider(sanctions.List(
        name=raw["name"], version=raw["version"], updated_at=raw["updated_at"],
        entries=[sanctions.Entry(**entry) for entry in raw["entries"]],
    ))


def test_shared_vectors() -> None:
    doc = _load()
    provider = _list_provider(doc)
    for case in doc["cases"]:
        if case.get("provider_error"):
            screener = sanctions.Screener(sanctions.UnavailableProvider("list:demo-ofac", "timeout"))
        else:
            screener = sanctions.Screener(provider)
        decision = screener.screen(sanctions.Subject(**case["subject"]))
        assert decision.outcome == case["outcome"], case["name"]
        assert decision.result == case["result"], case["name"]
        if case.get("provider_error"):
            assert decision.list_version == ""
            assert decision.requires_review is False
            assert "unavailable" in decision.reason
            continue
        assert decision.list_version == doc["list"]["version"]
        assert decision.provider == provider.name()
        assert decision.matches == case.get("matches", []), case["name"]
        assert decision.score == case.get("score", 0), case["name"]
        assert decision.requires_review == (case["outcome"] == sanctions.HOLD)

    for case in doc.get("id_cases", []):
        entry = sanctions.Entry(address=case["address"], name=case["name"])
        assert sanctions._entry_id(entry) == case["id"]

    for raw in doc["invalid_lists"]:
        with pytest.raises(sanctions.InvalidList):
            sanctions.ListProvider(sanctions.List(
                name=raw["name"], version=raw["version"], updated_at=0,
                entries=[sanctions.Entry(**entry) for entry in raw["entries"]],
            ))


def test_unknown_result_fails_closed() -> None:
    class Odd:
        def name(self) -> str:
            return "odd"

        def screen(self, subject: sanctions.Subject) -> sanctions.Match:
            return sanctions.Match(provider="odd", list_version="v1", result="maybe")

    decision = sanctions.Screener(Odd()).screen(sanctions.Subject())
    assert decision.outcome == sanctions.DENY
    assert decision.result == sanctions.UNAVAILABLE


def test_any_provider_error_fails_closed() -> None:
    class Broken:
        def name(self) -> str:
            return "broken"

        def screen(self, subject: sanctions.Subject) -> sanctions.Match:
            raise RuntimeError("boom")

    decision = sanctions.Screener(Broken()).screen(sanctions.Subject(address="x"))
    assert decision.outcome == sanctions.DENY
    assert "boom" in decision.reason


def test_entry_ids_are_stable() -> None:
    first = sanctions.ListProvider(sanctions.List(
        name="l", version="v1", updated_at=0,
        entries=[sanctions.Entry(address=" ABC "), sanctions.Entry(name="Jane   Doe")],
    ))
    again = sanctions.ListProvider(sanctions.List(
        name="l", version="v2", updated_at=0, entries=[sanctions.Entry(address="abc")],
    ))
    assert len(first._by_id) == 2
    assert all(len(entry_id) == 16 for entry_id in first._by_id)
    assert set(again._by_id) <= set(first._by_id)
