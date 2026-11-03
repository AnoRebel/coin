"""Connector exposure caps, rate limits, and governed limit changes.

Mirrors internal/connector/limits. A reservation is the connector's promise to
move value; submitting one checks the single-transfer size, the counterparty's
rate window, the asset-wide exposure cap, and the counterparty exposure cap, in
that order, before any value moves. Releasing a reservation gives the exposure
back.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field, replace


class LimitsError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"limits: {code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise LimitsError(code, message)


def _text(field: str, value: str, maximum: int) -> None:
    if not value.strip():
        _fail("invalid", f"{field} is required")
    if len(value) > maximum:
        _fail("invalid", f"{field} must be at most {maximum} characters")


@dataclass(frozen=True)
class Policy:
    asset: str
    max_exposure: int
    max_counterparty_exposure: int
    max_transfer: int
    rate_count: int
    rate_window: int

    def validate(self) -> None:
        _text("asset", self.asset, 128)
        if self.max_exposure <= 0 or self.max_counterparty_exposure <= 0 or self.max_transfer <= 0:
            _fail("invalid", "caps must be positive")
        if self.max_transfer > self.max_counterparty_exposure:
            _fail("invalid", "max_transfer above max_counterparty_exposure")
        if self.max_counterparty_exposure > self.max_exposure:
            _fail("invalid", "max_counterparty_exposure above max_exposure")
        if self.rate_count <= 0 or self.rate_window <= 0:
            _fail("invalid", "rate_count and rate_window must be positive")


@dataclass(frozen=True)
class Reservation:
    id: str
    asset: str
    counterparty: str
    amount: int
    at: int
    released: bool = False


@dataclass(frozen=True)
class Change:
    asset: str
    actor: str
    reason: str
    at: int
    prior: Policy | None
    next: Policy


@dataclass
class Book:
    policies: dict[str, Policy] = field(default_factory=dict)
    exposure_by_asset: dict[str, int] = field(default_factory=dict)
    counterparty_exposure: dict[str, int] = field(default_factory=dict)
    rates: dict[str, list[int]] = field(default_factory=dict)
    held: dict[str, Reservation] = field(default_factory=dict)
    history_log: list[Change] = field(default_factory=list)
    seq: int = 0

    def govern(self, next_policy: Policy, actor: str, reason: str, now: int) -> Change:
        next_policy.validate()
        _text("actor", actor, 128)
        _text("reason", reason, 512)
        if now < 0:
            _fail("invalid", "now must not be negative")
        change = Change(
            asset=next_policy.asset,
            actor=actor,
            reason=reason,
            at=now,
            prior=self.policies.get(next_policy.asset),
            next=next_policy,
        )
        self.policies[next_policy.asset] = next_policy
        self.history_log.append(change)
        return change

    def submit(self, asset: str, counterparty: str, amount: int, now: int) -> Reservation:
        policy = self.policies.get(asset)
        if policy is None:
            _fail("asset is not configured", asset)
        _text("counterparty", counterparty, 128)
        if amount <= 0:
            _fail("invalid", "amount must be positive")
        if now < 0:
            _fail("invalid", "now must not be negative")
        if amount > policy.max_transfer:
            _fail("transfer exceeds the maximum size", f"{amount} above {policy.max_transfer}")

        key = _key(asset, counterparty)
        window = [at for at in self.rates.get(key, []) if at > now - policy.rate_window]
        self.rates[key] = window
        if len(window) >= policy.rate_count:
            _fail("rate limit exceeded", f"{policy.rate_count} per {policy.rate_window}s")

        exposure = self.exposure_by_asset.get(asset, 0)
        if exposure + amount > policy.max_exposure:
            _fail("exposure cap reached", f"asset {asset} has {exposure} of {policy.max_exposure}")
        held = self.counterparty_exposure.get(key, 0)
        if held + amount > policy.max_counterparty_exposure:
            _fail(
                "exposure cap reached",
                f"counterparty {counterparty} has {held} of {policy.max_counterparty_exposure}",
            )

        self.seq += 1
        reservation = Reservation(
            id=_reservation_id(asset, counterparty, self.seq),
            asset=asset,
            counterparty=counterparty,
            amount=amount,
            at=now,
        )
        self.held[reservation.id] = reservation
        self.exposure_by_asset[asset] = exposure + amount
        self.counterparty_exposure[key] = held + amount
        self.rates[key] = window + [now]
        return reservation

    def release(self, reservation_id: str) -> Reservation:
        reservation = self.held.get(reservation_id)
        if reservation is None:
            _fail("unknown reservation", reservation_id)
        if reservation.released:
            _fail("reservation is already released", reservation_id)
        released = replace(reservation, released=True)
        self.held[reservation_id] = released
        key = _key(released.asset, released.counterparty)
        self.exposure_by_asset[released.asset] -= released.amount
        self.counterparty_exposure[key] -= released.amount
        return released

    def exposure(self, asset: str) -> int:
        return self.exposure_by_asset.get(asset, 0)

    def held_for(self, asset: str, counterparty: str) -> int:
        return self.counterparty_exposure.get(_key(asset, counterparty), 0)

    def history(self) -> tuple[Change, ...]:
        return tuple(self.history_log)

    def reservation(self, reservation_id: str) -> Reservation | None:
        return self.held.get(reservation_id)


def _key(asset: str, counterparty: str) -> str:
    return asset + "\x00" + counterparty


def _reservation_id(asset: str, counterparty: str, seq: int) -> str:
    h = hashlib.sha256()
    h.update(b"arne-reservation-v1")
    h.update(b"\x00")
    h.update(asset.encode())
    h.update(b"\x00")
    h.update(counterparty.encode())
    h.update(b"\x00")
    h.update(str(seq).encode())
    return h.hexdigest()[:32]
