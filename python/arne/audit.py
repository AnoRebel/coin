"""Append-only, hash-chained audit log.

Mirrors internal/audit. Each record commits to the previous record's hash, so
changing, reordering, or removing an entry breaks verification. Truncating the
newest records is not detectable from the records alone: publish or sign
``head`` to anchor the log. The chain is not signed.
"""

from __future__ import annotations

import hashlib
import json
import struct
import threading
import time
from collections.abc import Callable, Iterable
from dataclasses import asdict, dataclass, replace
from typing import IO

GENESIS = "0" * 64
MAX_DETAIL = 1024


class AuditError(Exception):
    """A rejected or broken audit chain, with a stable code."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise AuditError(code, message)


@dataclass(frozen=True)
class Entry:
    actor: str
    action: str
    target: str = ""
    result: str = ""
    detail: str = ""


@dataclass(frozen=True)
class Record:
    seq: int
    time: int
    actor: str
    action: str
    target: str
    result: str
    detail: str
    prev: str
    hash: str


def _valid_label(value: str, limit: int) -> bool:
    try:
        raw = value.encode("utf-8")
    except UnicodeEncodeError:
        return False
    return (
        0 < len(raw) <= limit
        and not any(ord(char) < 0x20 or ord(char) == 0x7F for char in value)
    )


def _validate(entry: Entry) -> None:
    if not (
        _valid_label(entry.actor, 128)
        and _valid_label(entry.action, 128)
        and _valid_label(entry.result, 128)
    ):
        _fail("invalid", "actor, action, or result")
    if entry.target and not _valid_label(entry.target, 1024):
        _fail("invalid", "target")
    try:
        detail = entry.detail.encode("utf-8")
    except UnicodeEncodeError:
        _fail("invalid", "detail")
    if len(detail) > MAX_DETAIL:
        _fail("invalid", "detail")


def chain_hash(record: Record) -> str:
    digest = hashlib.sha256()
    digest.update(b"arne-audit-v1\x00")
    digest.update(bytes.fromhex(record.prev))
    digest.update(struct.pack(">Q", record.seq))
    digest.update(struct.pack(">Q", record.time))
    for value in (record.actor, record.action, record.target, record.result, record.detail):
        raw = value.encode("utf-8")
        digest.update(struct.pack(">I", len(raw)))
        digest.update(raw)
    return digest.hexdigest()


def verify(records: Iterable[Record]) -> None:
    head = GENESIS
    for index, record in enumerate(records):
        if record.seq != index + 1:
            _fail("sequence", f"record {index} has sequence {record.seq}")
        if record.prev != head:
            _fail("chain", f"record {index}")
        if record.hash != chain_hash(record):
            _fail("hash", f"record {index}")
        head = record.hash


class Log:
    """A concurrency-safe in-memory chain."""

    def __init__(self, now: Callable[[], int] | None = None) -> None:
        self._now = now if now is not None else lambda: int(time.time())
        self._records: list[Record] = []
        self._head = GENESIS
        self._lock = threading.Lock()

    @property
    def head(self) -> str:
        with self._lock:
            return self._head

    def append(self, entry: Entry) -> Record:
        _validate(entry)
        with self._lock:
            record = Record(
                seq=len(self._records) + 1,
                time=int(self._now()),
                actor=entry.actor,
                action=entry.action,
                target=entry.target,
                result=entry.result,
                detail=entry.detail,
                prev=self._head,
                hash="",
            )
            record = replace(record, hash=chain_hash(record))
            self._records.append(record)
            self._head = record.hash
            return record

    def records(self) -> list[Record]:
        with self._lock:
            return list(self._records)

    def export(self, stream: IO[str]) -> None:
        for record in self.records():
            stream.write(json.dumps(asdict(record), separators=(",", ":")) + "\n")


def open(stream: IO[str], now: Callable[[], int] | None = None) -> Log:
    """Decode and verify a newline-delimited export."""
    records: list[Record] = []
    try:
        for line in stream:
            if not line.strip():
                continue
            records.append(Record(**json.loads(line)))
    except (ValueError, TypeError, KeyError) as error:
        _fail("invalid", str(error))
    verify(records)
    log = Log(now)
    log._records = records
    if records:
        log._head = records[-1].hash
    return log


__all__ = [
    "AuditError",
    "Entry",
    "GENESIS",
    "Log",
    "MAX_DETAIL",
    "Record",
    "chain_hash",
    "open",
    "verify",
]
