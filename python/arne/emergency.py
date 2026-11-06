"""Multi-party emergency pause and veto with automatic expiry.

Mirrors internal/governance/emergency.go. Actions are bounded by the committee
period, recorded with their signers and reason, and emit audit events.
"""

from __future__ import annotations

import hashlib
from collections.abc import Callable
from dataclasses import dataclass, field

from arne import governance

ACTIVE = "active"
EXPIRED = "expired"
RATIFIED = "ratified"
REVOKED = "revoked"


@dataclass
class Action:
    id: str
    kind: str
    reason: str
    signers: list[str]
    at: int
    expires_at: int
    status: str = ACTIVE
    operations: list[str] = field(default_factory=list)
    proposal: str = ""
    ratified_by: str = ""


@dataclass(frozen=True)
class AuditEvent:
    action: str
    id: str
    at: int
    reason: str = ""
    signers: list[str] | None = None
    proposal: str = ""


def action_id(
    kind: str,
    proposal: str,
    operations: list[str],
    signers: list[str],
    reason: str,
    at: int,
) -> str:
    h = hashlib.sha256()
    for part in (
        "arne-emergency-v1", kind, proposal,
        ",".join(sorted(operations)), ",".join(sorted(signers)), reason, str(at),
    ):
        h.update(part.encode())
        h.update(b"\x00")
    return h.digest()[:16].hex()


class Emergency:
    """A multi-party committee that can pause operations or veto a proposal."""

    def __init__(
        self,
        members: list[str],
        threshold: int,
        period: int,
        audit: Callable[[AuditEvent], None] | None = None,
    ) -> None:
        if len(members) < 2:
            governance._fail("invalid", "at least two members are required")
        if len(set(members)) != len(members) or any(not m.strip() for m in members):
            governance._fail("invalid", "members must be distinct and non-blank")
        if threshold < 2 or threshold > len(members):
            governance._fail("invalid", f"threshold must be 2..{len(members)}")
        if period <= 0:
            governance._fail("invalid", "period must be positive")
        self.members = list(members)
        self.threshold = threshold
        self.period = period
        self.actions: list[Action] = []
        self.audit = audit

    def pause(
        self, signers: list[str], operations: list[str], reason: str, now: int
    ) -> Action:
        if not operations:
            governance._fail("invalid", "at least one operation is required")
        if any(not op.strip() for op in operations):
            governance._fail("invalid", "operation is blank")
        canonical = sorted(operations)
        if len(set(canonical)) != len(canonical):
            governance._fail("invalid", "duplicate operation")
        return self._record("pause", "", canonical, signers, reason, now)

    def veto(self, signers: list[str], proposal: str, reason: str, now: int) -> Action:
        if not proposal.strip():
            governance._fail("invalid", "proposal is required")
        return self._record("veto", proposal, [], signers, reason, now)

    def _record(
        self,
        kind: str,
        proposal: str,
        operations: list[str],
        signers: list[str],
        reason: str,
        now: int,
    ) -> Action:
        if not reason.strip():
            governance._fail("invalid", "a public reason is required")
        who = self._check_signers(signers)
        action = Action(
            id=action_id(kind, proposal, operations, who, reason, now),
            kind=kind, reason=reason, signers=who, at=now,
            expires_at=now + self.period, operations=operations, proposal=proposal,
        )
        self.actions.append(action)
        self._emit(AuditEvent(kind, action.id, now, reason, who, proposal))
        return action

    def _check_signers(self, signers: list[str]) -> list[str]:
        if len(signers) < self.threshold:
            governance._fail(
                "threshold", f"{len(signers)} of {self.threshold} required"
            )
        who = sorted(signers)
        for signer in who:
            if signer not in self.members:
                governance._fail("signer", f"{signer} is not on the committee")
        if len(set(who)) != len(who):
            governance._fail("signer", "duplicate signer")
        return who

    def advance(self, now: int) -> list[str]:
        expired = []
        for action in self.actions:
            if action.status == ACTIVE and now >= action.expires_at:
                action.status = EXPIRED
                expired.append(action.id)
                self._emit(
                    AuditEvent("expire", action.id, now, action.reason, None, action.proposal)
                )
        return sorted(expired)

    def paused(self, operation: str, now: int) -> bool:
        for action in self.actions:
            if action.kind != "pause" or not self._active(action, now):
                continue
            if operation in action.operations or "all" in action.operations:
                return True
        return False

    def vetoed(self, proposal: str, now: int) -> bool:
        return any(
            action.kind == "veto"
            and action.proposal == proposal
            and self._active(action, now)
            for action in self.actions
        )

    @staticmethod
    def _active(action: Action, now: int) -> bool:
        return action.status == ACTIVE and now < action.expires_at

    def revoke(self, id_: str, signers: list[str], reason: str, now: int) -> None:
        action = self._find(id_)
        if action is None:
            governance._fail("unknown", f"action {id_}")
        if action.status != ACTIVE:
            governance._fail("status", f"action is {action.status}")
        if not reason.strip():
            governance._fail("invalid", "a public reason is required")
        who = self._check_signers(signers)
        action.status = REVOKED
        self._emit(AuditEvent("revoke", id_, now, reason, who, action.proposal))

    def ratify(self, id_: str, proposal: str, now: int) -> None:
        if not proposal.strip():
            governance._fail("invalid", "a governance proposal is required")
        action = self._find(id_)
        if action is None:
            governance._fail("unknown", f"action {id_}")
        if action.status not in (ACTIVE, EXPIRED):
            governance._fail("status", f"action is {action.status}")
        action.status = RATIFIED
        action.ratified_by = proposal
        self._emit(AuditEvent("ratify", id_, now, proposal, None, action.proposal))

    def _find(self, id_: str) -> Action | None:
        return next((action for action in self.actions if action.id == id_), None)

    def _emit(self, event: AuditEvent) -> None:
        if self.audit is not None:
            self.audit(event)
