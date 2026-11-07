"""Audit-chain vectors: append, export, reopen, and tamper detection."""

from __future__ import annotations

import io
import json
from pathlib import Path

import pytest
from arne import audit

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "audit.json"


def load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def entry(raw: dict) -> audit.Entry:
    return audit.Entry(**raw)


def record(raw: dict) -> audit.Record:
    return audit.Record(**raw)


def test_append_vectors() -> None:
    doc = load()
    assert audit.GENESIS == doc["genesis"]
    log = audit.Log(now=iter([1001, 1002, 1003]).__next__)
    assert log.head == doc["genesis"]
    for index, raw in enumerate(doc["inputs"]):
        stored = log.append(entry(raw))
        assert stored == record(doc["records"][index]), index
    assert log.head == doc["head"]
    assert log.records() == [record(raw) for raw in doc["records"]]


def test_export_and_open() -> None:
    doc = load()
    log = audit.Log(now=iter([1001, 1002, 1003]).__next__)
    for raw in doc["inputs"]:
        log.append(entry(raw))
    exported = io.StringIO()
    log.export(exported)
    assert exported.getvalue().count("\n") == len(doc["records"])

    reopened = audit.open(io.StringIO(exported.getvalue()), now=lambda: 2000)
    assert reopened.head == doc["head"]
    assert reopened.records() == [record(raw) for raw in doc["records"]]
    stored = reopened.append(
        audit.Entry(actor="dave", action="settings.manage", target="", result="allow", detail="")
    )
    assert (stored.seq, stored.prev, stored.time) == (4, doc["head"], 2000)


def test_tamper_detection() -> None:
    doc = load()
    for case in doc["tampered"]:
        if "jsonl_edit" in case:
            log = audit.Log(now=iter([1001, 1002, 1003]).__next__)
            for raw in doc["inputs"]:
                log.append(entry(raw))
            exported = io.StringIO()
            log.export(exported)
            edited = exported.getvalue().replace(case["jsonl_edit"][0], case["jsonl_edit"][1], 1)
            with pytest.raises(audit.AuditError) as caught:
                audit.open(io.StringIO(edited), now=lambda: 0)
            assert caught.value.code == case["error"], case["name"]
            continue
        with pytest.raises(audit.AuditError) as caught:
            audit.verify([record(raw) for raw in case["records"]])
        assert caught.value.code == case["error"], case["name"]


def test_invalid_entries() -> None:
    cases = [
        audit.Entry(actor="", action="payment.write", target="", result="allow", detail=""),
        audit.Entry(actor="alice", action="", target="", result="allow", detail=""),
        audit.Entry(actor="alice", action="payment.write", target="", result="", detail=""),
        audit.Entry(actor="alice\n", action="payment.write", target="", result="allow", detail=""),
        audit.Entry(actor="alice", action="payment.write", target="", result="allow", detail="a" * (audit.MAX_DETAIL + 1)),
    ]
    for case in cases:
        log = audit.Log(now=lambda: 1)
        with pytest.raises(audit.AuditError) as caught:
            log.append(case)
        assert caught.value.code == "invalid"


def test_truncation_is_only_detectable_against_an_anchor() -> None:
    doc = load()
    log = audit.Log(now=iter([1001, 1002, 1003]).__next__)
    for raw in doc["inputs"]:
        log.append(entry(raw))
    exported = io.StringIO()
    log.export(exported)
    truncated = io.StringIO("".join(exported.getvalue().splitlines(keepends=True)[:2]))
    short = audit.open(truncated, now=lambda: 0)
    audit.verify(short.records())
    assert short.head != doc["head"]
