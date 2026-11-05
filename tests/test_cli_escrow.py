"""Escrow CLI: create, show, release, and refund."""

from __future__ import annotations

import time

import httpx
from arne import cbor, cli, types
from arne.keys import keypair_from_mnemonic

MNEMONIC = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
NETWORK = "arne:devnet"
ESCROW_ID = "esc_" + "0123456789abcdef0123456789abcdef"


def escrow_record(payer: str) -> dict:
    return {
        "id": ESCROW_ID, "payer": payer, "beneficiary": payer, "arbiter": payer,
        "asset": "arce:devnet", "amount": 1_000, "timeout": int(time.time()) + 3600,
        "status": "open", "created_at": 1,
    }


def test_escrow_cli_flow(monkeypatch) -> None:
    key = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 0, NETWORK)
    submitted: list[dict] = []

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path == "/v1/transactions":
            submitted.append(cbor.decode(request.content))
            return httpx.Response(202, json={"hash": "accepted"})
        if path == "/v1/status":
            return httpx.Response(200, json={"network": NETWORK})
        if path == f"/v1/accounts/{key.address}":
            return httpx.Response(200, json={"address": key.address, "nonce": 0})
        if path == f"/v1/escrows/{ESCROW_ID}":
            return httpx.Response(200, json=escrow_record(key.address))
        if path.startswith("/v1/assets/"):
            return httpx.Response(200, json={"id": "arce:devnet", "decimals": 8})
        return httpx.Response(404, json={"error": {"code": "NOT_FOUND", "message": path}})

    monkeypatch.setattr(
        cli, "_client",
        lambda url: httpx.Client(base_url=url, transport=httpx.MockTransport(handler)),
    )
    base = ["--url", "http://node.test", "--mnemonic", MNEMONIC]
    timeout = str(int(time.time()) + 3600)
    assert cli.main([
        "escrow", "create", *base, "--beneficiary", key.address, "--asset", "arce:devnet",
        "--amount", "0.00001", "--timeout", timeout,
    ]) == 0
    assert cli.main(["escrow", "show", "--url", "http://node.test", "--id", ESCROW_ID]) == 0
    assert cli.main(["escrow", "release", *base, "--id", ESCROW_ID]) == 0
    assert cli.main(["escrow", "refund", *base, "--id", ESCROW_ID]) == 0

    assert [tx["type"] for tx in submitted] == [
        types.TX_CREATE_ESCROW, types.TX_RELEASE_ESCROW, types.TX_REFUND_ESCROW,
    ]
    create = submitted[0]["payload"]
    assert create == {
        "beneficiary": key.address, "arbiter": "", "asset": "arce:devnet",
        "amount": 1_000, "timeout": int(timeout),
    }
    assert [tx["payload"] for tx in submitted[1:]] == [{"id": ESCROW_ID}] * 2


def test_escrow_create_rejects_past_timeout(monkeypatch) -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"decimals": 8})

    monkeypatch.setattr(
        cli, "_client",
        lambda url: httpx.Client(base_url=url, transport=httpx.MockTransport(handler)),
    )
    assert cli.main([
        "escrow", "create", "--url", "http://node.test", "--mnemonic", MNEMONIC,
        "--beneficiary", "arce-devnet1q8t23v9987ns9qmtrxqxs4cqqsqjll0pz5n0e0dx",
        "--asset", "arce:devnet", "--amount", "1", "--timeout", "1",
    ]) == 2
