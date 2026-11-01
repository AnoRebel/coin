"""Hybrid Ed25519 + ML-DSA: vectors, downgrade rejection, and partial signatures."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import hybrid
from dilithium_py.ml_dsa import ML_DSA_65

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "hybrid.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _key(doc: dict) -> hybrid.Key:
    return hybrid.Key(
        ed25519=bytes.fromhex(doc["key"]["ed25519"]),
        ml_dsa_65=bytes.fromhex(doc["key"]["ml_dsa_65"]),
    )


def _sig(doc: dict) -> hybrid.Signature:
    return hybrid.Signature(
        ed25519=bytes.fromhex(doc["signature"]["ed25519"]),
        ml_dsa_65=bytes.fromhex(doc["signature"]["ml_dsa_65"]),
    )


def test_shared_vectors() -> None:
    doc = _load()
    key, signature = _key(doc), _sig(doc)
    hybrid.register(key)
    hybrid.verify(key, doc["message"].encode(), signature)

    seed = bytes.fromhex(doc["seed"])
    ml_public, ml_private = ML_DSA_65._keygen_internal(seed)
    assert ml_public == key.ml_dsa_65
    got = hybrid.sign(seed, ml_private, doc["message"].encode())
    assert got.ed25519 == signature.ed25519
    assert got.ml_dsa_65 == signature.ml_dsa_65


def test_downgrade_and_partial_signatures_fail() -> None:
    doc = _load()
    key, signature = _key(doc), _sig(doc)
    message = doc["message"].encode()

    with pytest.raises(hybrid.InvalidKey):
        hybrid.register(hybrid.Key(ed25519=key.ed25519, ml_dsa_65=b""))
    with pytest.raises(hybrid.InvalidKey):
        hybrid.register(hybrid.Key(ed25519=b"", ml_dsa_65=key.ml_dsa_65))
    with pytest.raises(hybrid.PartialSignature):
        hybrid.verify(key, message, hybrid.Signature(ed25519=signature.ed25519, ml_dsa_65=b""))
    with pytest.raises(hybrid.PartialSignature):
        hybrid.verify(key, message, hybrid.Signature(ed25519=b"", ml_dsa_65=signature.ml_dsa_65))

    bad_ml = bytearray(signature.ml_dsa_65)
    bad_ml[0] ^= 0xFF
    with pytest.raises(hybrid.VerifyFailed):
        hybrid.verify(key, message, hybrid.Signature(signature.ed25519, bytes(bad_ml)))
    bad_ed = bytearray(signature.ed25519)
    bad_ed[0] ^= 0xFF
    with pytest.raises(hybrid.VerifyFailed):
        hybrid.verify(key, message, hybrid.Signature(bytes(bad_ed), signature.ml_dsa_65))
    with pytest.raises(hybrid.VerifyFailed):
        hybrid.verify(key, b"other message", signature)


def test_validation() -> None:
    with pytest.raises(hybrid.InvalidKey):
        hybrid.sign(b"", b"", b"m")
    doc = _load()
    key, signature = _key(doc), _sig(doc)
    with pytest.raises(hybrid.InvalidKey):
        hybrid.verify(key, b"", signature)
    with pytest.raises(hybrid.InvalidKey):
        hybrid.verify(key, b"m", hybrid.Signature(ed25519=b"short", ml_dsa_65=signature.ml_dsa_65))
    with pytest.raises(hybrid.InvalidKey):
        hybrid.verify(hybrid.Key(ed25519=b"short", ml_dsa_65=key.ml_dsa_65), b"m", signature)
