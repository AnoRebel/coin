"""SDK parity: the documented Python flow against a real devnet."""

from __future__ import annotations

import time

import httpx
import pytest
from arne import ledger, types
from arne.api import ApiConfig, create_app
from arne.sdk import (
    Allowance,
    AllowanceTransfer,
    ApiError,
    Client,
    EscrowClose,
    EscrowCreate,
    IntentCreate,
    Payment,
    SubscriptionCharge,
    SubscriptionCreate,
    Wallet,
    is_code,
)
from fastapi.testclient import TestClient
from test_api import MNEMONIC, NETWORK, make_node


@pytest.fixture()
def sdk():
    node, _ = make_node()
    app = create_app(node, ApiConfig(devnet=True))
    internal = TestClient(app)

    def forward(request: httpx.Request) -> httpx.Response:
        response = internal.request(
            request.method, request.url.raw_path.decode(),
            headers=request.headers, content=request.content,
        )
        return httpx.Response(response.status_code, headers=response.headers,
                              content=response.content)

    client = Client("http://node", http=httpx.Client(transport=httpx.MockTransport(forward)))
    yield node, client
    client.close()


def test_quickstart(sdk) -> None:
    node, client = sdk
    height = int(time.time())

    def produce() -> None:
        nonlocal height
        node.produce_block(height)
        height += 1

    payer = Wallet.from_mnemonic(MNEMONIC, network=NETWORK, index=0)
    payee = Wallet.from_mnemonic(MNEMONIC, network=NETWORK, index=2)
    native = types.native_asset(NETWORK)

    faucet_hash = client.faucet(payer.address)
    produce()
    assert client.wait_receipt(faucet_hash)["success"]

    payment = Payment(to=payee.address, asset=native, amount=1_250_000_000)
    payment_hash = payer.send(client, types.TX_TRANSFER, payment.to_payload())
    produce()
    assert client.wait_receipt(payment_hash)["success"]
    assert client.account(payee.address)["balances"][native] == 1_250_000_000

    timeout = int(time.time()) + 3600
    escrow_hash = payer.send(client, types.TX_CREATE_ESCROW, EscrowCreate(
        beneficiary=payee.address, arbiter=payer.address, asset=native,
        amount=1_000, timeout=timeout,
    ).to_payload())
    produce()
    escrow_id = event_attr(client.wait_receipt(escrow_hash), "escrow_created", "id")
    escrow = client.escrow(escrow_id)
    assert escrow["status"] == "open" and escrow["amount"] == 1_000
    payer.send(client, types.TX_RELEASE_ESCROW, EscrowClose(id=escrow_id).to_payload())
    produce()
    assert client.escrow(escrow_id)["status"] == "released"

    intent_hash = payee.send(client, types.TX_CREATE_INTENT, IntentCreate(
        reference="invoice-1001", asset=native, amount=500,
    ).to_payload())
    produce()
    intent_id = event_attr(client.wait_receipt(intent_hash), "intent_created", "id")
    payer.send(client, types.TX_TRANSFER, Payment(
        to=payee.address, asset=native, amount=500, intent=intent_id,
    ).to_payload())
    produce()
    assert client.intent(intent_id)["status"] == "paid"

    sub_hash = payer.send(client, types.TX_CREATE_SUB, SubscriptionCreate(
        payee=payee.address, asset=native, amount_per_period=100, period=30, total_cap=1_000,
    ).to_payload())
    produce()
    sub_id = event_attr(client.wait_receipt(sub_hash), "subscription_created", "id")
    payee.send(client, types.TX_CHARGE_SUB, SubscriptionCharge(id=sub_id, amount=100).to_payload())
    produce()
    assert client.subscription(sub_id)["charged_total"] == 100

    payer.send(client, types.TX_APPROVE, Allowance(
        spender=payee.address, asset=native, amount=700,
    ).to_payload())
    produce()
    assert client.allowance(payer.address, payee.address, native)["remaining"] == 700
    payee.send(client, types.TX_TRANSFER_FROM, AllowanceTransfer(
        from_address=payer.address, to=payee.address, asset=native, amount=200,
    ).to_payload())
    produce()
    assert client.allowance(payer.address, payee.address, native)["remaining"] == 500

    with pytest.raises(ApiError) as caught:
        client.escrow("esc_missing")
    assert is_code(caught.value, "ESCROW_NOT_FOUND") and caught.value.status == 404

    assert len(client.blocks(limit=2)["items"]) == 2
    assert client.supply(native)["supply"] > 0


def event_attr(receipt: dict, event: str, attr: str) -> str:
    for entry in receipt["events"]:
        if entry["type"] == event and entry["attrs"].get(attr):
            return entry["attrs"][attr]
    raise AssertionError(f"receipt has no {event}.{attr}: {receipt}")


def test_batch_and_simulate(sdk) -> None:
    node, client = sdk
    payer = Wallet.from_mnemonic(MNEMONIC, network=NETWORK, index=0)
    payee = Wallet.from_mnemonic(MNEMONIC, network=NETWORK, index=2)
    client.faucet(payer.address)
    node.produce_block(int(time.time()))
    receipt = payer.simulate(client, types.TX_TRANSFER, Payment(
        to=payee.address, asset=types.native_asset(NETWORK), amount=7,
    ).to_payload())
    assert receipt["success"]


def test_sdk_assets_match_ledger_surface() -> None:
    """Every tx type the SDK can build exists in the protocol."""
    assert types.TX_TYPES >= {
        types.TX_TRANSFER, types.TX_BATCH, types.TX_REGISTER_ASSET,
        types.TX_CREATE_ESCROW, types.TX_RELEASE_ESCROW, types.TX_REFUND_ESCROW,
        types.TX_APPROVE, types.TX_TRANSFER_FROM, types.TX_REVOKE_ALLOWANCE,
        types.TX_MINT, types.TX_BURN, types.TX_CREATE_INTENT,
        types.TX_CREATE_SUB, types.TX_CHARGE_SUB, types.TX_CANCEL_SUB,
    }
    assert ledger.default_params().max_memo_bytes > 0
