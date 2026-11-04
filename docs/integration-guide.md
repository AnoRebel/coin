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

Persist the signed bytes and an operation key **before** submitting. Retry
those exact bytes and the same key after a transport error:

```python
raw = cbor.encode(tx.to_cbor())
response = httpx.post(
    f"{base}/v1/transactions", content=raw,
    headers={"Content-Type": "application/cbor", "Idempotency-Key": operation_id},
)
response.raise_for_status()
result = response.json()
# 202: accepted/pending; 200: retry with the original finalized receipt.
receipt = result.get("receipt")
```

Keys allow 1–128 ASCII letters, digits, `_`, `.`, `:`, or `-`. Their scope is
the authorization credential, network, and signer; reuse the same credential
on retries. Different signed bytes return `409 IDEMPOTENCY_CONFLICT`, so do
not re-sign or advance the nonce merely because a response was lost.

Retention is 24 hours from acceptance, with a 10,000-entry process-local cache.
Restart loses the cache. Capacity exhaustion returns `503 IDEMPOTENCY_FULL`
without evicting a live key. Use one API instance per retry sequence and retain
your own transaction/receipt records. Failed admission does not reserve a key.

Without a key, a pending transaction's identical hash is deduplicated, but
resubmission after confirmation normally fails its nonce check. After restart
or cache expiry, query the transaction hash (or the intent's `tx_hash`) before
deciding whether to sign another transaction. Retry transport errors and 5xx
with backoff; reconcile a 4xx instead of automatically rotating the nonce.

### Request and reconcile an intent

1. The payee signs `create_intent` with `asset`, integer-base-unit `amount`,
   a unique `reference`, and optional future `expires_at`/public `memo`.
2. Wait for the creation receipt and obtain the ID from `intent_created`.
3. Read `GET /v1/intents/{id}` or generate a canonical link with
   `arned intent link --id <id> --qr invoice.png`.
4. The payer resolves the intent on its declared network and signs a `transfer`
   with the matching `to`, `asset`, `amount`, and `intent` ID.
5. Submit with an idempotency key. On confirmation, reconcile `intent_paid` and
   the intent's recorded `payer`, `tx_hash`, and `paid_at`.

The ledger marks an intent paid only if the transfer succeeds. A new transfer
cannot pay it twice, regardless of HTTP cache lifetime. An original keyed
retry returns the original receipt while retained; a new payment attempt
returns `INTENT_PAID`. Expired or mismatched payments leave it open and move no
value. Store any sensitive invoice details off chain: the intent memo is public.

### Reconcile a refund

A refund is a new transfer, not a reversal of the original receipt. Reconcile
both sides:

1. The original recipient signs `transfer` with `refund_of` set to the original
   transaction hash. The destination is the original payer and the asset is the
   original asset; the ledger rejects anything else.
2. On confirmation, read the refund's own receipt and its `refund` event. The
   event contains `refund_of`, `from`, `to`, `asset`, `amount`, and
   `remaining`.
3. Query `GET /v1/transfers/{original-hash}` to obtain cumulative `refunded`
   and `refundable`. Treat this index as the source of truth for over-refund
   protection, not your local log.
4. Mark the original invoice paid minus the cumulative refunds. Keep the
   original receipt unchanged for audit.

Refunds are ordinary transfers for fees and nonces. The refund signer pays the
fee from its own balance, so a wallet holding only the received amount cannot
return the final units until it is funded with a fee balance. Repeated
submissions need a new nonce; an `Idempotency-Key` only replays the exact
signed bytes. A batch item or escrow release is not in the transfer index, so
it cannot be named by `refund_of`.

### Bill a subscription

Recurring billing is pull-based: the payer authorizes once, then the payee
submits each charge.

1. The payer signs `create_subscription` with `payee`, `asset`,
   `amount_per_period`, `period` seconds, and `total_cap`, plus optional
   `expires_at`. Obtain the ID from `subscription_created`; do not derive it
   from your own nonce unless you just signed the transaction.
2. At each billing point the payee signs `charge_subscription` with that `id`
   and the amount due. The ledger enforces both caps and the expiry.
3. Reconcile `subscription_charged`: it reports `charged_period`,
   `charged_total`, `remaining_total`, and `period_start`. Read the
   authorization with `GET /v1/subscriptions/{id}` for current state.
4. On `SUBSCRIPTION_LIMIT` or `SUBSCRIPTION_EXPIRED`, do not retry blindly.
   Record the shortfall, request a new authorization, or bill the remainder
   next period. On `SUBSCRIPTION_CLOSED`, stop billing.
5. If your job needs the amount still available this period, read the
   subscription and apply the same reset rule: `charged_period` is zero once
   block time has reached `period_start + period`.

Charges are ordinary transactions: each needs the payee's next nonce and pays
the fee from the payee's balance. An `Idempotency-Key` prevents duplicate
submission of the same signed bytes, but a second charge with a new nonce is a
new payment and is allowed within the caps. There is no on-chain scheduler;
run charges from a durable job with your own invoice IDs.

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

Reconnect with `Last-Event-ID: <last id>` to resume after that event, or with
`from=<height>` to replay inclusively from a height. Ids are block heights;
`stream=transactions` switches to receipts with `<height>-<index>` ids. A cursor
older than the last 1,000 retained blocks gets a `cursor_expired` event with the
earliest usable cursor. Python accepts `from_height` as an alias. A slow
consumer drops events rather than stalling block production, so reconcile with
block polling after a long disconnect.

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
`fee_swap` pool; otherwise the node rejects with `NO_FEE_ROUTE`. Set the
optional payload field `max_arce_fee` (ARCE base units) to bound the converted
fee; a conversion above it is rejected with `LIMIT_EXCEEDED` before charging.

## 9. Workspace roles

If the operator starts the node with `--workspace <file>`, include
`Authorization: Bearer <key>` on workspace and payment routes. The caller's
role decides whether a payment can be submitted, a payment record read, or a
member changed. Use a key per person or service; do not share the owner key with
integrations. Audit entries are available at `/v1/workspace/audit` for owners,
admins, and auditors, with `after`/`limit` pagination.

An auditor key additionally needs a grant covering the record's asset and the
current time (`POST /v1/audit/grants`, owner only). Without one, payment reads
return `403 AUDIT_SCOPE`. Handle that code as "not in scope", not as a retryable
error.

## 10. Webhooks and connectors

Signed webhooks are implemented. Enable them on the node with `--webhook <url>`
and `--webhook-key <64-hex-seed>`; generate a key with `arned webhook keygen`.
Each finalized transaction produces one signed JSON event, retried up to five
times with backoff on 429/5xx. Delivery is at-least-once, so deduplicate
`delivery_id`.

The receiver fetches the public key from `GET /v1/webhooks/key` (or pins the key
printed at startup), then verifies the Ed25519 signature over
`<X-Arne-Timestamp>\n<body>` and rejects timestamps outside five minutes:

Go:

```go
id, err := arne.VerifyWebhookRequest(r.Header, body, publicKey, time.Now(), 5*time.Minute)
if err != nil {
    http.Error(w, "bad signature", http.StatusUnauthorized)
    return
}
if seen(id) {
    w.WriteHeader(http.StatusOK) // deduplicate retries
    return
}
```

Python:

```python
from arne import webhook

delivery = webhook.verify_request(request.headers, body, public_key)
if delivery in seen:
    return Response(status_code=200)
```

The `transaction` event body contains the block height/hash and the full receipt,
including `transfer` events. Filter on receipt events instead of assuming every
transaction is a payment.

ISO 20022 (`pacs.008`, `camt.053`, `pain.001`), ILPv4/Open Payments, and EVM
bridging remain **specified but not implemented**. The connector interface is
defined in the OpenSpec change so the shape is stable, but there is no runtime.

For text notifications without webhooks, poll or use SSE and reconcile by
transaction hash. A transaction hash is unique per signed transaction and is the
safest external reference.

## 11. Error handling

```json
{"error": {"code": "INSUFFICIENT_FUNDS", "message": "insufficient arce:devnet balance"}}
```

Branch on `code`, not on the message or HTTP status alone. Full table:
[api-reference.md](api-reference.md#error-codes).

## 12. Test checklist for integrators

- Round-trip: build a transaction in your language and submit it; the hash must
  match the node's receipt hash.
- Reject a tampered signature, wrong network, and replayed nonce.
- Confirm a failed transaction leaves balances unchanged.
- Confirm base-unit conversion at the asset's exact decimals.
- Reconnect SSE with `from` and verify no missed blocks.
- Load-test your nonce management: one signer, many concurrent requests.
