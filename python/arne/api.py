"""FastAPI surface: REST /v1, JSON-RPC 2.0, and SSE events.

Response shapes mirror internal/api so client integrations work against either
node implementation.
"""

from __future__ import annotations

import base64
import binascii
import hashlib
import json
import re
import threading
import time
from collections.abc import Iterator
from dataclasses import dataclass, field
from typing import Any

from fastapi import FastAPI, Query, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from . import cbor, ledger, types
from .node import Node

MAX_BODY = 2 << 20
API_VERSION = "v1"
IDEMPOTENCY_TTL = 24 * 3600
IDEMPOTENCY_LIMIT = 10_000
_IDEMPOTENCY_KEY = re.compile(r"[A-Za-z0-9_.:-]{1,128}")


class ApiError(Exception):
    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message


@dataclass
class ApiConfig:
    api_keys: dict[str, list[str]] = field(default_factory=dict)
    allowed_origins: list[str] = field(default_factory=list)
    rate_limit: int = 0  # requests per second per IP; 0 disables
    devnet: bool = False


def error_response(status: int, code: str, message: str) -> JSONResponse:
    return JSONResponse(
        {"error": {"code": code, "message": message}}, status_code=status
    )


def _status_for(code: str) -> int:
    if code == ledger.UNAUTHORIZED:
        return 403
    if code in (ledger.ESCROW_NOT_FOUND, ledger.UNKNOWN_ASSET, ledger.REFUND_NOT_FOUND):
        return 404
    if code == ledger.LIMIT_EXCEEDED:
        return 429
    if code == ledger.MEMPOOL_CONFLICT:
        return 409
    return 400


def proof_view(proof) -> dict[str, Any]:
    return {
        "key": proof.key,
        "value": proof.value.hex() if proof.value is not None else "",
        "included": proof.value is not None,
        "depth": proof.depth,
        "terminal": proof.terminal.hex(),
        "siblings": [sibling.hex() for sibling in proof.siblings],
    }


def asset_view(node: Node, asset: types.Asset) -> dict[str, Any]:
    state = node.state
    return {
        "id": asset.id, "class": asset.asset_class, "symbol": asset.symbol,
        "name": asset.name, "decimals": asset.decimals, "policy": asset.policy,
        "max_supply": asset.max_supply, "issuer": asset.issuer,
        "metadata": asset.metadata, "registered": asset.registered,
        "supply": state.supply(asset.id), "burned": state.burned(asset.id),
    }


def _tx_bytes(body: bytes) -> bytes:
    body = body.strip()
    if not body:
        raise ApiError(400, "INVALID_ARGUMENT", "empty body")
    if body[:1] == b"{":
        try:
            wrapper = json.loads(body)
        except json.JSONDecodeError as exc:
            raise ApiError(400, "INVALID_ARGUMENT", "invalid JSON body") from exc
        raw = wrapper.get("tx") if isinstance(wrapper, dict) else None
        if not isinstance(raw, str) or not raw:
            raise ApiError(400, "INVALID_ARGUMENT", "tx field required")
        try:
            return base64.b64decode(raw, validate=True)
        except (binascii.Error, ValueError):
            try:
                return bytes.fromhex(raw)
            except ValueError as exc:
                raise ApiError(400, "INVALID_ARGUMENT", "tx must be base64 or hex") from exc
    return body


async def _read_body(request: Request) -> bytes:
    body = await request.body()
    if len(body) > MAX_BODY:
        raise ApiError(413, "BODY_TOO_LARGE", "request body above 2 MiB")
    return body


class RateLimiter:
    def __init__(self, rate: int) -> None:
        self.rate = float(rate)
        self.burst = float(rate * 2)
        self._lock = threading.Lock()
        self._buckets: dict[str, tuple[float, float]] = {}

    def allow(self, key: str) -> bool:
        now = time.monotonic()
        with self._lock:
            tokens, last = self._buckets.get(key, (self.burst, now))
            tokens = min(self.burst, tokens + (now - last) * self.rate)
            if tokens < 1:
                self._buckets[key] = (tokens, now)
                return False
            self._buckets[key] = (tokens - 1, now)
            return True


