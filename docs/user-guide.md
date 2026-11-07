# User guide

This guide uses a devnet. Nothing here is audited or suitable for real money.

All commands exist in both nodes:

- Go: `go run ./cmd/arned ...` (or `./bin/arned` after `make build`)
- Python: `uv run arne ...`

Examples show the Go form; replace with `uv run arne` for Python. The Python CLI
adds `--wait` to `faucet`, and `proof --key` for arbitrary state keys.

## 1. Create a wallet

```sh
go run ./cmd/arned wallet new --network arne:devnet --json
```

Output:

```json
{
  "mnemonic": "abandon abandon ... about",
  "address": "arce-devnet1qxqew...",
  "network": "arne:devnet"
}
```

Write the mnemonic down. It is the only backup; the project cannot recover it.
Addresses are network-scoped: a `arce1...` mainnet address is rejected on
devnet, which prevents cross-network loss.

Derive more accounts from the same mnemonic (BIP-44 path
`m/44'/9999'/0'/0/<index>`):

```sh
go run ./cmd/arned wallet address --mnemonic "..." --index 1
```

## 2. Get devnet funds

```sh
go run ./cmd/arned faucet --address arce-devnet1...
```

The faucet queues a transfer and prints the transaction hash. On the Python
node add `--wait` to block until it is confirmed. Otherwise wait for the next
block (about two seconds) and check the balance.

The faucet sends 100 ARCE per request and only works on devnet. It refuses once
its own balance runs low.

## 3. Check a balance

```sh
go run ./cmd/arned balance --address arce-devnet1... --asset arce:devnet
go run ./cmd/arned balance --mnemonic "..."            # all assets, JSON with --json
```

Amounts are stored as integer base units (8 decimals for ARCE, per-asset
otherwise). The CLI converts to human units. The API always returns base units.

## 4. Send a payment

```sh
go run ./cmd/arned pay \
  --mnemonic "..." \
  --to arce-devnet1... \
  --asset arce:devnet \
  --amount 12.5 \
  --memo "invoice 42"
```

- `--amount` accepts decimals up to the asset's precision.
- `--memo` alone is **public and permanent**. It is capped at 256 bytes. Use
  `--memo-to` to encrypt it (see below) before putting anything sensitive in it.
- The fee defaults to ARCE and is capped at `max_fee` (the CLI uses 10,000 base
  units = 0.0001 ARCE).
- Failed transactions are free and leave no trace in state. Rejected nonce,
  insufficient funds, or a bad signature means nothing was charged.

### Encrypted memos (opt-in)

A memo can be encrypted to a recipient's memo key. The recipient publishes that
key once; senders do not need it on chain:

```sh
arned memo pub --mnemonic "<recipient mnemonic>"        # share this hex key
arned pay --mnemonic "<sender>" --to arce-devnet1... \
  --asset arce:devnet --amount 12.5 \
  --memo "invoice 42" --memo-to <recipient memo key>
```

Seal and open without sending a payment:

```sh
arned memo seal --recipient <recipient memo key> --text "invoice 42"
arned memo open --mnemonic "<recipient mnemonic>" --sealed <hex or base64>
```

The memo is sealed with an ephemeral X25519 key and ChaCha20-Poly1305; only the
recipient's wallet seed derives the matching key. The chain stores the
ciphertext in `memo_enc` (bytes) and never sees the plaintext. Limits and
caveats:

- Plaintext is capped at 256 bytes; the sealed envelope is validated for shape
  and size before it is accepted. A transaction may carry `memo` or `memo_enc`,
  never both.
- Amount, asset, sender, recipient, and time remain public. Encryption hides
  the memo text only.
- Losing the wallet seed loses the memo; there is no recovery.
- `--memo` without `--memo-to` prints a public-memo warning and stores the text
  in clear.

### Payment URIs

Wallets can exchange a single `arce:` or `arne:` URI instead of separate fields:

```sh
arned uri build --to arce-devnet1... --asset arce:devnet --amount 12.5 \
  --memo "invoice 42" --expires 1700003600
arned uri parse --uri 'arce:arce-devnet1...?amount=12.5&asset=arce%3Adevnet&network=arne%3Adevnet'
arned pay --uri '<uri>' --yes
```

