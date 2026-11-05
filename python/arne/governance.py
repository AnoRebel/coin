"""On-chain proposal lifecycles: deposit, voting, timelock, execution.

Mirrors internal/governance. Consensus logic only: callers supply voting power
and action handlers. A failing action reverts every earlier action.
"""

from __future__ import annotations

import hashlib
from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any

PARAMETER = "parameter"
CODE_UPGRADE = "code_upgrade"
TREASURY = "treasury"
VALIDATOR_ADMISSION = "validator_admission"
ASSET_POLICY = "asset_policy"
COMPLIANCE_POLICY = "compliance_policy"
SUITE_CHANGE = "suite_change"
CONSTITUTIONAL = "constitutional"

KINDS = {
    PARAMETER: True, CODE_UPGRADE: True, TREASURY: True,
    VALIDATOR_ADMISSION: True, ASSET_POLICY: True, COMPLIANCE_POLICY: True,
    SUITE_CHANGE: True, CONSTITUTIONAL: True,
}

YES = "yes"
NO = "no"
ABSTAIN = "abstain"

DEPOSIT = "deposit"
VOTING = "voting"
PASSED = "passed"
REJECTED = "rejected"
TIMELOCK = "timelock"
EXECUTED = "executed"
CANCELLED = "cancelled"
FAILED = "failed"


