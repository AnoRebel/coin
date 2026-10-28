# Integration guide

This guide is for backend services that talk to a node. The REST contract is
identical on the Go and Python nodes.

Base URL: `http://127.0.0.1:8080`. Everything user-facing is under `/v1`.

## 1. Read first, write second

A typical integration:

1. `GET /v1/status` for height and `state_root`.
2. `GET /v1/assets/{asset}` for decimals.
3. `GET /v1/accounts/{address}` for the next nonce.
4. Build and sign the transaction **outside** the node.
5. `POST /v1/transactions` with canonical CBOR.
6. Poll `GET /v1/transactions/{hash}` or subscribe to `/v1/events`.

Never send a mnemonic to a node. The node has no endpoint that accepts one; the
devnet faucet is the only key-holding component, and only on devnet.

## 2. Submit a transaction

The body is either raw canonical CBOR (`Content-Type: application/cbor`) or
`{"tx": "<base64-or-hex>"}`. Response: `202 {"hash": "...", "status": "accepted"}`.

Python example (using the parity package for encoding and signing):

```python
import httpx
from arne import cbor, types
from arne.keys import keypair_from_mnemonic

net = "arne:devnet"
kp = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 0, net)
acct = httpx.get(f"{base}/v1/accounts/{kp.address}").json()
now = int(time.time())
tx = types.Tx(
    version=1, network=net, chain=types.chain_id(net), tx_type=types.TX_TRANSFER,
    signer=kp.address, nonce=acct["nonce"], created_at=now, expires_at=now + 3600,
    fee_asset=types.native_asset(net), max_fee=10_000,
    payload={"asset": "arce:devnet", "to": DEST, "amount": 1_250_000_000},
)
tx.sign(kp)
httpx.post(f"{base}/v1/transactions", content=cbor.encode(tx.to_cbor()),
           headers={"Content-Type": "application/cbor"})
```

A runnable version is `examples/python/client.py` (pay, escrow, release):

```sh
uv run python examples/python/client.py pay --to arce-devnet1... --amount 12.5
```

Go integrations can import `internal/protocol` inside this module, or reimplement
the canonical CBOR and signing payload from `docs/developer-guide.md`.

Receipts are not available until the transaction is included in a block:

```json
{
  "hash": "…", "height": 7, "success": true,
  "fee": 1000, "fee_asset": "arce:devnet",
  "events": [{"type": "transfer", "attrs": {"from": "…", "to": "…", "asset": "arce:devnet", "amount": "1250000000"}}]
}
```

Failures include `"success": false`, `"code"`, and `"error"`. A failed
transaction is not charged and changes no state.

## 3. Idempotency and retries

- Transaction hashes are deterministic: resubmitting the same signed bytes
  returns the same hash, and the mempool ignores duplicates.
- Signing a new transaction (new nonce or expiry) changes the hash. Do not
  rotate nonces on timeout; read the account again.
- `BAD_NONCE` means another transaction from the same signer was confirmed
  first. Rebuild the transaction with the fresh nonce.
- API-level `Idempotency-Key` headers are accepted in CORS preflight but are
  **not implemented**. Idempotency comes from the transaction hash today.

Recommended retry policy: retry transport errors and 5xx with backoff; never
retry a 4xx without changing the transaction.

## 4. Subscribe to events

Server-sent events:

```sh
curl -N http://127.0.0.1:8080/v1/events
curl -N "http://127.0.0.1:8080/v1/events?from=120"   # replay from height
```

Each event:

```
id: 7
event: block
data: {"type":"block","height":7,"time":...,"hash":"...","tx_hashes":["..."]}
```

Reconnect with `from=<last height + 1>`. The replay window is the last 1,000
blocks. Python accepts `from_height` as an alias. A slow consumer drops events
rather than stalling block production, so reconcile with block polling after a
long disconnect.

## 5. JSON-RPC

`POST /rpc`, object or batch:

```sh
curl -s http://127.0.0.1:8080/rpc -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":1,"method":"arne_getBalance",
  "params":{"address":"arce-devnet1...","asset":"arce:devnet"}
}'
```

Methods and error codes: [api-reference.md](api-reference.md#json-rpc).
Notifications (no `id`) return `204`.

## 6. Auth and limits

Set API keys in the node configuration:

```go
api.New(node, api.Config{
    APIKeys: map[string][]string{"ops-key": {"read", "write", "admin"}},
    RateLimit: 200,
    AllowedOrigins: []string{"https://app.example"},
})
```

- `Authorization: Bearer <key>` on writes.
- Scopes: `read` (no effect today, all reads are open), `write` (submit, faucet),
  `admin` (produce block).
- With no keys configured, writes are open — suitable only for a private devnet.
- Body limit is 2 MiB. Rate limit is per client IP with a burst of 2× the rate.
- CORS is allowlist-only; credentialed wildcard origins are rejected by design
  (do not add them).

## 7. Assets and decimals

Always read `decimals` from `/v1/assets/{id}`. Convert at the edge:

```
base_units = human_amount * 10**decimals
```

The API and ledger only use integer base units. Floating point in the ledger is
a bug; do not send fractional JSON numbers.

## 8. Fees

The fee asset is part of the signed transaction. Default to the native asset
(`arce:<network>`). A foreign fee asset requires a `route` and a funded
`fee_swap` pool; otherwise the node rejects with `NO_FEE_ROUTE`. Quote
requirements are the operator's responsibility today.

## 9. Webhooks and connectors

Signed webhooks, ISO 20022 (`pacs.008`, `camt.053`, `pain.001`), ILPv4/Open
Payments, and EVM bridging are **specified but not implemented**. Do not build
against them yet. The connector interface is defined in the OpenSpec change so
the shape is stable, but there is no runtime.

For now, integrate by polling/SSE and a reconciliation job keyed by transaction
hash. A transaction hash is unique per signed transaction and is the safest
external reference.

## 10. Error handling

```json
{"error": {"code": "INSUFFICIENT_FUNDS", "message": "insufficient arce:devnet balance"}}
```

Branch on `code`, not on the message or HTTP status alone. Full table:
[api-reference.md](api-reference.md#error-codes).

## 11. Test checklist for integrators

- Round-trip: build a transaction in your language and submit it; the hash must
  match the node's receipt hash.
- Reject a tampered signature, wrong network, and replayed nonce.
- Confirm a failed transaction leaves balances unchanged.
- Confirm base-unit conversion at the asset's exact decimals.
- Reconnect SSE with `from` and verify no missed blocks.
- Load-test your nonce management: one signer, many concurrent requests.
