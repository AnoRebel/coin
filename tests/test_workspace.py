"""Workspace roles, permissions, audit logging, and HTTP enforcement."""

from __future__ import annotations

import json
import time
from pathlib import Path

import pytest
from arne import cbor, types
from arne.api import ApiConfig, create_app
from arne.workspace import (
    AUDITOR,
    MEMBER,
    OWNER,
    PAYMENT_READ,
    PAYMENT_WRITE,
    Forbidden,
    LastOwner,
    Workspace,
)
from fastapi.testclient import TestClient

from test_api import NETWORK, make_node

KEYS = {"key-alice": "alice", "key-bob": "bob", "key-carol": "carol", "key-dave": "dave"}


def make_workspace(tmp_path: Path) -> Workspace:
    return Workspace(
        {"alice": OWNER, "bob": MEMBER, "carol": MEMBER, "dave": AUDITOR},
        KEYS,
        audit_path=str(tmp_path / "audit.jsonl"),
    )


def test_roles_and_enforcement(tmp_path: Path) -> None:
    ws = make_workspace(tmp_path)
    assert ws.authorize("dave", PAYMENT_READ)
    assert not ws.authorize("dave", PAYMENT_WRITE)
    with pytest.raises(Forbidden):
        ws.remove("carol", "bob")
    entries = ws.entries()
    assert entries[-1]["actor"] == "carol" and entries[-1]["result"] == "denied"

    ws.set_role("alice", "bob", "admin")
    with pytest.raises(Forbidden):
        ws.remove("bob", "alice")
    with pytest.raises(LastOwner):
        ws.remove("alice", "alice")
    ws.remove("bob", "carol")
    assert ws.role_of("carol") is None


def test_audit_persists(tmp_path: Path) -> None:
    path = tmp_path / "workspace.json"
    path.write_text(json.dumps({"members": {"alice": OWNER}, "principals": {"k": "alice"}}))
    ws = Workspace.load(str(path))
    ws.audit("alice", PAYMENT_WRITE, "abc", "accepted")
    ws.set_role("alice", "bob", MEMBER)

    again = Workspace.load(str(path))
    assert again.role_of("bob") == MEMBER
    assert [e["seq"] for e in again.entries()] == [1, 2]
    assert again.entries(after=1)[0]["target"] == "bob"


def test_http_enforcement(tmp_path: Path) -> None:
    node, keys = make_node()
    node.produce_block(int(time.time()))
    ws = make_workspace(tmp_path)
    client = TestClient(create_app(node, ApiConfig(devnet=True, workspace=ws)))
    owner = {"Authorization": "Bearer key-alice"}
    member = {"Authorization": "Bearer key-carol"}
    admin = {"Authorization": "Bearer key-bob"}
    auditor = {"Authorization": "Bearer key-dave"}

    assert client.get("/v1/workspace").status_code == 401
    me = client.get("/v1/workspace", headers=member).json()
    assert me["actor"] == "carol" and me["role"] == MEMBER
    assert client.get("/v1/transfers/missing", headers=auditor).status_code == 404
    assert client.get("/v1/workspace/audit", headers=auditor).status_code == 200
    assert client.post("/v1/transactions", headers=auditor, content=b"\xa0").status_code == 403
    assert client.put("/v1/workspace/members/bob", headers=member, json={"role": "admin"}).status_code == 403
    assert client.put("/v1/workspace/members/bob", headers=owner, json={"role": "admin"}).status_code == 200
    assert client.delete("/v1/workspace/members/bob", headers=member).status_code == 403
    assert client.delete("/v1/workspace/members/carol", headers=admin).status_code == 204

    funder = keys["funder"]
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK),
        tx_type=types.TX_TRANSFER, signer=funder.address, nonce=0,
        created_at=now, expires_at=now + 3600, fee_asset="arce:devnet", max_fee=10_000,
        payload={"asset": "arce:devnet", "to": funder.address, "amount": 42},
    )
    tx.sign(funder)
    accepted = client.post("/v1/transactions", headers=admin, content=cbor.encode(tx.to_cbor()))
    assert accepted.status_code == 202, accepted.text

    audit = client.get("/v1/workspace/audit", headers=owner).json()["items"]
    assert any(e["actor"] == "carol" and e["result"] == "denied" for e in audit)
    assert any(e["actor"] == "dave" and e["result"] == "denied" for e in audit)
    assert any(e["actor"] == "bob" and e["action"] == PAYMENT_WRITE for e in audit)
    assert all(not e["actor"].startswith("key-") for e in audit)
