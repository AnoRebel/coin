"""Scoped auditor access grants: vectors, lifecycle, and HTTP enforcement."""

from __future__ import annotations

import json
import time
from pathlib import Path

import pytest
from arne import access, cbor, types
from arne.api import ApiConfig, create_app
from arne.workspace import AUDITOR, MEMBER, OWNER, Workspace
from fastapi.testclient import TestClient
from test_api import NETWORK, make_node

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "audit-grants.json"
KEYS = {"key-alice": "alice", "key-bob": "bob", "key-carol": "carol", "key-dave": "dave"}


def make_workspace(tmp_path: Path) -> Workspace:
    return Workspace(
        {"alice": OWNER, "bob": MEMBER, "carol": MEMBER, "dave": AUDITOR},
        KEYS,
        audit_path=str(tmp_path / "audit.jsonl"),
    )


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    registry = access.Registry()
    for raw in doc["grants"]:
        grant = access.Grant.from_dict(raw)
        registry._grants[grant.id] = grant
    for case in doc["cases"]:
        try:
            grant = registry.check(case["auditor"], case["asset"], case["at"])
        except access.AccessError:
            assert case["result"] != access.ALLOWED, case
            continue
        assert case["result"] == access.ALLOWED, case
        assert grant.id == case["grant"], case
    assert len(registry.events()) == len(doc["cases"])
    for raw in doc.get("id_cases", []):
        grant = access.Grant.from_dict(raw)
        assert access._grant_id(grant) == grant.id


def test_grant_lifecycle(tmp_path: Path) -> None:
    path = tmp_path / "grants.json"
    registry = access.Registry.load(str(path))
    grant = registry.grant(access.Grant(" dave ", ["usd:devnet", "usd:devnet", " eur:devnet "], 100, 200, "audit"))
    assert len(grant.id) == 32
    assert grant.assets == ["eur:devnet", "usd:devnet"]
    with pytest.raises(access.GrantExists):
        registry.grant(grant)
    registry.check("dave", "usd:devnet", 150)

    reloaded = access.Registry.load(str(path))
    assert len(reloaded.list("dave")) == 1
    assert reloaded.list("erin") == []
    assert len(reloaded.events()) == 2

    with pytest.raises(access.UnknownGrant):
        reloaded.revoke("missing")
    reloaded.revoke(grant.id)
    with pytest.raises(access.AccessError):
        reloaded.check("dave", "usd:devnet", 150)
    assert access.Registry.load(str(path)).list() == []


def test_grant_validation() -> None:
    bad = [
        access.Grant("", ["usd:devnet"], 1, 2, "audit"),
        access.Grant("dave", [], 1, 2, "audit"),
        access.Grant("dave", ["usd:devnet"], 2, 2, "audit"),
        access.Grant("dave", ["usd:devnet"], -1, 2, "audit"),
        access.Grant("dave", ["usd:devnet"], 1, 0, "audit"),
        access.Grant("dave", ["usd:devnet"], 1, 2, ""),
    ]
    for grant in bad:
        with pytest.raises(access.InvalidGrant):
            access.Registry().grant(grant)


def test_http_scope_enforcement(tmp_path: Path) -> None:
    node, keys = make_node()
    node.produce_block(int(time.time()))
    workspace = make_workspace(tmp_path)
    grants = access.Registry()
    client = TestClient(create_app(node, ApiConfig(devnet=True, workspace=workspace, audit_grants=grants)))
    owner = {"Authorization": "Bearer key-alice"}
    member = {"Authorization": "Bearer key-carol"}
    auditor = {"Authorization": "Bearer key-dave"}

    funder = keys["funder"]
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK),
        tx_type=types.TX_TRANSFER, signer=funder.address, nonce=0,
        created_at=now, expires_at=now + 3600, fee_asset="arce:devnet", max_fee=10_000,
        payload={"asset": "arce:devnet", "to": funder.address, "amount": 7},
    )
    tx.sign(funder)
    response = client.post("/v1/transactions", headers=member, content=cbor.encode(tx.to_cbor()))
    assert response.status_code == 202, response.text
    node.produce_block(now + 1)
    tx_hash = response.json()["hash"]
    path = f"/v1/transfers/{tx_hash}"

    assert client.get(path, headers=auditor).status_code == 403
    grants.grant(access.Grant("dave", ["usd:devnet"], 0, now + 3600, "audit"))
    assert client.get(path, headers=auditor).status_code == 403
    grants.grant(access.Grant("dave", ["arce:devnet"], 0, now + 3600, "audit"))
    assert client.get(path, headers=auditor).status_code == 200
    assert client.get(path, headers=member).status_code == 200
    assert any(e["auditor"] == "dave" and e["result"] == "allowed" for e in grants.events())

    body = {"auditor": "dave", "assets": ["eur:devnet"], "from": 1, "until": 9_999_999_999, "purpose": "legal"}
    assert client.post("/v1/audit/grants", headers=member, json=body).status_code == 403
    created = client.post("/v1/audit/grants", headers=owner, json=body)
    assert created.status_code == 201, created.text
    grant_id = created.json()["id"]
    assert client.get("/v1/audit/grants", headers=auditor).status_code == 200
    assert client.get("/v1/audit/grants/log", headers=owner).status_code == 200
    assert client.delete(f"/v1/audit/grants/{grant_id}", headers=owner).status_code == 204
    assert client.delete("/v1/audit/grants/missing", headers=owner).status_code == 404
