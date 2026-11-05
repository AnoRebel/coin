# Embedding guide

Three ways to use ARNE from another system, from loosest to tightest coupling.

## 1. Run a node, use the API (recommended)

This is the supported production shape. Your service signs transactions and talks
HTTP; the node owns state and persistence.

```sh
arned devnet --listen 127.0.0.1:8080 --data-dir /var/lib/arne
```

Go client (`github.com/anorebel/anorebelcoin/sdk/go/arne`):

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	arne "github.com/anorebel/anorebelcoin/sdk/go/arne"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := arne.New("http://127.0.0.1:8080")
	status, err := client.Status(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(status.Chain, status.Height, status.StateRoot)
}
```

The client exposes reads for `Status`, `Account`, `Assets`, `Asset`, `Supply`,
`Proof`, `Escrow`, `Intent`, `Subscription`, `Transfer`, `AllowanceInfo`,
`Block`, `Blocks`, `Simulate`, `Receipt`, `WaitReceipt`, `Faucet`, and `RPC`.
`Wallet` adds signing: `NewWallet`/`WalletFromMnemonic`, `Build`, `BuildNonce`,
`Send`, `SendAndWait`, and `Simulate`. Payload builders cover transfers
(`Payment`), escrow, allowances, assets (`AssetRegister`, `Mint`, `Burn`),
invoices (`IntentCreate`), subscriptions, and batches. A node rejection is an
`*arne.Error`; use `arne.IsCode(err, "ESCROW_NOT_FOUND")` or `arne.AsError` to
branch on the stable code.

```go
payer, mnemonic, _ := arne.NewWallet("arne:devnet") // store the mnemonic
payee, _, _ := arne.NewWallet("arne:devnet")

// Devnet only: fund the payer, then wait for inclusion.
hash, _ := client.Faucet(ctx, payer.Address)
if _, err := client.WaitReceipt(ctx, hash); err != nil {
	log.Fatal(err)
}

payment := arne.Payment{To: payee.Address, Asset: "arce:devnet", Amount: 1_250_000_000}
hash, err = payer.Send(ctx, client, arne.TxTransfer, payment.Payload())
if err != nil {
	log.Fatal(err)
}
receipt, err := client.WaitReceipt(ctx, hash)
if err != nil || !receipt.Success {
	log.Fatalf("transfer failed: %+v %v", receipt, err)
}
```

Other transaction types use the same `Send` call with a payload struct:

```go
// Invoice: signed by the payee.
hash, _ = payee.Send(ctx, client, arne.TxCreateIntent, arne.IntentCreate{
	Reference: "invoice-1001", Asset: "arce:devnet", Amount: 500,
}.Payload())

// Pay it; payee, asset, and amount must match the intent.
_, _ = payer.Send(ctx, client, arne.TxTransfer, arne.Payment{
	To: payee.Address, Asset: "arce:devnet", Amount: 500, Intent: intentID,
}.Payload())

// Escrow and subscriptions follow the same pattern.
_, _ = payer.Send(ctx, client, arne.TxCreateEscrow, arne.EscrowCreate{
	Beneficiary: payee.Address, Arbiter: payee.Address, Asset: "arce:devnet",
	Amount: 1_000, Timeout: uint64(time.Now().Unix()) + 3600,
}.Payload())
_, _ = payer.Send(ctx, client, arne.TxCreateSub, arne.SubscriptionCreate{
	Payee: payee.Address, Asset: "arce:devnet",
	AmountPerPeriod: 100, Period: 30, TotalCap: 1_000,
}.Payload())
```

`BuildNonce` skips the account lookup for offline signing or when queueing
several transactions from one account. The SDK re-exports `Tx`, `KeyPair`,
`Asset`, `Receipt`, `Escrow`, `Intent`, `Subscription`, `Transfer`, key
derivation, canonical encoding, and `FormatAmount`, so callers do not
reimplement the wire format.

Message channels and governance transactions are not exposed yet: the node has
no message or governance endpoints, so the SDK does not pretend otherwise.


Install from another module:

```
require github.com/anorebel/anorebelcoin v0.0.0
replace github.com/anorebel/anorebelcoin => ../anorebelcoin
```

(A published tag replaces the `replace` line once releases exist.)

Python client (`arne.sdk`) mirrors the Go surface: reads, `Wallet`, the same
payloads, and `ApiError` with `is_code` for stable codes.

```python
from arne import types
from arne.sdk import Client, Payment, Wallet

