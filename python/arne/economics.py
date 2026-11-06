"""ARCE supply, bounded tail emission, genesis allocations, and vesting.

Mirrors internal/economics. Ordinary mints never pass the cap; tail emission
starts at the cap, only funds security, and stays within the hard annual bound.
"""

from __future__ import annotations

from dataclasses import dataclass, field

SECONDS_PER_YEAR = 365 * 24 * 60 * 60
MAX_TAIL_RATE_BPS = 500
VALIDATOR_REWARDS = "validator_rewards"
PROTOCOL_SECURITY = "protocol_security"

CATEGORIES = {"community", "contributors", "treasury", "validators", "liquidity"}


class EconomicsError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise EconomicsError(code, message)


@dataclass(frozen=True)
class Schedule:
    beneficiary: str
    category: str
    amount: int
    cliff: int = 0
    duration: int = 0


def validate_genesis(cap: int, schedules: list[Schedule]) -> int:
    if cap <= 0:
        _fail("invalid", "cap must be positive")
    if not schedules:
        _fail("invalid", "at least one allocation is required")
    total = 0
    for schedule in schedules:
        _validate(schedule)
        if schedule.amount > cap - total:
            _fail("cap", f"allocations exceed cap {cap}")
        total += schedule.amount
    return total


def _validate(schedule: Schedule) -> None:
    if not schedule.beneficiary.strip():
        _fail("invalid", "beneficiary is required")
    if schedule.category not in CATEGORIES:
        _fail("invalid", f"category {schedule.category!r}")
    if schedule.amount <= 0:
        _fail("invalid", "amount must be positive")


@dataclass(frozen=True)
class Entry:
    kind: str
    amount: int
    at: int
    purpose: str = ""


class Supply:
    """Circulating ARCE with a hard cap and a bounded security tail."""

    def __init__(self, cap: int) -> None:
        if cap <= 0:
            _fail("invalid", "cap must be positive")
        self.cap = cap
        self.tail_rate_bps = 0
        self.minted = 0
        self.burned = 0
        self.tail = 0
        self.entries: list[Entry] = []

    @property
    def total(self) -> int:
        return self.minted + self.tail - self.burned

    def set_tail_rate(self, bps: int) -> None:
        if bps > MAX_TAIL_RATE_BPS:
            _fail("rate", f"tail rate above {MAX_TAIL_RATE_BPS} bps")
        self.tail_rate_bps = bps

    def mint(self, amount: int, purpose: str, at: int) -> None:
        if amount <= 0 or not purpose.strip():
            _fail("invalid", "amount and purpose are required")
        if self.total > self.cap or amount > self.cap - self.total:
            _fail("cap", f"mint would exceed cap {self.cap}")
        self.minted += amount
        self.entries.append(Entry("mint", amount, at, purpose))

    def burn(self, amount: int, at: int) -> None:
        if amount <= 0:
            _fail("invalid", "amount must be positive")
        if amount > self.total:
            _fail("invalid", "burn above supply")
        self.burned += amount
        self.entries.append(Entry("burn", amount, at))

    def mint_tail(self, amount: int, epoch: int, at: int) -> None:
        if amount <= 0 or epoch <= 0:
            _fail("invalid", "amount and epoch are required")
        if self.total < self.cap:
            _fail("cap", "tail emission starts at the cap")
        if self.tail_rate_bps == 0:
            _fail("rate", "tail emission is disabled")
        if amount * SECONDS_PER_YEAR * 10_000 > self.total * self.tail_rate_bps * epoch:
            _fail(
                "rate",
                f"{amount} per {epoch}s exceeds {self.tail_rate_bps} bps a year",
            )
        self.tail += amount
        self.entries.append(Entry("tail", amount, at, PROTOCOL_SECURITY))

    def mint_tail_for(self, amount: int, epoch: int, at: int, purpose: str) -> None:
        if purpose not in (VALIDATOR_REWARDS, PROTOCOL_SECURITY):
            _fail("purpose", f"tail emission may not fund {purpose!r}")
        self.mint_tail(amount, epoch, at)
        last = self.entries[-1]
        self.entries[-1] = Entry(last.kind, last.amount, last.at, purpose)


@dataclass(frozen=True)
class VestingEvent:
    kind: str
    at: int
    amount: int


@dataclass
class Vesting:
    schedule: Schedule
    transferred: int = 0
    completed: bool = False
    events: list[VestingEvent] = field(default_factory=list)

    def __post_init__(self) -> None:
        _validate(self.schedule)

    def unlocked(self, now: int) -> int:
        s = self.schedule
        if now < s.cliff:
            return 0
        if s.duration == 0 or now >= s.cliff + s.duration:
            return s.amount
        return s.amount * (now - s.cliff) // s.duration

    def transferable(self, now: int) -> int:
        return self.unlocked(now) - self.transferred

    def transfer(self, amount: int, now: int) -> None:
        if amount <= 0:
            _fail("invalid", "amount must be positive")
        available = self.transferable(now)
        if amount > available:
            _fail("locked", f"{available} unlocked, {amount} requested")
        self.transferred += amount

    def advance(self, now: int) -> bool:
        if self.completed or now < self.schedule.cliff + self.schedule.duration:
            return False
        self.completed = True
        self.events.append(VestingEvent("vested", now, self.schedule.amount))
        return True
