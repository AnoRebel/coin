# API reference

Protocol `arne/1`, API `v1`, transaction version `1`.

- Base URL: `http://127.0.0.1:8080`
- Request body limit: 2 MiB
- Content types: `application/json`, `application/cbor`
- Errors: `{"error": {"code": "...", "message": "..."}}`
- Auth: `Authorization: Bearer <key or JWT>` on protected operations; reads are public

## Authentication

Reads are public. These operations require a credential when the node has one
configured:

| Operation | Scope |
| --- | --- |
| `POST /v1/transactions` | `write` |
| `POST /v1/faucet` | `write` |
| `POST /v1/blocks/produce` | `admin` |
| `/v1/workspace*` | workspace principal key |
| `/v1/audit/grants*` | workspace owner |

Credentials are checked in this order:

1. **API key** — `--api-key key=read,write[,admin]`. Send it as
   `Authorization: Bearer <key>`. `admin` implies `write` and `read`.
2. **OIDC access token** — when `--oidc-issuer`, `--oidc-audience`, and
   `--oidc-jwks` are set. The node accepts RS256 JWTs whose `kid` is a named RSA
   key in the static JWKS file, whose `iss` and `aud` match exactly, and whose
   `sub` is non-empty. Expired tokens and tokens before `nbf` are rejected, as
   are `alg: none`, HS256, and unknown keys. Scopes come from the `scope` string
   and the `scp` string or array.
3. **Mutual TLS** — with `--tls-client-ca`, a client certificate verified
   against that bundle authenticates the caller as admin on
   `POST /v1/blocks/produce`. It does not grant `write` or `read`.

A missing or invalid credential is `401 UNAUTHORIZED`; a valid credential
without the required scope is `403 FORBIDDEN`. When no credential is configured
the API is open, which is the devnet default. A bearer token for a workspace
principal authenticates as that member with `read` and `write` but never
`admin`.

`--rate-limit` applies per credential (its scope is the token subject) and falls
back to the client IP for anonymous requests. Exceeding it returns
`429 RATE_LIMITED`. Behind a proxy, bind the API to localhost or set the proxy to
overwrite `X-Forwarded-For`; the node trusts it for rate-limit keys.

## REST `/v1`

`GET /v1/openapi.json` serves the OpenAPI 3.1 document for this API. The same
file is committed at `spec/openapi.json`, and a contract test fails when the
document and the live routes disagree. The document regenerates nothing at
runtime: rebuild the node after editing it.

List endpoints share one pagination shape:

- `limit` is the page size, capped at 1,000.
- `cursor` is the opaque `next_cursor` from the previous page and is exclusive.
- `order` is `asc` or `desc`; the default is `desc` (newest or highest first).
- `next_cursor` appears only when another page exists.
- Unknown query parameters are rejected with `400 INVALID_ARGUMENT`, so a
  misspelled filter never silently returns unfiltered data.

### Health and discovery

| Method | Path | Description |
| --- | --- | --- |
| GET | `/v1/health` | `200 {"status":"ok","height":N}`; `503` with `"status":"halted"` |
| GET | `/v1/openapi.json` | OpenAPI 3.1 description of every public route |
| GET | `/v1/version` | `{"protocol":"arne/1","tx_version":1,"api":"v1","cbor":"deterministic","state":"smt-sha256"}` |
| GET | `/v1/status` | Node status |
| GET | `/v1/webhooks/key` | `{"algorithm":"ed25519","public_key":"…"}`, or `404 NOT_FOUND` when webhooks are disabled |

`/v1/status` response:

```json
{
  "network": "arne:devnet", "chain": "arne-devnet-1",
  "height": 42, "state_root": "…64 hex…", "time": 1793151119,
  "mempool": 0, "devnet": true, "protocol_version": "arne/1"
}
```

`halted` is present only when the node halted, with the reason.

### Workspace roles and audit

Enable with `--workspace <file>`. When enabled, every workspace/payment request
needs `Authorization: Bearer <key>` where the key appears in the file's
`principals` map. Without a workspace these routes behave as before
(`/v1/workspace*` returns `404`).

