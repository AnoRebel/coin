"""Keep secret bytes out of logs and errors.

Mirrors internal/crypto/redact. Wrap key material in Secret; repr, str, and JSON
encoding redact it. Read the bytes only where they are needed.
"""

from __future__ import annotations

import hmac
import json


class RedactedError(ValueError):
    pass


class Secret:
    __slots__ = ("_b",)

    def __init__(self, value: bytes) -> None:
        self._b = bytes(value)

    def bytes(self) -> bytes:
        return self._b

    def __len__(self) -> int:
        return len(self._b)

    def __repr__(self) -> str:
        return "Secret([REDACTED])"

    def __str__(self) -> str:
        return "[REDACTED]"

    def __format__(self, spec: str) -> str:
        return "[REDACTED]"

    def zero(self) -> None:
        self._b = bytes(len(self._b))

    def to_json(self) -> str:
        raise RedactedError("refusing to serialize secret material")


def equal(a: Secret, b: Secret) -> bool:
    return hmac.compare_digest(a.bytes(), b.bytes())


def equal_bytes(a: Secret, b: bytes) -> bool:
    return hmac.compare_digest(a.bytes(), b)


def dumps(value: object) -> str:
    """json.dumps that refuses Secret values anywhere in the tree."""

    def guard(item: object) -> None:
        if isinstance(item, Secret):
            raise RedactedError("refusing to serialize secret material")
        if isinstance(item, dict):
            for key, inner in item.items():
                guard(key)
                guard(inner)
        elif isinstance(item, (list, tuple)):
            for inner in item:
                guard(inner)

    guard(value)
    return json.dumps(value)
