"""W3C Verifiable Credentials 2.0 with selective disclosure, shared vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import keys, vc

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "vc.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _count_disclosures(credential: str) -> int:
    return len(credential.split("~")) - 1


def _tamper(credential: str) -> str:
    head, _, rest = credential.partition("~")
    segments = head.split(".")
    signature = segments[2]
    replacement = "B" if signature[0] == "A" else "A"
    segments[2] = replacement + signature[1:]
    return ".".join(segments) + "~" + rest


def test_shared_vectors() -> None:
    doc = _load()
    key = keys.keypair_from_private(bytes.fromhex(doc["issuer_seed"]), doc["network"])
    assert key.public.hex() == doc["issuer_public"]
    for case in doc["cases"]:
        presented = vc.present(doc["credential"], *case["disclose"])
        if case.get("tamper"):
            presented = _tamper(presented)
        else:
            assert _count_disclosures(presented) == len(case["disclose"]), case["name"]

        options = vc.VerifyOptions(
            now=case["now"],
            require=case["require"],
            revoked=tuple(case.get("revoked", ())),
            disclose=tuple(case["disclose"]),
        )
        expect = case["expect"]
        if expect.get("error"):
            with pytest.raises(vc.VCError, match=expect["error"]):
                vc.verify(presented, key.public, options)
            continue

        result = vc.verify(presented, key.public, options)
        assert result.claims == expect["claims"], case["name"]
        assert result.issuer == doc["issuer"] and result.id == doc["id"], case["name"]
        assert result.type == doc["type"], case["name"]
        assert result.issued_at == doc["issued_at"], case["name"]
        assert result.expires_at == doc["expires_at"], case["name"]


def test_issue_matches_go_vector() -> None:
    doc = _load()
    key = keys.keypair_from_private(bytes.fromhex(doc["issuer_seed"]), doc["network"])
    options = vc.IssueOptions(
        issuer=doc["issuer"],
        id=doc["id"],
        type=doc["type"],
        issued_at=doc["issued_at"],
        expires_at=doc["expires_at"],
        salts={name: bytes.fromhex(raw) for name, raw in doc["salts"].items()},
    )
    assert vc.issue(doc["claims"], options, key) == doc["credential"]


def test_expiry_boundary() -> None:
    doc = _load()
    key = keys.keypair_from_private(bytes.fromhex(doc["issuer_seed"]), doc["network"])
    with pytest.raises(vc.VCError, match="expired"):
        vc.verify(doc["credential"], key.public, vc.VerifyOptions(now=doc["expires_at"]))
