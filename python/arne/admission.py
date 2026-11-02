"""Public and consortium validator admission.

Mirrors internal/admission. Public mode admits any registered validator that
meets staking minimums; consortium mode needs distinct approvals from the
authorized policy before activation.
"""

from __future__ import annotations

from dataclasses import dataclass, field

PUBLIC = "public"
CONSORTIUM = "consortium"


class AdmissionError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise AdmissionError(code, message)


@dataclass(frozen=True)
class Policy:
    mode: str
    approvers: tuple[str, ...] = ()
    threshold: int = 0


@dataclass
class Approval:
    validator: str
    approvers: list[str] = field(default_factory=list)
    height: int = 0


@dataclass
class State:
    policy: Policy
    approvals: dict[str, Approval] = field(default_factory=dict)


def new(policy: Policy) -> State:
    if policy.mode not in (PUBLIC, CONSORTIUM):
        _fail("invalid", f"mode {policy.mode!r}")
    approvers = list(policy.approvers)
    if len(set(approvers)) != len(approvers) or any(not a for a in approvers):
        _fail("invalid", "approvers must be non-empty and distinct")
    approvers.sort()
    if policy.mode == CONSORTIUM:
        if not approvers:
            _fail("invalid", "consortium needs approvers")
        if not 0 < policy.threshold <= len(approvers):
            _fail("invalid", f"threshold must be in 1..{len(approvers)}")
    elif policy.threshold != 0 or approvers:
        _fail("invalid", "public mode has no approvers")
    return State(policy=Policy(policy.mode, tuple(approvers), policy.threshold))


def allowed(state: State, validator: str) -> bool:
    if state.policy.mode == PUBLIC:
        return True
    record = state.approvals.get(validator)
    return record is not None and len(record.approvers) >= state.policy.threshold


def approve(state: State, validator: str, approver: str, at: int) -> None:
    if state.policy.mode != CONSORTIUM:
        _fail("wrong mode", "public validators do not need approval")
    if not validator or not approver:
        _fail("invalid", "validator and approver are required")
    if approver not in state.policy.approvers:
        _fail("unauthorized approver", approver)
    record = state.approvals.get(validator)
    if record is None:
        record = Approval(validator=validator)
        state.approvals[validator] = record
    if len(record.approvers) >= state.policy.threshold:
        _fail("already approved", f"validator {validator}")
    if approver in record.approvers:
        _fail("duplicate approval", approver)
    record.approvers.append(approver)
    record.approvers.sort()
    if len(record.approvers) == state.policy.threshold:
        record.height = at


def approved(state: State) -> dict[str, bool]:
    return {v: True for v in state.approvals if allowed(state, v)}


def status(state: State, validator: str) -> str:
    if state.policy.mode == PUBLIC:
        return "public"
    return "approved" if allowed(state, validator) else "pending"