Parsing is strict: only `network`, `asset`, `amount`, `memo`, `expires`, and `intent` are
accepted, each at most once. The scheme must be lowercase `arce` or `arne`, the
recipient must be valid for the URI's network, the asset must be a canonical
asset ID, and the amount must be a plain positive decimal with no exponent or
leading zeros. Expired URIs and fragments are rejected.

`pay --uri` prints the network, recipient, asset, amount, memo, and expiry, then
stops unless `--yes` is also passed. Conflicting explicit `--amount`, `--to`,
`--asset`, or `--network` values are refused rather than guessing which value
you meant. Memos from URIs are shown as inert text; markup such as `<script>` is
warning-tagged and never executed. `--memo-to` still applies if you want to seal
the memo.

An optional `intent` is a 32-character lowercase hex payment-intent ID. Both
CLIs resolve it on the declared network, compare the link's recipient, asset,
and amount with the stored request, and preserve the ID in the signed transfer.
You can also pass `--intent <id>` to `pay`, `preview`, or `uri build`; an explicit
ID conflicting with the URI is rejected.

### Payment intents

Create a request as the payee, then share its link or PNG QR code:

```sh
arned intent create --mnemonic "<payee mnemonic>" --asset arce:devnet \
  --amount 12.5 --reference invoice-42 --memo "invoice 42" --wait
# Copy the printed intent ID into these commands:
arned intent show --id <intent-id>
arned intent link --id <intent-id> --qr invoice-42.png
```

`intent create` charges the usual transaction fee. References must contain
1–64 ASCII letters, digits, `_`, or `-` and cannot be reused by that payee on
the same network. Add `--expires <unix-seconds>` for an expiry; omit it for no
expiry. The optional memo is public, at most 256 UTF-8 bytes, and cannot contain
NUL, carriage returns, or newlines.
Request expiry is separate from transaction validity: the wallet caps a
payment's signed transaction at one hour (or the earlier request/link expiry),
so a long-lived request remains payable without signing a long-lived transfer.

Pay without manually copying the request's amount and recipient:

```sh
arned preview --mnemonic "<payer mnemonic>" --intent <intent-id> --json
arned pay --mnemonic "<payer mnemonic>" --intent <intent-id> --yes --wait
arned pay --mnemonic "<payer mnemonic>" --uri '<payment-link>' --yes --wait
```

The wallet checks that the API serves the declared network and shows the
resolved request before confirmation. Explicit recipient, asset, or amount
values must agree with the request. The ledger independently enforces expiry
and exact matching, records the payment transaction hash, and rejects a second
payment without moving value. `intent show` includes that hash after payment.
Simulation and failed payments leave the request open.

Python provides the same commands through `uv run arne`; use `--url` or
`ARNE_API` to select its node. Both implementations generate the same canonical
link and encode it locally in the QR image. `--qr` refuses to overwrite an
existing file. No mnemonic or payment link is sent to a QR-generation service.

