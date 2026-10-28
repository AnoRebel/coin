"""BIP-39 mnemonics, SLIP-0010 Ed25519 derivation, and ARNE key pairs."""

from __future__ import annotations

import hashlib
import hmac
import os
from dataclasses import dataclass
from pathlib import Path

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from .address import address_from_pubkey

PURPOSE_BIP44 = 44
COIN_TYPE_ARNE = 9999
SUITE_ED25519 = "ed25519-v1"

_WORDS: list[str] | None = None
_INDEX: dict[str, int] = {}


class MnemonicError(ValueError):
    pass


def _load_words() -> None:
    global _WORDS
    if _WORDS is not None:
        return
    path = Path(__file__).with_name("bip39_english.txt")
    words = path.read_text(encoding="utf-8").split()
    if len(words) != 2048:
        raise MnemonicError("wordlist must contain 2048 words")
    _WORDS = words
    for i, word in enumerate(words):
        _INDEX[word] = i


def mnemonic_from_entropy(entropy: bytes) -> str:
    if len(entropy) < 16 or len(entropy) > 32 or len(entropy) % 4 != 0:
        raise MnemonicError("entropy length")
    _load_words()
    checksum_bits = len(entropy) * 8 // 32
    digest = hashlib.sha256(entropy).digest()
    bits = "".join(f"{b:08b}" for b in entropy) + "".join(
        f"{digest[i // 8]:08b}"[i % 8] for i in range(checksum_bits)
    )
    return " ".join(_WORDS[int(bits[i : i + 11], 2)] for i in range(0, len(bits), 11))


def validate_mnemonic(mnemonic: str) -> None:
    _load_words()
    parts = mnemonic.split()
    if len(parts) not in (12, 15, 18, 21, 24):
        raise MnemonicError("word count")
    bits = ""
    for word in parts:
        if word not in _INDEX:
            raise MnemonicError(f"unknown word {word!r}")
        bits += f"{_INDEX[word]:011b}"
    checksum_bits = len(parts) * 11 // 33
    entropy_bits = bits[: len(bits) - checksum_bits]
    entropy = bytes(
        int(entropy_bits[i : i + 8], 2) for i in range(0, len(entropy_bits), 8)
    )
    digest = hashlib.sha256(entropy).digest()
    for i in range(checksum_bits):
        want = f"{digest[i // 8]:08b}"[i % 8]
        if bits[len(entropy_bits) + i] != want:
            raise MnemonicError("checksum")


def mnemonic_to_seed(mnemonic: str, passphrase: str = "") -> bytes:
    validate_mnemonic(mnemonic)
    return hashlib.pbkdf2_hmac(
        "sha512", mnemonic.encode(), ("mnemonic" + passphrase).encode(), 2048, 64
    )


def derive_path(
    mnemonic: str, passphrase: str = "", account: int = 0, change: int = 0, index: int = 0
) -> bytes:
    seed = mnemonic_to_seed(mnemonic, passphrase)
    key = hmac.new(b"ed25519 seed", seed, hashlib.sha512).digest()
    secret, chain = key[:32], key[32:]
    for i in (PURPOSE_BIP44, COIN_TYPE_ARNE, account, change, index):
        data = b"\x00" + secret + (i | 0x80000000).to_bytes(4, "big")
        key = hmac.new(chain, data, hashlib.sha512).digest()
        secret, chain = key[:32], key[32:]
    return secret


@dataclass
class KeyPair:
    private: bytes
    public: bytes
    address: str
    network: str

    def sign(self, payload: bytes) -> bytes:
        key = Ed25519PrivateKey.from_private_bytes(self.private)
        return key.sign(payload)


def keypair_from_private(seed: bytes, network: str) -> KeyPair:
    if len(seed) != 32:
        raise MnemonicError("seed length")
    key = Ed25519PrivateKey.from_private_bytes(seed)
    public = key.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    return KeyPair(seed, public, address_from_pubkey(public, network), network)


def keypair_from_mnemonic(
    mnemonic: str, passphrase: str = "", account: int = 0, change: int = 0, index: int = 0,
    network: str = "arne:devnet",
) -> KeyPair:
    return keypair_from_private(derive_path(mnemonic, passphrase, account, change, index), network)


def new_keypair(network: str) -> tuple[KeyPair, str]:
    mnemonic = mnemonic_from_entropy(os.urandom(32))
    return keypair_from_mnemonic(mnemonic, network=network), mnemonic


def verify_ed25519(public: bytes, payload: bytes, signature: bytes) -> bool:
    if len(public) != 32 or len(signature) != 64:
        return False
    try:
        Ed25519PrivateKey  # noqa: B018 - keep import used for type clarity
        from cryptography.exceptions import InvalidSignature
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

        Ed25519PublicKey.from_public_bytes(public).verify(signature, payload)
        return True
    except InvalidSignature:
        return False
