"""Data retention, legal hold, deletion/anonymization, and tombstones.

Mirrors internal/compliance/retention. Deleting an off-chain record leaves a
tombstone with its ledger reference and content hash so on-chain proofs stay
verifiable. A legal hold suspends deletion and anonymization.
"""

from __future__ import annotations

import hashlib
import threading
import time
from dataclasses import dataclass, field

DELETE = "delete"
ANONYMIZE = "anonymize"


class RetentionError(Exception):
    pass


class InvalidPolicy(RetentionError):
    pass


class AlreadyExists(RetentionError):
    pass


class UnknownRecord(RetentionError):
    pass


class OnChainRecord(RetentionError):
    pass


@dataclass
class Policy:
    cls: str
    days: int
    action: str
    anonymize_fields: list[str] = field(default_factory=list)


@dataclass
class Record:
    id: str
    cls: str
    created_at: int
    on_chain: bool = False
    ledger_ref: str = ""
    fields: dict[str, str] = field(default_factory=dict)


@dataclass
class Hold:
    record: str
    reason: str
    placed_by: str
    placed_at: int
    id: str = ""


@dataclass
class Tombstone:
    record: str
    cls: str
    ledger_ref: str
    content_hash: str
    action: str
    deleted_at: int

    def to_dict(self) -> dict[str, object]:
        return {
            "record": self.record, "class": self.cls, "ledger_ref": self.ledger_ref,
            "content_hash": self.content_hash, "action": self.action,
            "deleted_at": self.deleted_at,
        }


@dataclass
class Event:
    seq: int
    time: int
    record: str
    action: str
    result: str
    cls: str = ""
    reason: str = ""

    def to_dict(self) -> dict[str, object]:
        out: dict[str, object] = {
            "seq": self.seq, "time": self.time, "record": self.record,
            "action": self.action, "result": self.result,
        }
        if self.cls:
            out["class"] = self.cls
        if self.reason:
            out["reason"] = self.reason
        return out


@dataclass
class Result:
    deleted: list[str] = field(default_factory=list)
    anonymized: list[str] = field(default_factory=list)
    retained: list[str] = field(default_factory=list)
    held: list[str] = field(default_factory=list)
    tombstones: list[Tombstone] = field(default_factory=list)


