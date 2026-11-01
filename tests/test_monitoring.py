"""Transaction monitoring: alerts, freeze policy, and dispositions."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import monitoring

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "monitoring.json"


def _rules(doc: dict) -> list[monitoring.Rule]:
    return [monitoring.Rule(
        id=raw["id"], type=raw["type"], severity=raw["severity"],
        enabled=raw["enabled"], asset=raw.get("asset", ""),
        amount=raw.get("amount", 0), count=raw.get("count", 0),
        window=raw.get("window", 0), freeze=raw.get("freeze", False),
    ) for raw in doc["rules"]]


def _transactions(doc: dict) -> list[monitoring.Transaction]:
    return [monitoring.Transaction(
        id=raw["id"], from_=raw["from"], asset=raw["asset"],
        amount=raw["amount"], at=raw["at"],
    ) for raw in doc["transactions"]]


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    monitor = monitoring.Monitor(_rules(doc), doc["freeze_authorized"])
    raised: list[monitoring.Alert] = []
    for tx in _transactions(doc):
        raised.extend(monitor.evaluate(tx))

    assert len(raised) == len(doc["expect_alerts"])
    escalated = set(doc["expect_escalated"])
    for alert, want in zip(raised, doc["expect_alerts"], strict=True):
        assert (alert.id, alert.rule, alert.tx_id) == (want["id"], want["rule"], want["tx_id"])
        key = f"{alert.rule}/{alert.tx_id}"
        assert alert.freeze_applied == doc["expect_freeze"].get(key, False), key
        assert alert.escalated == (key in escalated), key
        assert not (alert.freeze_applied and alert.escalated), key

    case = doc["disposition"]
    closed = monitor.dispose(case["alert"], case["action"], case["reason"], case["reviewer"], case["at"])
    assert closed.status == "closed"
    assert closed.disposition == monitoring.REPORT
    assert closed.reviewer == case["reviewer"]
    assert closed.closed_at == case["at"]
    with pytest.raises(monitoring.AlertClosed):
        monitor.dispose(case["alert"], monitoring.REPORT, "again", "ana", 2001)


def test_freeze_needs_policy_and_escalates() -> None:
    rules = [
        monitoring.Rule(id="big", type=monitoring.THRESHOLD, severity=monitoring.HIGH,
                        enabled=True, amount=100, freeze=True),
        monitoring.Rule(id="allowed", type=monitoring.THRESHOLD, severity=monitoring.HIGH,
                        enabled=True, amount=100, freeze=True),
    ]
    monitor = monitoring.Monitor(rules, ["allowed"])
    alerts = {alert.rule: alert for alert in monitor.evaluate(
        monitoring.Transaction(id="t1", from_="a", asset="arce:devnet", amount=500, at=10))}
    assert not alerts["big"].freeze_applied and alerts["big"].escalated
    assert alerts["allowed"].freeze_applied and not alerts["allowed"].escalated
    with pytest.raises(monitoring.InvalidRule):
        monitoring.Monitor(
            [monitoring.Rule(id="x", type=monitoring.THRESHOLD, severity=monitoring.LOW,
                             enabled=True, amount=1)],
            ["y"],
        )


def test_evaluate_is_idempotent_and_velocity_counts() -> None:
    monitor = monitoring.Monitor([
        monitoring.Rule(id="v", type=monitoring.VELOCITY, severity=monitoring.MEDIUM,
                        enabled=True, count=2, window=60),
    ])
    assert monitor.evaluate(monitoring.Transaction(id="t1", from_="a", asset="a", amount=1, at=100)) == []
    assert monitor.evaluate(monitoring.Transaction(id="t1", from_="a", asset="a", amount=1, at=100)) == []
    alerts = monitor.evaluate(monitoring.Transaction(id="t2", from_="a", asset="a", amount=1, at=130))
    assert len(alerts) == 1 and alerts[0].reason == "2 transfers in 60s"
    assert monitor.evaluate(monitoring.Transaction(id="t3", from_="b", asset="a", amount=1, at=500)) == []


def test_validation() -> None:
    bad = [
        [monitoring.Rule(id="", type=monitoring.THRESHOLD, severity=monitoring.LOW, enabled=True, amount=1)],
        [monitoring.Rule(id="a", type="other", severity=monitoring.LOW, enabled=True, amount=1)],
        [monitoring.Rule(id="a", type=monitoring.THRESHOLD, severity=monitoring.LOW, enabled=True, amount=0)],
        [monitoring.Rule(id="a", type=monitoring.THRESHOLD, severity="critical", enabled=True, amount=1)],
        [monitoring.Rule(id="a", type=monitoring.VELOCITY, severity=monitoring.LOW, enabled=True, count=1, window=10)],
        [monitoring.Rule(id="a", type=monitoring.VELOCITY, severity=monitoring.LOW, enabled=True, count=2, window=0)],
        [
            monitoring.Rule(id="a", type=monitoring.THRESHOLD, severity=monitoring.LOW, enabled=True, amount=1),
            monitoring.Rule(id="a", type=monitoring.THRESHOLD, severity=monitoring.LOW, enabled=True, amount=1),
        ],
    ]
    for rules in bad:
        with pytest.raises((monitoring.InvalidRule, monitoring.DuplicateRule)):
            monitoring.Monitor(rules)

    monitor = monitoring.Monitor([])
    with pytest.raises(monitoring.InvalidRule):
        monitor.evaluate(monitoring.Transaction(id="", from_="a", asset="a", amount=1, at=1))
    with pytest.raises(monitoring.UnknownAlert):
        monitor.dispose("missing", monitoring.REPORT, "r", "ana", 1)
    with pytest.raises(monitoring.InvalidRule):
        monitor.dispose("x", "shred", "r", "ana", 1)
