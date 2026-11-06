"""ARCE supply, bounded tail emission, genesis allocations, and vesting.

Mirrors internal/economics. Ordinary mints never pass the cap; tail emission
starts at the cap, only funds security, and stays within the hard annual bound.
"""

from __future__ import annotations

import hashlib
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


DEFAULT_MAX_CHANGE_BPS = 1250


class FeeMarket:
    """Deterministic congestion rule for the next base fee."""

    def __init__(
        self, base: int, minimum: int, maximum: int, target: int, max_change_bps: int
    ) -> None:
        if base <= 0 or target <= 0 or minimum <= 0 or not minimum <= base <= maximum:
            _fail("invalid", "base, min, max and target must be ordered and positive")
        if not 1 <= max_change_bps <= 10_000:
            _fail("invalid", "max_change_bps must be 1..10000")
        self.base_fee = base
        self.min_base_fee = minimum
        self.max_base_fee = maximum
        self.target_units = target
        self.max_change_bps = max_change_bps

    def next(self, used: int) -> int:
        if used == self.target_units:
            return self.base_fee
        over = used > self.target_units
        diff = abs(used - self.target_units)
        change = self.base_fee * diff // self.target_units
        change = min(change, self.base_fee * self.max_change_bps // 10_000)
        value = self.base_fee + change if over else self.base_fee - change
        return max(self.min_base_fee, min(value, self.max_base_fee))


def effective_fee(base: int, priority: int, max_priority: int, min_fee: int) -> int:
    if priority > max_priority:
        _fail("fee", "priority fee above cap")
    fee = base + priority
    return max(fee, min_fee)


@dataclass(frozen=True)
class Split:
    burn: int
    validators: int
    treasury: int


def split_fee(fee: int, burn_bps: int, validator_bps: int) -> Split:
    if burn_bps + validator_bps > 10_000:
        _fail("invalid", "fee bps above 10000")
    burn = fee * burn_bps // 10_000
    validators = fee * validator_bps // 10_000
    return Split(burn=burn, validators=validators, treasury=fee - burn - validators)


class Sponsorship:
    """A bounded fee budget. It cannot move the sponsored account's funds."""

    def __init__(
        self,
        sponsor: str,
        sponsored: str,
        asset: str,
        budget: int,
        expires_at: int,
    ) -> None:
        if not sponsor.strip() or not sponsored.strip() or not asset.strip():
            _fail("invalid", "sponsor, sponsored and asset are required")
        if sponsor == sponsored:
            _fail("invalid", "an account cannot sponsor itself")
        if budget <= 0:
            _fail("invalid", "budget must be positive")
        self.sponsor = sponsor
        self.sponsored = sponsored
        self.asset = asset
        self.budget = budget
        self.spent = 0
        self.expires_at = expires_at

    def remaining(self, now: int) -> int:
        if now >= self.expires_at:
            return 0
        return self.budget - self.spent

    def pay(self, amount: int, now: int) -> None:
        if amount <= 0:
            _fail("invalid", "amount must be positive")
        if now >= self.expires_at:
            _fail("expired", f"sponsorship ended at {self.expires_at}")
        if amount > self.budget - self.spent:
            _fail("budget", f"{self.budget - self.spent} left, {amount} requested")
        self.spent += amount


CONVERSION_RATE_SCALE = 100_000_000


def _proof_digest(
    asset: str, rate: int, reserves: int, liabilities: int, at: int
) -> str:
    h = hashlib.sha256()
    for part in ("arne-reserve-v1", asset, str(rate), str(reserves), str(liabilities), str(at)):
        h.update(part.encode())
        h.update(b"\x00")
    return h.digest()[:16].hex()


@dataclass
class Proof:
    """A published reserve and liability commitment for one route."""

    asset: str
    rate: int
    reserves: int
    liabilities: int
    at: int
    digest: str

    def verify(self, reserves: int, liabilities: int) -> None:
        """Check the commitment against observed values, then coverage."""
        if self.digest != _proof_digest(
            self.asset, self.rate, self.reserves, self.liabilities, self.at
        ):
            _fail("proof", "commitment digest does not match its fields")
        if reserves != self.reserves or liabilities != self.liabilities:
            _fail(
                "proof",
                f"observed {reserves}/{liabilities}, committed "
                f"{self.reserves}/{self.liabilities}",
            )
        if reserves < liabilities:
            _fail("unbacked", f"{reserves} reserves for {liabilities} liabilities")


class Reserve:
    """One protocol-operated conversion route.

    Reserves and liabilities are ARCE base units. A route never mints ARCE; it
    can only quote from its spare reserves.
    """

    def __init__(self, asset: str, rate: int) -> None:
        if not asset.strip() or rate <= 0:
            _fail("invalid", "asset and a positive rate are required")
        self.asset = asset
        self.rate = rate
        self.reserves = 0
        self.liabilities = 0

    @property
    def covered(self) -> bool:
        return self.reserves >= self.liabilities

    @property
    def disabled(self) -> bool:
        return not self.covered

    @property
    def spare(self) -> int:
        if not self.covered:
            return 0
        return self.reserves - self.liabilities

    def set_reserves(self, amount: int) -> None:
        self.reserves = amount

    def set_liabilities(self, amount: int) -> None:
        self.liabilities = amount

    def recapitalize(self, amount: int) -> None:
        self.reserves += amount

    def quote(self, asset_amount: int, min_arce: int) -> int:
        if asset_amount <= 0:
            _fail("invalid", "amount must be positive")
        if self.disabled:
            _fail(
                "unbacked",
                f"route {self.asset} has {self.reserves} reserves for "
                f"{self.liabilities} liabilities",
            )
        arce = asset_amount * self.rate // CONVERSION_RATE_SCALE
        if arce < min_arce:
            _fail("slippage", f"{arce} ARCE below minimum {min_arce}")
        if arce > self.spare:
            _fail("unbacked", f"{arce} ARCE requested, {self.spare} spare")
        return arce

    def commit(self, at: int) -> Proof:
        return Proof(
            asset=self.asset,
            rate=self.rate,
            reserves=self.reserves,
            liabilities=self.liabilities,
            at=at,
            digest=_proof_digest(
                self.asset, self.rate, self.reserves, self.liabilities, at
            ),
        )
