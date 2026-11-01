"""BLS proof-of-possession: shared vectors, missing PoP, and rogue keys."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import bls

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "bls.json"


def _doc() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def test_shared_vectors() -> None:
    doc = _doc()
    message = doc["message"].encode()
    registry = bls.Registry(doc["network"])
    secrets: dict[str, bytes] = {}
    for raw in doc["validators"]:
        secret = bls.secret_from_seed(raw["seed"].encode())
        assert secret.hex() == raw["secret"]
        public_key = bls.public_key_of(secret)
        assert public_key.hex() == raw["public_key"]
        pop = bls.sign_pop(secret, doc["network"], raw["id"])
        assert pop.hex() == raw["pop"]
        registry.register(raw["id"], public_key, pop)
        signature = bls.sign(secret, message)
        assert registry.verify(raw["id"], message, signature)
        secrets[raw["id"]] = secret

    signatures = [bls.sign(secrets[i], message) for i in doc["aggregate"]["validators"]]
    aggregate = bls.aggregate(signatures)
    assert aggregate.hex() == doc["aggregate"]["signature"]
    registry.verify_aggregate(doc["aggregate"]["validators"], message, aggregate)

    keys = [registry.public_key(i) for i in doc["aggregate"]["validators"]]
    assert not bls.verify_aggregate(keys, b"other", aggregate)


def test_missing_pop_is_rejected() -> None:
    doc = _doc()
    registry = bls.Registry(doc["network"])
    first = doc["validators"][0]
    registry.register(first["id"], bytes.fromhex(first["public_key"]), bytes.fromhex(first["pop"]))
    signature = bls.sign(bytes.fromhex(first["secret"]), doc["message"].encode())
    with pytest.raises(bls.NoPoP):
        registry.verify_aggregate([first["id"], doc["validators"][1]["id"]], doc["message"].encode(), signature)
    with pytest.raises(bls.InvalidKey):
        registry.verify_aggregate([first["id"], first["id"]], doc["message"].encode(), signature)
    with pytest.raises(bls.DuplicateKey):
        registry.register(first["id"], bytes.fromhex(first["public_key"]), bytes.fromhex(first["pop"]))


def test_rogue_key_cannot_register() -> None:
    doc = _doc()
    victim = doc["validators"][0]
    registry = bls.Registry(doc["network"])
    registry.register(victim["id"], bytes.fromhex(victim["public_key"]), bytes.fromhex(victim["pop"]))
    rogue = bytes.fromhex(doc["rogue"]["public_key"])
    forged = bytes.fromhex(doc["rogue"]["forged_signature"])

    # The forged aggregate is valid curve math; PoP is what stops it.
    keys = [rogue, bytes.fromhex(victim["public_key"])]
    assert bls.verify_aggregate(keys, doc["message"].encode(), forged)
    with pytest.raises(bls.RogueKey):
        registry.register("rogue", rogue, forged)
    with pytest.raises(bls.NoPoP):
        registry.verify_aggregate([victim["id"], "rogue"], doc["message"].encode(), forged)


def test_validation() -> None:
    with pytest.raises(bls.InvalidKey):
        bls.Registry("")
    with pytest.raises(bls.InvalidKey):
        bls.secret_from_seed(b"")
    with pytest.raises(bls.InvalidKey):
        bls.public_key_of(b"short")
    with pytest.raises(bls.InvalidKey):
        bls.sign(b"short", b"m")
    with pytest.raises(bls.InvalidKey):
        bls.aggregate([])
    assert not bls.verify(b"", b"m", b"")
    assert not bls.verify_aggregate([], b"m", b"")
    registry = bls.Registry("arce:devnet")
    with pytest.raises(bls.InvalidKey):
        registry.register("", b"", b"")
    assert not registry.verify("missing", b"m", b"s")
    with pytest.raises(bls.InvalidKey):
        registry.verify_aggregate([], b"m", b"s")
