"""WebAuthn passkey assertions for scoped session and transaction approval.

Mirrors internal/passkey. A relying party chooses a scope (a session, or a
specific transaction payload), the challenge is derived from that scope, and
authorization recomputes it. An assertion cannot be replayed for a different
scope, origin, relying party, or after expiry. Only the enrolled credential
public key is used; the root account key never reaches the authenticator.

This module verifies assertions. It does not parse attestation objects or COSE
keys: the enrolling layer extracts the credential ID, algorithm, and public key.
"""

from __future__ import annotations

import base64
import hashlib
import json
import threading
import urllib.parse
from dataclasses import dataclass, replace

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import ec, ed25519

PURPOSE_SESSION = "session"
PURPOSE_TRANSACTION = "transaction"

ALG_ED25519 = "ed25519"
ALG_ES256 = "es256"

MIN_AUTHENTICATOR_DATA = 37
MAX_CREDENTIAL_ID = 1024

FLAG_USER_PRESENT = 0x01
FLAG_USER_VERIFIED = 0x04


class PasskeyError(Exception):
    """A rejected passkey operation, with a stable code for conformance."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise PasskeyError(code, message)


def _valid_label(value: str) -> bool:
    return (
        bool(value)
        and len(value) <= 128
        and not any(ord(char) < 0x20 or ord(char) == 0x7F for char in value)
    )


def _valid_hash(value: str) -> bool:
    return len(value) == 64 and all(char in "0123456789abcdef" for char in value)


def _valid_credential_id(value: str) -> bool:
    return (
        bool(value)
        and len(value) <= MAX_CREDENTIAL_ID
        and all(char.isascii() and (char.isalnum() or char in "-_") for char in value)
    )


def _valid_origin(value: str) -> bool:
    parts = urllib.parse.urlsplit(value)
    return (
        parts.scheme in ("https", "http")
        and bool(parts.netloc)
        and bool(parts.hostname)
        and parts.username is None
        and parts.password is None
        and not parts.path
        and not parts.query
        and not parts.fragment
    )


@dataclass(frozen=True)
class Scope:
    purpose: str
    payload: str
    expires_at: int
    nonce: str

    def validate(self) -> None:
        if self.purpose == PURPOSE_SESSION:
            if self.payload:
                _fail("invalid", "session scope cannot bind a payload")
        elif self.purpose == PURPOSE_TRANSACTION:
            if not _valid_hash(self.payload):
                _fail("invalid", "transaction scope needs a 32-byte payload hash")
        else:
            _fail("invalid", "purpose")
        if self.expires_at <= 0:
            _fail("invalid", "expiry")
        if not _valid_label(self.nonce):
            _fail("invalid", "nonce")


def challenge(scope: Scope) -> str:
    """Derive the base64url challenge for a scope."""
    scope.validate()
    preimage = "\x00".join(
        (
            "arne-passkey-v1",
            scope.purpose,
            scope.payload,
            str(scope.expires_at),
            scope.nonce,
        )
    )
    digest = hashlib.sha256(preimage.encode("utf-8")).digest()
    return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")


@dataclass(frozen=True)
class Credential:
    id: str
    account: str
    rpid: str
    origin: str
    alg: str
    public_key: str
    user_verification: bool = False
    counter: int = 0

    def validate(self) -> None:
        if (
            not _valid_credential_id(self.id)
            or not _valid_label(self.account)
            or not _valid_label(self.rpid)
        ):
            _fail("invalid", "credential identity")
        if not _valid_origin(self.origin):
            _fail("invalid", "origin")
        try:
            raw = bytes.fromhex(self.public_key)
        except ValueError:
            _fail("invalid", f"{self.alg} key")
        if self.alg == ALG_ED25519:
            if len(raw) != 32:
                _fail("invalid", "ed25519 key")
        elif self.alg == ALG_ES256:
            if len(raw) != 65 or raw[0] != 4:
                _fail("invalid", "es256 key")
            try:
                ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), raw)
            except ValueError:
                _fail("invalid", "es256 key")
        else:
            _fail("invalid", "algorithm")


@dataclass(frozen=True)
class Assertion:
    client_data_json: bytes
    authenticator_data: bytes
    signature: bytes


def _verify_signature(credential: Credential, assertion: Assertion) -> bool:
    message = assertion.authenticator_data + hashlib.sha256(
        assertion.client_data_json
    ).digest()
    raw = bytes.fromhex(credential.public_key)
    try:
        if credential.alg == ALG_ED25519:
            ed25519.Ed25519PublicKey.from_public_bytes(raw).verify(
                assertion.signature, message
            )
        elif credential.alg == ALG_ES256:
            ec.EllipticCurvePublicKey.from_encoded_point(
                ec.SECP256R1(), raw
            ).verify(assertion.signature, message, ec.ECDSA(hashes.SHA256()))
        else:
            return False
        return True
    except (InvalidSignature, ValueError):
        return False


class Manager:
    """Enrolled credentials and their signature counters."""

    def __init__(self) -> None:
        self._credentials: dict[str, Credential] = {}
        self._lock = threading.Lock()

    def enroll(self, credential: Credential) -> None:
        credential.validate()
        with self._lock:
            if credential.id in self._credentials:
                _fail("duplicate", "credential already enrolled")
            self._credentials[credential.id] = credential

    def revoke(self, id: str) -> None:
        with self._lock:
            if id not in self._credentials:
                _fail("credential", "unknown credential")
            del self._credentials[id]

    def get(self, id: str) -> Credential | None:
        with self._lock:
            return self._credentials.get(id)

    def authorize(
        self, id: str, scope: Scope, assertion: Assertion, now: int
    ) -> Credential:
        scope.validate()
        with self._lock:
            credential = self._credentials.get(id)
            if credential is None:
                _fail("credential", "unknown credential")
            if now >= scope.expires_at:
                _fail("expired", "assertion expired")

            try:
                client = json.loads(assertion.client_data_json)
                client_type = client["type"]
                client_challenge = client["challenge"]
                client_origin = client["origin"]
            except (ValueError, KeyError, TypeError):
                _fail("client_data", "invalid client data")
            if client_type != "webauthn.get":
                _fail("client_data", "wrong ceremony type")
            if client_challenge != challenge(scope):
                _fail("challenge", "challenge mismatch")
            if client_origin != credential.origin:
                _fail("origin", "origin mismatch")

            data = assertion.authenticator_data
            if len(data) < MIN_AUTHENTICATOR_DATA:
                _fail("authenticator_data", "invalid authenticator data")
            if data[:32] != hashlib.sha256(credential.rpid.encode()).digest():
                _fail("rp_id", "relying party mismatch")
            flags = data[32]
            if not flags & FLAG_USER_PRESENT or (
                credential.user_verification and not flags & FLAG_USER_VERIFIED
            ):
                _fail("flags", "user presence or verification missing")
            counter = int.from_bytes(data[33:37], "big")
            if counter and credential.counter and counter <= credential.counter:
                _fail("counter", "counter did not advance")
            if not _verify_signature(credential, assertion):
                _fail("signature", "bad signature")
            if counter > credential.counter:
                credential = replace(credential, counter=counter)
                self._credentials[id] = credential
            return credential


__all__ = [
    "ALG_ED25519",
    "ALG_ES256",
    "Assertion",
    "Credential",
    "Manager",
    "MIN_AUTHENTICATOR_DATA",
    "PasskeyError",
    "PURPOSE_SESSION",
    "PURPOSE_TRANSACTION",
    "Scope",
    "challenge",
]
