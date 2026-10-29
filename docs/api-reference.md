# API reference

Protocol `arne/1`, API `v1`, transaction version `1`.

- Base URL: `http://127.0.0.1:8080`
- Request body limit: 2 MiB
- Content types: `application/json`, `application/cbor`
- Errors: `{"error": {"code": "...", "message": "..."}}`
- Auth: `Authorization: Bearer <key>` (writes only, when keys are configured)

## REST `/v1`

### Health and discovery

| Method | Path | Description |
| --- | --- | --- |
| GET | `/v1/health` | `200 {"status":"ok","height":N}`; `503` with `"status":"halted"` |
| GET | `/v1/version` | `{"protocol":"arne/1","tx_version":1,"api":"v1","cbor":"deterministic","state":"smt-sha256"}` |
| GET | `/v1/status` | Node status |

`/v1/status` response:

```json
{
  "network": "arne:devnet", "chain": "arne-devnet-1",
  "height": 42, "state_root": "…64 hex…", "time": 1793151119,
  "mempool": 0, "devnet": true, "protocol_version": "arne/1"
}
```

`halted` is present only when the node halted, with the reason.

### Assets

| Method | Path | Description |
| --- | --- | --- |
| GET | `/v1/assets` | `{"items":[asset…]}` |
| GET | `/v1/assets/{asset-id}` | One asset, or `404 UNKNOWN_ASSET` |
| GET | `/v1/supply/{asset-id}` | `{"asset":"…","supply":N,"burned":N}` |

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
| GET | `/v1/blocks` | `limit` (default 50), `before` (height) | Newest-first page |
| GET | `/v1/blocks/{height}` | — | One block, or `404 BLOCK_NOT_FOUND` |
| GET | `/v1/transactions/{hash}` | — | Receipt, or `404 TX_NOT_FOUND` |

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

`GET /v1/events?from=<height>` streams `text/event-stream`:

```
id: 7
event: block
data: {"type":"block","height":7,"time":…,"hash":"…","tx_hashes":["…"]}
```

Replay covers the last 1,000 blocks. Python also accepts `from_height`.

### Writes

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| POST | `/v1/transactions` | `write` | Submit canonical CBOR or `{"tx":"base64-or-hex"}`; `202 {"hash":"…","status":"accepted"}` |
| POST | `/v1/transactions/simulate` | — | Dry run; returns a receipt, never commits |
| POST | `/v1/blocks/produce` | `admin` | Produce a block now (devnet/operator use) |
| POST | `/v1/faucet` | `write` | Devnet only; body `{"address":"…"}`; `202 {"hash":"…"}` |

Submitting does not mean success. Poll the receipt or watch events.

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

### Transaction types

| Type | Payload |
| --- | --- |
| `transfer` | `asset`, `to`, `amount`, optional `memo` (≤256 bytes) |
| `batch` | `items`: 1–100 × `{asset, to, amount}` |
| `register_asset` | `asset`: full asset map without supply |
| `create_escrow` | `asset`, `beneficiary`, `arbiter`, `amount`, `timeout` |
| `release_escrow` | `id` |
| `refund_escrow` | `id` |
| `approve_allowance` | `spender`, `asset`, `amount`, optional `expires_at` |
| `transfer_from` | `from`, `to`, `asset`, `amount` |
| `revoke_allowance` | `spender`, `asset` |
| `mint` | `asset`, `to`, `amount` (issuer only, policy permitting) |
| `burn` | `asset`, `amount` (burnable policy only) |

`amount` is always integer base units and must be > 0 where an amount is
required. A batch is atomic: any failing item rolls back the whole transaction.

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
code), `-32004` block/transaction not found, `-32603` internal.

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
| `FEE_TOO_LOW` | Required fee above `max_fee` | 400 |
| `NO_FEE_ROUTE` | No route or empty `fee_swap` pool for the fee asset | 400 |
| `INSUFFICIENT_FUNDS` | Balance too low, including pool underfunding | 400 |
| `UNKNOWN_ASSET` | Asset not registered | 404 |
| `ASSET_EXISTS` | Asset ID already registered | 400 |
| `UNAUTHORIZED` | Wrong issuer/arbiter/owner for the action | 403 |
| `SUPPLY_LIMIT` | Fixed-supply or cap exceeded | 400 |
| `ESCROW_NOT_FOUND` | Unknown escrow ID | 404 |
| `ESCROW_CLOSED` | Escrow already released/refunded | 400 |
| `ALLOWANCE_EXCEEDED` | Over limit or expired allowance | 400 |
| `LIMIT_EXCEEDED` | Batch too large, priority fee capped, mempool full | 429 / 400 |
| `CONSERVATION_VIOLATION` | Supply invariant broken (node halts) | 400 |
| `INVARIANT_VIOLATION` | Internal/state invariant broken (node halts) | 400 |

Non-ledger API errors: `INVALID_ARGUMENT` (400), `BODY_TOO_LARGE` (413),
`FORBIDDEN` (403, missing scope), `RATE_LIMITED` (429), `NOT_FOUND` (404,
disabled faucet), `TX_NOT_FOUND`/`BLOCK_NOT_FOUND` (404), `UNSUPPORTED` (500).

## Mempool and nonce rules

These rules apply to the in-process node. Under `arned start`, the
`mempool` count in `/v1/status` is CometBFT's, and the app-side check below runs
on every `check_tx`; proposal building keeps the same nonce rule.

- `CheckTx` accepts nonces in `[current, current + 64]` to allow pipelining.
- Block selection only includes the exact next nonce per signer, in order.
- A higher-priority transaction with the same signer and nonce replaces the
  mempool entry.
- Mempool limit: 10,000 transactions.

## Not implemented

Idempotency keys, WebSocket, signed webhooks, pagination cursors beyond
`before`/`cursor`, OIDC/JWT, mTLS, message/channel APIs, contract APIs, and
shielded APIs. They appear in the OpenSpec change, not in this server.
