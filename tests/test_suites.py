"""Signature-suite lifecycle: active, deprecated history, disabled rejection."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import suites
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "suites.json"


def _ed_verify(public_key: bytes, message: bytes, signature: bytes) -> bool:
    from cryptography.exceptions import InvalidSignature
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

    try:
        Ed25519PublicKey.from_public_bytes(public_key).verify(signature, message)
        return True
    except (InvalidSignature, ValueError):
        return False


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    registry = suites.Registry([
        suites.Suite(id=raw["id"], algorithm=raw["algorithm"], status=raw["status"], verify=_ed_verify)
        for raw in doc["suites"]
    ])
    private = Ed25519PrivateKey.from_private_bytes(bytes(range(1, 33)))
    public = private.public_key().public_bytes_raw()
    message = b"register account"
    signature = private.sign(message)

    for case in doc["cases"]:
        try:
            registry.can_register(case["suite"])
            registered = True
        except suites.SuiteError:
            registered = False
        assert registered == case["register"], case["suite"]
        try:
            registry.verify(case["suite"], public, message, signature)
            verified = True
        except suites.SuiteError:
            verified = False
        assert verified == case["verify"], case["suite"]


def test_error_kinds_and_validation() -> None:
    registry = suites.Registry([
        suites.Suite(id="ed", algorithm="ed25519", status=suites.ACTIVE, verify=_ed_verify),
        suites.Suite(id="old", algorithm="ed25519", status=suites.DEPRECATED, verify=_ed_verify),
        suites.Suite(id="off", algorithm="ed25519", status=suites.DISABLED),
    ])
    with pytest.raises(suites.DeprecatedSuite):
        registry.can_register("old")
    with pytest.raises(suites.DisabledSuite):
        registry.can_register("off")
    with pytest.raises(suites.UnknownSuite):
        registry.can_register("missing")
    with pytest.raises(suites.DisabledSuite):
        registry.verify("off", b"", b"", b"")
    with pytest.raises(suites.UnknownSuite):
        registry.verify("missing", b"", b"", b"")
    assert registry.ids() == ["ed", "off", "old"]

    bad = [
        [suites.Suite(id="", algorithm="ed25519", status=suites.ACTIVE, verify=_ed_verify)],
        [suites.Suite(id="a", algorithm="", status=suites.ACTIVE, verify=_ed_verify)],
        [suites.Suite(id="a", algorithm="ed25519", status="retired", verify=_ed_verify)],
        [suites.Suite(id="a", algorithm="ed25519", status=suites.ACTIVE)],
        [
            suites.Suite(id="a", algorithm="ed25519", status=suites.ACTIVE, verify=_ed_verify),
            suites.Suite(id="a", algorithm="ed25519", status=suites.ACTIVE, verify=_ed_verify),
        ],
    ]
    for entries in bad:
        with pytest.raises(suites.SuiteError):
            suites.Registry(entries)
