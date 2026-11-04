"""HTTP parity tests: REST /v1, JSON-RPC, auth, and devnet flow."""

from __future__ import annotations

import base64
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


def test_intent_query(client: TestClient, setup) -> None:
    node, keys = setup
    key = keys["funder"]
    missing = client.get("/v1/intents/" + "0" * 32)
    assert missing.status_code == 404
    assert missing.json()["error"]["code"] == "INTENT_NOT_FOUND"
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type="create_intent",
        signer=key.address, nonce=0, created_at=now, expires_at=now + 3600,
        fee_asset="arce:devnet", max_fee=10_000,
        payload={"asset": "arce:devnet", "amount": 42, "reference": "invoice_42"},
    )
    tx.sign(key)
    submitted = client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()))
    assert submitted.status_code == 202, submitted.text
    node.produce_block(now + 1)
    intent_id = types.intent_id(NETWORK, key.address, "invoice_42")
    response = client.get("/v1/intents/" + intent_id)
    assert response.status_code == 200
    assert response.json() == {
        "id": intent_id, "payee": key.address, "reference": "invoice_42", "asset": "arce:devnet",
        "amount": 42, "expires_at": 0, "status": "open", "created_at": now + 1,
    }


def test_subscription_query(client: TestClient, setup) -> None:
    node, keys = setup
    payer, payee = keys["funder"], keys["recipient"]
    sub_id = types.subscription_id(NETWORK, payer.address, payee.address, "arce:devnet", 0)
    missing = client.get("/v1/subscriptions/" + "0" * 32)
    assert missing.status_code == 404
    assert missing.json()["error"]["code"] == "SUBSCRIPTION_NOT_FOUND"
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type=types.TX_CREATE_SUB,
        signer=payer.address, nonce=0, created_at=now, expires_at=now + 3600,
        fee_asset="arce:devnet", max_fee=10_000,
        payload={"payee": payee.address, "asset": "arce:devnet", "amount_per_period": 500,
                 "period": 3600, "total_cap": 2000},
    )
    tx.sign(payer)
    submitted = client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()))
    assert submitted.status_code == 202, submitted.text
    node.produce_block(now + 1)
    body = client.get(f"/v1/subscriptions/{sub_id}").json()
    assert body["id"] == sub_id and body["payer"] == payer.address and body["payee"] == payee.address
    assert (body["amount_per_period"], body["period"], body["total_cap"]) == (500, 3600, 2000)
    assert body["status"] == "active" and body["charged_total"] == 0


def test_transfer_query(client: TestClient, setup) -> None:
    node, keys = setup
    key = keys["funder"]
    missing = client.get("/v1/transfers/" + "0" * 64)
    assert missing.status_code == 404
    assert missing.json()["error"]["code"] == "TRANSFER_NOT_FOUND"
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type=types.TX_TRANSFER,
        signer=key.address, nonce=0, created_at=now, expires_at=now + 3600,
        fee_asset="arce:devnet", max_fee=10_000,
        payload={"asset": "arce:devnet", "to": key.address, "amount": 42},
    )
    tx.sign(key)
    submitted = client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()))
    assert submitted.status_code == 202, submitted.text
    node.produce_block(now + 1)
    assert client.get(f"/v1/transfers/{tx.hash()}").json() == {
        "hash": tx.hash(), "from": key.address, "to": key.address, "asset": "arce:devnet",
        "amount": 42, "refund_of": "", "refunded": 0, "refundable": 42,
    }


def test_submit_idempotency(client: TestClient, setup) -> None:
    node, keys = setup
    key = keys["funder"]
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type="transfer",
        signer=key.address, nonce=0, created_at=now, expires_at=now + 3600,
        fee_asset="arce:devnet", max_fee=1000,
        payload={"asset": "arce:devnet", "to": key.address, "amount": 42},
    )
    tx.sign(key)
    raw = cbor.encode(tx.to_cbor())
    headers = {"Idempotency-Key": "invoice-42"}
    first = client.post("/v1/transactions", content=raw, headers=headers)
    assert first.status_code == 202
    retry = client.post("/v1/transactions", json={"tx": base64.b64encode(raw).decode()}, headers=headers)
    assert retry.status_code == 202 and retry.json() == first.json()
    node.produce_block(now + 1)
    finalized = client.post("/v1/transactions", content=raw, headers=headers)
    assert finalized.status_code == 200, finalized.text
    assert finalized.json()["hash"] == tx.hash()
    assert finalized.json()["receipt"] == node.receipt(tx.hash()).to_dict()
    tx.nonce = 1
    tx.signatures.clear()
    tx.sign(key)
    conflict = client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()), headers=headers)
    assert conflict.status_code == 409
    assert conflict.json()["error"]["code"] == "IDEMPOTENCY_CONFLICT"


@pytest.mark.parametrize("key", ["", "has space", "x" * 129, "bad,key"])
def test_invalid_idempotency_key(client: TestClient, key: str) -> None:
    result = client.post("/v1/transactions", content=b"\xa0", headers={"Idempotency-Key": key})
    assert result.status_code == 400
    assert result.json()["error"]["code"] == "INVALID_ARGUMENT"


def test_duplicate_idempotency_headers(client: TestClient) -> None:
    result = client.post("/v1/transactions", content=b"\xa0",
                         headers=[("Idempotency-Key", "one"), ("Idempotency-Key", "two")])
    assert result.status_code == 400
    assert result.json()["error"]["code"] == "INVALID_ARGUMENT"


