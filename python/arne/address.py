"""Bech32m network-scoped addresses (BIP-350)."""

from __future__ import annotations

import hashlib

CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
GENERATORS = [0x3B6A57B2, 0x26508E6D, 0x1EA119FA, 0x3D4233DD, 0x2A1462B3]
HRPS = ("arce", "arce-testnet", "arce-devnet", "arce-consortium")


class AddressError(ValueError):
    pass


def hrp_for(network: str) -> str:
    if not network.startswith("arne:"):
        raise AddressError("invalid network")
    short = network[5:]
    if short == "mainnet":
        return "arce"
    if short == "testnet":
        return "arce-testnet"
    if short == "devnet":
        return "arce-devnet"
    if short.startswith("consortium/"):
        return "arce-consortium"
    raise AddressError("invalid network")


def address_from_pubkey(public: bytes, network: str) -> str:
    digest = hashlib.sha256(public).digest()
    return _bech32_encode(hrp_for(network), b"\x01" + digest[:20])


def decode_address(addr: str) -> tuple[str, bytes]:
    if addr.lower() != addr and addr.upper() != addr:
        raise AddressError("mixed case")
    addr = addr.lower()
    index = addr.rfind("1")
    if index < 1:
        raise AddressError("separator")
    hrp, rest = addr[:index], addr[index + 1 :]
    if hrp not in HRPS:
        raise AddressError("unknown hrp")
    data = _bech32_decode(hrp, rest)
    if len(data) != 21 or data[0] != 1:
        raise AddressError("payload")
    return hrp, bytes(data[1:])


def validate_address(addr: str, network: str) -> None:
    hrp, _ = decode_address(addr)
    if hrp != hrp_for(network):
        raise AddressError("wrong network")


def _polymod(values: list[int]) -> int:
    chk = 0x2BC830A3
    for value in values:
        top = chk >> 25
        chk = (chk & 0x1FFFFFF) << 5 ^ value
        for i in range(5):
            if (top >> i) & 1:
                chk ^= GENERATORS[i]
    return chk


def _hrp_expand(hrp: str) -> list[int]:
    return [ord(c) >> 5 for c in hrp] + [0] + [ord(c) & 31 for c in hrp]


def _convert_bits(data: bytes, from_bits: int, to_bits: int, pad: bool) -> bytes:
    acc = 0
    bits = 0
    out = bytearray()
    maxv = (1 << to_bits) - 1
    for value in data:
        if value >> from_bits:
            raise AddressError("invalid data")
        acc = (acc << from_bits) | value
        bits += from_bits
        while bits >= to_bits:
            bits -= to_bits
            out.append((acc >> bits) & maxv)
    if pad:
        if bits:
            out.append((acc << (to_bits - bits)) & maxv)
    elif bits >= from_bits or ((acc << (to_bits - bits)) & maxv):
        raise AddressError("invalid padding")
    return bytes(out)


def _bech32_encode(hrp: str, data: bytes) -> str:
    five = _convert_bits(data, 8, 5, True)
    values = _hrp_expand(hrp) + list(five) + [0] * 6
    mod = _polymod(values) ^ 0x2BC830A3
    checksum = [(mod >> (5 * (5 - i))) & 31 for i in range(6)]
    return hrp + "1" + "".join(CHARSET[v] for v in list(five) + checksum)


def _bech32_decode(hrp: str, rest: str) -> bytes:
    if len(rest) < 6:
        raise AddressError("short checksum")
    try:
        five = [CHARSET.index(c) for c in rest]
    except ValueError as exc:
        raise AddressError("invalid character") from exc
    if _polymod(_hrp_expand(hrp) + five) != 0x2BC830A3:
        raise AddressError("checksum")
    return _convert_bits(bytes(five[:-6]), 5, 8, False)
