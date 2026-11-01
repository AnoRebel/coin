"""Shared Rust core through cffi: every primitive must match the vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import core

pytestmark = pytest.mark.skipif(not core.available(), reason="crypto core library not built")

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _vectors(name: str) -> dict:
    return json.loads((VECTORS / name).read_text(encoding="utf-8"))


def test_hashes() -> None:
    doc = _vectors("core.json")
    for case in doc["sha256"]:
        assert core.sha256(bytes.fromhex(case["input_hex"])).hex() == case["digest"]
    for case in doc["blake3"]:
        assert core.blake3(bytes.fromhex(case["input_hex"])).hex() == case["digest"]


def test_ed25519() -> None:
    doc = _vectors("core.json")["ed25519"]
    seed = bytes.fromhex(doc["seed"])
    public_key = core.ed25519_public_key(seed)
    assert public_key.hex() == doc["public_key"]
    message = bytes.fromhex(doc["message_hex"])
    signature = core.ed25519_sign(seed, message)
    assert signature.hex() == doc["signature"]
    assert core.ed25519_verify(public_key, message, signature)
    assert not core.ed25519_verify(public_key, b"other", signature)


def test_bls() -> None:
    doc = _vectors("bls.json")
    message = doc["message"].encode()
    secrets: dict[str, bytes] = {}
    public_keys: dict[str, bytes] = {}
    signatures: dict[str, bytes] = {}
    for validator in doc["validators"]:
        secret = core.bls_secret_from_seed(validator["seed"].encode())
        assert secret.hex() == validator["secret"]
        public_key = core.bls_public_key(secret)
        assert public_key.hex() == validator["public_key"]
        signature = core.bls_sign(secret, message)
        assert core.bls_verify(public_key, message, signature)
        secrets[validator["id"]] = secret
        public_keys[validator["id"]] = public_key
        signatures[validator["id"]] = signature

    ids = doc["aggregate"]["validators"]
    aggregate = core.bls_aggregate([signatures[i] for i in ids])
    assert aggregate.hex() == doc["aggregate"]["signature"]
    assert core.bls_aggregate_verify([public_keys[i] for i in ids], message, aggregate)
    assert not core.bls_aggregate_verify([public_keys[i] for i in ids], b"other", aggregate)


def test_ml_dsa() -> None:
    doc = _vectors("hybrid.json")
    seed = bytes.fromhex(doc["seed"])
    public_key = core.ml_dsa_public_key(seed)
    assert public_key.hex() == doc["key"]["ml_dsa_65"]
    message = doc["message"].encode()
    signature = core.ml_dsa_sign(seed, message, b"arne-hybrid-v1")
    assert signature.hex() == doc["signature"]["ml_dsa_65"]
    assert core.ml_dsa_verify(public_key, message, b"arne-hybrid-v1", signature)
    assert not core.ml_dsa_verify(public_key, message, b"other", signature)


def test_invalid_input_is_rejected() -> None:
    with pytest.raises(core.InvalidInput):
        core.ed25519_public_key(b"short")
    with pytest.raises(core.InvalidInput):
        core.bls_sign(b"short", b"m")
    with pytest.raises(core.InvalidInput):
        core.bls_aggregate([])
    with pytest.raises(core.InvalidInput):
        core.ml_dsa_public_key(b"short")
