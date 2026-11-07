"""Team channel messages with idempotent publishing, catch-up, and retention.

Mirrors internal/channel. A message ID is derived from the channel, sender, and
client ID, so a sender can retry after a timeout without duplicating a message.
Readers page with a sequence cursor. Subscriptions carry best-effort sequence
hints only; callers recover from the store with ``read``.

Content is either plaintext (``text``, ``payment``, ``notice``, ``file``) or an
opaque ``ciphertext``. Encrypted channels store MLS ciphertext and leave the
plaintext fields empty. This module does not encrypt anything itself.
"""

from __future__ import annotations

import hashlib
import queue
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass, replace

MAX_TEXT_BYTES = 4096
MAX_CIPHERTEXT_BYTES = 64 << 10
DEFAULT_LIMIT = 100
MAX_LIMIT = 500

TEXT = "text"
PAYMENT = "payment"
NOTICE = "notice"
FILE = "file"

KINDS = frozenset({TEXT, PAYMENT, NOTICE, FILE})


class ChannelError(Exception):
    """A rejected channel operation, with a stable code for conformance."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise ChannelError(code, message)


def _has_control(value: str) -> bool:
    return any(ord(char) < 0x20 or ord(char) == 0x7F for char in value)


def _valid_label(value: str) -> bool:
    return bool(value) and len(value) <= 128 and not _has_control(value)


def _valid_client_id(value: str) -> bool:
    if not value or len(value) > 128:
        return False
    return all(
        char.isascii() and (char.isalnum() or char in "_-")
        for char in value
    )


def _valid_hash(value: str) -> bool:
    return len(value) == 64 and all(char in "0123456789abcdef" for char in value)


@dataclass(frozen=True)
class FilePointer:
    hash: str
    pointer: str
    size: int = 0
    name: str = ""


@dataclass(frozen=True)
class Message:
    channel: str
    sender: str
    client_id: str
    text: str = ""
    payment: str = ""
    notice: str = ""
    file: FilePointer | None = None
    ciphertext: bytes = b""
    kind: str = ""
    id: str = ""
    seq: int = 0
    time: int = 0
    system: bool = False

    def validate(self) -> None:
        if not _valid_label(self.channel):
            _fail("invalid", "channel")
        if not _valid_label(self.sender):
            _fail("invalid", "sender")
        if not _valid_client_id(self.client_id):
            _fail("invalid", "client_id")
        if self.ciphertext:
            if len(self.ciphertext) > MAX_CIPHERTEXT_BYTES:
                _fail("invalid", "ciphertext too large")
            if self.text or self.payment or self.notice or self.file is not None:
                _fail("invalid", "ciphertext with plaintext fields")
            if self.kind not in KINDS:
                _fail("invalid", "kind")
            return
        if self.kind and self.kind not in KINDS:
            _fail("invalid", "kind")
        if len(self.text.encode("utf-8")) > MAX_TEXT_BYTES:
            _fail("invalid", "text")
        if self.payment and not _valid_hash(self.payment):
            _fail("invalid", "payment reference")
        if self.file is not None:
            if (
                not _valid_hash(self.file.hash)
                or not self.file.pointer
                or len(self.file.pointer) > 2048
                or _has_control(self.file.pointer)
                or len(self.file.name) > 255
                or _has_control(self.file.name)
            ):
                _fail("invalid", "file pointer")
        if self.notice and len(self.notice.encode("utf-8")) > MAX_TEXT_BYTES:
            _fail("invalid", "notice")
        if self.payment and self.file is not None:
            _fail("invalid", "payment and file in one message")
        if not self.text and not self.payment and not self.notice and self.file is None:
            _fail("invalid", "empty message")

    def payload_kind(self) -> str:
        if self.kind:
            return self.kind
        if self.notice:
            return NOTICE
        if self.payment:
            return PAYMENT
        if self.file is not None:
            return FILE
        return TEXT


def message_id(channel: str, sender: str, client_id: str) -> str:
    digest = hashlib.sha256(b"arne-message-v1\x00")
    for part in (channel, sender, client_id):
        encoded = part.encode("utf-8")
        digest.update(len(encoded).to_bytes(4, "big"))
        digest.update(encoded)
    return digest.digest()[:16].hex()


def _same_content(left: Message, right: Message) -> bool:
    return replace(left, id="", seq=0, time=0, system=False) == replace(
        right, id="", seq=0, time=0, system=False
    )


class Store:
    """A concurrency-safe in-memory channel log."""

    def __init__(
        self,
        retention: float,
        now: Callable[[], float] = time.time,
    ) -> None:
        if retention <= 0:
            _fail("retention", "retention required")
        self._retention = retention
        self._now = now
        self._overrides: dict[str, float] = {}
        self._next: dict[str, int] = {}
        self._logs: dict[str, list[Message]] = {}
        self._seen: dict[str, Message] = {}
        self._subs: dict[str, set[queue.Queue[int]]] = {}
        self._lock = threading.Lock()

    def set_retention(self, channel: str, retention: float) -> None:
        if not _valid_label(channel) or retention <= 0:
            _fail("retention", "retention required")
        with self._lock:
            self._overrides[channel] = retention

    def publish(self, message: Message) -> tuple[Message, bool]:
        """Append a message. Returns (stored, duplicate)."""
        message.validate()
        kind = message.payload_kind()
        message_id_value = message_id(
            message.channel, message.sender, message.client_id
        )
        with self._lock:
            existing = self._seen.get(message_id_value)
            if existing is not None:
                candidate = replace(message, kind=kind)
                if not _same_content(existing, candidate):
                    _fail("conflict", "client_id reused")
                return existing, True
            seq = self._next.get(message.channel, 0) + 1
            self._next[message.channel] = seq
            stored = replace(
                message,
                id=message_id_value,
                seq=seq,
                time=int(self._now()),
                kind=kind,
                system=bool(message.notice),
            )
            self._logs.setdefault(message.channel, []).append(stored)
            self._seen[message_id_value] = stored
            for hint in self._subs.get(message.channel, ()):
                try:
                    hint.put_nowait(stored.seq)
                except queue.Full:
                    pass  # the reader catches up from the store
            return stored, False

    def read(self, channel: str, after: int = 0, limit: int = DEFAULT_LIMIT) -> list[Message]:
        """Return messages after a sequence cursor, oldest first."""
        if not _valid_label(channel):
            _fail("invalid", "channel")
        limit = DEFAULT_LIMIT if limit <= 0 else min(limit, MAX_LIMIT)
        with self._lock:
            logs = self._logs.get(channel, [])
            cutoff = self._cutoff(channel)
            first = next(
                (index for index, message in enumerate(logs) if message.time >= cutoff),
                -1,
            )
            if after > 0:
                if first == -1:
                    if after < self._next.get(channel, 0):
                        _fail("cursor_expired", "cursor is outside retention")
                elif after + 1 < logs[first].seq:
                    _fail("cursor_expired", "cursor is outside retention")
            result: list[Message] = []
            index = first
            while index >= 0 and index < len(logs) and len(result) < limit:
                message = logs[index]
                if message.time >= cutoff and message.seq > after:
                    result.append(message)
                index += 1
            return result

    def prune(self) -> int:
        """Remove messages older than their channel retention."""
        with self._lock:
            now = int(self._now())
            removed = 0
            for channel, logs in list(self._logs.items()):
                retention = self._overrides.get(channel, self._retention)
                cutoff = now - int(retention)
                kept: list[Message] = []
                for message in logs:
                    if message.time < cutoff:
                        self._seen.pop(message.id, None)
                        removed += 1
                        continue
                    kept.append(message)
                self._logs[channel] = kept
            return removed

    def subscribe(self, channel: str) -> tuple[queue.Queue[int], Callable[[], None]]:
        """Return best-effort sequence hints and a cancel function."""
        hint: queue.Queue[int] = queue.Queue(maxsize=1)
        with self._lock:
            self._subs.setdefault(channel, set()).add(hint)

        def cancel() -> None:
            with self._lock:
                subscribers = self._subs.get(channel)
                if subscribers is not None:
                    subscribers.discard(hint)
                    if not subscribers:
                        del self._subs[channel]

        return hint, cancel

    def _cutoff(self, channel: str) -> int:
        retention = self._overrides.get(channel, self._retention)
        return int(self._now()) - int(retention)
