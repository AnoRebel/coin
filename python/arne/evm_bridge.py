"""EVM lock/mint and burn/release bridge guard.

Mirrors internal/connector/evm. The bridge holds no keys and talks to no chain:
it decides whether an observed event may execute, then records that decision so
a relayer mints or releases exactly once. Finality, replay, per-asset rate
limits, and an emergency pause all gate submission.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field

INBOUND = "inbound"
OUTBOUND = "outbound"

_TX_HASH_CHARS = set("0123456789abcdef")


class EVMError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"evm: {code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise EVMError(code, message)


def message_id(chain_id: int, tx_hash: str, log_index: int) -> str:
    digest = hashlib.sha256()
    digest.update(b"arne-bridge-v1")
    digest.update(b"\x00")
    digest.update(str(chain_id).encode())
    digest.update(b"\x00")
    digest.update(tx_hash.encode())
    digest.update(b"\x00")
    digest.update(str(log_index).encode())
    return digest.digest()[:16].hex()


@dataclass(frozen=True)
class Message:
    id: str
    direction: str
    chain_id: int
    block_number: int
    tx_hash: str
    log_index: int
    asset: str
    amount: int
    sender: str
    recipient: str
    confirmations: int


@dataclass(frozen=True)
class Limit:
    asset: str
    max: int
    window: int


@dataclass(frozen=True)
class Config:
    chain_id: int
    finality: int
    limits: tuple[Limit, ...] = ()


@dataclass
class _Window:
    start: int
    used: int = 0


@dataclass
class Bridge:
    config: Config
    paused: bool = False
    reason: str = ""
    seen: set[str] = field(default_factory=set)
    windows: dict[str, _Window] = field(default_factory=dict)

    def __post_init__(self) -> None:
        if self.config.chain_id <= 0:
            _fail("invalid", "chain_id is required")
        if self.config.finality <= 0:
            _fail("invalid", "finality must be positive")
        assets: set[str] = set()
        for limit in self.config.limits:
            if not limit.asset.strip():
                _fail("invalid", "limit asset is required")
            if limit.asset in assets:
                _fail("invalid", f"duplicate limit for {limit.asset!r}")
            assets.add(limit.asset)
            if limit.max <= 0 or limit.window <= 0:
                _fail("invalid", f"limit for {limit.asset!r} needs a positive max and window")

    def pause(self, reason: str) -> None:
        if self.paused:
            _fail("invalid", "already paused")
        if not reason.strip():
            _fail("invalid", "pause reason is required")
        if len(reason) > 200:
            _fail("invalid", "pause reason is too long")
        self.paused, self.reason = True, reason

    def resume(self) -> None:
        if not self.paused:
            _fail("invalid", "not paused")
        self.paused, self.reason = False, ""

    def submit(self, message: Message, now: int) -> None:
        if self.paused:
            _fail("paused", self.reason)
        if not message.id:
            message = _with_id(message)
        self._validate(message)
        if message.id in self.seen:
            _fail("replay", message.id)
        if message.confirmations < self.config.finality:
            _fail(
                "not final",
                f"{message.confirmations} of {self.config.finality} confirmations",
            )
        self._charge(message.asset, message.amount, now)
        self.seen.add(message.id)

    def _validate(self, message: Message) -> None:
        if message.direction not in (INBOUND, OUTBOUND):
            _fail("invalid", f"direction {message.direction!r}")
        if message.chain_id != self.config.chain_id:
            _fail("wrong chain", f"{message.chain_id}, expected {self.config.chain_id}")
        if (
            len(message.tx_hash) != 66
            or not message.tx_hash.startswith("0x")
            or any(char not in _TX_HASH_CHARS for char in message.tx_hash[2:])
        ):
            _fail("invalid", "tx_hash must be 0x followed by 64 lowercase hex characters")
        _text("asset", message.asset)
        _text("sender", message.sender)
        _text("recipient", message.recipient)
        if message.amount <= 0:
            _fail("invalid", "amount must be positive")
        if message.id != message_id(message.chain_id, message.tx_hash, message.log_index):
            _fail("invalid", "message id does not match the chain event")

    def _charge(self, asset: str, amount: int, now: int) -> None:
        for limit in self.config.limits:
            if limit.asset != asset:
                continue
            window = self.windows.get(asset)
            if window is None or now >= window.start + limit.window:
                window = _Window(start=now)
                self.windows[asset] = window
            if amount > limit.max - window.used:
                _fail(
                    "rate limit exceeded",
                    f"{asset} has {limit.max - window.used} of {limit.max} left",
                )
            window.used += amount
            return


def _with_id(message: Message) -> Message:
    return Message(
        id=message_id(message.chain_id, message.tx_hash, message.log_index),
        direction=message.direction,
        chain_id=message.chain_id,
        block_number=message.block_number,
        tx_hash=message.tx_hash,
        log_index=message.log_index,
        asset=message.asset,
        amount=message.amount,
        sender=message.sender,
        recipient=message.recipient,
        confirmations=message.confirmations,
    )


def _text(field_name: str, value: str) -> None:
    if not value.strip():
        _fail("invalid", f"{field_name} is required")
    if len(value) > 128:
        _fail("invalid", f"{field_name} must be at most 128 characters")
