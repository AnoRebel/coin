"""Secret redaction: formatting, JSON, copies, and constant-time equality."""

from __future__ import annotations

import json

import pytest
from arne import redact


def test_formatting_redacts() -> None:
    secret = redact.Secret(b"super-secret-key-material")
    assert str(secret) == "[REDACTED]"
    assert repr(secret) == "Secret([REDACTED])"
    assert f"{secret}" == "[REDACTED]"
    assert "super-secret" not in f"{secret!r}"
    assert "super-secret" not in repr({"key": secret})


def test_json_refuses_secrets() -> None:
    secret = redact.Secret(b"super-secret")
    with pytest.raises(redact.RedactedError):
        secret.to_json()
    with pytest.raises(redact.RedactedError):
        redact.dumps(secret)
    with pytest.raises(redact.RedactedError):
        redact.dumps({"nested": [secret]})
    assert redact.dumps({"ok": 1}) == json.dumps({"ok": 1})


def test_copy_zero_and_equal() -> None:
    raw = bytearray(b"secret")
    secret = redact.Secret(bytes(raw))
    raw[0] = ord("X")
    assert redact.equal_bytes(secret, b"secret")
    secret.zero()
    assert len(secret) == 6 and redact.equal_bytes(secret, b"\x00" * 6)
    assert redact.equal(redact.Secret(b"same"), redact.Secret(b"same"))
    assert not redact.equal(redact.Secret(b"same"), redact.Secret(b"samf"))
    assert not redact.equal(redact.Secret(b"same"), redact.Secret(b"same-longer"))