Integrations can use `Idempotency-Key` on transaction submission to retrieve
the original result when retrying the **same signed bytes**. This is not a way
to rerun a wallet command with a new nonce or timestamp; see the
[integration guide](integration-guide.md#3-idempotency-and-retries) for cache
retention and restart limits.

### Refunds

A refund is a new `transfer` signed by the original recipient and linked to
the original transaction hash. History stays immutable: the original payment
and its transfer record are never edited.

```sh
# Original recipient refunds part or all of a payment.
arned pay --mnemonic "<original recipient mnemonic>" \
  --refund-of <original-tx-hash> --amount 5 --wait
# Omit --amount to return everything still refundable.
arned preview --mnemonic "<original recipient mnemonic>" --refund-of <hash>
```

The wallet reads `GET /v1/transfers/{hash}` and fills the destination with the
original payer, the original asset, and the remaining refundable amount.
Explicit `--to`, `--asset`, or `--amount` values must agree with that record.

The ledger enforces the same rules independently:

- only the original recipient may sign the refund;
- value returns to the original payer, in the original asset;
- the sum of refunds may not exceed the original amount;
- a refund of a refund is rejected;
- `intent` and `refund_of` cannot be combined in one transfer.

Refunds pay the usual transaction fee from the refund signer's balance. An
original recipient who holds only the received amount cannot return the last
units until the wallet has a small fee balance; fund it first or refund
slightly less.

Refunds are ordinary transfers for fees, nonces, and receipts. Each successful
refund emits a `refund` event carrying `refund_of`, `from`, `to`, `asset`,
`amount`, and the `remaining` refundable amount. Batch items and escrow
releases are not indexed for `refund_of`; refund those only through the batch
or escrow flow that produced them.

### Subscriptions

A subscription is a bounded, on-chain authorization for recurring charges. The
payer signs it; the payee charges against it; the payer cancels it.

```sh
# Payer: allow up to 5 ARCE per 30 days and 60 ARCE in total.
arned subscription create --mnemonic "<payer mnemonic>" \
  --payee <payee-address> --asset arce:devnet \
  --amount-per-period 5 --period 2592000 --total-cap 60 --wait

# Payee: charge. Omit --amount to take what this period still allows.
arned subscription charge --mnemonic "<payee mnemonic>" \
  --id <subscription-id> --wait

# Anyone can inspect; only the payer can cancel.
arned subscription show --id <subscription-id>
arned subscription cancel --mnemonic "<payer mnemonic>" \
  --id <subscription-id> --wait
```

- One period is `period` seconds. Reaching the next period resets the
  per-period counter; unused periods do not accumulate.
- No charge may exceed `amount_per_period` in its period or `total_cap` over
  the authorization.
- `--expires <unix-time>` optionally stops charges after that block time.
- Cancellation is immediate and permanent; later charges are rejected.
- Only the named payee may charge, and only the payer may cancel.
- The payee pays the transaction fee; the payer pays the charged amount.
- Nothing charges automatically. The payee or a payroll job submits each charge.

`arne` accepts the same commands. Both CLIs print the subscription ID at
creation; it is derived from the payer, payee, asset, and creation nonce.

### Preview before signing

`preview` signs a transfer, dry-runs it against current state, and prints what
would happen. It never submits the transaction:

```sh
arned preview --mnemonic "..." --to arce-devnet1... \
  --asset arce:devnet --amount 12.5 --json
arned preview --mnemonic "..." --uri '<uri>' \
  --known arce-devnet1trustedaddress --json
```

The preview reports the resolved network, signer, recipient, asset, amount, fee,
and memo, plus warnings:

- the recipient is your own address;
- the recipient resembles a `--known` contact (shared prefix and suffix) without
  being exactly that contact — verify the full address;
- the asset is not registered on the network;
- the memo is public, so use `--memo-to` for sensitive text;
- the simulated transaction would fail (for example `INSUFFICIENT_FUNDS`).

`ok` is false when the transfer could not execute, but the preview itself still
succeeds. Scripts should check `ok` and `warnings` in `--json` output. Fee is the
simulated fee; failed transactions are free in v1.

Track the receipt:

```sh
go run ./cmd/arned status --json          # height, mempool, state root
curl -s http://127.0.0.1:8080/v1/transactions/<hash>
```

## 5. Pay fees in another asset

A registered asset can pay fees only when the operator has set a
`route/<asset>` rate (ARCE base units per 1e8 units of that asset) and funded
the `fee_swap` pool. Otherwise the node returns `NO_FEE_ROUTE`; nothing is
charged because the whole transaction rolls back.

The rate is a governance/operator action today; there is no CLI command for it.
It will be a governed transaction in a later change.

## 6. Register your own asset

```sh
go run ./cmd/arned asset register \
  --mnemonic "..." \
  --id eur:devnet --symbol EUR --name "Demo Euro" \
  --decimals 2 --policy capped --max-supply 1000000
```

Policies:

| Policy | Meaning |
| --- | --- |
| `fixed` | Minted once at genesis/registration; no further minting |
| `capped` | Issuer may mint up to `max_supply` |
| `mintable` | Issuer may mint without a cap |
| `burnable` | Holders may burn their own balance |

Classes: `fiat`, `crypto`, `stablecoin`, `commodity`, `security`, `point`, or
`other`. Optional standards metadata is carried in the asset map:

| Key | Standard | Shape |
| --- | --- | --- |
| `iso4217` | Currency code | three uppercase letters, e.g. `TZS` |
| `iso24165` | Digital token identifier | 9–32 uppercase alphanumerics |
| `iso17442` | Legal entity identifier | 20 uppercase alphanumerics |
| `ucum` | Unit code | UCUM characters, e.g. `kg` |

Malformed known keys and unknown classes are rejected with `INVALID_PAYLOAD`.
The identifier, class, decimals, and issuer are fixed at registration; a second
registration of the same ID fails with `ASSET_EXISTS`, so precision can never
silently change. Unknown metadata keys are stored as opaque strings.

Only the issuer recorded at registration may mint. `burn` requires the asset
policy to allow it. Listing:

```sh
go run ./cmd/arned asset list
```

The Python CLI derives the asset ID from `--symbol` and the network:
`uv run arne asset register --symbol EUR --name "Demo Euro" ...`.

## 7. Escrow

An escrow locks funds until released to the beneficiary, refunded, or expired.
The ID is deterministic:
`esc_` plus the first 16 bytes of the SHA-256 of the canonical escrow fields.

Create one with the CLI. `--timeout` is a Unix timestamp, and `--arbiter`
defaults to the payer:

```sh
arned escrow create --mnemonic "$MNEMONIC" \
  --beneficiary arce-devnet1... --arbiter arce-devnet1... \
  --asset arce:devnet --amount 50 \
  --timeout $(( $(date +%s) + 86400 )) --wait
```

Inspect, release, or refund:

```sh
arned escrow show --id esc_...
arned escrow release --mnemonic "$MNEMONIC" --id esc_... --wait
arned escrow refund  --mnemonic "$MNEMONIC" --id esc_... --wait
```

The Python CLI has the same four commands
(`uv run arne escrow ...`). Add `--wait` to print the receipt; without it the
command prints only the transaction hash.

Rules:

- The amount leaves the payer's spendable balance immediately.
- The payer may release to the beneficiary at any time, but may only refund
  after `timeout`.
- The beneficiary may release after `timeout`.
- The arbiter may release or refund at any time.
- Anyone else, or any of these actions before its allowed time, gets
  `UNAUTHORIZED` and no state change.
- The closer must hold ARCE for the transaction fee. With no ARCE, the attempt
  fails with `INSUFFICIENT_FUNDS` before the escrow itself is examined.
- Closing twice fails with `ESCROW_CLOSED`.
- Expiry does not move funds by itself; after `timeout` the beneficiary or payer
  submits the release or refund. Automatic sweeping is planned.

## 8. Allowances (delegated spending)

Allow a spender to move up to a limit of your funds:

1. Submit `approve_allowance` with `spender`, `asset`, `amount`, `expires_at`.
2. The spender submits `transfer_from` with `from` (owner), `to`, `asset`,
   `amount`.
3. Revoke with `revoke_allowance`.

`expires_at` is checked against the time of the block carrying the spend, not
wall clock. An allowance is reduced by each spend and can never exceed its
limit; over-spending or spending after expiry returns `ALLOWANCE_EXCEEDED` and
changes nothing. Revocation zeroes the remaining allowance immediately.

There is no CLI command for allowances yet; use the HTTP API with a signed
transaction (see the integration guide) or the example client as a template.

## 9. Verify a payment with a state proof

Every balance and account field is a leaf in the state tree. The proof binds a
key and value to the state root in `/v1/status`.

```sh
go run ./cmd/arned proof --address arce-devnet1... --asset arce:devnet
```

Python node (any key):

```sh
uv run arne proof --key "balance/arce-devnet1.../arce:devnet"
```

To verify independently, fetch `/v1/status`, take `state_root`, and verify the
siblings with `arne.smt` (Python) or `internal/smt` (Go). A proof with
`"included": false` proves the key is absent at that root.

## 10. Batch payments

A single transaction can contain up to 100 transfers, submitted as one signed
`batch` through the API.

- `mode: "atomic"` (default): any failing item rolls back the whole batch, so
  nothing moves.
- `mode: "best_effort"`: each item is validated and applied on its own. Failed
  items do not block the rest, and the receipt carries one `batch_item` event per
  item with `index`, `success`, and, on failure, `code`.

Best-effort batches are the payroll primitive: build the recipient list, submit
once, then reconcile the `batch_item` events against the run. The fee is charged
once for the transaction. Scheduled/recurring runs are still planned.

## 11. Workspace roles and audit

A node started with `--workspace <file>` enforces team roles on payments and
payment records. The file maps API keys to member IDs and member IDs to roles:

```json
{
  "members": {"alice": "owner", "bob": "admin", "carol": "member", "dave": "auditor"},
  "principals": {"key-alice": "alice", "key-bob": "bob"}
}
```

Every request then needs its key. Members can pay, auditors can only read,
admins manage members below owner, and owners manage everyone:

```sh
curl -H "Authorization: Bearer key-carol" http://127.0.0.1:8080/v1/workspace
curl -H "Authorization: Bearer key-dave"  http://127.0.0.1:8080/v1/workspace/audit
curl -X PUT -H "Authorization: Bearer key-alice" \
  -d '{"role":"admin"}' http://127.0.0.1:8080/v1/workspace/members/bob
```

Privileged attempts, including denials, are appended to
`<workspace-file>.audit.jsonl` with actor, action, target, time, and result. The
last owner cannot be removed or demoted.

Auditors do not see payment records by default. An owner grants an asset and a
time window:

```sh
curl -X POST -H "Authorization: Bearer key-alice" \
  -d '{"auditor":"dave","assets":["usd:devnet"],"from":1790000000,"until":1798000000,"purpose":"Q1 audit"}' \
  http://127.0.0.1:8080/v1/audit/grants
```

Reads outside that window or asset fail with `AUDIT_SCOPE`, and the attempt is
logged. Access stops at `until` without any manual cleanup.

## 12. Recovery and key safety

- Restore with the mnemonic and the same network ID and account index.
- The same mnemonic produces different addresses on mainnet and devnet; this is
  intentional, not a bug.
- Keys live only where you sign. The node never needs your mnemonic.
- Passkeys and hardware-signer workflows are specified but not implemented.
- Social recovery is a wallet library workflow today: guardians sign a recovery
  request, a threshold of at least two makes it ready, a delay must pass, and
  the owner can cancel before execution. The node does not yet change an
  account key from a recovery, and there is no CLI or REST route.
- Session keys are scoped library grants: an app gets named scopes, allowed
  assets, a cumulative spending limit, and an expiry. A watch-only account can
  read balances and history but cannot sign. Neither is exposed in `arned` yet.
- External signers use one interface: the device displays the canonical
  network, recipient, asset, amount, fee, nonce, and expiry, then signs those
  exact bytes. A device integration is not shipped; the adapter contract is.

## 13. What to do when something fails

- `BAD_NONCE`: another transaction from the same account was confirmed first.
  Re-read the account nonce and re-sign.
- `INSUFFICIENT_FUNDS`: the amount plus fee exceeds the balance.
- `EXPIRED`: the transaction's `expires_at` passed before inclusion.
- `NO_FEE_ROUTE`: the chosen fee asset has no rate or the swap pool is empty.
- `LIMIT_EXCEEDED`: the converted fee exceeds your `max_arce_fee`, or a batch
  or priority fee is above its cap.
- `UNKNOWN_ASSET`: wrong ID or wrong network suffix.
- `WRONG_NETWORK`: address or transaction built for another network.

Full error table: [api-reference.md](api-reference.md#error-codes).

## 14. Team channels (developer preview)

Team channels are not yet available from the `arned` CLI or the REST API. The
message log exists as `internal/channel` in Go and `arne.channel` in Python for
embedding and testing.

What the log guarantees today:

- A sender retries with the same client ID and the message is stored once. The
  retry returns the original message instead of creating a duplicate.
- Members page through messages with a sequence cursor and can catch up after
  being offline. A cursor older than the channel retention window fails with
  `cursor_expired`, so the member restarts from the oldest available message.
- A message can carry text, a payment reference, a system notice, or a file
  pointer. File bytes stay off the channel; only the content hash and a
  retrieval pointer are stored.
- Retention is per channel. Expired messages are no longer returned.

Channels are **not end-to-end encrypted** yet: MLS group state (task 10.6) is
not implemented, and the log is in memory. Do not send sensitive content or
treat channels as durable until that work lands.
