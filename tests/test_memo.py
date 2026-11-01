"""Sealed memo round trips and the shared cross-language vector."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import keys, memo

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _key(index: int) -> keys.KeyPair:
    return keys.keypair_from_mnemonic(
        "abandon abandon abandon abandon abandon abandon abandon abandon abandon "
        "abandon abandon about",
        "", 0, 0, index, "arne:devnet",
    )


def test_seal_open() -> None:
    bob, alice = _key(1), _key(0)
    sealed = memo.seal(memo.public_key(bob.private), b"invoice 42")
    assert b"invoice" not in sealed
    memo.validate(sealed)
    assert memo.open_memo(bob.private, sealed) == b"invoice 42"
    with pytest.raises(memo.MemoError):
        memo.open_memo(alice.private, sealed)


def test_seal_limits() -> None:
    bob = _key(1)
    with pytest.raises(memo.MemoError):
        memo.seal(memo.public_key(bob.private), b"x" * (memo.MAX_PLAINTEXT + 1))
    memo.validate(memo.seal(memo.public_key(bob.private), b"x" * memo.MAX_PLAINTEXT))
    with pytest.raises(memo.MemoError):
        memo.validate(b"not cbor")


def test_memo_vector() -> None:
    data = json.loads((VECTORS / "memo.json").read_text(encoding="utf-8"))
    recipient = keys.keypair_from_mnemonic(
        data["mnemonic"], "", 0, 0, data["recipient_index"], data["network"]
    )
    assert memo.public_key(recipient.private).hex() == data["recipient_public"]
    sealed = bytes.fromhex(data["sealed"])
    assert memo.open_memo(recipient.private, sealed) == data["plaintext"].encode()
    # Same fixed entropy must reproduce the vector byte for byte.
    assert memo.seal(
        bytes.fromhex(data["recipient_public"]),
        data["plaintext"].encode(),
        ephemeral_seed=bytes.fromhex(data["ephemeral_seed"]),
        nonce=bytes.fromhex(data["nonce"]),
    ) == sealed



def test_errors_do_not_leak_key_or_plaintext() -> None:
    recipient = memo.public_key(bytes([0x11]) * 32)
    sealed = memo.seal(recipient, b"top-secret-memo")
    seed = bytes([0x22]) * 32
    with pytest.raises(memo.MemoError) as excinfo:
        memo.open_memo(seed, sealed)
    message = str(excinfo.value)
    assert seed.hex() not in message
    assert "top-secret" not in message
    with pytest.raises(memo.MemoError) as excinfo:
        memo.validate(b"malformed-memo")
    assert "malformed-memo" not in str(excinfo.value)
