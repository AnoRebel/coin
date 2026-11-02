"""Deterministic validator lifecycle, stake, delegation, and epochs.

Mirrors internal/staking. Consensus logic only: callers move ARCE, then apply
the resulting set to the consensus engine. Registration verifies a BLS proof
of possession so a rogue key cannot enter an aggregate.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from arne import bls

PENDING = "pending"
ACTIVE = "active"
JAILED = "jailed"
UNBONDING = "unbonding"
UNBONDED = "unbonded"


class StakingError(Exception):
    """A rejected staking operation, with a stable code for conformance."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise StakingError(code, message)


@dataclass(frozen=True)
class Params:
    min_self_bond: int
    min_stake: int
    max_power_bps: int
    epoch_blocks: int
    unbonding_blocks: int


@dataclass
class Validator:
    address: str
    consensus_key: str
    pop: str
    commission_bps: int
    self_bond: int
    status: str = PENDING
    jailed_until: int = 0
    unbonding_end: int = 0


@dataclass(frozen=True)
class UnbondingEntry:
    validator: str
    delegator: str
    amount: int
    release: int


@dataclass
class State:
    network: str
    params: Params
    height: int = 0
    epoch: int = 0
    validators: dict[str, Validator] = field(default_factory=dict)
    delegations: dict[str, dict[str, int]] = field(default_factory=dict)
    unbonding: list[UnbondingEntry] = field(default_factory=list)
    powers: dict[str, int] = field(default_factory=dict)
    active: list[str] = field(default_factory=list)


def new(network: str, params: Params) -> State:
    if not network:
        _fail("invalid", "network is required")
    if params.epoch_blocks == 0:
        _fail("invalid", "epoch_blocks must be positive")
    if params.min_self_bond == 0 or params.min_stake == 0:
        _fail("invalid", "stake minimums must be positive")
    if params.max_power_bps > 10_000:
        _fail("invalid", "max_power_bps above 10000")
    return State(network=network, params=params)


def register(state: State, validator: Validator, at: int) -> None:
    if not validator.address:
        _fail("invalid", "validator address is required")
    if validator.address in state.validators:
        _fail("duplicate", f"validator {validator.address}")
    if validator.commission_bps > 10_000:
        _fail("commission", "commission above 10000 bps")
    if validator.self_bond < state.params.min_self_bond:
        _fail("self-bond", f"self-bond below {state.params.min_self_bond}")
    try:
        key = bytes.fromhex(validator.consensus_key)
        pop = bytes.fromhex(validator.pop)
    except ValueError as exc:
        _fail("invalid", f"consensus key or proof is not hex: {exc}")
    if not key:
        _fail("invalid", "consensus key is not hex")
    if not pop:
        _fail("invalid", "proof of possession is not hex")
    if not bls.verify(key, bls.pop_message(state.network, validator.address, key), pop):
        _fail("pop", "proof of possession does not match the key")
    _touch(state, at)
    validator.status = PENDING
    state.validators[validator.address] = validator
    state.delegations[validator.address] = {validator.address: validator.self_bond}


def delegate(state: State, validator: str, delegator: str, amount: int, at: int) -> None:
    v = state.validators.get(validator)
    if v is None:
        _fail("unknown", f"validator {validator}")
    if v.status == UNBONDED:
        _fail("status", "validator is unbonded")
    if not delegator:
        _fail("invalid", "delegator is required")
    if amount <= 0:
        _fail("invalid", "amount must be positive")
    _touch(state, at)
    state.delegations.setdefault(validator, {})
    state.delegations[validator][delegator] = state.delegations[validator].get(delegator, 0) + amount
    if delegator == validator:
        v.self_bond += amount


def undelegate(state: State, validator: str, delegator: str, amount: int, at: int) -> None:
    v = state.validators.get(validator)
    if v is None:
        _fail("unknown", f"validator {validator}")
    if v.status == UNBONDED:
        _fail("status", "validator is unbonded")
    if amount <= 0:
        _fail("invalid", "amount must be positive")
    held = state.delegations.get(validator, {}).get(delegator, 0)
    if amount > held:
        _fail("insufficient", f"undelegate {amount} above stake {held}")
    _touch(state, at)
    state.delegations[validator][delegator] = held - amount
    if delegator == validator:
        v.self_bond -= amount
    state.unbonding.append(UnbondingEntry(
        validator=validator, delegator=delegator, amount=amount,
        release=at + state.params.unbonding_blocks,
    ))


def begin_unbonding(state: State, validator: str, at: int) -> None:
    v = state.validators.get(validator)
    if v is None:
        _fail("unknown", f"validator {validator}")
    if v.status not in (PENDING, ACTIVE):
        _fail("status", f"cannot unbond from {v.status}")
    _touch(state, at)
    v.status = UNBONDING
    v.unbonding_end = at + state.params.unbonding_blocks


