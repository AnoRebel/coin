"""Hybrid Ed25519 + ML-DSA-65 registration and signatures.

Mirrors internal/crypto/hybrid. Verification requires both components, so a
hybrid account cannot be downgraded by stripping one signature.
"""

from __future__ import annotations

from dataclasses import dataclass

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)
from dilithium_py.ml_dsa import ML_DSA_65

CONTEXT = b"arne-hybrid-v1"

ED25519_PUBLIC_KEY_SIZE = 32
ED25519_SIGNATURE_SIZE = 64
ML_DSA_PUBLIC_KEY_SIZE = 1952
ML_DSA_SIGNATURE_SIZE = 3309


class HybridError(Exception):
    pass


class InvalidKey(HybridError):
    pass


class PartialSignature(HybridError):
    pass


class VerifyFailed(HybridError):
    pass


@dataclass
class Key:
    ed25519: bytes
    ml_dsa_65: bytes


@dataclass
class Signature:
    ed25519: bytes
    ml_dsa_65: bytes


def register(key: Key) -> None:
    if len(key.ed25519) != ED25519_PUBLIC_KEY_SIZE:
        raise InvalidKey("ed25519 public key")
    if len(key.ml_dsa_65) != ML_DSA_PUBLIC_KEY_SIZE:
        raise InvalidKey("ml-dsa-65 public key")


def sign(ed_seed: bytes, ml_private: bytes, message: bytes) -> Signature:
    if len(ed_seed) != 32:
        raise InvalidKey("ed25519 seed")
    if len(ml_private) != 4032:  # ML-DSA-65 private key size
        raise InvalidKey("ml-dsa-65 private key")
    if not message:
        raise InvalidKey("empty message")
    private = Ed25519PrivateKey.from_private_bytes(ed_seed)
    return Signature(
        ed25519=private.sign(message),
        ml_dsa_65=ML_DSA_65.sign(ml_private, message, ctx=CONTEXT, deterministic=True),
    )


def verify(key: Key, message: bytes, signature: Signature) -> None:
    register(key)
    if not message:
        raise InvalidKey("empty message")
    if not signature.ed25519 or not signature.ml_dsa_65:
        raise PartialSignature("signature is missing a component")
    if len(signature.ed25519) != ED25519_SIGNATURE_SIZE or len(signature.ml_dsa_65) != ML_DSA_SIGNATURE_SIZE:
        raise InvalidKey("signature size")
    try:
        Ed25519PublicKey.from_public_bytes(key.ed25519).verify(signature.ed25519, message)
    except (InvalidSignature, ValueError) as exc:
        raise VerifyFailed("ed25519") from exc
    if not ML_DSA_65.verify(key.ml_dsa_65, message, signature.ml_dsa_65, ctx=CONTEXT):
        raise VerifyFailed("ml-dsa-65")
