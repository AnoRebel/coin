"""Typed client, wallet, and payloads for the ARNE REST API.

The wire format stays in :mod:`arne.types` and :mod:`arne.cbor`; this module only
adds the HTTP surface and signing defaults, mirroring the Go SDK.
"""

from __future__ import annotations

import json
import time
from dataclasses import dataclass, field
from typing import Any

import httpx

from . import cbor, types
from .keys import KeyPair, keypair_from_mnemonic, new_keypair

DEFAULT_FEE = 10_000
DEFAULT_TTL = 3600


class ApiError(Exception):
    """A node rejection with the stable error code and HTTP status."""

    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.status = status
        self.code = code
        self.message = message


def is_code(err: BaseException, code: str) -> bool:
    """Report whether err is an ApiError with the given stable code."""
    return isinstance(err, ApiError) and err.code == code


def _api_error(response: httpx.Response) -> ApiError:
    try:
        body = response.json().get("error", {})
    except ValueError:
        body = {}
    code = str(body.get("code") or "HTTP_ERROR")
    message = str(body.get("message") or response.text.strip())
    return ApiError(response.status_code, code, message)


class Client:
    """Talks to the REST /v1 surface."""

    def __init__(
        self, base_url: str, api_key: str | None = None, timeout: float = 30.0,
        http: httpx.Client | None = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self._http = http or httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> Client:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def _request(
        self, method: str, path: str, *, params: dict[str, Any] | None = None,
        content: bytes | None = None, content_type: str | None = None,
    ) -> Any:
        headers = {}
        if content_type:
            headers["Content-Type"] = content_type
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        response = self._http.request(
            method, self.base_url + path, params=params, content=content, headers=headers,
        )
        if response.status_code >= 300:
            raise _api_error(response)
        if not response.content:
            return None
        return response.json()

    def status(self) -> dict[str, Any]:
        return self._request("GET", "/v1/status")

    def account(self, address: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/accounts/{address}")

    def assets(self) -> list[dict[str, Any]]:
        return self._request("GET", "/v1/assets")["items"]

    def asset(self, asset_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/assets/{asset_id}")

    def supply(self, asset_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/supply/{asset_id}")

    def escrow(self, escrow_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/escrows/{escrow_id}")

    def intent(self, intent_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/intents/{intent_id}")

    def subscription(self, subscription_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/subscriptions/{subscription_id}")

    def transfer(self, tx_hash: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/transfers/{tx_hash}")

    def allowance(self, owner: str, spender: str, asset_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/allowances/{owner}/{spender}/{asset_id}")

    def proof(self, key: str) -> dict[str, Any]:
        return self._request("GET", "/v1/proofs/" + key)

    def block(self, height: int) -> dict[str, Any]:
        return self._request("GET", f"/v1/blocks/{height}")

    def blocks(self, limit: int = 0, cursor: int = 0) -> dict[str, Any]:
        params: dict[str, Any] = {}
        if limit:
            params["limit"] = limit
        if cursor:
            params["cursor"] = cursor
        return self._request("GET", "/v1/blocks", params=params or None)

    def submit_raw(self, raw: bytes) -> str:
        body = self._request("POST", "/v1/transactions", content=raw, content_type="application/cbor")
        return body["hash"]

    def submit(self, tx: types.Tx) -> str:
        return self.submit_raw(cbor.encode(tx.to_cbor()))

    def simulate(self, tx: types.Tx) -> dict[str, Any]:
        raw = cbor.encode(tx.to_cbor())
        return self._request(
            "POST", "/v1/transactions/simulate", content=raw, content_type="application/cbor",
        )

    def receipt(self, tx_hash: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/transactions/{tx_hash}")

    def wait_receipt(self, tx_hash: str, timeout: float = 30.0) -> dict[str, Any]:
        deadline = time.monotonic() + timeout
        while True:
            try:
                return self.receipt(tx_hash)
            except ApiError as err:
                if err.status != 404:
                    raise
            if time.monotonic() >= deadline:
                raise TimeoutError(f"no receipt for {tx_hash}")
            time.sleep(0.5)

    def faucet(self, address: str) -> str:
        return self._request(
            "POST", "/v1/faucet", content=json.dumps({"address": address}).encode(),
            content_type="application/json",
        )["hash"]

    def rpc(self, method: str, params: Any = None) -> Any:
        body = self._request(
            "POST", "/rpc",
            content=json.dumps(
                {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
            ).encode(),
            content_type="application/json",
        )
        if body.get("error"):
            raise ApiError(400, str(body["error"].get("data") or "RPC_ERROR"),
                           str(body["error"].get("message") or "rpc error"))
        return body.get("result")


@dataclass
class Wallet:
    """A signing identity with fee and lifetime defaults."""

    keypair: KeyPair
    fee_asset: str = ""
    max_fee: int = 0
    priority_fee: int = 0
    ttl: int = 0

    @classmethod
    def new(cls, network: str) -> tuple[Wallet, str]:
        keypair, mnemonic = new_keypair(network)
        return cls(keypair), mnemonic

    @classmethod
    def from_mnemonic(
        cls, mnemonic: str, passphrase: str = "", network: str = "arne:devnet", index: int = 0,
    ) -> Wallet:
        return cls(keypair_from_mnemonic(mnemonic, passphrase, 0, 0, index, network))

    @property
    def address(self) -> str:
        return self.keypair.address

    @property
    def network(self) -> str:
        return self.keypair.network

    def build(
        self, client: Client, tx_type: str, payload: dict[str, Any],
        nonce: int | None = None, now: int | None = None,
    ) -> types.Tx:
        if nonce is None:
            nonce = int(client.account(self.address)["nonce"])
        if now is None:
            now = int(time.time())
        ttl = self.ttl or DEFAULT_TTL
        tx = types.Tx(
            version=types.CURRENT_TX_VERSION, network=self.network,
            chain=types.chain_id(self.network), tx_type=tx_type, signer=self.address,
            nonce=nonce, created_at=now, expires_at=now + ttl,
            fee_asset=self.fee_asset or types.native_asset(self.network),
            max_fee=self.max_fee or DEFAULT_FEE, priority_fee=self.priority_fee,
            payload=dict(payload),
        )
        tx.sign(self.keypair)
        return tx

    def send(self, client: Client, tx_type: str, payload: dict[str, Any]) -> str:
        return client.submit(self.build(client, tx_type, payload))

    def send_and_wait(
        self, client: Client, tx_type: str, payload: dict[str, Any], timeout: float = 30.0,
    ) -> dict[str, Any]:
        return client.wait_receipt(self.send(client, tx_type, payload), timeout)

    def simulate(self, client: Client, tx_type: str, payload: dict[str, Any]) -> dict[str, Any]:
        return client.simulate(self.build(client, tx_type, payload))


# Payloads mirror the Go SDK and the ledger's accepted fields.


@dataclass
class Payment:
    to: str
    asset: str
    amount: int
    memo: str = ""
    memo_enc: bytes = b""
    intent: str = ""
    refund_of: str = ""
    max_arce_fee: int = 0

    def to_payload(self) -> dict[str, Any]:
        payload: dict[str, Any] = {"to": self.to, "asset": self.asset, "amount": self.amount}
        if self.memo:
            payload["memo"] = self.memo
        if self.memo_enc:
            payload["memo_enc"] = self.memo_enc
        if self.intent:
            payload["intent"] = self.intent
        if self.refund_of:
            payload["refund_of"] = self.refund_of
        if self.max_arce_fee:
            payload["max_arce_fee"] = self.max_arce_fee
        return payload


@dataclass
class EscrowCreate:
    beneficiary: str
    arbiter: str
    asset: str
    amount: int
    timeout: int

    def to_payload(self) -> dict[str, Any]:
        return {
            "beneficiary": self.beneficiary, "arbiter": self.arbiter, "asset": self.asset,
            "amount": self.amount, "timeout": self.timeout,
        }


@dataclass
class EscrowClose:
    id: str

    def to_payload(self) -> dict[str, Any]:
        return {"id": self.id}


@dataclass
class Allowance:
    spender: str
    asset: str
    amount: int
    expires_at: int = 0

    def to_payload(self) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "spender": self.spender, "asset": self.asset, "amount": self.amount,
        }
        if self.expires_at:
            payload["expires_at"] = self.expires_at
        return payload


@dataclass
class AllowanceRevoke:
    spender: str
    asset: str

    def to_payload(self) -> dict[str, Any]:
        return {"spender": self.spender, "asset": self.asset}


@dataclass
class AllowanceTransfer:
    from_address: str
    to: str
    asset: str
    amount: int

    def to_payload(self) -> dict[str, Any]:
        return {
            "from": self.from_address, "to": self.to, "asset": self.asset, "amount": self.amount,
        }


@dataclass
class AssetRegister:
    asset: types.Asset

    def to_payload(self) -> dict[str, Any]:
        return {"asset": self.asset.to_cbor()}


@dataclass
class Mint:
    asset: str
    to: str
    amount: int

    def to_payload(self) -> dict[str, Any]:
        return {"asset": self.asset, "to": self.to, "amount": self.amount}


@dataclass
class Burn:
    asset: str
    amount: int

    def to_payload(self) -> dict[str, Any]:
        return {"asset": self.asset, "amount": self.amount}


@dataclass
class IntentCreate:
    reference: str
    asset: str
    amount: int
    expires_at: int = 0
    memo: str = ""

    def to_payload(self) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "reference": self.reference, "asset": self.asset, "amount": self.amount,
        }
        if self.expires_at:
            payload["expires_at"] = self.expires_at
        if self.memo:
            payload["memo"] = self.memo
        return payload


@dataclass
class SubscriptionCreate:
    payee: str
    asset: str
    amount_per_period: int
    period: int
    total_cap: int
    expires_at: int = 0

    def to_payload(self) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "payee": self.payee, "asset": self.asset,
            "amount_per_period": self.amount_per_period, "period": self.period,
            "total_cap": self.total_cap,
        }
        if self.expires_at:
            payload["expires_at"] = self.expires_at
        return payload


@dataclass
class SubscriptionCharge:
    id: str
    amount: int

    def to_payload(self) -> dict[str, Any]:
        return {"id": self.id, "amount": self.amount}


@dataclass
class SubscriptionCancel:
    id: str

    def to_payload(self) -> dict[str, Any]:
        return {"id": self.id}


@dataclass
class BatchItem:
    to: str
    asset: str
    amount: int

    def to_payload(self) -> dict[str, Any]:
        return {"to": self.to, "asset": self.asset, "amount": self.amount}


@dataclass
class Batch:
    items: list[BatchItem] = field(default_factory=list)
    best_effort: bool = False

    def to_payload(self) -> dict[str, Any]:
        return {
            "items": [item.to_payload() for item in self.items],
            "mode": "best_effort" if self.best_effort else "atomic",
        }


__all__ = [
    "Allowance", "AllowanceRevoke", "AllowanceTransfer", "ApiError", "AssetRegister",
    "Batch", "BatchItem", "Burn", "Client", "EscrowClose", "EscrowCreate", "IntentCreate",
    "Mint", "Payment", "SubscriptionCancel", "SubscriptionCharge", "SubscriptionCreate",
    "Wallet", "is_code",
]