| Method | Path | Permission | Description |
| --- | --- | --- | --- |
| GET | `/v1/workspace` | authenticated | Caller identity, role, and permission list |
| GET | `/v1/workspace/members` | `member.read` | `{"items":[{"id","role"}…]}` |
| PUT | `/v1/workspace/members/{id}` | `member.manage` | Body `{"role":"owner\|admin\|member\|auditor"}` |
| DELETE | `/v1/workspace/members/{id}` | `member.manage` | `204`; also drops that member's principals |

Membership writes accept `Idempotency-Key`; see [Idempotency](#idempotency).
| GET | `/v1/workspace/audit?after=N&limit=M` | `audit.read` | Audit entries with `seq > after` |

Roles and permissions:

| Permission | owner | admin | member | auditor |
| --- | --- | --- | --- | --- |
| `channel.read` | yes | yes | yes | yes |
| `channel.write` | yes | yes | yes | — |
| `channel.manage` | yes | yes | — | — |
| `payment.read` | yes | yes | yes | yes |
| `payment.write` | yes | yes | yes | — |
| `member.read` | yes | yes | yes | yes |
| `member.manage` | yes | yes | — | — |
| `settings.manage` | yes | — | — | — |
| `audit.read` | yes | yes | — | yes |

Audit entry:

```json
{"seq":3,"time":1793441482,"actor":"alice","action":"member.manage",
 "target":"bob","result":"ok","detail":"member -> admin"}
```

`result` is `ok`, `accepted`, or `denied`. Denied permission checks are audited
too. Only an owner may change or remove an owner; the last owner cannot be
removed or demoted (`409 LAST_OWNER`).

### Auditor access grants

When a workspace is configured, auditors are restricted to the assets and time
window granted to them. Other roles are not scoped here. Grants are stored in
`--audit-grants <file>`; without that flag they live in memory and are lost on
restart. Every check, allowed or denied, is appended to `<file>.log.jsonl`.

| Method | Path | Permission | Description |
| --- | --- | --- | --- |
| GET | `/v1/audit/grants` | `audit.read` | Auditors see their own grants; others see all |
| POST | `/v1/audit/grants` | `settings.manage` | Create a grant; owner only |
| DELETE | `/v1/audit/grants/{id}` | `settings.manage` | Revoke a grant; `204` |
| GET | `/v1/audit/grants/log?after=N&limit=M` | `settings.manage` | Access log; owner only |

Create body:

```json
{"auditor":"dave","assets":["usd:devnet"],"from":1790000000,"until":1798000000,"purpose":"Q1 audit"}
```

`from`/`until` are unix seconds and the window is half-open: `from <= t < until`.
`assets` is 1–32 asset IDs, deduplicated and sorted. `purpose` is required (1–256
characters). The grant ID is deterministic: the first 16 bytes of
`SHA-256("arne-grant-v1" || 0 || auditor || 0 || purpose || 0 || from || 0 || until || 0 || sorted-assets)`.
Recreating an identical grant returns `409 GRANT_EXISTS`.

Access log entry:

```json
{"seq":4,"time":1793441500,"auditor":"dave","action":"access",
 "grant":"62be2c5fb24b575b5cc9bd5227f1d216","asset":"usd:devnet","result":"allowed"}
```

`result` is `allowed`, `no-grant`, `expired`, `not-yet-active`, or
`out-of-scope`; lifecycle events use `action` `grant`/`revoke`. An auditor read
outside scope returns `403 AUDIT_SCOPE` and is logged.

### Assets

| Method | Path | Query | Description |
| --- | --- | --- | --- |
| GET | `/v1/assets` | `limit`, `cursor`, `order`, `class`, `issuer` | Filtered, paginated assets |
| GET | `/v1/assets/{asset-id}` | — | One asset, or `404 UNKNOWN_ASSET` |
| GET | `/v1/supply/{asset-id}` | — | `{"asset":"…","supply":N,"burned":N}` |

`cursor` is the last asset ID of the previous page; `class` matches the exact
asset class (`fiat`, `crypto`, `stablecoin`, …) and `issuer` matches the issuing
address exactly.

Asset object:

```json
{
  "id": "usd:devnet", "class": "fiat", "symbol": "USD", "name": "Demo US Dollar",
  "decimals": 2, "policy": "capped", "max_supply": 10000000000,
  "issuer": "arce-devnet1…", "metadata": {"iso4217": "USD"},
  "registered": 0, "supply": 0, "burned": 0
}
```

Asset ID shape: `^[a-z][a-z0-9]{1,15}:[a-z0-9][a-z0-9/-]{0,63}$`, and the suffix
must match the network short name (`devnet`, `mainnet`, `testnet`, or the
consortium name).

### Accounts

| Method | Path | Query | Description |
| --- | --- | --- | --- |
| GET | `/v1/accounts/{address}` | — | Balances, nonce, auth |
| GET | `/v1/accounts/{address}/proof` | `asset` | State proof for a balance, or the nonce if `asset` is omitted |
| GET | `/v1/proofs/{key}` | — | Proof for any namespaced state key (the key may contain `/`) |

```json
{
  "address": "arce-devnet1…", "exists": true, "nonce": 3,
  "auth": "ed25519-v1", "balances": {"arce:devnet": 123456789}
}
```

Unknown accounts return `200` with `"exists": false` and empty balances; they are
not an error.

Proof shape:

```json
{
  "key": "balance/arce-devnet1…/arce:devnet",
  "value": "0000001792f86480", "included": true,
  "depth": 6, "terminal": "…64 hex…",
  "siblings": ["…64 hex…", "…"]
}
```

`value` is hex of the stored bytes. `included: false` with a valid `terminal`
proves absence at the returned `state_root`.

### Escrows and allowances

| Method | Path | Description |
| --- | --- | --- |
| GET | `/v1/escrows/{id}` | Escrow object, or `404 ESCROW_NOT_FOUND` |
| GET | `/v1/intents/{id}` | Payment intent, or `404 INTENT_NOT_FOUND` |
| GET | `/v1/subscriptions/{id}` | Recurring authorization, or `404 SUBSCRIPTION_NOT_FOUND` |
| GET | `/v1/transfers/{hash}` | Indexed payment transfer, or `404 TRANSFER_NOT_FOUND` |
| GET | `/v1/allowances/{owner}/{spender}/{asset}` | `{"owner","spender","asset","remaining","expires_at"}` |

```json
{
  "id": "esc_…", "payer": "…", "beneficiary": "…", "arbiter": "…",
  "asset": "arce:devnet", "amount": 5000000000, "timeout": 1793237519,
  "status": "open", "created_at": 1793151119
}
```

`status` is `open`, `released`, or `refunded`.

### Blocks and transactions

| Method | Path | Query | Description |
| --- | --- | --- | --- |
| GET | `/v1/blocks` | `limit` (default 50), `cursor`, `order`, `sort`, `proposer` | Paginated blocks |
| GET | `/v1/blocks/{height}` | — | One block, or `404 BLOCK_NOT_FOUND`; `410 PRUNED` outside the retention window |
| GET | `/v1/transactions/{hash}` | — | Receipt, or `404 TX_NOT_FOUND` |
| GET | `/v1/transactions/{hash}/status` | — | Deprecated alias of the receipt; sends `Deprecation`, `Sunset`, and `Link` headers |

`/v1/blocks` sorts by `height` only (`sort=height`); `cursor` is the last
returned height and is exclusive in either direction. `proposer` matches the
block proposer address exactly. The deprecated status alias has the same body
as `/v1/transactions/{hash}`; its `Sunset` header is
`Tue, 04 May 2027 00:00:00 GMT` and `Link` points at the successor.

Block page:

```json
{"items": [{"height": 7, "hash": "…", "proposer": "…", "…": "…"}],
 "next_cursor": 5}
```

Block:

```json
{
  "height": 7, "hash": "…", "time": 1793151119, "proposer": "arce-devnet1…",
  "prev_root": "…", "state_root": "…", "tx_root": "…",
  "tx_hashes": ["…"], "cursor": 6
}
```

Receipt:

```json
{
  "hash": "…", "height": 7, "success": true,
  "fee": 1000, "fee_asset": "arce:devnet",
  "events": [{"type": "transfer", "attrs": {"from": "…", "to": "…", "asset": "arce:devnet", "amount": "1250000000"}}]
}
```

Failures add `"code"` and `"error"`.

### Events

`GET /v1/events` streams `text/event-stream`. `stream` selects the feed:
`blocks` (default) sends one `block` event per finalized block, and
`transactions` sends one `transaction` event per transaction with its receipt.

```
id: 7
event: block
data: {"type":"block","height":7,"time":…,"hash":"…","tx_hashes":["…"]}

id: 7-0
event: transaction
data: {"type":"transaction","height":7,"time":…,"hash":"…","index":0,
       "receipt":{"hash":"…","success":true,"fee":1000,"events":[…]}}
```

- Event ids are the block height for `blocks` and `<height>-<index>` for
  `transactions`. Send the last id back as `Last-Event-ID` to resume after it,
  or use `?from=<height>`, which is inclusive and replays from that height.
- `Last-Event-ID` wins over `from`. A transaction cursor resumes inside its
  block, so two transactions in one block do not duplicate after a reconnect.
- Replay covers the last 1,000 retained blocks. A cursor older than that gets
  `event: cursor_expired` with `{"stream":"…","earliest":"…"}` before the
  available events; reconnect from that cursor. Blocks pruned by the node's
  retention policy are also reported this way.
- The stream starts live after any replay. Slow subscribers drop events instead
  of blocking consensus; reconcile from the chain after a long disconnect.

```sh
curl -N "http://127.0.0.1:8080/v1/events?from=120"
curl -N -H "Last-Event-ID: 481-2" "http://127.0.0.1:8080/v1/events?stream=transactions"
```

Python also accepts `from_height`. A `message` stream is not emitted yet:
workspace messaging (`10.7`) has no publisher, so `stream=messages` is rejected
with `400 INVALID_ARGUMENT` rather than opening an empty stream silently.

### Webhook deliveries

With `--webhook` configured, the node POSTs one signed event per finalized
transaction. Delivery is at-least-once; retries reuse `delivery_id`.

```
POST /your-endpoint
Content-Type: application/json
X-Arne-Delivery: 9f0c…            (stable across retries)
X-Arne-Timestamp: 1793151119
X-Arne-Signature: <hex Ed25519>

{"delivery_id":"9f0c…","type":"transaction","created_at":1793151119,
 "data":{"block_height":42,"block_hash":"…","time":1793151118,
         "receipt":{"hash":"…","success":true,"fee":1000,"events":[…]}}}
```

The signature covers `<timestamp>\n<body>`. Reject timestamps outside 5 minutes
and deduplicate `X-Arne-Delivery`. 2xx acknowledges; 429/5xx retry with
exponential backoff (5 attempts); other 4xx are final. The queue holds 256
events; overload drops new events, so reconcile from the chain after downtime.

### Writes

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| POST | `/v1/transactions` | `write` | Submit canonical CBOR or `{"tx":"base64-or-hex"}`; `202 {"hash":"…","status":"accepted"}`; accepts `Idempotency-Key` |
| POST | `/v1/transactions/simulate` | — | Dry run; returns a receipt, never commits |
| POST | `/v1/blocks/produce` | `admin` | Produce a block now (devnet/operator use); accepts `Idempotency-Key` |
| POST | `/v1/faucet` | `write` | Devnet only; body `{"address":"…"}`; `202 {"hash":"…"}`; accepts `Idempotency-Key` |

Submitting does not mean success. Poll the receipt or watch events.

### Idempotency

Every REST write except JSON-RPC accepts one optional `Idempotency-Key` header:
1–128 ASCII letters, digits, `_`, `.`, `:`, or `-`. This covers transaction
submission, faucet calls, block production, workspace membership changes, and
auditor-grant creation or revocation. `/rpc` has its own request ids; a retry is
only safe if the JSON-RPC method is itself idempotent.

- Retry the same method, path, and body with the same key. A replay returns the
  original status, headers, and body with `Idempotent-Replay: true` and does not
  run the handler again.
- Reusing a key for a different method, path, or body in the same credential
  returns `409 IDEMPOTENCY_CONFLICT`. A concurrent request with the same key
  while the first is still running also returns `409`; retry shortly.
- Only successful (`2xx`) responses are cached, so a validation failure or an
  authorization failure leaves the key free for a corrected retry.
- Successful writes retain a key for 24 hours, without extending expiry on
  retries. Expired keys are reusable.
- The cache holds at most 10,000 entries. If all are unexpired, a new key
  returns `503 IDEMPOTENCY_FULL` rather than evicting another client's record.

Transaction submission has one extra rule: a retry after finalization returns
`200 {"hash","status":"finalized","receipt":{…}}` with the original receipt
while the node retains it, instead of the cached `202`. Raw CBOR and a JSON
wrapper containing the same bytes refer to the same request, and a transaction
key is scoped by credential, network, and signer.

The cache is process-local and is lost on restart; use one API instance for a
retry sequence. It is not a durable job queue and does not guarantee that an
accepted transaction will finalize. Intent state and account nonces still
prevent duplicate execution after cache loss. Receipt lookup follows the
node's pruning policy; retain accounting receipts in your integration.

## Transaction envelope

Canonical CBOR map:

| Field | Type | Notes |
| --- | --- | --- |
| `v` | uint | must be `1` |
| `network` | text | e.g. `arne:devnet` |
| `chain` | text | must match `types.chain_id(network)` |
| `type` | text | see below |
| `signer` | text | Bech32m address |
| `nonce` | uint | exact account nonce |
| `created_at` | uint | unix seconds |
| `expires_at` | uint | `0` = no expiry; must be ≤ now + 30 days |
| `fee_asset` | text | asset ID |
| `max_fee` | uint | base units |
| `priority_fee` | uint | optional, default 0 |
| `payload` | map | type-specific |
| `signatures` | array | `{"suite":"ed25519-v1","pubkey":bytes,"sig":bytes}` |

Signature payload:
`{"dom":"arne-sig-v1","network","chain","suite","purpose","expires_at","body"}`,
where `body` is the envelope without signatures and with `v` as uint.

`memo_enc` is the canonical CBOR map
`{"v":1,"epk":bytes32,"nonce":bytes12,"ct":bytes}` sealed to the recipient's
memo public key (X25519 + HKDF-SHA-256 + ChaCha20-Poly1305). The ledger cannot
decrypt it and rejects malformed envelopes or plaintexts above 256 bytes.

The envelope and each signature object are closed schemas in `v: 1`: any field
not listed above is rejected with `INVALID_SCHEMA` before signature
verification, so a relay cannot add uncommitted fields to a signed transaction.
New envelope fields require a new version. The `payload` map is typed per
transaction; unknown payload keys are currently ignored.

### Transaction types

| Type | Payload |
| --- | --- |
| `transfer` | `asset`, `to`, `amount`, optional `intent` ID, optional `refund_of` transaction hash, optional `memo` (text ≤256 bytes) or `memo_enc` (sealed bytes; mutually exclusive) |
| `create_intent` | `reference` (1–64 ASCII letters, digits, `_`, `-`), `asset`, positive `amount`, optional future `expires_at`, optional `memo` (≤256 UTF-8 bytes; no NUL, CR, LF) |
| `create_subscription` | `payee`, `asset`, positive `amount_per_period`, positive `period` (seconds), positive `total_cap`, optional future `expires_at` — signed by the payer |
| `charge_subscription` | `id`, positive `amount` — signed by the payee |
| `cancel_subscription` | `id` — signed by the payer |
| `batch` | `items`: 1–100 × `{asset, to, amount}`, optional `mode`: `atomic` (default) or `best_effort` |
| `register_asset` | `asset`: full asset map without supply |
| `create_escrow` | `asset`, `beneficiary`, `arbiter`, `amount`, `timeout` |
| `release_escrow` | `id` — payer or arbiter anytime, beneficiary after `timeout` |
| `refund_escrow` | `id` — payer after `timeout`, arbiter anytime |
| `approve_allowance` | `spender`, `asset`, `amount`, optional `expires_at` |
| `transfer_from` | `from`, `to`, `asset`, `amount` |
| `revoke_allowance` | `spender`, `asset` |
| `mint` | `asset`, `to`, `amount` (issuer only, policy permitting) |
| `burn` | `asset`, `amount` (burnable policy only) |

Any transaction payload may include `max_arce_fee` (positive integer, ARCE base
units). When the fee is paid in another asset, the converted ARCE amount must
not exceed it; otherwise the transaction is rejected with `LIMIT_EXCEEDED`
before any fee is charged. For a native fee it caps the fee directly.

`amount` is always integer base units and must be > 0 where an amount is
required. An `atomic` batch rolls back entirely if any item fails. In a
`best_effort` batch each item is validated and applied independently; failures
are skipped and reported as `batch_item` events with `success: "false"` and a
`code`, while successful items commit.

### Payment intents

Submit a `create_intent` transaction signed by the payee through the existing
transaction endpoint. Creation charges the usual transaction fee. The stable
ID is the first 16 bytes, in lowercase hex, of
`SHA-256("arne-intent-v1\0" + network + "\0" + payee + "\0" + reference)`.
The same payee/reference cannot be created twice on one network.

`GET /v1/intents/{id}` returns `id`, `payee`, `reference`, `asset`, `amount`,
`expires_at`, `status`, and `created_at`, plus `memo` when present. Omitted
creation expiry is stored as `0` (no expiry). A payment includes the ID in its
`transfer` payload; its recipient, asset, and amount must exactly match. Expiry
is evaluated at block time (payment is rejected when block time is greater
than `expires_at`). Successful payment records `status: "paid"`, `payer`,
`tx_hash`, and `paid_at`, and emits `intent_paid`. Creation emits
`intent_created`. Simulation and failed payments do not consume the intent.

This on-chain guard prevents a second payment. A keyed retry of the original
signed transfer returns its original result as described above; a new transfer
attempting to pay that intent fails with `INTENT_PAID`.

`arned intent link --id <id>` and `arne intent link --id <id>` resolve the
stored request and output the same canonical `arne:` URI, including `network`,
`intent`, recipient, asset, amount, and optional memo/expiry. Amounts omit
unnecessary fractional zeros. Add `--qr <new-file.png>` for a local PNG QR code.

### Refunds

A refund is a `transfer` whose payload adds `refund_of`: the 64-character
lowercase hex hash of an earlier direct transfer. It must be signed by that
transfer's recipient, return the same asset to the original payer, and not
exceed the amount still refundable.

`GET /v1/transfers/{hash}` returns the index entry used by both CLIs:

```json
{
  "hash": "…64 hex…", "from": "arce-devnet1…", "to": "arce-devnet1…",
  "asset": "arce:devnet", "amount": 500000000,
  "refund_of": "", "refunded": 200000000, "refundable": 300000000
}
```

`refunded` is cumulative for that original hash; `refundable` is `amount -
refunded`. A refund transaction itself is indexed with `refund_of` set, so a
refund of a refund is rejected. Combining `intent` and `refund_of` is
`INVALID_PAYLOAD`. Successful refunds emit a `refund` event with `refund_of`,
`from`, `to`, `asset`, `amount`, and `remaining`. Only direct `transfer` and
`transfer_from` records are indexed; batch items and escrow releases are not.

### Subscriptions

A subscription is an on-chain authorization: the payer signs
`create_subscription`, then only the named payee may submit
`charge_subscription` within its limits. The payer may cancel at any time.

```json
{
  "id": "…32 hex…", "payer": "arce-devnet1…", "payee": "arce-devnet1…",
  "asset": "arce:devnet", "amount_per_period": 500000000,
  "period": 2592000, "total_cap": 6000000000,
  "charged_period": 0, "charged_total": 0, "period_start": 1793151119,
  "expires_at": 0, "status": "active", "created_at": 1793151119
}
```

The ID is
`SHA-256("arne-sub-v1\0" + network + "\0" + payer + "\0" + payee + "\0" + asset + "\0" + nonce)`
truncated to 16 bytes, where `nonce` is the payer's creation nonce. Derive it
from the signed transaction rather than inventing one; the CLIs print it.

Rules:

- `amount_per_period` caps the total charged inside one period. When the block
  time reaches `period_start + period`, the counter resets and `period_start`
  advances by whole periods. Unused periods do not accumulate.
- `total_cap` caps all charges for the life of the authorization.
- `expires_at` (optional) rejects charges after that block time.
- `status` is `active` or `cancelled`. Cancellation is immediate; later charges
  and a second cancellation return `SUBSCRIPTION_CLOSED`.
- A charge transfers `amount` from payer to payee and emits
  `subscription_charged` with `charged_period`, `charged_total`,
  `remaining_total`, and `period_start`.
- Creation emits `subscription_created`. Cancellation emits
  `subscription_cancelled`.
- The payee pays the transaction fee; the payer pays the charged amount.
- `payee` must differ from `payer`; zero amounts, periods, or caps are
  `INVALID_PAYLOAD`.

Charges are ordinary transactions for nonces, fees, and receipts. There is no
automatic scheduler: the payee (or a payroll job) submits each charge.

## JSON-RPC

`POST /rpc` with a single object or an array (batch). Notifications omit `id` and
return `204`. Params is always an object.

| Method | Params | Result |
| --- | --- | --- |
| `arne_status` | — | `{network, chain, height, state_root, mempool, halted}` |
| `arne_chainId` | — | Same as `arne_status` (compatibility alias) |
| `arne_blockNumber` | — | height |
| `arne_getBlockByNumber` | `{height}` | block |
| `arne_getBalance` | `{address, asset}` | base units |
| `arne_getAccount` | `{address}` | `{address, nonce, balances, exists}` |
| `arne_listAssets` | — | asset ID array |
| `arne_sendRawTransaction` | `{tx}` base64 or hex | transaction hash |
| `arne_getTransactionReceipt` | `{hash}` | receipt |
| `arne_getProof` | `{key}` | proof |

RPC error codes: `-32700` parse, `-32600` invalid request, `-32601` method not
found, `-32602` invalid params, `-32000` ledger rejection (with `data` = ledger
code), `-32004` block/transaction not found, `-32005` block pruned (message
includes the oldest available height), `-32603` internal.

## Error codes

Branch on `code`.

| Code | Meaning | HTTP |
| --- | --- | --- |
| `INVALID_SCHEMA` | Malformed envelope, bad version, expiry too far future | 400 |
| `INVALID_PAYLOAD` | Missing/wrong-typed payload field, bad address | 400 |
| `BAD_SIGNATURE` | Signature or public key does not verify | 400 |
| `WRONG_NETWORK` | Transaction or address for another network/chain | 400 |
| `EXPIRED` | Past `expires_at` | 400 |
| `BAD_NONCE` | Nonce is not exactly the account nonce | 400 |
| `MEMPOOL_CONFLICT` | In-process mempool already has an equal/higher-priority transaction at this signer/nonce | 409 |
| `IDEMPOTENCY_CONFLICT` | Key reused with different signed bytes in the same scope | 409 |
| `IDEMPOTENCY_FULL` | No free or expired idempotency cache slot | 503 |
| `FEE_TOO_LOW` | Required fee above `max_fee` | 400 |
| `NO_FEE_ROUTE` | No route or empty `fee_swap` pool for the fee asset | 400 |
| `INSUFFICIENT_FUNDS` | Balance too low, including pool underfunding | 400 |
| `UNKNOWN_ASSET` | Asset not registered | 404 |
| `ASSET_EXISTS` | Asset ID already registered | 400 |
| `INTENT_NOT_FOUND` | Intent does not exist | 404 for GET; 400 for transfer submission |
| `INTENT_PAID` | Intent already paid; query it for the original transaction hash | 400 |
| `INTENT_EXPIRED` | Block time is past the intent expiry | 400 |
| `INTENT_EXISTS` | Payee/reference already registered | 400 |
| `INTENT_MISMATCH` | Recipient, asset, or amount differs from the intent | 400 |
| `REFUND_NOT_FOUND` | `refund_of` does not name an indexed transfer | 404 |
| `REFUND_MISMATCH` | Wrong payer/asset destination, or refunding a refund | 400 |
| `REFUND_EXCEEDED` | Refund is larger than the remaining original amount | 400 |
| `SUBSCRIPTION_NOT_FOUND` | Subscription does not exist | 404 |
| `SUBSCRIPTION_EXISTS` | Derived subscription ID already exists | 400 |
| `SUBSCRIPTION_CLOSED` | Authorization is already cancelled | 400 |
| `SUBSCRIPTION_EXPIRED` | Block time is past the authorization expiry | 400 |
| `SUBSCRIPTION_LIMIT` | Charge exceeds the per-period or total cap | 400 |
| `UNAUTHORIZED` | Wrong issuer/arbiter/owner for the action | 403 |
| `SUPPLY_LIMIT` | Fixed-supply or cap exceeded | 400 |
| `ESCROW_NOT_FOUND` | Unknown escrow ID | 404 |
| `ESCROW_CLOSED` | Escrow already released/refunded | 400 |
| `ALLOWANCE_EXCEEDED` | Over limit or expired allowance | 400 |
| `LIMIT_EXCEEDED` | Batch too large, priority fee capped, mempool full | 429 / 400 |
| `CONSERVATION_VIOLATION` | Supply invariant broken (node halts) | 400 |
| `INVARIANT_VIOLATION` | Internal/state invariant broken (node halts) | 400 |
| `LAST_OWNER` | Cannot remove or demote the last workspace owner | 409 |
| `AUDIT_SCOPE` | Auditor read outside the asset or window of an active grant | 403 |
| `GRANT_EXISTS` | An identical auditor grant already exists | 409 |

Non-ledger API errors: `INVALID_ARGUMENT` (400), `BODY_TOO_LARGE` (413),
`FORBIDDEN` (403, missing scope or workspace permission), `AUDIT_SCOPE` (403), `RATE_LIMITED` (429),
`NOT_FOUND` (404, disabled faucet or unconfigured workspace),
`TX_NOT_FOUND`/`BLOCK_NOT_FOUND`/`TRANSFER_NOT_FOUND` (404), `PRUNED` (410, block
outside `--keep-blocks`), `UNSUPPORTED` (500).

## Mempool and nonce rules

These rules apply to the in-process node. Under `arned start`, the
`mempool` count in `/v1/status` is CometBFT's, and the app-side check below runs
on every `check_tx`; proposal building keeps the same nonce rule.

- `CheckTx` accepts nonces in `[current, current + 64]` to allow pipelining.
- Block selection only includes the exact next nonce per signer, in order.
- A higher-priority transaction with the same signer and nonce replaces the
  mempool entry.
- A different transaction with equal or lower priority at that nonce is
  rejected with `MEMPOOL_CONFLICT`, not reported as accepted.
- Mempool limit: 10,000 transactions.

## Not implemented

Idempotency for JSON-RPC writes, WebSocket, message/channel APIs, contract
APIs, and shielded APIs. They appear in the OpenSpec change, not in this
server. Streaming is SSE only; there is no WebSocket endpoint.