def test_keyed_invalid_transaction_does_not_reserve_key(client: TestClient, setup) -> None:
    headers = {"Idempotency-Key": "recoverable"}
    result = client.post("/v1/transactions", content=b"\xa0", headers=headers)
    assert result.status_code == 400
    assert result.json()["error"]["code"] == "INVALID_SCHEMA"
    _, keys = setup
    key = keys["funder"]
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type="transfer",
        signer=key.address, nonce=0, created_at=now, expires_at=now + 3600,
        fee_asset="arce:devnet", max_fee=1000,
        payload={"asset": "arce:devnet", "to": key.address, "amount": 42},
    )
    tx.sign(key)
    assert client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()), headers=headers).status_code == 202


def test_idempotency_auth_scope(setup) -> None:
    node, keys = setup
    key = keys["funder"]
    client = TestClient(create_app(node, ApiConfig(api_keys={"one": ["write"], "two": ["write"]})))
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type="transfer",
        signer=key.address, nonce=0, created_at=now, expires_at=now + 3600,
        fee_asset="arce:devnet", max_fee=1000,
        payload={"asset": "arce:devnet", "to": key.address, "amount": 42},
    )
    tx.sign(key)
    raw = cbor.encode(tx.to_cbor())
    headers = {"Idempotency-Key": "shared", "Authorization": "Bearer one"}
    assert client.post("/v1/transactions", content=raw, headers=headers).status_code == 202
    node.produce_block(now + 1)
    assert client.post("/v1/transactions", content=raw, headers={"Idempotency-Key": "shared"}).status_code == 401
    headers["Authorization"] = "Bearer two"
    # Another principal must not retrieve the first principal's cached receipt.
    assert client.post("/v1/transactions", content=raw, headers=headers).status_code == 400
    headers["Authorization"] = "Bearer one"
    assert client.post("/v1/transactions", content=raw, headers=headers).status_code == 200


def test_mempool_conflict_does_not_reserve_key(client, setup) -> None:
    node, keys = setup
    key = keys["funder"]
    now = int(time.time())
    tx = types.Tx(version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type="transfer",
                  signer=key.address, nonce=0, created_at=now, expires_at=now+3600,
                  fee_asset="arce:devnet", max_fee=1000,
                  payload={"asset": "arce:devnet", "to": key.address, "amount": 42})
    tx.sign(key)
    assert client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()), headers={"Idempotency-Key": "first"}).status_code == 202
    tx.payload["amount"] = 43
    tx.signatures.clear()
    tx.sign(key)
    rejected = client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()), headers={"Idempotency-Key": "second"})
    assert rejected.status_code == 409, rejected.text
    assert rejected.json()["error"]["code"] == "MEMPOOL_CONFLICT"
    node.produce_block(now+1)
    tx.nonce = 1
    tx.signatures.clear()
    tx.sign(key)
    assert client.post("/v1/transactions", content=cbor.encode(tx.to_cbor()), headers={"Idempotency-Key": "second"}).status_code == 202


def test_idempotency_expiry_and_capacity(setup, monkeypatch) -> None:
    from arne import api

    node, keys = setup
    key = keys["funder"]
    client = TestClient(create_app(node))
    monkeypatch.setattr(api, "IDEMPOTENCY_LIMIT", 1)
    monkeypatch.setattr(api, "IDEMPOTENCY_TTL", -1)
    now = int(time.time())

    def raw(nonce: int) -> bytes:
        tx = types.Tx(
            version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type="transfer",
            signer=key.address, nonce=nonce, created_at=now, expires_at=now + 3600,
            fee_asset="arce:devnet", max_fee=1000,
            payload={"asset": "arce:devnet", "to": key.address, "amount": 42},
        )
        tx.sign(key)
        return cbor.encode(tx.to_cbor())

    headers = {"Idempotency-Key": "expiring"}
    assert client.post("/v1/transactions", content=raw(0), headers=headers).status_code == 202
    node.produce_block(now + 1)
    monkeypatch.setattr(api, "IDEMPOTENCY_TTL", 24 * 3600)
    assert client.post("/v1/transactions", content=raw(1), headers=headers).status_code == 202
    result = client.post("/v1/transactions", content=raw(2), headers={"Idempotency-Key": "new-key"})
    assert result.status_code == 503
    assert result.json()["error"]["code"] == "IDEMPOTENCY_FULL"
    # Existing retries must still work when full, without eviction.
    assert client.post("/v1/transactions", content=raw(1), headers=headers).status_code == 202


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


def test_event_stream_replay_and_validation(client: TestClient, setup) -> None:
    node, keys = setup
    with client.stream("GET", "/v1/events?from=1") as response:
        lines: list[str] = []
        for line in response.iter_lines():
            lines.append(line)
            if line == "" and "event: block" in lines:
                break
    body = "\n".join(lines)
    assert "id: 1" in body and "event: block" in body

    key = keys["funder"]
    now = int(time.time())
    tx = types.Tx(
        version=1, network=NETWORK, chain=types.chain_id(NETWORK), tx_type=types.TX_TRANSFER,
        signer=key.address, nonce=0, created_at=now, expires_at=now + 3600,
        fee_asset="arce:devnet", max_fee=10_000,
        payload={"asset": "arce:devnet", "to": key.address, "amount": 5},
    )
    tx.sign(key)
    assert client.post("/v1/transactions", content=cbor.encode(tx.to_cbor())).status_code == 202
    node.produce_block(now + 1)

    with client.stream("GET", "/v1/events?stream=transactions&from=1") as response:
        lines = []
        for line in response.iter_lines():
            lines.append(line)
            if line == "" and "event: transaction" in lines:
                break
    body = "\n".join(lines)
    assert "id: 2-0" in body or "id: 1-0" in body
    assert tx.hash() in body

    assert client.get("/v1/events?stream=messages").status_code == 400
    assert client.get("/v1/events?from=abc").status_code == 400
