"""Signer vectors: canonical details, software keys, hardware adapters."""

from __future__ import annotations

import json
from dataclasses import replace
from pathlib import Path

import pytest
from arne import signer

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "signer.json"


def load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def details(raw: dict) -> signer.Details:
    return signer.Details(**raw)


def test_details_vectors() -> None:
    doc = load()
    base = details(doc["details"])
    base.validate()
    assert base.payload().decode("utf-8") == doc["payload"]
    for case in doc["invalid"]:
        candidate = replace(base, **case["overrides"])
        with pytest.raises(signer.SignerError) as caught:
            candidate.validate()
        assert caught.value.code == "invalid", case["name"]


def test_software_signer() -> None:
    doc = load()
    base = details(doc["details"])
    software = signer.Software(bytes(range(32)))
    signature = software.sign(base)
    assert signer.verify(software.public_key(), base, signature)
    bad = bytearray(signature)
    bad[0] ^= 0xFF
    assert not signer.verify(software.public_key(), base, bytes(bad))


class FakeDevice:
    def __init__(self, public_key: str, suite: str, signature: bytes) -> None:
        self._public_key = public_key
        self._suite = suite
        self._signature = signature
        self.displayed: str | None = None
        self.display_error: Exception | None = None
        self.sign_error: Exception | None = None

    def public_key(self) -> str:
        return self._public_key

    def suite(self) -> str:
        return self._suite

    def display(self, text: str) -> None:
        self.displayed = text
        if self.display_error is not None:
            raise self.display_error

    def sign(self, payload: bytes) -> bytes:
        if self.sign_error is not None:
            raise self.sign_error
        return self._signature


def test_hardware_signer() -> None:
    doc = load()
    base = details(doc["details"])
    seed = bytes([2]) + bytes(31)
    software = signer.Software(seed)
    device = FakeDevice(software.public_key(), signer.SUITE_ED25519, software.sign(base))
    hardware = signer.Hardware(device)
    signature = hardware.sign(base)
    assert signer.verify(software.public_key(), base, signature)
    assert device.displayed == base.human()
    assert 'memo: "invoice 42"' in (device.displayed or "")

    device._suite = "secp256k1-v1"
    with pytest.raises(signer.SignerError) as caught:
        hardware.sign(base)
    assert caught.value.code == "unsupported"

    device._suite = signer.SUITE_ED25519
    device.display_error = RuntimeError("rejected")
    with pytest.raises(signer.SignerError) as caught:
        hardware.sign(base)
    assert caught.value.code == "display"

    device.display_error = None
    device._signature = signer.Software(bytes(32)).sign(base)
    with pytest.raises(signer.SignerError) as caught:
        hardware.sign(base)
    assert caught.value.code == "signature"
