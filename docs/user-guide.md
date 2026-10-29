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
- `--memo` is **public and permanent**. It is capped at 256 bytes. Never put
  secrets or personal data in a memo. Encrypted memos are planned, not shipped.
- The fee defaults to ARCE and is capped at `max_fee` (the CLI uses 10,000 base
  units = 0.0001 ARCE).
- Failed transactions are free and leave no trace in state. Rejected nonce,
  insufficient funds, or a bad signature means nothing was charged.

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

Create one with the example client (works against either node):

```sh
export ARNE_MNEMONIC="abandon abandon ... about"
uv run python examples/python/client.py escrow \
  --beneficiary arce-devnet1... \
  --arbiter arce-devnet1... \
  --amount 50 --timeout 86400
```

Release or refund:

```sh
uv run python examples/python/client.py release --id esc_...
```

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

`expires_at` is checked against the last block time, not wall clock. An
allowance is reduced by each spend and can never exceed its limit; over-spending
returns `ALLOWANCE_EXCEEDED` and changes nothing.

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

A single transaction can contain up to 100 transfers. All items succeed or the
whole batch rolls back. Payroll and scheduled runs are planned; today a batch
is submitted as one signed `batch` transaction through the API.

## 11. Recovery and key safety

- Restore with the mnemonic and the same network ID and account index.
- The same mnemonic produces different addresses on mainnet and devnet; this is
  intentional, not a bug.
- Keys live only where you sign. The node never needs your mnemonic.
- Passkeys, social recovery, and hardware-signer workflows are specified but not
  implemented.

## 12. What to do when something fails

- `BAD_NONCE`: another transaction from the same account was confirmed first.
  Re-read the account nonce and re-sign.
- `INSUFFICIENT_FUNDS`: the amount plus fee exceeds the balance.
- `EXPIRED`: the transaction's `expires_at` passed before inclusion.
- `NO_FEE_ROUTE`: the chosen fee asset has no rate or the swap pool is empty.
- `UNKNOWN_ASSET`: wrong ID or wrong network suffix.
- `WRONG_NETWORK`: address or transaction built for another network.

Full error table: [api-reference.md](api-reference.md#error-codes).