class Manager:
    def __init__(self, policies: list[Policy]) -> None:
        self._policies: dict[str, Policy] = {}
        self._records: dict[str, Record] = {}
        self._holds: dict[str, Hold] = {}
        self._tombstones: list[Tombstone] = []
        self._events: list[Event] = []
        self._lock = threading.Lock()
        for index, policy in enumerate(policies):
            policy.cls = policy.cls.strip()
            _validate_policy(policy, index)
            if policy.cls in self._policies:
                raise InvalidPolicy(f"duplicate class {policy.cls!r}")
            self._policies[policy.cls] = policy

    def add(self, record: Record) -> None:
        if record.on_chain:
            raise OnChainRecord("on-chain records are immutable")
        if not record.id.strip() or not record.cls.strip() or record.created_at <= 0:
            raise InvalidPolicy("id, class, and created_at are required")
        with self._lock:
            if record.id in self._records:
                raise AlreadyExists(f"record {record.id!r}")
            self._records[record.id] = record

    def record(self, record_id: str) -> Record | None:
        with self._lock:
            return self._records.get(record_id)

    def hold(self, hold: Hold) -> Hold:
        if not hold.record.strip() or not hold.reason.strip() or not hold.placed_by.strip() or hold.placed_at <= 0:
            raise InvalidPolicy("record, reason, placed_by, and placed_at are required")
        with self._lock:
            record = self._records.get(hold.record)
            if record is None:
                raise UnknownRecord(hold.record)
            hold.id = hold.id or _hold_id(hold)
            if hold.id in self._holds:
                raise AlreadyExists(f"hold {hold.id!r}")
            self._holds[hold.id] = hold
            self._append(Event(0, hold.placed_at, record.id, "hold", "placed",
                               cls=record.cls, reason=hold.reason))
        return hold

    def release(self, hold_id: str, by: str, at: int = 0) -> None:
        with self._lock:
            hold = self._holds.get(hold_id)
            if hold is None:
                raise UnknownRecord(hold_id)
            del self._holds[hold_id]
            self._append(Event(0, at or int(time.time()), hold.record, "release", "released",
                               reason=f"released by {by.strip()}"))

    def apply(self, at: int) -> Result:
        with self._lock:
            result = Result()
            for record_id in sorted(self._records):
                record = self._records[record_id]
                policy = self._policies.get(record.cls)
                if policy is None:
                    result.retained.append(record_id)
                    self._append(Event(0, at, record_id, "retain", "retained",
                                       cls=record.cls, reason="no retention policy"))
                    continue
                if at - record.created_at < policy.days * 86400:
                    result.retained.append(record_id)
                    self._append(Event(0, at, record_id, "retain", "retained",
                                       cls=record.cls, reason="within retention"))
                    continue
                hold = self._hold_for(record_id)
                if hold is not None:
                    result.held.append(record_id)
                    self._append(Event(0, at, record_id, "retain", "held",
                                       cls=record.cls, reason=f"legal hold {hold.id}"))
                    continue
                if policy.action == DELETE:
                    tombstone = Tombstone(
                        record=record_id, cls=record.cls, ledger_ref=record.ledger_ref,
                        content_hash=_content_hash(record), action=DELETE, deleted_at=at,
                    )
                    result.tombstones.append(tombstone)
                    del self._records[record_id]
                    result.deleted.append(record_id)
                    self._append(Event(0, at, record_id, "delete", "deleted", cls=record.cls))
                elif policy.action == ANONYMIZE:
                    for name in policy.anonymize_fields:
                        if name in record.fields:
                            record.fields[name] = ""
                    result.anonymized.append(record_id)
                    self._append(Event(0, at, record_id, "anonymize", "anonymized", cls=record.cls))
            self._tombstones.extend(result.tombstones)
            return result

    def tombstones(self) -> list[Tombstone]:
        with self._lock:
            return list(self._tombstones)

    def events(self, after: int = 0, limit: int = 100) -> list[dict[str, object]]:
        limit = 100 if limit <= 0 else min(limit, 1000)
        with self._lock:
            return [event.to_dict() for event in self._events if event.seq > after][:limit]

    def _hold_for(self, record_id: str) -> Hold | None:
        matching = [hold for hold in self._holds.values() if hold.record == record_id]
        return min(matching, key=lambda hold: hold.id) if matching else None

    def _append(self, event: Event) -> None:
        event.seq = self._events[-1].seq + 1 if self._events else 1
        if not event.time:
            event.time = int(time.time())
        self._events.append(event)


def _validate_policy(policy: Policy, index: int) -> None:
    if not policy.cls:
        raise InvalidPolicy(f"policies[{index}]: class is required")
    if policy.days <= 0:
        raise InvalidPolicy(f"class {policy.cls!r}: days must be positive")
    if policy.action == DELETE:
        if policy.anonymize_fields:
            raise InvalidPolicy(f"class {policy.cls!r}: delete takes no anonymize_fields")
    elif policy.action == ANONYMIZE:
        if not policy.anonymize_fields:
            raise InvalidPolicy(f"class {policy.cls!r}: anonymize requires anonymize_fields")
    else:
        raise InvalidPolicy(f"class {policy.cls!r}: action must be delete or anonymize")


def _hold_id(hold: Hold) -> str:
    parts = [hold.record, hold.reason, hold.placed_by, str(hold.placed_at)]
    digest = hashlib.sha256(b"arne-hold-v1" + b"".join(
        b"\x00" + part.encode() for part in parts
    )).digest()
    return digest[:16].hex()


def _content_hash(record: Record) -> str:
    digest = hashlib.sha256()
    digest.update(b"arne-retention-record-v1")
    for part in [record.id, record.cls, str(record.created_at), record.ledger_ref]:
        digest.update(b"\x00" + part.encode())
    for key in sorted(record.fields):
        digest.update(b"\x00" + key.encode() + b"\x00" + record.fields[key].encode())
    return digest.hexdigest()
