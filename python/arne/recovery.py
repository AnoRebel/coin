"""Guardian-based social recovery with a threshold and a timelock.

Mirrors internal/recovery. A policy names the owner, at least two guardians, a
threshold of at least two, and a delay. Signed guardian approvals move a
request from pending to ready; execution waits for the delay; the owner can
cancel before execution. A single guardian can never execute alone.

This is a state machine over signed messages. It does not change an account key
in the ledger.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, replace

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

PENDING = "pending"
READY = "ready"
EXECUTED = "executed"
CANCELLED = "cancelled"


class RecoveryError(Exception):
    """A rejected recovery operation, with a stable code for conformance."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise RecoveryError(code, message)


def _valid_label(value: str) -> bool:
    return (
        bool(value)
        and len(value) <= 128
        and not any(ord(char) < 0x20 or ord(char) == 0x7F for char in value)
    )


def _valid_hash(value: str) -> bool:
    return len(value) == 64 and all(char in "0123456789abcdef" for char in value)


@dataclass(frozen=True)
class Guardian:
    id: str
    public_key: str


@dataclass(frozen=True)
class Policy:
    account: str
    owner_public_key: str
    guardians: tuple[Guardian, ...]
    threshold: int
    delay: int

    def validate(self) -> None:
        if not _valid_label(self.account) or not _valid_hash(self.owner_public_key):
            _fail("invalid", "account or owner key")
        if self.threshold < 2:
            _fail("invalid", "threshold must be at least 2")
        if self.delay <= 0:
            _fail("invalid", "delay must be positive")
        if len(self.guardians) < self.threshold:
            _fail("invalid", "fewer guardians than the threshold")
        ids: set[str] = set()
        keys: set[str] = set()
        for guardian in self.guardians:
            if not _valid_label(guardian.id) or not _valid_hash(guardian.public_key):
                _fail("invalid", "guardian")
            if guardian.id in ids:
                _fail("invalid", "duplicate guardian id")
            if guardian.public_key in keys:
                _fail("invalid", "duplicate guardian key")
            if guardian.public_key == self.owner_public_key:
                _fail("invalid", "owner cannot be a guardian")
            ids.add(guardian.id)
            keys.add(guardian.public_key)


@dataclass(frozen=True)
class Approval:
    guardian: str
    signature: str


@dataclass(frozen=True)
class Request:
    id: str
    account: str
    new_public_key: str
    created_at: int
    executable_at: int
    state: str = PENDING
    approvals: tuple[Approval, ...] = ()


def request_id(account: str, new_key: str, nonce: str) -> str:
    digest = hashlib.sha256(b"arne-recovery-v1\x00")
    for part in (account, new_key, nonce):
        encoded = part.encode("utf-8")
        digest.update(len(encoded).to_bytes(4, "big"))
        digest.update(encoded)
    return digest.digest()[:16].hex()


def approval_payload(id: str, account: str, new_key: str) -> bytes:
    return f"arne-recovery-approval-v1\n{id}\n{account}\n{new_key}".encode()


def cancel_payload(id: str, account: str) -> bytes:
    return f"arne-recovery-cancel-v1\n{id}\n{account}".encode()


def _verify(public_key: str, payload: bytes, signature: str) -> bool:
    try:
        sig = bytes.fromhex(signature)
        Ed25519PublicKey.from_public_bytes(bytes.fromhex(public_key)).verify(sig, payload)
        return True
    except (ValueError, InvalidSignature):
        return False


class Manager:
    """A policy and its recovery requests."""

    def __init__(self, policy: Policy) -> None:
        policy.validate()
        self._policy = policy
        self._requests: dict[str, Request] = {}
        self._order: list[str] = []

    @property
    def policy(self) -> Policy:
        return self._policy

    def start(self, new_key: str, nonce: str, now: int) -> Request:
        if not _valid_hash(new_key) or not _valid_label(nonce):
            _fail("invalid", "new key or nonce")
        id = request_id(self._policy.account, new_key, nonce)
        existing = self._requests.get(id)
        if existing is not None:
            return existing
        request = Request(
            id=id,
            account=self._policy.account,
            new_public_key=new_key,
            created_at=now,
            executable_at=now + self._policy.delay,
        )
        self._requests[id] = request
        self._order.append(id)
        return request

    def approve(self, id: str, guardian_id: str, signature: str) -> Request:
        request = self._requests.get(id)
        if request is None:
            _fail("unknown", "request")
        if request.state not in (PENDING, READY):
            _fail("state", request.state)
        guardian = self._guardian(guardian_id)
        if guardian is None:
            _fail("guardian", guardian_id)
        if any(approval.guardian == guardian_id for approval in request.approvals):
            _fail("duplicate", guardian_id)
        if not _verify(
            guardian.public_key,
            approval_payload(id, request.account, request.new_public_key),
            signature,
        ):
            _fail("signature", "approval")
        approvals = tuple(
            sorted(
                request.approvals + (Approval(guardian=guardian_id, signature=signature),),
                key=lambda approval: approval.guardian,
            )
        )
        state = READY if len(approvals) >= self._policy.threshold else PENDING
        updated = replace(request, approvals=approvals, state=state)
        self._requests[id] = updated
        return updated

    def execute(self, id: str, now: int) -> Request:
        request = self._requests.get(id)
        if request is None:
            _fail("unknown", "request")
        if request.state != READY:
            _fail("threshold" if request.state == PENDING else "state", request.state)
        if now < request.executable_at:
            _fail("timelock", "delay has not elapsed")
        updated = replace(request, state=EXECUTED)
        self._requests[id] = updated
        return updated

    def cancel(self, id: str, owner_signature: str, now: int) -> Request:
        request = self._requests.get(id)
        if request is None:
            _fail("unknown", "request")
        if request.state not in (PENDING, READY):
            _fail("state", request.state)
        if not _verify(
            self._policy.owner_public_key,
            cancel_payload(id, request.account),
            owner_signature,
        ):
            _fail("signature", "cancel")
        updated = replace(request, state=CANCELLED)
        self._requests[id] = updated
        return updated

    def get(self, id: str) -> Request | None:
        return self._requests.get(id)

    def requests(self) -> list[Request]:
        return [self._requests[id] for id in self._order]

    def _guardian(self, id: str) -> Guardian | None:
        for guardian in self._policy.guardians:
            if guardian.id == id:
                return guardian
        return None


def public_key_from_seed(seed: bytes) -> str:
    """Derive a hex public key from a 32-byte Ed25519 seed (test helpers)."""
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

    key = Ed25519PrivateKey.from_private_bytes(seed).public_key()
    return key.public_bytes(Encoding.Raw, PublicFormat.Raw).hex()


def sign_with_seed(seed: bytes, payload: bytes) -> str:
    """Sign with a 32-byte Ed25519 seed and return hex (test helpers)."""
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

    return Ed25519PrivateKey.from_private_bytes(seed).sign(payload).hex()
