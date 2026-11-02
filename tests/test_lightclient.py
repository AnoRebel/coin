"""Commit certificates, validator-set proofs, and light-client vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import bls, lightclient, smt

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _load() -> tuple[dict, dict, dict]:
    bls_doc = json.loads((VECTORS / "bls.json").read_text())
    doc = json.loads((VECTORS / "lightclient.json").read_text())
    secrets = {v["id"]: bytes.fromhex(v["secret"]) for v in bls_doc["validators"]}
    keys = {v["id"]: v["public_key"] for v in bls_doc["validators"]}
    return doc, secrets, keys


def _sign(network: str, cert: lightclient.Certificate, secret: bytes) -> str:
    return bls.sign(secret, lightclient.message(network, cert)).hex()


def test_certificate_vectors() -> None:
    doc, secrets, keys = _load()
    trusted = lightclient.Set(
        height=doc["trusted_height"],
        validators=[
            lightclient.Validator(id=v["id"], public_key=keys[v["id"]], power=v["power"])
            for v in doc["validators"]
        ],
    )
    lightclient.validate_set(trusted)

    state = smt.Tree()
    state.set("balance/a/arce", b"100")
    state_root = state.state_root().hex()
    proof = state.prove("balance/a/arce")
    next_set = lightclient.Set(height=doc["certificate"]["height"] + 1, validators=trusted.validators)
    next_root = lightclient.set_root(next_set)

    for case in doc["cases"]:
        height = case.get("height") or doc["certificate"]["height"]
        cert = lightclient.Certificate(
            height=height, round=doc["certificate"]["round"],
            block_hash=doc["certificate"]["block_hash"], state_root=state_root,
            next_validators_root=next_root,
        )
        cert.signatures = [
            lightclient.Signature(validator=signer, signature=_sign(doc["network"], cert, secrets[signer]))
            for signer in case["signers"]
        ]
        if case.get("forge"):
            cert.signatures[-1] = lightclient.Signature(
                validator=cert.signatures[-1].validator,
                signature=_sign(doc["network"], cert, secrets[case["forge"]]),
            )
        if case.get("unknown"):
            cert.signatures.append(lightclient.Signature(
                validator=case["unknown"],
                signature=_sign(doc["network"], cert, secrets["v1"]),
            ))

        error = None
        signed = None
        try:
            signed = lightclient.verify(doc["network"], trusted, cert)
        except lightclient.LightClientError as exc:
            error = exc

        expected = case["expect"]
        if "error" in expected:
            assert error is not None, case["name"]
            assert error.code == expected["error"], (case["name"], error)
            continue
        assert error is None, (case["name"], error)
        assert signed == expected["signed"]
        lightclient.verify_state(cert, "balance/a/arce", b"100", proof)
        lightclient.advance(doc["network"], trusted, cert, next_set)


def test_validator_set_proof() -> None:
    _, _, keys = _load()
    validator_set = lightclient.Set(height=1, validators=[
        lightclient.Validator(id="v1", public_key=keys["v1"], power=2),
        lightclient.Validator(id="v2", public_key=keys["v2"], power=1),
    ])
    root = lightclient.set_root(validator_set)
    proof = lightclient.prove(validator_set, "v2")
    assert proof is not None
    lightclient.verify_member(root, validator_set.validators[1], proof)

    tampered = lightclient.Validator(id="v2", public_key=keys["v2"], power=99)
    with pytest.raises(lightclient.LightClientError):
        lightclient.verify_member(root, tampered, proof)
    with pytest.raises(lightclient.LightClientError):
        lightclient.verify_member("00" + root[2:], validator_set.validators[1], proof)


def test_state_proof_rejects_wrong_value() -> None:
    state = smt.Tree()
    state.set("k", b"v")
    cert = lightclient.Certificate(
        height=2, round=1, block_hash="b",
        state_root=state.state_root().hex(), next_validators_root="00" * 32,
    )
    proof = state.prove("k")
    lightclient.verify_state(cert, "k", b"v", proof)
    with pytest.raises(lightclient.LightClientError):
        lightclient.verify_state(cert, "k", b"other", proof)
