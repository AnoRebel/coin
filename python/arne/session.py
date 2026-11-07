"""Scoped, expiring session keys and watch-only accounts.

Mirrors internal/session. A grant names an account, a session public key,
scopes, allowed assets, a cumulative spending limit, and an expiry. Authorize
checks one request and consumes the limit; sign checks the key and request
before producing a signature. A watch-only account can never sign.
"""

from __future__ import annotations

from dataclasses import dataclass

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

READ = "read"
TRANSFER = "transfer"
PAYMENT = "payment"
AUDIT = "audit"

SCOPES = frozenset({READ, TRANSFER, PAYMENT, AUDIT})


class SessionError(Exception):
    """A rejected session operation, with a stable code for conformance."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise SessionError(code, message)


def _valid_label(value: str) -> bool:
    return (
        bool(value)
        and len(value) <= 128
        and not any(ord(char) < 0x20 or ord(char) == 0x7F for char in value)
    )


def _valid_hash(value: str) -> bool:
    return len(value) == 64 and all(char in "0123456789abcdef" for char in value)


@dataclass(frozen=True)
class Grant:
    account: str
    public_key: str
    scopes: tuple[str, ...]
    assets: tuple[str, ...] = ()
    limit: int = 0
    expires_at: int = 0


@dataclass(frozen=True)
class Request:
    account: str
    scope: str
    at: int
    asset: str = ""
    amount: int = 0


class Session:
    """A grant with its remaining spending limit."""

    def __init__(self, grant: Grant) -> None:
        if not _valid_label(grant.account) or not _valid_hash(grant.public_key) or grant.expires_at <= 0:
            _fail("invalid", "account, key, or expiry")
        if not grant.scopes:
            _fail("invalid", "no scopes")
        if len(set(grant.scopes)) != len(grant.scopes) or any(
            scope not in SCOPES for scope in grant.scopes
        ):
            _fail("invalid", "scope")
        if len(set(grant.assets)) != len(grant.assets) or any(
            not _valid_label(asset) for asset in grant.assets
        ):
            _fail("invalid", "asset")
        spending = any(scope in (TRANSFER, PAYMENT) for scope in grant.scopes)
        if spending and (not grant.assets or grant.limit <= 0):
            _fail("invalid", "spending scope needs assets and a limit")
        self._grant = grant
        self._remaining = grant.limit

    @property
    def grant(self) -> Grant:
        return self._grant

    @property
    def remaining(self) -> int:
        return self._remaining

    def authorize(self, request: Request) -> None:
        if request.at >= self._grant.expires_at:
            _fail("expired", "session expired")
        if request.account != self._grant.account:
            _fail("account", request.account)
        if request.scope not in self._grant.scopes:
            _fail("scope", request.scope)
        if request.scope not in (TRANSFER, PAYMENT):
            return
        if request.amount <= 0:
            _fail("invalid", "zero amount")
        if request.asset not in self._grant.assets:
            _fail("asset", request.asset)
        if request.amount > self._remaining:
            _fail("limit", "spending limit exceeded")
        self._remaining -= request.amount

    def sign(self, seed: bytes, payload: bytes, request: Request) -> bytes:
        if len(seed) != 32:
            _fail("invalid", "seed")
        private = Ed25519PrivateKey.from_private_bytes(seed)
        public = private.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw).hex()
        if public != self._grant.public_key:
            _fail("key", "key does not match the session")
        self.authorize(request)
        return private.sign(payload)


@dataclass(frozen=True)
class Account:
    address: str
    public_key: str
    watch_only: bool = False

    def __post_init__(self) -> None:
        if not _valid_label(self.address) or not _valid_hash(self.public_key):
            _fail("invalid", "address or key")

    def sign(self, seed: bytes, payload: bytes) -> bytes:
        if self.watch_only:
            _fail("watch_only", "watch-only account cannot sign")
        if len(seed) != 32:
            _fail("invalid", "seed")
        private = Ed25519PrivateKey.from_private_bytes(seed)
        public = private.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw).hex()
        if public != self.public_key:
            _fail("key", "key does not match the account")
        return private.sign(payload)


def public_key_from_seed(seed: bytes) -> str:
    return (
        Ed25519PrivateKey.from_private_bytes(seed)
        .public_key()
        .public_bytes(Encoding.Raw, PublicFormat.Raw)
        .hex()
    )


def verify(public_key: str, payload: bytes, signature: bytes) -> bool:
    try:
        Ed25519PublicKey.from_public_bytes(bytes.fromhex(public_key)).verify(signature, payload)
        return True
    except (ValueError, InvalidSignature):
        return False


__all__ = [
    "Account", "AUDIT", "Grant", "PAYMENT", "READ", "Request", "Session",
    "SessionError", "TRANSFER", "public_key_from_seed", "verify",
]