class GovernanceError(Exception):
    """A rejected governance operation, with a stable code."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise GovernanceError(code, message)


@dataclass(frozen=True)
class Params:
    min_deposit: int
    deposit_period: int
    voting_period: int
    timelock: int
    quorum_bps: int
    threshold_bps: int
    constitutional_threshold_bps: int
    constitutional_timelock: int
    max_actions: int


@dataclass(frozen=True)
class Action:
    type: str
    params: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Vote:
    option: str
    weight: int
    at: int


@dataclass
class Proposal:
    kind: str
    proposer: str
    nonce: int
    actions: list[Action]
    deposit: int
    activation_height: int
    title: str = ""
    network: str = ""
    id: str = ""
    status: str = ""
    created_at: int = 0
    voting_start: int = 0
    voting_end: int = 0
    timelock_end: int = 0
    total_power: int = 0
    yes: int = 0
    no: int = 0
    abstain: int = 0
    votes: dict[str, Vote] = field(default_factory=dict)
    refunded: bool = False
    execution_error: str = ""


def proposal_id(network: str, proposer: str, nonce: int) -> str:
    h = hashlib.sha256()
    h.update(b"arne-proposal-v1")
    h.update(b"\x00")
    h.update(network.encode())
    h.update(b"\x00")
    h.update(proposer.encode())
    h.update(b"\x00")
    h.update(str(nonce).encode())
    return h.digest()[:16].hex()


@dataclass
class State:
    network: str
    params: Params
    proposals: dict[str, Proposal] = field(default_factory=dict)


def new(network: str, params: Params) -> State:
    if not network:
        _fail("invalid", "network is required")
    if params.voting_period == 0:
        _fail("invalid", "voting_period must be positive")
    if params.max_actions <= 0:
        _fail("invalid", "max_actions must be positive")
    if (
        params.quorum_bps > 10_000
        or params.threshold_bps > 10_000
        or params.constitutional_threshold_bps > 10_000
    ):
        _fail("invalid", "bps above 10000")
    if params.constitutional_threshold_bps < params.threshold_bps:
        _fail("invalid", "constitutional threshold below ordinary")
    if params.constitutional_timelock < params.timelock:
        _fail("invalid", "constitutional timelock below ordinary")
    return State(network=network, params=params)


def submit(state: State, proposal: Proposal, total_power: int, now: int) -> None:
    if not proposal.proposer:
        _fail("invalid", "proposer is required")
    if proposal.kind not in KINDS:
        _fail("invalid", f"kind {proposal.kind!r}")
    if proposal.deposit < state.params.min_deposit:
        _fail("invalid", f"deposit below {state.params.min_deposit}")
    if proposal.activation_height == 0:
        _fail("invalid", "activation_height is required")
    if total_power == 0:
        _fail("invalid", "total voting power is zero")
    if not proposal.actions or len(proposal.actions) > state.params.max_actions:
        _fail("invalid", f"actions must be 1..{state.params.max_actions}")
    for action in proposal.actions:
        if not action.type.strip():
            _fail("invalid", "action type is required")
    if not proposal.id:
        proposal.id = proposal_id(state.network, proposal.proposer, proposal.nonce)
    if proposal.id in state.proposals:
        _fail("duplicate", f"proposal {proposal.id}")
    proposal.network = state.network
    proposal.status = DEPOSIT
    proposal.created_at = now
    proposal.voting_start = now + state.params.deposit_period
    proposal.voting_end = proposal.voting_start + state.params.voting_period
    proposal.total_power = total_power
    proposal.votes = {}
    if state.params.deposit_period == 0:
        proposal.status = VOTING
    state.proposals[proposal.id] = proposal


def get(state: State, proposal_id_: str) -> Proposal:
    proposal = state.proposals.get(proposal_id_)
    if proposal is None:
        _fail("unknown", f"proposal {proposal_id_}")
    return proposal


def advance(state: State, now: int) -> list[str]:
    changed: list[str] = []
    for pid in sorted(state.proposals):
        proposal = state.proposals[pid]
        if proposal.status == DEPOSIT and now >= proposal.voting_start:
            proposal.status = VOTING
            changed.append(pid)
        elif proposal.status == VOTING and now >= proposal.voting_end:
            _tally(state, proposal)
            changed.append(pid)
    return changed


def _tally(state: State, proposal: Proposal) -> None:
    cast = proposal.yes + proposal.no + proposal.abstain
    if cast * 10_000 < proposal.total_power * state.params.quorum_bps:
        proposal.status = REJECTED
        return
    threshold = state.params.threshold_bps
    if proposal.kind == CONSTITUTIONAL:
        threshold = state.params.constitutional_threshold_bps
    decided = proposal.yes + proposal.no
    if decided == 0 or proposal.yes * 10_000 < decided * threshold:
        proposal.status = REJECTED
        return
    proposal.status = PASSED


def vote(
    state: State, pid: str, voter: str, option: str, weight: int, now: int
) -> None:
    proposal = state.proposals.get(pid)
    if proposal is None:
        _fail("unknown", f"proposal {pid}")
    if not voter:
        _fail("invalid", "voter is required")
    if weight == 0:
        _fail("invalid", "weight must be positive")
    if option not in (YES, NO, ABSTAIN):
        _fail("invalid", f"option {option!r}")
    if (
        proposal.status != VOTING
        or now < proposal.voting_start
        or now >= proposal.voting_end
    ):
        _fail("status", f"proposal is {proposal.status}")
    if voter in proposal.votes:
        _fail("voted", f"voter {voter}")
    if proposal.yes + proposal.no + proposal.abstain + weight > proposal.total_power:
        _fail("power", "cast above snapshot power")
    proposal.votes[voter] = Vote(option=option, weight=weight, at=now)
    if option == YES:
        proposal.yes += weight
    elif option == NO:
        proposal.no += weight
    else:
        proposal.abstain += weight


def queue(state: State, pid: str, now: int) -> None:
    proposal = state.proposals.get(pid)
    if proposal is None:
        _fail("unknown", f"proposal {pid}")
    if proposal.status != PASSED:
        _fail("status", f"proposal is {proposal.status}")
    timelock = state.params.timelock
    if proposal.kind == CONSTITUTIONAL:
        timelock = state.params.constitutional_timelock
    proposal.timelock_end = now + timelock
    proposal.status = TIMELOCK


Handler = Callable[[Action, Proposal], Callable[[], None] | None]


class Registry:
    """Maps action types to handlers and runs them atomically."""

    def __init__(self) -> None:
        self.handlers: dict[str, Handler] = {}

    def register(self, action_type: str, handler: Handler) -> None:
        if not action_type or handler is None:
            _fail("invalid", "action type and handler are required")
        if action_type in self.handlers:
            _fail("duplicate", f"action {action_type}")
        self.handlers[action_type] = handler

    def apply(self, proposal: Proposal) -> None:
        for action in proposal.actions:
            if action.type not in self.handlers:
                _fail("action", f"no handler for {action.type}")
        undos: list[Callable[[], None]] = []
        for action in proposal.actions:
            try:
                undo = self.handlers[action.type](action, proposal)
            except Exception as exc:
                for prior in reversed(undos):
                    prior()
                raise exc
            if undo is not None:
                undos.append(undo)


def execute(state: State, pid: str, now: int, registry: Registry) -> None:
    proposal = state.proposals.get(pid)
    if proposal is None:
        _fail("unknown", f"proposal {pid}")
    if proposal.status != TIMELOCK:
        _fail("status", f"proposal is {proposal.status}")
    if now < proposal.timelock_end:
        _fail("status", f"timelock ends at {proposal.timelock_end}")
    try:
        registry.apply(proposal)
    except Exception as exc:
        proposal.status = FAILED
        proposal.execution_error = str(exc)
        raise
    proposal.status = EXECUTED
    proposal.execution_error = ""


def cancel(state: State, pid: str, actor: str, now: int) -> None:
    proposal = state.proposals.get(pid)
    if proposal is None:
        _fail("unknown", f"proposal {pid}")
    if actor != proposal.proposer:
        _fail("unauthorized", "only the proposer may cancel")
    if proposal.status not in (DEPOSIT, VOTING, PASSED, TIMELOCK):
        _fail("status", f"proposal is {proposal.status}")
    proposal.refunded = proposal.yes + proposal.no + proposal.abstain == 0
    proposal.status = CANCELLED