def jail(state: State, validator: str, at: int, until: int) -> None:
    v = state.validators.get(validator)
    if v is None:
        _fail("unknown", f"validator {validator}")
    if v.status == UNBONDED:
        _fail("status", "validator is unbonded")
    if until <= at:
        _fail("invalid", "jail end must be after the current height")
    _touch(state, at)
    v.status = JAILED
    v.jailed_until = until


def unjail(state: State, validator: str, at: int) -> None:
    v = state.validators.get(validator)
    if v is None:
        _fail("unknown", f"validator {validator}")
    if v.status != JAILED:
        _fail("status", f"validator is {v.status}, not jailed")
    if at < v.jailed_until:
        _fail("status", f"jail ends at {v.jailed_until}")
    _touch(state, at)
    v.status = PENDING


def advance_epoch(state: State, at: int) -> None:
    next_height = (state.epoch + 1) * state.params.epoch_blocks
    if at != next_height:
        _fail("epoch", f"epoch {state.epoch + 1} starts at {next_height}")
    state.height = at
    state.epoch += 1

    state.unbonding = [u for u in state.unbonding if u.release > at]

    for v in state.validators.values():
        if v.status == UNBONDING and v.unbonding_end <= at:
            v.status = UNBONDED
            v.self_bond = 0
            state.delegations.pop(v.address, None)
            continue
        if v.status in (JAILED, UNBONDED, UNBONDING):
            continue
        v.status = ACTIVE if _eligible(state, v) else PENDING

    candidates = sorted(
        address for address, v in state.validators.items() if v.status == ACTIVE
    )
    total = sum(stake(state, address) for address in candidates)
    limit = total
    if 0 < state.params.max_power_bps < 10_000:
        limit = total * state.params.max_power_bps // 10_000

    state.active = candidates
    state.powers = {
        address: min(stake(state, address), limit) for address in candidates
    }


def _eligible(state: State, v: Validator) -> bool:
    if v.self_bond < state.params.min_self_bond:
        return False
    return stake(state, v.address) >= state.params.min_stake


def _touch(state: State, at: int) -> None:
    if at < state.height:
        _fail("invalid", f"height {at} is before {state.height}")
    state.height = at


def stake(state: State, validator: str) -> int:
    return sum(state.delegations.get(validator, {}).values())


def voting_power(state: State, validator: str) -> int:
    return state.powers.get(validator, 0)


@dataclass(frozen=True)
class Penalty:
    total: int
    delegators: dict[str, int]


def penalize(state: State, validator: str, bps: int, at: int) -> Penalty:
    """Remove a percentage of bonded stake and unbonding stake.

    Does not change the validator status; the slashing policy jails separately.
    """
    v = state.validators.get(validator)
    if v is None:
        _fail("unknown", f"validator {validator}")
    if v.status == UNBONDED:
        _fail("status", "validator is unbonded")
    if not 0 < bps <= 10_000:
        _fail("invalid", "slash bps must be in 1..10000")
    _touch(state, at)
    cuts: dict[str, int] = {}
    total = 0
    for delegator, amount in state.delegations.get(validator, {}).items():
        cut = amount * bps // 10_000
        state.delegations[validator][delegator] = amount - cut
        if delegator == validator:
            v.self_bond -= cut
        cuts[delegator] = cuts.get(delegator, 0) + cut
        total += cut
    for i, u in enumerate(state.unbonding):
        if u.validator != validator:
            continue
        cut = u.amount * bps // 10_000
        state.unbonding[i] = UnbondingEntry(u.validator, u.delegator, u.amount - cut, u.release)
        cuts[u.delegator] = cuts.get(u.delegator, 0) + cut
        total += cut
    return Penalty(total=total, delegators=cuts)



def snapshot(state: State) -> dict:
    """Return the deterministic shape used by the shared vectors."""
    views = []
    for address in sorted(state.validators):
        v = state.validators[address]
        total = stake(state, address)
        views.append({
            "address": address,
            "status": v.status,
            "self_bond": v.self_bond,
            "delegated": total - v.self_bond,
            "stake": total,
            "voting_power": state.powers.get(address, 0),
            "commission_bps": v.commission_bps,
        })
    unbonding = sorted(
        state.unbonding,
        key=lambda u: (u.validator, u.delegator, u.release),
    )
    return {
        "epoch": state.epoch,
        "height": state.height,
        "active": list(state.active),
        "validators": views,
        "unbonding": [u.__dict__ for u in unbonding],
    }
