"""Opt-in sealed memos: only the recipient's viewing key decrypts them."""

from __future__ import annotations

import hashlib
import os
from dataclasses import dataclass

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

from . import cbor

VERSION = 1
MAX_PLAINTEXT = 256
_INFO = b"arne-memo-v1"


class MemoError(ValueError):
    pass


@dataclass
class _Envelope:
    epk: bytes
    nonce: bytes
    ct: bytes


def _derive(seed: bytes) -> tuple[bytes, bytes]:
    """Ed25519 seed to deterministic, explicitly clamped X25519 key pair."""
    if len(seed) != 32:
        raise MemoError("seed length")
    scalar = bytearray(hashlib.sha256(_INFO + seed).digest())
    scalar[0] &= 248
    scalar[31] &= 127
    scalar[31] |= 64
    private = X25519PrivateKey.from_private_bytes(bytes(scalar))
    public = private.public_key().public_bytes_raw()
    return bytes(scalar), public


def public_key(seed: bytes) -> bytes:
    """Memo public key to share with senders."""
    return _derive(seed)[1]


def _key(shared: bytes) -> bytes:
    return HKDF(algorithm=hashes.SHA256(), length=32, salt=None, info=_INFO).derive(shared)


def seal(
    recipient: bytes,
    plaintext: bytes,
    ephemeral_seed: bytes | None = None,
    nonce: bytes | None = None,
) -> bytes:
    if len(recipient) != 32:
        raise MemoError("recipient key")
    if len(plaintext) > MAX_PLAINTEXT:
        raise MemoError(f"memo above {MAX_PLAINTEXT} bytes")
    ephemeral_seed = ephemeral_seed or os.urandom(32)
    nonce = nonce or os.urandom(12)
    if len(ephemeral_seed) != 32 or len(nonce) != 12:
        raise MemoError("ephemeral seed or nonce length")
    ephemeral_private, ephemeral_public = _derive(ephemeral_seed)
    shared = X25519PrivateKey.from_private_bytes(ephemeral_private).exchange(
        X25519PublicKey.from_public_bytes(recipient)
    )
    ct = ChaCha20Poly1305(_key(shared)).encrypt(nonce, plaintext, None)
    return cbor.encode({"v": VERSION, "epk": ephemeral_public, "nonce": nonce, "ct": ct})


def open_memo(seed: bytes, sealed: bytes) -> bytes:
    env = _parse(sealed)
    private, _ = _derive(seed)
    try:
        shared = X25519PrivateKey.from_private_bytes(private).exchange(
            X25519PublicKey.from_public_bytes(env.epk)
        )
        return ChaCha20Poly1305(_key(shared)).decrypt(env.nonce, env.ct, None)
    except Exception as exc:  # noqa: BLE001 - any failure means no cleartext
        raise MemoError("cannot decrypt") from exc


def validate(sealed: bytes) -> None:
    """Shape and size checks the ledger can perform without decrypting."""
    env = _parse(sealed)
    if not 16 <= len(env.ct) <= MAX_PLAINTEXT + 16:
        raise MemoError("ciphertext length")


def _parse(raw: bytes) -> _Envelope:
    try:
        m = cbor.decode_map(raw)
    except cbor.CBORError as exc:
        raise MemoError(f"malformed sealed memo: {exc}") from exc
    if len(m) != 4:
        raise MemoError("unknown sealed memo field")
    if m.get("v") != VERSION:
        raise MemoError("sealed memo version")
    values = []
    for key in ("epk", "nonce", "ct"):
        value = m.get(key)
        if not isinstance(value, (bytes, bytearray)):
            raise MemoError(key)
        values.append(bytes(value))
    epk, nonce, ct = values
    if len(epk) != 32 or len(nonce) != 12:
        raise MemoError("key or nonce length")
    return _Envelope(epk, nonce, ct)


__all__ = ["MemoError", "VERSION", "public_key", "seal", "open_memo", "validate"]
