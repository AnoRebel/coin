# Tokenomics

## Assets

- **ARCE (AnorebelCoin)** is the native utility asset: fees, and later staking
  and governance. It is a capped asset with 8 decimals.
- **ARNE (AnorebelNetwork)** is the network/protocol name, not a second token.
- Any other asset is registered on-chain with its own decimals, supply policy,
  issuer, and metadata. ARCE never wraps or holds those assets; each asset has
  its own conserved supply.

## Supply

| Parameter | Value |
| --- | --- |
| Max ARCE supply | 1,000,000,000 ARCE |
| Base units | 1e17 (8 decimals) |
| Genesis supply | Sum of genesis allocations + pools; nothing is implicitly minted |
| Tail emission | **Not implemented**; the current policy is capped |
| Mint authority | No issuer; ARCE cannot be minted after genesis |

The native asset is created in genesis with `policy: capped` and
`max_supply: native_max_supply`. `mint` rejects it because the native asset has
no issuer and its policy is not mintable. Changing the cap requires a genesis
change and a new network; governance for that is specified, not shipped.

Devnet genesis (for reference only, not a mainnet allocation):

| Account | ARCE |
| --- | --- |
| Funder | 1,000 |
| Faucet | 10,000 |
| `fee_swap` reserve | 1,000,000 |
| **Total** | **1,011,000** |

Verify on a running node:

```sh
curl -s http://127.0.0.1:8080/v1/supply/arce:devnet
curl -s http://127.0.0.1:8080/v1/assets/arce:devnet
```

`supply = balances + escrow_held + burned` for every asset, checked after each
transaction and block. A violation halts the node.

## Fees

Default parameters:

| Parameter | Value |
| --- | --- |
| `base_fee` | 1,000 base units (0.00001 ARCE) |
| `min_fee` | 1,000 |
| `max_priority_fee` | 1e12 |
| Split | 50% burn, 30% validators, 20% treasury |

```
fee = max(base_fee, min_fee) + priority_fee
```

The fee must be ≤ the signed `max_fee`, otherwise `FEE_TOO_LOW`. A payment in
another asset must also respect the optional `max_arce_fee` payload field (ARCE
base units), which bounds the converted ARCE fee and fails with
`LIMIT_EXCEEDED`; a native fee is capped directly by it. Failed
transactions are free and fully rolled back. That is a deliberate v1 shortcut:
it avoids charging for bugs but leaves a spam vector until fee-on-failure lands.

Split mechanics:

- **Burn** increases `burned/<asset>` and reduces circulating supply.
- **Validators** and **treasury** credit named pools (`pool/validators/<asset>`,
  `pool/treasury/<asset>`). Pools are accounting buckets; there is no withdrawal
  or budget-spend transaction yet.
- **Faucet** and **community** pools exist in the schema and devnet genesis but
  have no consensus spending path besides the devnet faucet key.

There is no fee market adjustment algorithm yet; `base_fee` is a genesis
parameter. Congestion-based base-fee updates are specified.

## Paying fees in other assets

An asset can pay fees only if:

1. it is registered on the network, and
2. a `route/<asset>` rate is set (ARCE base units per 1e8 units of that asset),
   and
3. the `fee_swap` pool holds enough ARCE.

The payer's fee moves to the `treasury` pool in the foreign asset; the
`fee_swap` pool pays the ARCE equivalent, which is burned/validated/treasured
normally. No route or an underfunded pool → `NO_FEE_ROUTE`, and the transaction
rolls back with no charge.

Routes are an operator/governance action today (no CLI). A route is a price and
carries risk: an incorrect rate over- or under-charges, and the pool can be
drained. Treat route setting as privileged.

## Staking, governance, and treasury

Specified but not implemented:

- validator bonding, delegation, epochs, rewards, jailing, slashing;
- proposal deposits, stake-weighted voting, timelocks, upgrades;
- treasury budgets and outflows;
- tail emission and vesting.

No rewards accrue today, and the validator/treasury pools are not claimable.
Do not market yield, staking, or governance rights for this implementation.

## Risks

- Early, unaudited software: loss of funds from bugs is possible.
- Single-producer devnet: no liveness or finality guarantee beyond one process.
- Route mispricing can drain the fee-swap reserve.
- Frozen parameters: changing fees or the cap needs a coordinated new network.
- Regulatory treatment of ARCE is unresolved and jurisdiction-dependent.

This document is descriptive, not an offer, solicitation, or financial advice.
