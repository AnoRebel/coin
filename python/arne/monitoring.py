"""Transaction-monitoring alerts and analyst dispositions.

Mirrors internal/compliance/monitoring. A rule may request a freeze, but the
monitor applies one only when the rule is listed in the freeze policy. Every
other request is escalated for a human; no rule freezes funds on its own.
"""

from __future__ import annotations

import hashlib
import threading
import time
from dataclasses import dataclass

THRESHOLD = "threshold"
VELOCITY = "velocity"

LOW = "low"
MEDIUM = "medium"
HIGH = "high"

NO_ACTION = "no_action"
ESCALATE = "escalate"
REPORT = "report"

_ACTIONS = {NO_ACTION, ESCALATE, REPORT}
_SEVERITIES = {LOW, MEDIUM, HIGH}


class MonitoringError(Exception):
    pass


class InvalidRule(MonitoringError):
    pass


class UnknownAlert(MonitoringError):
    pass


class AlertClosed(MonitoringError):
    pass


class DuplicateRule(MonitoringError):
    pass


@dataclass
class Rule:
    id: str
    type: str
    severity: str
    enabled: bool
    asset: str = ""
    amount: int = 0
    count: int = 0
    window: int = 0
    freeze: bool = False


@dataclass
class Transaction:
    id: str
    from_: str
    asset: str
    amount: int
    at: int


@dataclass
class Alert:
    id: str
    rule: str
    tx_id: str
    from_: str
    asset: str
    amount: int
    severity: str
    reason: str
    created_at: int
    status: str = "open"
    freeze_requested: bool = False
    freeze_applied: bool = False
    escalated: bool = False
    disposition: str = ""
    reviewer: str = ""
    closed_at: int = 0

    def to_dict(self) -> dict[str, object]:
        out: dict[str, object] = {
            "id": self.id, "rule": self.rule, "tx_id": self.tx_id,
            "from": self.from_, "asset": self.asset, "amount": self.amount,
            "severity": self.severity, "reason": self.reason,
            "created_at": self.created_at, "status": self.status,
        }
        for name in ("freeze_requested", "freeze_applied", "escalated"):
            if getattr(self, name):
                out[name] = True
        if self.disposition:
            out["disposition"] = self.disposition
        if self.reviewer:
            out["reviewer"] = self.reviewer
        if self.closed_at:
            out["closed_at"] = self.closed_at
        return out


class Monitor:
    def __init__(self, rules: list[Rule], freeze_authorized: list[str] | None = None) -> None:
        self._rules: list[Rule] = []
        self._freeze_ok = {rule_id.strip() for rule_id in (freeze_authorized or [])}
        self._alerts: dict[str, Alert] = {}
        self._order: list[str] = []
        self._seen: set[str] = set()
        self._history: dict[str, list[int]] = {}
        self._lock = threading.Lock()
        ids: set[str] = set()
        for index, rule in enumerate(rules):
            rule.id = rule.id.strip()
            rule.asset = rule.asset.strip()
            _validate(rule, index)
            if rule.id in ids:
                raise DuplicateRule(rule.id)
            ids.add(rule.id)
            self._rules.append(rule)
        unknown = {rule_id for rule_id in self._freeze_ok if rule_id and rule_id not in ids}
        if unknown:
            raise InvalidRule(f"freeze authority for unknown rule {sorted(unknown)[0]!r}")

    def evaluate(self, tx: Transaction) -> list[Alert]:
        if not tx.id.strip() or not tx.from_.strip() or not tx.asset.strip() or tx.amount <= 0 or tx.at <= 0:
            raise InvalidRule("id, from, asset, amount, and at are required")
        with self._lock:
            if tx.id in self._seen:
                return []
            self._seen.add(tx.id)
            self._history.setdefault(tx.from_, []).append(tx.at)
            raised: list[Alert] = []
            for rule in self._rules:
                if not rule.enabled:
                    continue
                reason = _match(rule, tx, self._history[tx.from_])
                if reason is None:
                    continue
                alert = Alert(
                    id=_alert_id(rule.id, tx.id), rule=rule.id, tx_id=tx.id,
                    from_=tx.from_, asset=tx.asset, amount=tx.amount,
                    severity=rule.severity, reason=reason, created_at=tx.at,
                    freeze_requested=rule.freeze,
                )
                if rule.freeze:
                    if rule.id in self._freeze_ok:
                        alert.freeze_applied = True
                    else:
                        alert.escalated = True
                        alert.reason += "; freeze requires policy authorization, escalated"
                if alert.id in self._alerts:
                    continue
                self._alerts[alert.id] = alert
                self._order.append(alert.id)
                raised.append(alert)
            return raised

    def alerts(self) -> list[Alert]:
        with self._lock:
            return [self._alerts[alert_id] for alert_id in self._order]

    def alert(self, alert_id: str) -> Alert | None:
        with self._lock:
            return self._alerts.get(alert_id)

    def dispose(self, alert_id: str, action: str, reason: str, reviewer: str, at: int = 0) -> Alert:
        action, reason, reviewer = action.strip(), reason.strip(), reviewer.strip()
        if action not in _ACTIONS:
            raise InvalidRule(f"unknown disposition {action!r}")
        if not reason or not reviewer:
            raise InvalidRule("reason and reviewer are required")
        with self._lock:
            alert = self._alerts.get(alert_id)
            if alert is None:
                raise UnknownAlert(alert_id)
            if alert.status != "open":
                raise AlertClosed(alert_id)
            alert.status = "closed"
            alert.disposition = action
            alert.reviewer = reviewer
            alert.closed_at = at or int(time.time())
            if action == ESCALATE:
                alert.escalated = True
            return alert


def _validate(rule: Rule, index: int) -> None:
    if not rule.id:
        raise InvalidRule(f"rules[{index}]: id is required")
    if rule.severity not in _SEVERITIES:
        raise InvalidRule("severity must be low, medium, or high")
    if rule.type == THRESHOLD:
        if rule.amount <= 0:
            raise InvalidRule("threshold rule needs a positive amount")
    elif rule.type == VELOCITY:
        if rule.count < 2 or rule.window <= 0:
            raise InvalidRule("velocity rule needs count >= 2 and a positive window")
    else:
        raise InvalidRule("type must be threshold or velocity")


def _match(rule: Rule, tx: Transaction, times: list[int]) -> str | None:
    if rule.asset and rule.asset != tx.asset:
        return None
    if rule.type == THRESHOLD:
        if tx.amount < rule.amount:
            return None
        return f"amount {tx.amount} at or above {rule.amount}"
    from_ts = tx.at - rule.window
    count = sum(1 for t in times if from_ts < t <= tx.at)
    if count < rule.count:
        return None
    return f"{count} transfers in {rule.window}s"


def _alert_id(rule: str, tx: str) -> str:
    digest = hashlib.sha256(b"arne-alert-v1\x00" + rule.encode() + b"\x00" + tx.encode()).digest()
    return digest[:16].hex()
