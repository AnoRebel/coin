"""Common interface for software, hardware, and external transaction signers.

Mirrors internal/signer. A signer receives only human-readable transaction
details and the canonical payload built from them. A hardware adapter displays
the details, signs the payload, and verifies the returned signature. It never
receives an account seed.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Protocol, runtime_checkable

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

SUITE_ED25519 = "ed25519-v1"
MAX_PAYLOAD_BYTES = 4096


class SignerError(Exception):
    """A rejected signing operation, with a stable code for conformance."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise SignerError(code, message)


def _valid_label(value: str) -> bool:
    return (
        bool(value)
        and len(value) <= 128
        and not any(ord(char) < 0x20 or ord(char) == 0x7F for char in value)
    )


@dataclass(frozen=True)
class Details:
    network: str
    account: str
    to: str
    asset: str
    amount: int
    fee: int
    fee_asset: str
    nonce: int
    expires_at: int
    memo: str = ""

    def validate(self) -> None:
        for value in (self.network, self.account, self.to, self.asset, self.fee_asset):
            if not _valid_label(value):
                _fail("invalid", "missing or invalid field")
        if self.amount <= 0 or self.expires_at <= 0:
            _fail("invalid", "amount and expiry are required")
        if len(self.memo.encode("utf-8")) > 256:
            _fail("invalid", "memo")

    def human(self) -> str:
        lines = [
            f"network: {self.network}",
            f"account: {self.account}",
            f"to: {self.to}",
            f"asset: {self.asset}",
            f"amount: {self.amount}",
            f"fee: {self.fee} {self.fee_asset}",
            f"nonce: {self.nonce}",
            f"expires: {self.expires_at}",
        ]
        if self.memo:
            lines.append(f"memo: {json.dumps(self.memo)}")
        return "\n".join(lines)

    def payload(self) -> bytes:
        return ("arne-sign-v1\n" + self.human()).encode("utf-8")


@runtime_checkable
class Signer(Protocol):
    def public_key(self) -> str: ...
    def suite(self) -> str: ...
    def sign(self, details: Details) -> bytes: ...


class Software:
    """An in-process Ed25519 signer."""

    def __init__(self, seed: bytes) -> None:
        if len(seed) != 32:
            _fail("invalid", "seed")
        self._private = Ed25519PrivateKey.from_private_bytes(seed)

    def public_key(self) -> str:
        return self._private.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw).hex()

    def suite(self) -> str:
        return SUITE_ED25519

    def sign(self, details: Details) -> bytes:
        details.validate()
        return self._private.sign(details.payload())


@runtime_checkable
class Device(Protocol):
    def public_key(self) -> str: ...
    def suite(self) -> str: ...
    def display(self, text: str) -> None: ...
    def sign(self, payload: bytes) -> bytes: ...


class Hardware:
    """Adapter that displays details and verifies the device signature."""

    def __init__(self, device: Device) -> None:
        if device is None or len(device.public_key()) != 64 or not device.suite():
            _fail("invalid", "device identity")
        self._device = device

    def public_key(self) -> str:
        return self._device.public_key()

    def suite(self) -> str:
        return self._device.suite()

    def sign(self, details: Details) -> bytes:
        details.validate()
        if self._device.suite() != SUITE_ED25519:
            _fail("unsupported", f"suite {self._device.suite()}")
        payload = details.payload()
        if len(payload) > MAX_PAYLOAD_BYTES:
            _fail("unsupported", "payload too large")
        try:
            self._device.display(details.human())
        except Exception as error:  # noqa: BLE001 - device errors become display errors
            _fail("display", str(error))
        signature = self._device.sign(payload)
        try:
            Ed25519PublicKey.from_public_bytes(
                bytes.fromhex(self._device.public_key())
            ).verify(signature, payload)
        except (ValueError, InvalidSignature):
            _fail("signature", "device signature does not verify")
        return signature


def verify(public_key: str, details: Details, signature: bytes) -> bool:
    try:
        details.validate()
        Ed25519PublicKey.from_public_bytes(bytes.fromhex(public_key)).verify(
            signature, details.payload()
        )
        return True
    except (ValueError, InvalidSignature, SignerError):
        return False


__all__ = [
    "Details", "Device", "Hardware", "MAX_PAYLOAD_BYTES", "SUITE_ED25519",
    "Signer", "SignerError", "Software", "verify",
]
