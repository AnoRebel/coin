"""Commit certificates, validator-set proofs, and light-client checks.

Mirrors internal/lightclient. A verifier counts only signatures from its
trusted set and requires more than two-thirds of the trusted voting power.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field

from arne import bls, smt


class LightClientError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise LightClientError(code, message)


@dataclass(frozen=True)
class Validator:
    id: str
    public_key: str
    power: int


@dataclass
class Set:
    height: int
    validators: list[Validator] = field(default_factory=list)


@dataclass(frozen=True)
class Signature:
    validator: str
    signature: str


@dataclass
class Certificate:
    height: int
    round: int
    block_hash: str
    state_root: str
    next_validators_root: str
    signatures: list[Signature] = field(default_factory=list)


def validate_set(s: Set) -> None:
    if s.height == 0 or not s.validators:
        _fail("invalid", "set needs a height and at least one validator")
    seen: set[str] = set()
    for v in s.validators:
        if not v.id or v.power <= 0:
            _fail("invalid", "validator needs an id and positive power")
        if v.id in seen:
            _fail("duplicate validator", f"validator {v.id}")
        seen.add(v.id)
        try:
            key = bytes.fromhex(v.public_key)
        except ValueError:
            _fail("invalid", f"validator {v.id} key is not hex")
        if not key:
            _fail("invalid", f"validator {v.id} key is not hex")


def set_root(s: Set) -> str:
    tree = smt.Tree()
    for v in s.validators:
        tree.set(_member_key(v.id), _member_value(v))
    return tree.state_root().hex()


def prove(s: Set, id: str) -> smt.Proof | None:
    if not any(v.id == id for v in s.validators):
        return None
    tree = smt.Tree()
    for v in s.validators:
        tree.set(_member_key(v.id), _member_value(v))
    return tree.prove(_member_key(id))


def verify_member(root: str, v: Validator, proof: smt.Proof) -> None:
    parsed = _parse_root(root)
    if proof.key != _member_key(v.id):
        _fail("invalid proof", f"proof is for {proof.key!r}")
    if proof.value != _member_value(v):
        _fail("invalid proof", "validator value mismatch")
    try:
        proof.verify(parsed)
    except smt.ProofError as exc:
        _fail("invalid proof", str(exc))


def verify(network: str, trusted: Set, cert: Certificate) -> int:
    validate_set(trusted)
    if cert.height <= trusted.height:
        _fail("outdated", f"certificate height {cert.height}, trusted {trusted.height}")
    if not cert.block_hash or not cert.state_root or not cert.next_validators_root:
        _fail("invalid", "certificate fields are empty")
    by_id = {v.id: v for v in trusted.validators}
    total = sum(v.power for v in trusted.validators)
    msg = message(network, cert)
    signed = 0
    seen: set[str] = set()
    for sig in cert.signatures:
        v = by_id.get(sig.validator)
        if v is None:
            _fail("unknown validator", sig.validator)
        if sig.validator in seen:
            _fail("duplicate validator", sig.validator)
        seen.add(sig.validator)
        try:
            key = bytes.fromhex(v.public_key)
            raw = bytes.fromhex(sig.signature)
        except ValueError:
            _fail("forged", "signature is not hex")
        if not raw or not bls.verify(key, msg, raw):
            _fail("forged", f"signature from {sig.validator}")
        signed += v.power
    if signed * 3 <= total * 2:
        _fail("insufficient quorum", f"signed {signed} of {total}")
    return signed


def advance(network: str, trusted: Set, cert: Certificate, next_set: Set) -> Set:
    verify(network, trusted, cert)
    validate_set(next_set)
    if next_set.height != cert.height + 1:
        _fail("invalid", f"next set height {next_set.height}, certificate {cert.height}")
    if set_root(next_set) != cert.next_validators_root:
        _fail("invalid proof", "next set root mismatch")
    return next_set


def verify_state(cert: Certificate, key: str, value: bytes, proof: smt.Proof) -> None:
    root = _parse_root(cert.state_root)
    if proof.key != key:
        _fail("invalid proof", f"proof is for {proof.key!r}")
    if proof.value != value:
        _fail("invalid proof", "value mismatch")
    try:
        proof.verify(root)
    except smt.ProofError as exc:
        _fail("invalid proof", str(exc))


def message(network: str, cert: Certificate) -> bytes:
    digest = hashlib.sha256()
    digest.update(b"arne-cert-v1")
    for part in (
        network, str(cert.height), str(cert.round), cert.block_hash,
        cert.state_root, cert.next_validators_root,
    ):
        digest.update(b"\x00" + part.encode())
    return digest.digest()


def _member_key(id: str) -> str:
    return "validator/" + id


def _member_value(v: Validator) -> bytes:
    return f"{v.public_key}:{v.power}".encode()


def _parse_root(root: str) -> bytes:
    try:
        raw = bytes.fromhex(root)
    except ValueError:
        _fail("invalid", "root is not 32-byte hex")
    if len(raw) != 32:
        _fail("invalid", "root is not 32-byte hex")
    return raw