def create_app(node: Node, config: ApiConfig | None = None) -> FastAPI:
    config = config or ApiConfig()
    limiter = RateLimiter(config.rate_limit) if config.rate_limit > 0 else None
    # shortcut: process-local cache; persist before multi-instance API deployment.
    submissions: dict[bytes, tuple[bytes, str, float]] = {}
    submissions_lock = threading.Lock()

    def submit_with_key(raw: bytes, key: str, principal: str) -> tuple[str, bool]:
        if not key:
            return node.submit_tx(raw), False
        if _IDEMPOTENCY_KEY.fullmatch(key) is None:
            raise ApiError(400, "INVALID_ARGUMENT", "Idempotency-Key must be 1-128 letters, digits, _, ., :, or -")
        try:
            tx = types.Tx.from_cbor(cbor.decode_map(raw))
        except (cbor.CBORError, types.ValidationError) as exc:
            raise ApiError(400, "INVALID_SCHEMA", f"decode: {exc}") from exc
        scope = hashlib.sha256("\0".join((principal, tx.network, tx.signer, key)).encode()).digest()
        fingerprint = hashlib.sha256(raw).digest()
        with submissions_lock:
            now = time.monotonic()
            if previous := submissions.get(scope):
                if now < previous[2]:
                    if previous[0] != fingerprint:
                        raise ApiError(409, "IDEMPOTENCY_CONFLICT", "key already used for another signed transaction")
                    return previous[1], True
                del submissions[scope]
            if len(submissions) >= IDEMPOTENCY_LIMIT:
                for expired in [scope for scope, item in submissions.items() if now >= item[2]]:
                    del submissions[expired]
                if len(submissions) >= IDEMPOTENCY_LIMIT:
                    raise ApiError(503, "IDEMPOTENCY_FULL", "idempotency cache full; retry later")
            tx_hash = node.submit_tx(raw)
            submissions[scope] = (fingerprint, tx_hash, time.monotonic() + IDEMPOTENCY_TTL)
            return tx_hash, False
    app = FastAPI(
        title="AnorebelNetwork API", version="1.0.0", docs_url="/docs",
        openapi_url="/openapi.json",
    )

    @app.middleware("http")
    async def guard(request: Request, call_next):
        origin = request.headers.get("origin", "")
        if origin and origin in config.allowed_origins:
            if request.method == "OPTIONS":
                response = Response(status_code=204)
            else:
                response = await call_next(request)
            response.headers["Access-Control-Allow-Origin"] = origin
            response.headers["Vary"] = "Origin"
            response.headers["Access-Control-Allow-Methods"] = "GET, POST, OPTIONS"
            response.headers["Access-Control-Allow-Headers"] = (
                "Authorization, Content-Type, Idempotency-Key"
            )
        elif request.method == "OPTIONS":
            response = Response(status_code=204)
        else:
            response = await call_next(request)
        response.headers["X-Content-Type-Options"] = "nosniff"
        response.headers["X-Frame-Options"] = "DENY"
        response.headers["Referrer-Policy"] = "no-referrer"
        return response

    def require(scope: str):
        def check(request: Request) -> None:
            if not config.api_keys:
                return
            key = request.headers.get("authorization", "").removeprefix("Bearer ")
            scopes = config.api_keys.get(key)
            if scopes is None:
                raise ApiError(401, "UNAUTHORIZED", "missing or invalid API key")
            if scope not in scopes and "admin" not in scopes:
                raise ApiError(403, "FORBIDDEN", f"missing scope {scope}")

        return check

    def rate_check(request: Request) -> None:
        if limiter is not None:
            client = request.client.host if request.client else "unknown"
            if not limiter.allow(client):
                raise ApiError(429, "RATE_LIMITED", "too many requests")

    @app.exception_handler(ApiError)
    async def api_error_handler(_: Request, exc: ApiError) -> JSONResponse:
        return error_response(exc.status, exc.code, exc.message)

    @app.exception_handler(ledger.LedgerError)
    async def ledger_error_handler(_: Request, exc: ledger.LedgerError) -> JSONResponse:
        return error_response(_status_for(exc.code), exc.code, exc.message)

    # --- reads ---

    @app.get("/v1/health")
    def health() -> Response:
        status = node.status()
        payload = {"status": "halted" if status["halted"] else "ok", "height": status["height"]}
        return JSONResponse(payload, status_code=503 if status["halted"] else 200)

    @app.get("/v1/version")
    def version() -> dict[str, Any]:
        return {
            "protocol": "arne/1", "tx_version": 1, "api": API_VERSION,
            "cbor": "deterministic", "state": "smt-sha256",
        }

    @app.get("/v1/status")
    def status() -> dict[str, Any]:
        return node.status()

    @app.get("/v1/assets")
    def list_assets() -> dict[str, Any]:
        return {"items": [
            asset_view(node, node.state.asset(asset_id))
            for asset_id in node.state.assets()
        ]}

    @app.get("/v1/assets/{asset_id}")
    def get_asset(asset_id: str) -> dict[str, Any]:
        asset = node.state.asset(asset_id)
        if asset is None:
            raise ApiError(404, ledger.UNKNOWN_ASSET, "asset not found")
        return asset_view(node, asset)

    @app.get("/v1/accounts/{address}")
    def get_account(address: str) -> dict[str, Any]:
        info, exists = node.state.account(address)
        if not exists:
            return {"address": address, "exists": False, "balances": {}, "nonce": 0}
        return {
            "address": info.address, "exists": True, "nonce": info.nonce,
            "auth": info.auth, "balances": info.balances,
        }

    @app.get("/v1/accounts/{address}/proof")
    def account_proof(address: str, asset: str = "") -> dict[str, Any]:
        key = f"account/{address}/nonce" if not asset else f"balance/{address}/{asset}"
        return proof_view(node.proof(key))

    @app.get("/v1/supply/{asset_id}")
    def supply(asset_id: str) -> dict[str, Any]:
        if node.state.asset(asset_id) is None:
            raise ApiError(404, ledger.UNKNOWN_ASSET, "asset not found")
        return {
            "asset": asset_id, "supply": node.state.supply(asset_id),
            "burned": node.state.burned(asset_id),
        }

    @app.get("/v1/intents/{intent_id}")
    def get_intent(intent_id: str) -> dict[str, Any]:
        with node._lock:
            intent = node.state.intent(intent_id)
        if intent is None:
            raise ApiError(404, "INTENT_NOT_FOUND", "intent not found")
        return intent.to_cbor()

    @app.get("/v1/transfers/{tx_hash}")
    def get_transfer(tx_hash: str) -> dict[str, Any]:
        with node._lock:
            transfer = node.state.transfer(tx_hash)
            refunded = node.state.refunded(tx_hash) if transfer is not None else 0
        if transfer is None:
            raise ApiError(404, "TRANSFER_NOT_FOUND", "transfer not found")
        return {
            "hash": tx_hash, "from": transfer.sender, "to": transfer.to,
            "asset": transfer.asset, "amount": transfer.amount,
            "refund_of": transfer.refund_of, "refunded": refunded,
            "refundable": transfer.amount - refunded,
        }

    @app.get("/v1/escrows/{escrow_id}")
    def get_escrow(escrow_id: str) -> dict[str, Any]:
        escrow = node.state.escrow(escrow_id)
        if escrow is None:
            raise ApiError(404, ledger.ESCROW_NOT_FOUND, "escrow not found")
        return escrow.to_cbor()

    @app.get("/v1/allowances/{owner}/{spender}/{asset_id}")
    def get_allowance(owner: str, spender: str, asset_id: str) -> dict[str, Any]:
        return {
            "owner": owner, "spender": spender, "asset": asset_id,
            "remaining": node.state.allowance(owner, spender, asset_id),
            "expires_at": node.state.allowance_expiry(owner, spender, asset_id),
        }

    @app.get("/v1/blocks")
    def list_blocks(limit: int = 50, before: int = 0) -> dict[str, Any]:
        return {"items": [b.to_dict() for b in node.blocks_page(limit, before)]}

    @app.get("/v1/blocks/{height}")
    def get_block(height: int) -> dict[str, Any]:
        block = node.block(height)
        if block is None:
            raise ApiError(404, "BLOCK_NOT_FOUND", "block not found")
        return block.to_dict()

    @app.get("/v1/transactions/{tx_hash}")
    def get_transaction(tx_hash: str) -> dict[str, Any]:
        receipt = node.receipt(tx_hash)
        if receipt is None:
            raise ApiError(404, "TX_NOT_FOUND", "transaction not found")
        return receipt.to_dict()

    @app.get("/v1/proofs/{key:path}")
    def key_proof(key: str) -> dict[str, Any]:
        return proof_view(node.proof(key))

    @app.get("/v1/webhooks/key")
    def webhook_key() -> dict[str, str]:
        key = node.webhook_public_key
        if not key:
            raise ApiError(404, "NOT_FOUND", "webhooks disabled")
        return {"algorithm": "ed25519", "public_key": key}

    @app.get("/v1/events")
    def events(
        request: Request, from_height: int = 0, from_: int = Query(0, alias="from")
    ) -> StreamingResponse:
        start = from_ or from_height

        def stream() -> Iterator[str]:
            if start > 0:
                for block in reversed(node.blocks_page(1000, 0)):
                    if block.height < start:
                        continue
                    data = {
                        "type": "block", "height": block.height, "time": block.time,
                        "hash": block.hash(), "tx_hashes": block.tx_hashes,
                    }
                    yield f"id: {block.height}\nevent: block\ndata: {json.dumps(data)}\n\n"
            queue = node.subscribe()
            try:
                while True:
                    if request.is_disconnected():
                        return
                    try:
                        event = queue.get(timeout=1.0)
                    except Exception:
                        continue
                    yield (
                        f"id: {event.height}\nevent: block\n"
                        f"data: {json.dumps(event.to_dict())}\n\n"
                    )
            finally:
                node.unsubscribe(queue)

        return StreamingResponse(stream(), media_type="text/event-stream")

    # --- writes ---

    @app.post("/v1/transactions", status_code=202)
    async def submit_tx(request: Request) -> Response:
        rate_check(request)
        require("write")(request)
        raw = _tx_bytes(await _read_body(request))
        keys = request.headers.getlist("Idempotency-Key")
        if len(keys) > 1 or (keys and not keys[0]):
            raise ApiError(400, "INVALID_ARGUMENT", "one non-empty Idempotency-Key required")
        tx_hash, replay = submit_with_key(
            raw, keys[0] if keys else "", request.headers.get("Authorization", ""),
        )
        receipt = node.receipt(tx_hash) if replay else None
        if receipt is not None:
            return JSONResponse({"hash": tx_hash, "status": "finalized", "receipt": receipt.to_dict()})
        return JSONResponse({"hash": tx_hash, "status": "accepted"}, status_code=202)

    @app.post("/v1/transactions/simulate")
    async def simulate_tx(request: Request) -> dict[str, Any]:
        rate_check(request)
        raw = _tx_bytes(await _read_body(request))
        return node.simulate(raw).to_dict()

    @app.post("/v1/blocks/produce")
    async def produce_block(request: Request) -> dict[str, Any]:
        rate_check(request)
        require("admin")(request)
        return node.produce_block().to_dict()

    @app.post("/v1/faucet", status_code=202)
    async def faucet(request: Request) -> dict[str, Any]:
        rate_check(request)
        require("write")(request)
        if not config.devnet:
            raise ApiError(404, "NOT_FOUND", "faucet disabled")
        try:
            payload = json.loads(await _read_body(request))
        except json.JSONDecodeError as exc:
            raise ApiError(400, "INVALID_ARGUMENT", "address required") from exc
        address = payload.get("address") if isinstance(payload, dict) else None
        if not isinstance(address, str) or not address:
            raise ApiError(400, "INVALID_ARGUMENT", "address required")
        return {"hash": node.faucet(address)}

    # --- JSON-RPC 2.0 ---

    @app.post("/rpc")
    async def rpc(request: Request) -> Response:
        rate_check(request)
        body = await _read_body(request)
        try:
            payload = json.loads(body)
        except json.JSONDecodeError:
            return JSONResponse(
                {"jsonrpc": "2.0", "error": {"code": -32700, "message": "parse error"}}
            )
        if isinstance(payload, dict) and payload.get("method"):
            if "id" not in payload:
                dispatch(node, payload)
                return Response(status_code=204)
            return JSONResponse(dispatch(node, payload))
        if not isinstance(payload, list):
            return JSONResponse({
                "jsonrpc": "2.0",
                "error": {"code": -32700, "message": "parse error"},
            })
        out = []
        for item in payload:
            if not isinstance(item, dict):
                out.append(_rpc_error(None, -32600, "invalid request"))
                continue
            if "id" not in item:
                dispatch(node, item)
                continue
            out.append(dispatch(node, item))
        if not out:
            return Response(status_code=204)
        return JSONResponse(out)

    return app


