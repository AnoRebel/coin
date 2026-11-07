"""Passkey vectors: enrollment, scoped assertions, origin and counter checks."""

from __future__ import annotations

import base64
import json
from pathlib import Path

import pytest
from arne import passkey

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "passkey.json"

ERRORS = {
    "invalid": passkey.PasskeyError,
    "credential": passkey.PasskeyError,
    "expired": passkey.PasskeyError,
    "client_data": passkey.PasskeyError,
    "challenge": passkey.PasskeyError,
    "origin": passkey.PasskeyError,
    "rp_id": passkey.PasskeyError,
    "flags": passkey.PasskeyError,
    "counter": passkey.PasskeyError,
    "signature": passkey.PasskeyError,
    "authenticator_data": passkey.PasskeyError,
}


def load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def credential(raw: dict) -> passkey.Credential:
    return passkey.Credential(**raw)


def scope(raw: dict) -> passkey.Scope:
    return passkey.Scope(**raw)


def assertion(raw: dict) -> passkey.Assertion:
    return passkey.Assertion(
        client_data_json=base64.b64decode(raw["client_data"]),
        authenticator_data=base64.b64decode(raw["authenticator_data"]),
        signature=base64.b64decode(raw["signature"]),
    )


def test_challenge_vectors() -> None:
    for case in load()["challenges"]:
        assert passkey.challenge(scope(case["scope"])) == case["challenge"]


def test_invalid_scopes_and_credentials() -> None:
    doc = load()
    for case in doc["invalid_scopes"]:
        with pytest.raises(passkey.PasskeyError) as caught:
            passkey.challenge(scope(case["scope"]))
        assert caught.value.code == "invalid", case["name"]
    for case in doc["invalid_credentials"]:
        manager = passkey.Manager()
        with pytest.raises(passkey.PasskeyError) as caught:
            manager.enroll(credential(case["credential"]))
        assert caught.value.code == "invalid", case["name"]


def test_assertion_vectors() -> None:
    for case in load()["cases"]:
        manager = passkey.Manager()
        manager.enroll(credential(case["credential"]))
        with_error = case.get("error")
        try:
            stored = manager.authorize(
                case["credential"]["id"],
                scope(case["scope"]),
                assertion(case),
                case["at"],
            )
        except passkey.PasskeyError as error:
            assert with_error is not None, f"{case['name']}: {error}"
            assert error.code == with_error, case["name"]
            current = manager.get(case["credential"]["id"])
            assert current is not None
            assert current.counter == case["credential"]["counter"], case["name"]
        else:
            assert with_error is None, case["name"]
            assert stored.counter == case["expect_counter"], case["name"]


def test_enrollment_lifecycle() -> None:
    doc = load()
    case = doc["cases"][0]
    manager = passkey.Manager()
    manager.enroll(credential(case["credential"]))
    with pytest.raises(passkey.PasskeyError) as caught:
        manager.enroll(credential(case["credential"]))
    assert caught.value.code == "duplicate"
    manager.revoke(case["credential"]["id"])
    with pytest.raises(passkey.PasskeyError) as caught:
        manager.authorize(
            case["credential"]["id"], scope(case["scope"]), assertion(case), 1
        )
    assert caught.value.code == "credential"
    with pytest.raises(passkey.PasskeyError) as caught:
        manager.authorize("missing", scope(case["scope"]), assertion(case), 1)
    assert caught.value.code == "credential"
