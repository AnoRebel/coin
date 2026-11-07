"""Equivocation and downtime penalties.

Mirrors internal/slashing. Double-sign evidence is verified against the
validator's registered BLS key. Downtime evidence is structural and must be
derived by the consensus adapter from finalized commit data.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass

from arne import bls, staking


class SlashingError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise SlashingError(code, message)


@dataclass(frozen=True)
class Params:
    window_blocks: int
    jail_blocks: int
    double_sign_bps: int
    downtime_bps: int
    reporter_bps: int
    downtime_threshold_bps: int
    downtime_min_blocks: int
    reporter_cap: int = 0
    burn_bps: int = 0


@dataclass(frozen=True)
class Vote:
    height: int
    round: int
    block_hash: str
    signature: str


@dataclass(frozen=True)
class DoubleSignEvidence:
    validator: str
    a: Vote
    b: Vote


@dataclass(frozen=True)
class DowntimeEvidence:
    validator: str
    from_: int
    to: int
    missed: int
    total: int


@dataclass(frozen=True)
class Result:
    kind: str
    validator: str
    slashed: int
    reporter: int
    treasury: int
    jail_until: int
    delegators: dict[str, int]
    burn: int = 0


@dataclass(frozen=True)
class Split:
    burn: int
    reporter: int
    treasury: int


def split_penalty(slashed: int, params: Params) -> Split:
    """Divide a slashed amount; the treasury takes the remainder."""
    reporter = slashed * params.reporter_bps // 10_000
    if 0 < params.reporter_cap < reporter:
        reporter = params.reporter_cap
    burn = slashed * params.burn_bps // 10_000
    if reporter + burn > slashed:
        burn = slashed - reporter
    return Split(burn=burn, reporter=reporter, treasury=slashed - reporter - burn)


@dataclass
class Engine:
    network: str
    params: Params
    applied: dict[str, bool]


def new(network: str, params: Params) -> Engine:
    if not network:
        _fail("invalid", "network is required")
    if params.window_blocks == 0 or params.jail_blocks == 0:
        _fail("invalid", "window and jail blocks must be positive")
    if not 0 < params.double_sign_bps <= 10_000:
        _fail("invalid", "double_sign_bps must be in 1..10000")
    if not 0 < params.downtime_bps <= 10_000:
        _fail("invalid", "downtime_bps must be in 1..10000")
    if params.reporter_bps > 10_000:
        _fail("invalid", "reporter_bps above 10000")
    if params.burn_bps > 10_000:
        _fail("invalid", "burn_bps above 10000")
    return Engine(network=network, params=params, applied={})


def double_sign(state: staking.State, engine: Engine, ev: DoubleSignEvidence, at: int) -> Result:
    if not ev.validator or not ev.a.block_hash or not ev.b.block_hash:
        _fail("invalid", "missing validator or block hash")
    if ev.a.height == 0 or ev.a.height != ev.b.height:
        _fail("invalid", "votes must share a height")
    if ev.a.round != ev.b.round:
        _fail("invalid", "votes must share a round")
    if ev.a.block_hash == ev.b.block_hash:
        _fail("invalid", "votes are not conflicting")
    if at < ev.a.height:
        _fail("invalid", "evidence from the future")
    if at > ev.a.height + engine.params.window_blocks:
        _fail("stale", f"evidence window closed at {ev.a.height + engine.params.window_blocks}")
    _eligible(state, ev.validator)
    key = f"double|{ev.validator}|{ev.a.height}|{ev.a.round}"
    if engine.applied.get(key):
        _fail("duplicate", f"double-sign at {ev.a.height}")
    consensus_key = state.validators[ev.validator].consensus_key
    _verify_vote(engine.network, consensus_key, ev.validator, ev.a)
    _verify_vote(engine.network, consensus_key, ev.validator, ev.b)
    result = _apply(state, engine, "double_sign", ev.validator, engine.params.double_sign_bps, at)
    engine.applied[key] = True
    return result


def downtime(state: staking.State, engine: Engine, ev: DowntimeEvidence, at: int) -> Result:
    if not ev.validator or ev.from_ == 0 or ev.to < ev.from_:
        _fail("invalid", "invalid downtime window")
    if ev.total != ev.to - ev.from_ + 1:
        _fail("forged", "total does not match the window")
    if ev.missed > ev.total:
        _fail("forged", "missed above total")
    if ev.total < engine.params.downtime_min_blocks:
        _fail("insufficient", f"window shorter than {engine.params.downtime_min_blocks} blocks")
    if at < ev.to:
        _fail("invalid", "evidence from the future")
    if at > ev.to + engine.params.window_blocks:
        _fail("stale", f"evidence window closed at {ev.to + engine.params.window_blocks}")
    if ev.missed * 10_000 < ev.total * engine.params.downtime_threshold_bps:
        _fail("insufficient", "missed share below threshold")
    _eligible(state, ev.validator)
    key = f"downtime|{ev.validator}|{ev.from_}|{ev.to}"
    if engine.applied.get(key):
        _fail("duplicate", f"downtime window {ev.from_}-{ev.to}")
    result = _apply(state, engine, "downtime", ev.validator, engine.params.downtime_bps, at)
    engine.applied[key] = True
    return result


def _eligible(state: staking.State, validator: str) -> None:
    v = state.validators.get(validator)
    if v is None:
        _fail("not-eligible", f"validator {validator}")
    if v.status == staking.UNBONDED:
        _fail("not-eligible", "validator is unbonded")


def _verify_vote(network: str, key_hex: str, validator: str, vote: Vote) -> None:
    try:
        key = bytes.fromhex(key_hex)
        sig = bytes.fromhex(vote.signature)
    except ValueError:
        _fail("forged", "signature is not hex")
    if not sig:
        _fail("forged", "signature is not hex")
    if not bls.verify(key, vote_message(network, validator, vote.height, vote.round, vote.block_hash), sig):
        _fail("forged", "vote signature does not match the validator")


def _apply(
    state: staking.State, engine: Engine, kind: str, validator: str, bps: int, at: int
) -> Result:
    penalty = staking.penalize(state, validator, bps, at)
    jail_until = at + engine.params.jail_blocks
    staking.jail(state, validator, at, jail_until)
    split = split_penalty(penalty.total, engine.params)
    return Result(
        kind=kind, validator=validator, slashed=penalty.total,
        reporter=split.reporter, treasury=split.treasury,
        jail_until=jail_until, delegators=dict(penalty.delegators), burn=split.burn,
    )


def vote_message(network: str, validator: str, height: int, round_: int, block_hash: str) -> bytes:
    digest = hashlib.sha256()
    digest.update(b"arne-vote-v1")
    for part in (network, validator, str(height), str(round_), block_hash):
        digest.update(b"\x00" + part.encode())
    return digest.digest()