payer, mnemonic = Wallet.new("arne:devnet")  # store the mnemonic
with Client("http://127.0.0.1:8080", api_key="…") as client:
    client.faucet(payer.address)  # devnet only
    payload = Payment(to=destination, asset="arce:devnet", amount=1_250_000_000)
    tx_hash = payer.send(client, types.TX_TRANSFER, payload.to_payload())
    receipt = client.wait_receipt(tx_hash)
    assert receipt["success"]
```

See `examples/python/client.py` for a standalone script.

## 2. Embed the Python node in-process

Useful for tests, simulations, and single-tenant services. The node and API are
plain objects; no subprocess required.

```python
import time
import uvicorn

from arne import ledger, types
from arne.api import ApiConfig, create_app
from arne.keys import keypair_from_mnemonic
from arne.node import Config, MemoryStore, Node, devnet_genesis

MNEMONIC = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
network = "arne:devnet"
funder = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 0, network)
faucet = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 1, network)

config = Config(
    network=network, chain=types.chain_id(network),
    params=ledger.default_params(), proposer=funder.address,
    faucet=faucet, devnet=True,
)
node = Node.open(config, devnet_genesis(network, int(time.time()), funder, faucet), MemoryStore())

app = create_app(node, ApiConfig(devnet=True))
uvicorn.run(app, host="127.0.0.1", port=8080)
```

Swap `MemoryStore()` for `FileStore("/var/lib/arne")` to persist. You can call
`node.produce_block()`, `node.submit_tx(raw)`, and `node.simulate(raw)` directly
in tests.

## 3. Use the ledger as a library

Python, no network, no HTTP:

```python
from arne import ledger, types
from arne.keys import keypair_from_mnemonic, new_keypair

network = "arne:devnet"
alice = keypair_from_mnemonic(
    "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
    "", 0, 0, 0, network,
)
bob, _ = new_keypair(network)
native = types.native_asset(network)

state = ledger.new_genesis(ledger.Genesis(
    network=network, chain=types.chain_id(network), time=1_700_000_000,
    params=ledger.default_params(),
    allocations=[ledger.Allocation(alice.address, native, 1_000_000_000)],
))
now = 1_700_000_100
tx = types.Tx(
    version=1, network=network, chain=types.chain_id(network), tx_type=types.TX_TRANSFER,
    signer=alice.address, nonce=0, created_at=now, expires_at=now + 3600,
    fee_asset=native, max_fee=10_000,
    payload={"asset": native, "to": bob.address, "amount": 250_000_000},
)
tx.sign(alice)
receipt = state.apply_tx(tx, now, 1)
assert receipt.success, receipt.error
assert state.balance(bob.address, native) == 250_000_000
assert state.root() == state.root()
```

Go: the same code lives in `internal/ledger` and `internal/smt`. Go's `internal/`
rule means external modules cannot import it directly; fork the module, vendor
it, or use the SDK client. That restriction is deliberate: consensus code should
not become a casual dependency.

## 4. Mempool and block production

Embedding code that wants to produce blocks:

```go
n, err := node.New(cfg, genesis, store)
hash, err := n.SubmitTx(rawCBOR)
block, err := n.ProduceBlock(uint64(time.Now().Unix()))
```

`ProduceBlock` selects only executable transactions in nonce order, applies them
atomically, writes the block and state snapshot, and broadcasts an event. A
conservation or invariant error halts the node and is returned; do not ignore it.

## 5. What not to embed

- Do not embed a private key in a node process. Sign outside.
- Do not run two writers against one data directory. Pebble takes an exclusive
  lock; the file store does not, so use one process per Python data directory.
- Do not treat the devnet faucet as a wallet service.

See [operator-guide.md](operator-guide.md) for deployment and
[integration-guide.md](integration-guide.md) for the HTTP contract.
