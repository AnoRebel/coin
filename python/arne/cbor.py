"""Canonical deterministic CBOR, byte-compatible with the Go implementation."""

from __future__ import annotations

import struct
from typing import Any

MAX_DEPTH = 64


class CBORError(ValueError):
    pass


def encode(value: Any) -> bytes:
    return _encode(value, 0)


def _head(major: int, n: int) -> bytes:
    if n < 24:
        return bytes([(major << 5) | n])
    if n <= 0xFF:
        return bytes([(major << 5) | 24, n])
    if n <= 0xFFFF:
        return bytes([(major << 5) | 25]) + struct.pack(">H", n)
    if n <= 0xFFFFFFFF:
        return bytes([(major << 5) | 26]) + struct.pack(">I", n)
    return bytes([(major << 5) | 27]) + struct.pack(">Q", n)


def _encode(value: Any, depth: int) -> bytes:
    if depth > MAX_DEPTH:
        raise CBORError("depth limit")
    if value is None:
        return b"\xf6"
    if value is True:
        return b"\xf5"
    if value is False:
        return b"\xf4"
    if isinstance(value, int) and not isinstance(value, bool):
        if value >= 0:
            if value > 0xFFFFFFFFFFFFFFFF:
                raise CBORError("integer too large")
            return _head(0, value)
        if value < -0x10000000000000000:
            raise CBORError("integer too small")
        return _head(1, -1 - value)
    if isinstance(value, str):
        raw = value.encode("utf-8")
        return _head(3, len(raw)) + raw
    if isinstance(value, (bytes, bytearray)):
        raw = bytes(value)
        return _head(2, len(raw)) + raw
    if isinstance(value, (list, tuple)):
        return _head(4, len(value)) + b"".join(_encode(v, depth + 1) for v in value)
    if isinstance(value, dict):
        encoded = [(_encode(k, depth + 1), _encode(v, depth + 1)) for k, v in value.items()]
        for k, _ in encoded:
            if k[:1] and k[0] >> 5 != 3:
                raise CBORError("map keys must be text")
        encoded.sort(key=lambda kv: (len(kv[0]), kv[0]))
        return _head(5, len(encoded)) + b"".join(k + v for k, v in encoded)
    raise CBORError(f"unsupported type {type(value).__name__}")


def decode(data: bytes) -> Any:
    value, offset = _decode(data, 0, 0)
    if offset != len(data):
        raise CBORError("trailing bytes")
    return value


def _decode(data: bytes, offset: int, depth: int) -> tuple[Any, int]:
    if depth > MAX_DEPTH:
        raise CBORError("depth limit")
    if offset >= len(data):
        raise CBORError("truncated")
    initial = data[offset]
    major, info = initial >> 5, initial & 0x1F
    value, offset = _decode_head(data, offset, info)
    if major == 7:
        if info == 20:
            return False, offset
        if info == 21:
            return True, offset
        if info == 22:
            return None, offset
        raise CBORError("reserved simple value")
    if major == 0:
        return value, offset
    if major == 1:
        if value > 0x7FFFFFFFFFFFFFFF:
            raise CBORError("negative integer too large")
        return -1 - value, offset
    if major in (2, 3):
        if value > len(data) - offset:
            raise CBORError("truncated string")
        end = offset + value
        raw = data[offset:end]
        if major == 2:
            return bytes(raw), end
        try:
            return raw.decode("utf-8"), end
        except UnicodeDecodeError as exc:
            raise CBORError("invalid utf-8") from exc
    if major == 4:
        if value > len(data) - offset:
            raise CBORError("truncated array")
        out = []
        for _ in range(value):
            item, offset = _decode(data, offset, depth + 1)
            out.append(item)
        return out, offset
    if major == 5:
        if value > len(data) - offset:
            raise CBORError("truncated map")
        out: dict[str, Any] = {}
        previous: bytes | None = None
        for _ in range(value):
            start = offset
            key, offset = _decode(data, offset, depth + 1)
            if not isinstance(key, str):
                raise CBORError("map keys must be text")
            encoded = data[start:offset]
            if previous is not None and not _key_less(previous, encoded):
                raise CBORError("non-canonical map order")
            previous = encoded
            if key in out:
                raise CBORError("duplicate map key")
            out[key], offset = _decode(data, offset, depth + 1)
        return out, offset
    raise CBORError("unsupported major type")


def _key_less(a: bytes, b: bytes) -> bool:
    if len(a) != len(b):
        return len(a) < len(b)
    return a < b


def _decode_head(data: bytes, offset: int, info: int) -> tuple[int, int]:
    if info < 24:
        return info, offset + 1
    sizes = {24: 1, 25: 2, 26: 4, 27: 8}
    size = sizes.get(info)
    if size is None:
        raise CBORError("indefinite or reserved length")
    if offset + 1 + size > len(data):
        raise CBORError("truncated head")
    value = int.from_bytes(data[offset + 1 : offset + 1 + size], "big")
    if (size == 1 and value < 24) or (size == 2 and value <= 0xFF) or (
        size == 4 and value <= 0xFFFF
    ) or (size == 8 and value <= 0xFFFFFFFF):
        raise CBORError("non-shortest integer")
    return value, offset + 1 + size


def decode_map(data: bytes) -> dict[str, Any]:
    """Decode and require a map (the canonical ARNE object shape)."""
    value = decode(data)
    if not isinstance(value, dict):
        raise CBORError("expected a map")
    return value
