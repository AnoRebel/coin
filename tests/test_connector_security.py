"""Connector transport security: allowlists, mTLS rotation, and redaction."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import connector_security as security

VECTORS = (
    Path(__file__).resolve().parent.parent / "spec" / "vectors" / "connector_security.json"
)


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _credential(raw: dict) -> security.Credential:
    return security.Credential(
        id=raw["id"],
        fingerprint=raw["fingerprint"],
        not_before=raw["not_before"],
        not_after=raw.get("not_after", 0),
    )


def _run(allow, registry, case: dict):
    kind = case["kind"]
    if kind == "allowlist":
        security.Allowlist(case["hosts"])
        return {}
    if kind == "endpoint":
        endpoint = security.parse_endpoint(case["url"], allow)
        return {"host": endpoint.host, "path": endpoint.path, "port": endpoint.port}
    if kind == "register":
        registry.register(_credential(case["credential"]))
        return {}
    if kind == "authorize":
        return {"id": registry.authorize(case["fingerprint"], case["now"]).id}
    if kind == "rotate":
        registry.rotate(
            case["old_id"], _credential(case["credential"]), case["now"], case["overlap"]
        )
        return {}
    if kind == "revoke":
        registry.revoke(case["id"], case["at"])
        return {}
    if kind == "retires_at":
        at = registry.retires_at(case["id"])
        return {"found": at is not None, "at": at or 0}
    if kind == "redact_url":
        return {"text": security.redact_url(case["url"])}
    if kind == "redact_headers":
        return {"headers": security.redact_headers(case["headers"])}
    if kind == "redact_account":
        return {"text": security.redact_account(case["account"])}
    raise AssertionError(kind)


def test_shared_vectors() -> None:
    doc = _load()
    allow = security.Allowlist(doc["allowlist"])
    registry = security.Registry()

    for case in doc["cases"]:
        expect = case.get("expect", {})
        if expect.get("error"):
            with pytest.raises(security.ConnectorSecurityError, match=expect["error"]):
                _run(allow, registry, case)
            continue
        got = _run(allow, registry, case)
        for key, value in expect.items():
            assert got.get(key) == value, (case["name"], key, got, value)


def test_rotation_and_revocation_boundaries() -> None:
    registry = security.Registry()
    old = security.Credential("old", "a" * 64, 0)
    registry.register(old)
    registry.rotate("old", security.Credential("new", "b" * 64, 0), 100, 30)

    assert registry.authorize(old.fingerprint, 129).id == "old"
    with pytest.raises(security.ConnectorSecurityError, match="retired"):
        registry.authorize(old.fingerprint, 130)
    assert registry.authorize("b" * 64, 100).id == "new"

    registry.revoke("new", 200)
    assert registry.authorize("b" * 64, 199).id == "new"
    with pytest.raises(security.ConnectorSecurityError, match="retired"):
        registry.authorize("b" * 64, 200)


def test_redaction_never_echoes_a_parse_failure() -> None:
    assert security.redact_url("https://user:secret@bad host/x") == "[invalid-url]"
    assert security.redact_headers({"X-Api-Key": "k"}) == {"X-Api-Key": "REDACTED"}
