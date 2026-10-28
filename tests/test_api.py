"""HTTP parity tests: REST /v1, JSON-RPC, auth, and devnet flow."""

from __future__ import annotations

import time

import pytest
from arne import cbor, ledger, types
from arne.api import ApiConfig, create_app
from arne.keys import keypair_from_mnemonic
from arne.node import Config, MemoryStore, Node, devnet_genesis
from fastapi.testclient import TestClient

NETWORK = "arne:devnet"
MNEMONIC = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"


def make_node() -> tuple[Node, dict[str, object]]:
    funder = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 0, NETWORK)
    faucet = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 1, NETWORK)
    recipient = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 2, NETWORK)
    config = Config(
        network=NETWORK, chain=types.chain_id(NETWORK), params=ledger.default_params(),
        proposer=funder.address, faucet=faucet, devnet=True,
    )
    node = Node.open(
        config, devnet_genesis(NETWORK, 1_700_000_000, funder, faucet), MemoryStore()
    )
    return node, {"funder": funder, "faucet": faucet, "recipient": recipient}


@pytest.fixture()
def setup():
    node, keys = make_node()
    node.produce_block(int(time.time()))
    return node, keys


@pytest.fixture()
def client(setup) -> TestClient:
    node, _ = setup
    return TestClient(create_app(node, ApiConfig(devnet=True)))


def test_read_surface(client: TestClient, setup) -> None:
    node, keys = setup
    funder = keys["funder"]
    status = client.get("/v1/status").json()
    assert status["network"] == NETWORK and status["height"] == 1
    assert client.get("/v1/health").json()["status"] == "ok"
    assert client.get("/v1/version").json()["api"] == "v1"
    assets = client.get("/v1/assets").json()["items"]
    assert {a["id"] for a in assets} == {"arce:devnet", "usd:devnet"}
    assert client.get("/v1/assets/usd:devnet").json()["symbol"] == "USD"
    assert client.get("/v1/assets/nope:devnet").status_code == 404
    account = client.get(f"/v1/accounts/{funder.address}").json()
    assert account["exists"] and account["balances"]["arce:devnet"] > 0
    assert client.get(f"/v1/accounts/{funder.address}/proof?asset=arce:devnet").json()["included"]
    assert client.get("/v1/supply/arce:devnet").json()["supply"] > 0
    assert client.get("/v1/blocks").json()["items"][0]["height"] == 1
    assert client.get("/v1/blocks/1").json()["height"] == 1
    assert client.get("/v1/blocks/99").status_code == 404
    assert client.get("/v1/proofs/balance/anything/nothing").json()["included"] is False
    assert client.get(f"/v1/escrows/esc_{'0' * 32}").status_code == 404
    allowance = client.get(
        f"/v1/allowances/{funder.address}/{funder.address}/arce:devnet"
    ).json()
    assert allowance["remaining"] == 0


def test_faucet_and_payment_flow(client: TestClient, setup) -> None:
    node, keys = setup
    funder, recipient = keys["funder"], keys["recipient"]
    response = client.post("/v1/faucet", json={"address": recipient.address})
    assert response.status_code == 202
    faucet_hash = response.json()["hash"]
    now = int(time.time())
    node.produce_block(now + 1)
    assert node.receipt(faucet_hash).success

    account = client.get(f"/v1/accounts/{recipient.address}").json()
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK),
        tx_type=types.TX_TRANSFER, signer=recipient.address, nonce=account["nonce"],
        created_at=now, expires_at=now + 3600, fee_asset="arce:devnet", max_fee=10_000,
        payload={"asset": "arce:devnet", "to": funder.address, "amount": 5_000_000},
    )
    tx.sign(recipient)
    response = client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()))
    assert response.status_code == 202, response.text
    tx_hash = response.json()["hash"]
    assert client.get(f"/v1/transactions/{tx_hash}").status_code == 404
    node.produce_block(now + 2)
    receipt = client.get(f"/v1/transactions/{tx_hash}").json()
    assert receipt["success"] and receipt["fee"] == 1000

    simulated = client.post(
        "/v1/transactions/simulate", content=cbor.encode(tx.to_cbor())
    ).json()
    assert simulated["success"] is False  # nonce already spent


def test_submit_rejects_garbage(client: TestClient) -> None:
    assert client.post("/v1/transactions", content=b"\xa0").status_code == 400
    assert client.post("/v1/transactions", content=b"").status_code == 400
    assert client.post("/v1/transactions", json={"tx": "!!!"}).status_code == 400


def test_json_rpc(client: TestClient, setup) -> None:
    node, keys = setup
    funder = keys["funder"]
    single = client.post("/rpc", json={
        "jsonrpc": "2.0", "id": "a", "method": "arne_status", "params": {},
    }).json()
    assert single["id"] == "a" and single["result"]["network"] == NETWORK

    balance = client.post("/rpc", json={
        "jsonrpc": "2.0", "id": 1, "method": "arne_getBalance",
        "params": {"address": funder.address, "asset": "arce:devnet"},
    }).json()
    assert balance["result"] > 0

    batch = client.post("/rpc", json=[
        {"jsonrpc": "2.0", "id": 1, "method": "arne_blockNumber"},
        {"jsonrpc": "2.0", "id": 2, "method": "arne_listAssets"},
        {"jsonrpc": "2.0", "method": "arne_blockNumber"},  # notification
    ]).json()
    assert [item["id"] for item in batch] == [1, 2]
    assert batch[0]["result"] == 1
    assert "arce:devnet" in batch[1]["result"]

    missing = client.post("/rpc", json={
        "jsonrpc": "2.0", "id": 3, "method": "arne_nope",
    }).json()
    assert missing["error"]["code"] == -32601
    bad = client.post("/rpc", content=b"not json").json()
    assert bad["error"]["code"] == -32700
    assert client.post("/rpc", json={"jsonrpc": "1.0", "id": 4, "method": "arne_status"}).json()["error"]["code"] == -32600
    assert client.post("/rpc", json={"jsonrpc": "2.0", "method": "arne_status"}).status_code == 204


def test_api_key_scopes(setup) -> None:
    node, keys = setup
    app = create_app(node, ApiConfig(
        devnet=False, api_keys={"read-key": ["read"], "write-key": ["write"]}
    ))
    client = TestClient(app)
    assert client.get("/v1/status").status_code == 200
    assert client.post("/v1/transactions", content=b"\xa0").status_code == 401
    response = client.post(
        "/v1/transactions", content=b"\xa0",
        headers={"Authorization": "Bearer read-key"},
    )
    assert response.status_code == 403
    assert client.post("/v1/faucet", json={"address": "x"}).status_code == 401


def test_faucet_disabled_outside_devnet(setup) -> None:
    node, _ = setup
    client = TestClient(create_app(node, ApiConfig(devnet=False)))
    assert client.post("/v1/faucet", json={"address": "x"}).status_code == 404
