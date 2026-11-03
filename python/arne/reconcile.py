"""Idempotent cross-rail reconciliation and exception reporting.

Mirrors internal/connector/reconcile. An operation is submitted with an
idempotency key and a correlation identifier. The first matching confirmation
settles it once; an identical repeat is ignored; a mismatch puts it into
conflict. A reconciliation run ages past-deadline operations according to
policy and reports matched, unmatched, and conflicting operations with totals.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field, replace

ACCEPTED = "accepted"
DUPLICATE = "duplicate"
CONFLICT = "conflict"
UNKNOWN = "unknown"
INVALID = "invalid"

PENDING = "pending"
SETTLED = "settled"
FAILED = "failed"
IN_CONFLICT = "conflicting"

_IDENTIFIER = re.compile(r"^[A-Za-z0-9_.:-]{1,128}$")


class ReconcileError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"reconcile: {code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise ReconcileError(code, message)


@dataclass(frozen=True)
class Policy:
    fail_on_timeout: bool = False


@dataclass(frozen=True)
class Operation:
    key: str
    correlation: str
    asset: str
    amount: int
    created_at: int
    timeout: int

    def validate(self) -> None:
        _identifier("key", self.key)
        _identifier("correlation", self.correlation)
        _asset(self.asset)
        if self.amount <= 0:
            _fail("invalid", "amount must be positive")
        if self.created_at < 0:
            _fail("invalid", "created_at must not be negative")
        if self.timeout <= 0:
            _fail("invalid", "timeout must be positive")


@dataclass(frozen=True)
class Confirmation:
    correlation: str
    asset: str
    amount: int
    reference: str
    at: int

    def validate(self) -> None:
        _identifier("correlation", self.correlation)
        _asset(self.asset)
        if self.amount <= 0:
            _fail("invalid", "amount must be positive")
        if not self.reference.strip() or len(self.reference) > 256:
            _fail("invalid", "reference is required")
        if self.at < 0:
            _fail("invalid", "at must not be negative")


@dataclass(frozen=True)
class State:
    operation: Operation
    status: str = PENDING
    reference: str = ""
    confirmed_at: int = 0
    conflict_detail: str = ""


@dataclass(frozen=True)
class Exception_:
    kind: str
    correlation: str
    detail: str = ""


@dataclass(frozen=True)
class Total:
    asset: str
    settled: int = 0
    failed: int = 0
    pending: int = 0
    conflicting: int = 0


@dataclass(frozen=True)
class Report:
    matched: int
    unmatched: int
    conflicting: int
    totals: tuple[Total, ...]
    exceptions: tuple[Exception_, ...]


@dataclass
class _Entry:
    state: State
    timed_out_reported: bool = False


@dataclass
class Ledger:
    policy: Policy
    by_key: dict[str, _Entry] = field(default_factory=dict)
    by_corr: dict[str, _Entry] = field(default_factory=dict)
    order: list[_Entry] = field(default_factory=list)
    excepted: list[Exception_] = field(default_factory=list)

    def submit(self, operation: Operation) -> tuple[State, str]:
        try:
            operation.validate()
        except ReconcileError:
            return State(operation), INVALID
        existing = self.by_key.get(operation.key)
        if existing is not None:
            return existing.state, DUPLICATE
        existing = self.by_corr.get(operation.correlation)
        if existing is not None:
            self.excepted.append(
                Exception_(
                    "correlation_reused",
                    operation.correlation,
                    f"key {operation.key} conflicts with {existing.state.operation.key}",
                )
            )
            return existing.state, CONFLICT
        entry = _Entry(State(operation))
        self.by_key[operation.key] = entry
        self.by_corr[operation.correlation] = entry
        self.order.append(entry)
        return entry.state, ACCEPTED

    def confirm(self, confirmation: Confirmation, now: int) -> tuple[State, str]:
        try:
            confirmation.validate()
        except ReconcileError:
            return State(Operation("", "", "", 0, 0, 0)), INVALID
        entry = self.by_corr.get(confirmation.correlation)
        if entry is None:
            self.excepted.append(
                Exception_("unknown_confirmation", confirmation.correlation)
            )
            return State(Operation("", "", "", 0, 0, 0)), UNKNOWN

        state = entry.state
        operation = state.operation
        if state.status == SETTLED:
            if (
                confirmation.asset == operation.asset
                and confirmation.amount == operation.amount
                and confirmation.reference == state.reference
            ):
                return state, DUPLICATE
            return self._conflict(
                entry,
                f"settled as {operation.asset}/{operation.amount} from {state.reference}, "
                f"got {confirmation.asset}/{confirmation.amount} from {confirmation.reference}",
            )
        if state.status == IN_CONFLICT:
            return self._conflict(entry, "operation is already in conflict")
        if state.status == FAILED:
            self.excepted.append(
                Exception_(
                    "late_confirmation",
                    confirmation.correlation,
                    f"operation failed before confirmation at {confirmation.at}",
                )
            )
            return state, CONFLICT

        deadline = operation.created_at + operation.timeout
        if self.policy.fail_on_timeout and now >= deadline:
            entry.state = replace(state, status=FAILED)
            if not entry.timed_out_reported:
                entry.timed_out_reported = True
                self.excepted.append(
                    Exception_(
                        "missing_confirmation",
                        confirmation.correlation,
                        f"no confirmation by {deadline}",
                    )
                )
            self.excepted.append(
                Exception_(
                    "late_confirmation",
                    confirmation.correlation,
                    f"confirmation at {confirmation.at} arrived after the deadline",
                )
            )
            return entry.state, CONFLICT

        if (
            confirmation.asset != operation.asset
            or confirmation.amount != operation.amount
        ):
            return self._conflict(
                entry,
                f"expected {operation.asset}/{operation.amount}, "
                f"got {confirmation.asset}/{confirmation.amount}",
            )

        entry.state = replace(
            state,
            status=SETTLED,
            reference=confirmation.reference,
            confirmed_at=confirmation.at,
        )
        return entry.state, ACCEPTED

    def reconcile(self, now: int) -> Report:
        totals: dict[str, Total] = {}
        matched = unmatched = conflicting = 0

        for entry in self.order:
            state = entry.state
            operation = state.operation
            if state.status == PENDING and now >= operation.created_at + operation.timeout:
                if self.policy.fail_on_timeout:
                    state = replace(state, status=FAILED)
                    entry.state = state
                if not entry.timed_out_reported:
                    entry.timed_out_reported = True
                    self.excepted.append(
                        Exception_(
                            "missing_confirmation",
                            operation.correlation,
                            f"no confirmation by {operation.created_at + operation.timeout}",
                        )
                    )

            total = totals.get(operation.asset, Total(operation.asset))
            if state.status == SETTLED:
                matched += 1
                total = replace(total, settled=total.settled + operation.amount)
            elif state.status == IN_CONFLICT:
                conflicting += 1
                total = replace(total, conflicting=total.conflicting + operation.amount)
            else:
                unmatched += 1
                if state.status == FAILED:
                    total = replace(total, failed=total.failed + operation.amount)
                else:
                    total = replace(total, pending=total.pending + operation.amount)
            totals[operation.asset] = total

        return Report(
            matched=matched,
            unmatched=unmatched,
            conflicting=conflicting,
            totals=tuple(totals[asset] for asset in sorted(totals)),
            exceptions=tuple(self.excepted),
        )

    def state(self, correlation: str) -> State | None:
        entry = self.by_corr.get(correlation)
        return None if entry is None else entry.state

    def _conflict(self, entry: _Entry, detail: str) -> tuple[State, str]:
        entry.state = replace(entry.state, status=IN_CONFLICT, conflict_detail=detail)
        self.excepted.append(
            Exception_(
                "conflicting_confirmation",
                entry.state.operation.correlation,
                detail,
            )
        )
        return entry.state, CONFLICT


def _identifier(field_name: str, value: str) -> None:
    if not _IDENTIFIER.match(value):
        _fail("invalid", f"{field_name} must be 1..128 characters of [A-Za-z0-9_.:-]")


def _asset(value: str) -> None:
    if not value.strip() or len(value) > 128:
        _fail("invalid", "asset is required")