# --- JSON-RPC dispatch ---


def _rpc_error(request_id: Any, code: int, message: str, data: Any = None) -> dict:
    error: dict[str, Any] = {"code": code, "message": message}
    if data is not None:
        error["data"] = data
    payload: dict[str, Any] = {"jsonrpc": "2.0", "error": error}
    if request_id is not None:
        payload["id"] = request_id
    return payload


def _rpc_result(request_id: Any, result: Any) -> dict:
    payload: dict[str, Any] = {"jsonrpc": "2.0", "result": result}
    if request_id is not None:
        payload["id"] = request_id
    return payload


def dispatch(node: Node, request: dict) -> dict:
    request_id = request.get("id")
    if request.get("jsonrpc") != "2.0":
        return _rpc_error(request_id, -32600, "invalid request")
    params = request.get("params") or {}
    if not isinstance(params, dict):
        return _rpc_error(request_id, -32602, "params must be an object")
    method = request.get("method", "")
    try:
        return _rpc_result(request_id, _call(node, method, params))
    except ApiError as exc:
        return _rpc_error(request_id, -32602, exc.message)
    except ledger.LedgerError as exc:
        return _rpc_error(request_id, -32000, exc.message, exc.code)
    except _RpcNotFound as exc:
        return _rpc_error(request_id, -32004, str(exc))
    except _RpcMethodNotFound as exc:
        return _rpc_error(request_id, -32601, str(exc))


