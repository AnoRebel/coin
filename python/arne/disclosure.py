"""Grant-scoped disclosure for sealed memos.

Mirrors internal/compliance/disclosure. A memo opens only through a grant, and a
grant reaches only the transfers in its scope. A user grant is issued by the data
owner; an exceptional grant needs two distinct approvers and a legal-process
reference. Every grant and every open or denied open is logged.
"""

from __future__ import annotations

import hashlib
import threading
import time
from dataclasses import dataclass, field

from . import memo

USER = "user"
EXCEPTIONAL = "exceptional"


class DisclosureError(Exception):
    pass


class InvalidGrant(DisclosureError):
    pass


class NoGrant(DisclosureError):
    pass


class Forbidden(DisclosureError):
    pass


class NotActive(DisclosureError):
    pass


class Expired(DisclosureError):
    pass


class OutOfScope(DisclosureError):
    pass


class NoMemo(DisclosureError):
    pass


class DecryptFailed(DisclosureError):
    pass


@dataclass
class Grant:
    kind: str
    subject: str
    scope: list[str]
    from_ts: int
    until: int
    purpose: str
    granted_by: str
    granted_at: int
    approvers: list[str] = field(default_factory=list)
    legal_process: str = ""
    id: str = ""


@dataclass
class Event:
    seq: int
    time: int
    result: str
    actor: str = ""
    grant: str = ""
    kind: str = ""
    transfer: str = ""
    reason: str = ""
    scope: list[str] = field(default_factory=list)
    purpose: str = ""
    approvers: list[str] = field(default_factory=list)

    def to_dict(self) -> dict[str, object]:
        out: dict[str, object] = {"seq": self.seq, "time": self.time, "result": self.result}
        for name in ("actor", "grant", "kind", "transfer", "reason", "purpose"):
            value = getattr(self, name)
            if value:
                out[name] = value
        if self.scope:
            out["scope"] = list(self.scope)
        if self.approvers:
            out["approvers"] = list(self.approvers)
        return out


class Manager:
    def __init__(self) -> None:
        self._memos: dict[str, bytes] = {}
        self._grants: dict[str, Grant] = {}
        self._events: list[Event] = []
        self._lock = threading.Lock()

    def add_memo(self, transfer: str, sealed: bytes) -> None:
        if not transfer.strip() or not sealed:
            raise InvalidGrant("transfer and sealed memo are required")
        try:
            memo.validate(sealed)
        except memo.MemoError as exc:
            raise InvalidGrant(str(exc)) from exc
        with self._lock:
            if transfer in self._memos:
                raise InvalidGrant(f"transfer {transfer!r} already registered")
            self._memos[transfer] = bytes(sealed)

    def grant(self, grant: Grant) -> Grant:
        grant.subject = grant.subject.strip()
        grant.purpose = grant.purpose.strip()
        grant.granted_by = grant.granted_by.strip()
        grant.legal_process = grant.legal_process.strip()
        grant.scope = _normalize(grant.scope)
        grant.approvers = _normalize(grant.approvers)
        _validate(grant)
        if not grant.id:
            grant.id = _grant_id(grant)
        with self._lock:
            if grant.id in self._grants:
                raise InvalidGrant(f"grant {grant.id!r} already exists")
            self._grants[grant.id] = grant
            self._append(Event(
                seq=0, time=grant.granted_at, result="granted", actor=grant.granted_by,
                grant=grant.id, kind=grant.kind, scope=list(grant.scope),
                purpose=grant.purpose, approvers=list(grant.approvers),
            ))
        return grant

    def grant_by_id(self, grant_id: str) -> Grant | None:
        with self._lock:
            return self._grants.get(grant_id)

    def open(self, actor: str, grant_id: str, transfer: str, seed: bytes, at: int) -> bytes:
        with self._lock:
            grant = self._grants.get(grant_id)
            sealed = self._memos.get(transfer)
        if grant is None:
            self._deny(actor, grant_id, "", transfer, at, "no grant")
            raise NoGrant(grant_id)
        if grant.subject != actor.strip():
            self._deny(actor, grant_id, grant.kind, transfer, at, "not grant subject")
            raise Forbidden(actor)
        if at < grant.from_ts:
            self._deny(actor, grant_id, grant.kind, transfer, at, "not active yet")
            raise NotActive(grant_id)
        if at >= grant.until:
            self._deny(actor, grant_id, grant.kind, transfer, at, "expired")
            raise Expired(grant_id)
        if transfer not in grant.scope:
            self._deny(actor, grant_id, grant.kind, transfer, at, "outside scope")
            raise OutOfScope(transfer)
        if sealed is None:
            self._deny(actor, grant_id, grant.kind, transfer, at, "unknown transfer")
            raise NoMemo(transfer)
        try:
            plaintext = memo.open_memo(seed, sealed)
        except memo.MemoError as exc:
            self._deny(actor, grant_id, grant.kind, transfer, at, "decrypt failed")
            raise DecryptFailed(transfer) from exc
        with self._lock:
            self._append(Event(
                seq=0, time=at, result="opened", actor=actor, grant=grant.id,
                kind=grant.kind, transfer=transfer, scope=list(grant.scope),
                purpose=grant.purpose, approvers=list(grant.approvers),
            ))
        return plaintext

    def events(self, after: int = 0, limit: int = 100) -> list[dict[str, object]]:
        limit = 100 if limit <= 0 else min(limit, 1000)
        with self._lock:
            return [event.to_dict() for event in self._events if event.seq > after][:limit]

    def _deny(self, actor: str, grant_id: str, kind: str, transfer: str, at: int, reason: str) -> None:
        with self._lock:
            self._append(Event(
                seq=0, time=at or int(time.time()), result="denied", actor=actor,
                grant=grant_id, kind=kind, transfer=transfer, reason=reason,
            ))

    def _append(self, event: Event) -> None:
        event.seq = self._events[-1].seq + 1 if self._events else 1
        if not event.time:
            event.time = int(time.time())
        self._events.append(event)


def _validate(grant: Grant) -> None:
    if not grant.subject:
        raise InvalidGrant("subject is required")
    if not grant.purpose:
        raise InvalidGrant("purpose is required")
    if not 1 <= len(grant.scope) <= 64:
        raise InvalidGrant("scope must list 1 to 64 transfers")
    if grant.from_ts <= 0 or grant.until <= grant.from_ts:
        raise InvalidGrant("until must be after a positive from")
    if grant.granted_at <= 0:
        raise InvalidGrant("granted_at is required")
    if grant.kind == USER:
        if grant.approvers or grant.legal_process:
            raise InvalidGrant("a user grant takes no approvers or legal process")
    elif grant.kind == EXCEPTIONAL:
        if len(grant.approvers) < 2:
            raise InvalidGrant("exceptional access needs at least two approvers")
        if not grant.legal_process:
            raise InvalidGrant("exceptional access needs a legal process reference")
    else:
        raise InvalidGrant("kind must be user or exceptional")


def _normalize(values: list[str]) -> list[str]:
    return sorted({value.strip() for value in values if value.strip()})


def _grant_id(grant: Grant) -> str:
    digest = hashlib.sha256()
    digest.update(b"arne-disclosure-grant-v1")
    parts = [
        grant.kind, grant.subject.strip(), grant.purpose.strip(),
        str(grant.from_ts), str(grant.until),
        *_normalize(grant.scope), "", *_normalize(grant.approvers),
    ]
    for part in parts:
        digest.update(b"\x00" + part.encode())
    return digest.digest()[:16].hex()
