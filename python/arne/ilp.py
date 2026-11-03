"""ILPv4 and Open Payments: prepare/fulfill, quotes, grants, HTLC, STREAM.

Mirrors internal/connector/ilp. Times are unix seconds; an adapter converts to
ISO 8601 at the wire boundary. The prepare condition is the SHA-256 of the
fulfillment preimage, and the HTLC lets the receiver claim before expiry and the
sender refund only at or after it, so a timely claim always wins.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field, replace

HELD = "held"
CLAIMED = "claimed"
REFUNDED = "refunded"

_ACCESS_TYPES = {"incoming-payment", "quote", "outgoing-payment"}
_ACCESS_ACTIONS = {"create", "read", "list", "complete", "delete"}
_ADDRESS_CHARS = set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_~")


class ILPError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"ilp: {code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise ILPError(code, message)


@dataclass(frozen=True)
class Amount:
    asset: str
    value: int

    def validate(self) -> None:
        if not self.asset.strip():
            _fail("invalid", "asset is required")
        if self.value <= 0:
            _fail("invalid", "amount must be positive")


def validate_address(address: str) -> None:
    if not address or len(address.encode()) > 1023:
        _fail("invalid address", repr(address))
    if address.startswith(".") or ".." in address:
        _fail("invalid address", repr(address))
    segments = address.removesuffix(".").split(".")
    if len(segments) < 2:
        _fail("invalid address", f"{address!r} needs two segments")
    for segment in segments:
        if not segment or len(segment) > 64:
            _fail("invalid address", repr(address))
        if any(char not in _ADDRESS_CHARS for char in segment):
            _fail("invalid address", repr(address))


def condition(fulfillment: bytes) -> bytes:
    if len(fulfillment) != 32:
        _fail("invalid", "fulfillment must be 32 bytes")
    return hashlib.sha256(fulfillment).digest()


def _matches(expected: bytes, fulfillment: bytes) -> None:
    if len(expected) != 32 or len(fulfillment) != 32:
        _fail("condition does not match fulfillment", "wrong preimage")
    if hashlib.sha256(fulfillment).digest() != expected:
        _fail("condition does not match fulfillment", "wrong preimage")


@dataclass(frozen=True)
class Prepare:
    amount: int
    expires_at: int
    condition: bytes
    destination: str
    data: bytes = b""

    def validate(self, now: int) -> None:
        if self.amount <= 0:
            _fail("invalid", "amount must be positive")
        validate_address(self.destination)
        if len(self.condition) != 32:
            _fail("invalid", "condition must be 32 bytes")
        if now >= self.expires_at:
            _fail("expired", f"prepare at {now} expired at {self.expires_at}")

    def fulfill(self, now: int, fulfillment: bytes) -> bytes:
        self.validate(now)
        _matches(self.condition, fulfillment)
        return fulfillment


@dataclass(frozen=True)
class Reject:
    code: str
    triggered_by: str
    message: str = ""


def new_reject(code: str, triggered_by: str, message: str = "") -> Reject:
    if len(code) != 3 or code[0] not in "FTR":
        _fail("invalid reject code", repr(code))
    if len(message) > 512:
        _fail("invalid", "message must be at most 512 characters")
    if triggered_by:
        validate_address(triggered_by)
    return Reject(code, triggered_by, message)


@dataclass(frozen=True)
class Quote:
    id: str
    expires_at: int
    debit: Amount
    receive: Amount
    fee: Amount = field(default_factory=lambda: Amount("", 0))

    def validate(self, now: int) -> None:
        if not self.id.strip():
            _fail("invalid", "quote id is required")
        try:
            self.debit.validate()
        except ILPError as exc:
            _fail(exc.code, f"debit: {exc}")
        try:
            self.receive.validate()
        except ILPError as exc:
            _fail(exc.code, f"receive: {exc}")
        if self.fee.asset or self.fee.value:
            try:
                self.fee.validate()
            except ILPError as exc:
                _fail(exc.code, f"fee: {exc}")
        if now >= self.expires_at:
            _fail("expired", f"quote at {now} expired at {self.expires_at}")


@dataclass(frozen=True)
class Access:
    type: str
    actions: tuple[str, ...]
    limits: tuple[Amount, ...] = ()

    def validate(self) -> None:
        if self.type not in _ACCESS_TYPES:
            _fail("invalid", f"access type {self.type!r}")
        if not self.actions:
            _fail("invalid", f"{self.type} needs an action")
        for action in self.actions:
            if action not in _ACCESS_ACTIONS:
                _fail("invalid", f"action {action!r}")
        for limit in self.limits:
            try:
                limit.validate()
            except ILPError as exc:
                _fail(exc.code, f"limit: {exc}")


@dataclass(frozen=True)
class Grant:
    access_token: str
    access: tuple[Access, ...]
    expires_at: int

    def validate(self, now: int) -> None:
        if not self.access_token.strip():
            _fail("invalid", "access token is required")
        if not self.access:
            _fail("invalid", "grant needs an access right")
        seen: set[str] = set()
        for access in self.access:
            if access.type in seen:
                _fail("invalid", f"duplicate access type {access.type!r}")
            seen.add(access.type)
            access.validate()
        if now >= self.expires_at:
            _fail("expired", f"grant at {now} expired at {self.expires_at}")

    def check(self, now: int, kind: str, action: str) -> None:
        self.validate(now)
        for access in self.access:
            if access.type != kind:
                continue
            if action not in access.actions:
                _fail("access denied", f"{action} on {kind}")
            return
        _fail("access denied", f"no {kind} access")


@dataclass(frozen=True)
class Escrow:
    id: str
    sender: str
    receiver: str
    amount: Amount
    condition: bytes
    expires_at: int
    status: str = HELD
    reference: str = ""
    fulfillment: bytes = b""

    def claim(self, now: int, fulfillment: bytes) -> Escrow:
        if self.status != HELD:
            _fail("wrong state", f"escrow is {self.status}")
        if now >= self.expires_at:
            _fail("expired", f"claim at {now} expired at {self.expires_at}")
        _matches(self.condition, fulfillment)
        return replace(self, status=CLAIMED, fulfillment=fulfillment)

    def refund(self, now: int) -> Escrow:
        if self.status != HELD:
            _fail("wrong state", f"escrow is {self.status}")
        if now < self.expires_at:
            _fail("not yet expired", f"refund at {now} expires at {self.expires_at}")
        return replace(self, status=REFUNDED)


def new_escrow(
    id: str,
    sender: str,
    receiver: str,
    amount: Amount,
    condition: bytes,
    expires_at: int,
    reference: str = "",
) -> Escrow:
    if not id.strip():
        _fail("invalid", "id is required")
    _account("sender", sender)
    _account("receiver", receiver)
    if sender == receiver:
        _fail("invalid", "sender and receiver must differ")
    amount.validate()
    if len(condition) != 32:
        _fail("invalid", "condition must be 32 bytes")
    return Escrow(id, sender, receiver, amount, condition, expires_at, HELD, reference)


def _account(label: str, value: str) -> None:
    if not value.strip():
        _fail("invalid", f"{label}: account is required")
    if len(value) > 128:
        _fail("invalid", f"{label}: account must be at most 128 characters")


@dataclass(frozen=True)
class Receipt:
    stream_id: str
    sequence: int
    total_received: int


@dataclass
class Stream:
    id: str
    sequence: int = 0
    total: int = 0

    def __post_init__(self) -> None:
        if not self.id.strip():
            _fail("invalid", "stream id is required")

    def apply(self, receipt: Receipt) -> None:
        if receipt.stream_id != self.id:
            _fail("invalid", f"stream {receipt.stream_id!r}")
        if receipt.sequence <= self.sequence:
            _fail("replay", f"sequence {receipt.sequence} after {self.sequence}")
        if receipt.total_received < self.total:
            _fail("invalid", f"total {receipt.total_received} below {self.total}")
        self.sequence = receipt.sequence
        self.total = receipt.total_received
