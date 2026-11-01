"""BLS12-381 signatures with proof of possession.

Mirrors internal/crypto/bls. Validator keys must prove possession before they
can join an aggregate; otherwise a rogue key can cancel another key.
"""

from __future__ import annotations

import hashlib
import threading

from py_ecc.bls import G2ProofOfPossession as _bls
from py_ecc.optimized_bls12_381 import curve_order

DST = b"BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_"
_POP_DOMAIN = b"arne-bls-pop-v1"


class BLSError(Exception):
    pass


class InvalidKey(BLSError):
    pass


class NoPoP(BLSError):
    pass


class RogueKey(BLSError):
    pass


class DuplicateKey(BLSError):
    pass


def secret_from_seed(seed: bytes) -> bytes:
    if not seed:
        raise InvalidKey("seed is required")
    digest = hashlib.sha256(b"arne-bls-key-v1" + seed).digest()
    while int.from_bytes(digest, "big") >= curve_order:
        digest = hashlib.sha256(digest).digest()
    return digest


def _scalar(secret: bytes) -> int:
    if len(secret) != 32:
        raise InvalidKey("secret key length")
    value = int.from_bytes(secret, "big")
    if not 0 < value < curve_order:
        raise InvalidKey("secret key")
    return value


def public_key_of(secret: bytes) -> bytes:
    return _bls.SkToPk(_scalar(secret))


def sign(secret: bytes, msg: bytes) -> bytes:
    if not msg:
        raise InvalidKey("empty message")
    return _bls.Sign(_scalar(secret), msg)


def verify(public_key: bytes, msg: bytes, signature: bytes) -> bool:
    if not public_key or not msg or not signature:
        return False
    try:
        return _bls.Verify(public_key, msg, signature)
    except Exception:  # noqa: BLE001 - malformed input is just a failed verify
        return False


def aggregate(signatures: list[bytes]) -> bytes:
    if not signatures:
        raise InvalidKey("no signatures")
    try:
        return _bls.Aggregate(signatures)
    except Exception as exc:  # noqa: BLE001
        raise InvalidKey("signature") from exc


def verify_aggregate(public_keys: list[bytes], msg: bytes, signature: bytes) -> bool:
    """Curve math only. Callers accepting third-party keys must check PoP first."""
    if not public_keys or not msg or not signature:
        return False
    try:
        return _bls.AggregateVerify(public_keys, [msg] * len(public_keys), signature)
    except Exception:  # noqa: BLE001
        return False


def pop_message(network: str, validator: str, public_key: bytes) -> bytes:
    digest = hashlib.sha256()
    digest.update(_POP_DOMAIN)
    for part in (network.encode(), validator.encode(), public_key):
        digest.update(b"\x00" + part)
    return digest.digest()


def sign_pop(secret: bytes, network: str, validator: str) -> bytes:
    return sign(secret, pop_message(network, validator, public_key_of(secret)))


class Registry:
    def __init__(self, network: str) -> None:
        if not network:
            raise InvalidKey("network is required")
        self.network = network
        self._keys: dict[str, tuple[bytes, bytes]] = {}
        self._lock = threading.RLock()

    def register(self, validator: str, public_key: bytes, pop: bytes) -> None:
        if not validator or not public_key or not pop:
            raise InvalidKey("validator, public key, and PoP are required")
        with self._lock:
            if validator in self._keys:
                raise DuplicateKey(validator)
            if not verify(public_key, pop_message(self.network, validator, public_key), pop):
                raise RogueKey(validator)
            self._keys[validator] = (bytes(public_key), bytes(pop))

    def public_key(self, validator: str) -> bytes | None:
        with self._lock:
            entry = self._keys.get(validator)
            return entry[0] if entry else None

    def verify(self, validator: str, msg: bytes, signature: bytes) -> bool:
        with self._lock:
            entry = self._keys.get(validator)
        return bool(entry) and verify(entry[0], msg, signature)

    def verify_aggregate(self, validators: list[str], msg: bytes, signature: bytes) -> None:
        if not validators:
            raise InvalidKey("no validators")
        with self._lock:
            seen: set[str] = set()
            keys: list[bytes] = []
            for validator in validators:
                if validator in seen:
                    raise InvalidKey(f"duplicate validator {validator!r}")
                seen.add(validator)
                entry = self._keys.get(validator)
                if entry is None:
                    raise NoPoP(validator)
                keys.append(entry[0])
        if not verify_aggregate(keys, msg, signature):
            raise InvalidKey("aggregate signature")
