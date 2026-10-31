"""Subscription CLI: create, charge with a default amount, show, cancel."""

from __future__ import annotations

import time

import httpx
from arne import cbor, cli, types
from arne.keys import keypair_from_mnemonic

MNEMONIC = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
NETWORK = "arne:devnet"
PAYEE = "arce-devnet1q8t23v9987ns9qmtrxqxs4cqqsqjll0pz5n0e0dx"


def test_subscription_cli_flow(monkeypatch) -> None:
    key = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 0, NETWORK)
    sub_id = types.subscription_id(NETWORK, key.address, PAYEE, "arce:devnet", 4)
    subscription = {
        "id": sub_id, "payer": key.address, "payee": PAYEE, "asset": "arce:devnet",
        "amount_per_period": 500_000_000, "period": 3600, "total_cap": 5_000_000_000,
        "charged_period": 100_000_000, "charged_total": 200_000_000,
        "period_start": int(time.time()) - 10, "expires_at": 0,
        "status": "active", "created_at": 1,
    }
    submitted: list[dict] = []

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/v1/transactions":
            submitted.append(cbor.decode(request.content))
            return httpx.Response(202, json={"hash": "accepted"})
        if request.url.path == "/v1/status":
            return httpx.Response(200, json={"network": NETWORK})
        if request.url.path == f"/v1/accounts/{key.address}":
            return httpx.Response(200, json={"address": key.address, "nonce": 4})
        if request.url.path == f"/v1/subscriptions/{sub_id}":
            return httpx.Response(200, json=subscription)
        return httpx.Response(200, json={"id": "arce:devnet", "symbol": "ARCE", "decimals": 8})

    monkeypatch.setattr(
        cli, "_client", lambda url: httpx.Client(base_url=url, transport=httpx.MockTransport(handler))
    )
    base = ["--url", "http://node.test", "--mnemonic", MNEMONIC]
    assert cli.main(["subscription", "create", *base, "--payee", PAYEE, "--asset", "arce:devnet",
                     "--amount-per-period", "10", "--period", "3600", "--total-cap", "50"]) == 0
    assert cli.main(["subscription", "charge", *base, "--id", sub_id]) == 0
    assert cli.main(["subscription", "show", "--url", "http://node.test", "--id", sub_id]) == 0
    assert cli.main(["subscription", "cancel", *base, "--id", sub_id]) == 0

    assert [tx["type"] for tx in submitted] == [
        types.TX_CREATE_SUB, types.TX_CHARGE_SUB, types.TX_CANCEL_SUB,
    ]
    payloads = [tx["payload"] for tx in submitted]
    assert payloads[0]["payee"] == PAYEE
    assert payloads[0]["amount_per_period"] == 1_000_000_000
    assert payloads[0]["total_cap"] == 5_000_000_000
    assert payloads[0]["period"] == 3600
    # The default charge fills what the current period still allows.
    assert (payloads[1]["id"], payloads[1]["amount"]) == (sub_id, 400_000_000)
    assert payloads[2]["id"] == sub_id


def test_subscription_charge_refuses_empty_period(monkeypatch) -> None:
    key = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 0, NETWORK)
    sub_id = types.subscription_id(NETWORK, key.address, PAYEE, "arce:devnet", 0)
    subscription = {
        "id": sub_id, "payer": key.address, "payee": PAYEE, "asset": "arce:devnet",
        "amount_per_period": 500_000_000, "period": 3600, "total_cap": 500_000_000,
        "charged_period": 500_000_000, "charged_total": 500_000_000,
        "period_start": int(time.time()), "expires_at": 0,
        "status": "active", "created_at": 1,
    }

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/v1/status":
            return httpx.Response(200, json={"network": NETWORK})
        if request.url.path.startswith("/v1/subscriptions/"):
            return httpx.Response(200, json=subscription)
        return httpx.Response(200, json={"decimals": 8})

    monkeypatch.setattr(
        cli, "_client", lambda url: httpx.Client(base_url=url, transport=httpx.MockTransport(handler))
    )
    assert cli.main(["subscription", "charge", "--url", "http://node.test",
                      "--mnemonic", MNEMONIC, "--id", sub_id]) == 2