def _call(node: Node, method: str, params: dict) -> Any:
    if method in ("arne_status", "arne_chainId"):
        status = node.status()
        return {
            "network": status["network"], "chain": status["chain"],
            "height": status["height"], "state_root": status["state_root"],
            "mempool": status["mempool"], "halted": status["halted"],
        }
    if method == "arne_blockNumber":
        return node.status()["height"]
    if method == "arne_getBlockByNumber":
        height = params.get("height", 0)
        if not isinstance(height, int) or isinstance(height, bool):
            raise ApiError(400, "INVALID_ARGUMENT", "height must be a number")
        block = node.block(height)
        if block is None:
            raise _RpcNotFound("block not found")
        return block.to_dict()
    if method == "arne_getBalance":
        return node.state.balance(params.get("address", ""), params.get("asset", ""))
    if method == "arne_getAccount":
        info, exists = node.state.account(params.get("address", ""))
        return {
            "address": info.address, "nonce": info.nonce,
            "balances": info.balances, "exists": exists,
        }
    if method == "arne_listAssets":
        return node.state.assets()
    if method == "arne_sendRawTransaction":
        raw_text = params.get("tx", "")
        if not isinstance(raw_text, str):
            raise ApiError(400, "INVALID_ARGUMENT", "tx must be base64 or hex")
        try:
            raw = base64.b64decode(raw_text, validate=True)
        except (binascii.Error, ValueError):
            try:
                raw = bytes.fromhex(raw_text)
            except ValueError as exc:
                raise ApiError(400, "INVALID_ARGUMENT", "tx must be base64 or hex") from exc
        return node.submit_tx(raw)
    if method == "arne_getTransactionReceipt":
        receipt = node.receipt(params.get("hash", ""))
        if receipt is None:
            raise _RpcNotFound("transaction not found")
        return receipt.to_dict()
    if method == "arne_getProof":
        return proof_view(node.proof(params.get("key", "")))
    raise _RpcMethodNotFound(f"method not found: {method}")


class _RpcNotFound(Exception):
    pass


class _RpcMethodNotFound(Exception):
    pass
