"""Signed webhook delivery with bounded retries and replay protection.

Mirrors internal/webhook. Delivery is at-least-once: the delivery ID is stable
across retries, and each attempt gets a fresh signed timestamp.
"""

from __future__ import annotations

import json
import queue
import secrets
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey

HEADER_DELIVERY = "X-Arne-Delivery"
HEADER_TIMESTAMP = "X-Arne-Timestamp"
HEADER_SIGNATURE = "X-Arne-Signature"
DEFAULT_MAX_SKEW = 300
MAX_DELAY = 30.0


class WebhookError(Exception):
    """Base class for webhook failures."""


class ReplayError(WebhookError):
    """The signed timestamp is outside the accepted window."""


class SignatureError(WebhookError):
    """The signature or key is malformed or invalid."""


class HeaderError(WebhookError):
    """A required delivery header is missing."""


def _payload(timestamp: int, body: bytes) -> bytes:
    return f"{timestamp}\n".encode() + body


class Signer:
    """Ed25519 signer for webhook bodies."""

    def __init__(self, seed: bytes) -> None:
        if len(seed) != 32:
            raise ValueError("webhook seed must be 32 bytes")
        self._key = Ed25519PrivateKey.from_private_bytes(seed)

    @property
    def public_key(self) -> str:
        return self._key.public_key().public_bytes_raw().hex()

    def sign(self, timestamp: int, body: bytes) -> str:
        return self._key.sign(_payload(timestamp, body)).hex()


def verify(
    public_key: str,
    timestamp: str,
    body: bytes,
    signature: str,
    now: float | None = None,
    max_skew: int = DEFAULT_MAX_SKEW,
) -> None:
    """Verify a signature and reject stale or future timestamps."""
    try:
        ts = int(timestamp)
    except (TypeError, ValueError) as exc:
        raise HeaderError("invalid timestamp") from exc
    current = time.time() if now is None else now
    if abs(current - ts) > max_skew:
        raise ReplayError("stale timestamp")
    try:
        pub = Ed25519PublicKey.from_public_bytes(bytes.fromhex(public_key))
        sig = bytes.fromhex(signature)
        pub.verify(sig, _payload(ts, body))
    except Exception as exc:  # cryptography raises InvalidSignature/ValueError
        raise SignatureError("bad signature") from exc


def verify_request(
    headers: Any,
    body: bytes,
    public_key: str,
    now: float | None = None,
    max_skew: int = DEFAULT_MAX_SKEW,
) -> str:
    """Verify standard headers and return the delivery ID."""
    def get(name: str) -> str:
        if hasattr(headers, "get"):
            value = headers.get(name)
            if value is None:
                value = headers.get(name.lower())
            return value or ""
        return ""

    delivery = get(HEADER_DELIVERY)
    if not delivery:
        raise HeaderError("missing delivery header")
    verify(
        public_key, get(HEADER_TIMESTAMP), body, get(HEADER_SIGNATURE),
        now=now, max_skew=max_skew,
    )
    return delivery


@dataclass
class Config:
    endpoints: list[str]
    signer: Signer
    max_attempts: int = 5
    base_delay: float = 0.5
    queue_size: int = 256
    client: httpx.Client | None = None
    sleep: Callable[[float], None] = time.sleep


class Dispatcher:
    """Deliver queued events to every endpoint, retrying retryable failures."""

    def __init__(self, cfg: Config) -> None:
        if not cfg.endpoints:
            raise ValueError("webhook: no endpoints")
        self.cfg = cfg
        self.dropped = 0
        self._closed = False
        self._lock = threading.Lock()
        self._queue: queue.Queue[tuple[str, Any] | None] = queue.Queue(cfg.queue_size)
        self._client = cfg.client or httpx.Client(timeout=10.0)
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._started = False

    @property
    def public_key(self) -> str:
        return self.cfg.signer.public_key

    def start(self) -> None:
        self._started = True
        self._thread.start()

    def close(self) -> None:
        with self._lock:
            if self._closed:
                return
            self._closed = True
            if self._started:
                self._queue.put(None)
        if self._started:
            self._thread.join()

    def enqueue(self, kind: str, data: Any) -> bool:
        with self._lock:
            if self._closed:
                return False
            try:
                self._queue.put_nowait((kind, data))
                return True
            except queue.Full:
                self.dropped += 1
                return False

    def _run(self) -> None:
        while True:
            item = self._queue.get()
            if item is None:
                return
            self._deliver(*item)

    def _deliver(self, kind: str, data: Any) -> None:
        delivery_id = secrets.token_hex(16)
        body = json.dumps({
            "delivery_id": delivery_id, "type": kind,
            "created_at": int(time.time()), "data": data,
        }, separators=(",", ":")).encode()
        for endpoint in self.cfg.endpoints:
            self._post(endpoint, delivery_id, body)

    def _post(self, endpoint: str, delivery_id: str, body: bytes) -> None:
        delay = self.cfg.base_delay
        for attempt in range(1, self.cfg.max_attempts + 1):
            timestamp = int(time.time())
            headers = {
                "Content-Type": "application/json",
                HEADER_DELIVERY: delivery_id,
                HEADER_TIMESTAMP: str(timestamp),
                HEADER_SIGNATURE: self.cfg.signer.sign(timestamp, body),
            }
            try:
                response = self._client.post(endpoint, content=body, headers=headers)
                if 200 <= response.status_code < 300:
                    return
                if response.status_code != 429 and response.status_code < 500:
                    return
            except httpx.HTTPError:
                pass
            if attempt == self.cfg.max_attempts:
                return
            self.cfg.sleep(delay)
            delay = min(delay * 2, MAX_DELAY)


__all__ = [
    "Config", "Dispatcher", "HeaderError", "ReplayError", "SignatureError",
    "Signer", "WebhookError", "verify", "verify_request",
]
