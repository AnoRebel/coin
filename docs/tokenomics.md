# Tokenomics

## Assets

- **ARCE (AnoRebelCoin)** is the native utility asset: fees, and later staking
  and governance. It is a capped asset with 8 decimals.
- **ARNE (AnoRebelNetwork)** is the network/protocol name, not a second token.
- Any other asset is registered on-chain with its own decimals, supply policy,
  issuer, and metadata. ARCE never wraps or holds those assets; each asset has
  its own conserved supply.

## Supply

| Parameter | Value |
| --- | --- |
| Max ARCE supply | 1,000,000,000 ARCE |
| Base units | 1e17 (8 decimals) |
| Genesis supply | Sum of genesis allocations + pools; nothing is implicitly minted |
| Tail emission | Bounded: starts at the cap, at most 5% a year, security purposes only |
| Mint authority | No issuer; ordinary mints stop at the cap |

The native asset is created in genesis with `policy: capped` and
`max_supply: native_max_supply`. `mint` rejects it because the native asset has
no issuer and its policy is not mintable. Changing the cap is a constitutional
change and then a new genesis; ordinary governance cannot raise it. That rule
is implemented as the `supply_cap` invariant and applied when a proposal is
submitted.

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

## Tail emission

`internal/economics` (mirrored by `python/arne/economics.py`) implements the
terminal emission policy:

- Ordinary mints stop at the cap. A mint that would pass it is refused with
  `cap`; it is never silently reduced.
- Tail emission starts only once circulating supply has reached the cap.
- The annual rate is governance-set in basis points but hard-bounded at 500 bps
  (5%). A rate above the bound is refused with `rate`.
- Each epoch's mint is checked against the rate:
  `amount × 365 days × 10000 ≤ supply × rate_bps × epoch_seconds`. A mint above
  the annualized allowance is refused with `rate`.
- Only `validator_rewards` and `protocol_security` are accepted purposes; any
  other purpose is refused with `purpose`, so tail emission cannot fund grants
  or marketing.
- Every ordinary mint, tail mint, and burn is appended to the supply journal
  with its amount, purpose, and time.

The node does not call the tail-emission module yet; `native_max_supply` and
the ledger's ordinary mint cap are the live checks.

## Genesis allocation and vesting

A genesis allocation is published in full: beneficiary, category, amount,
cliff, and duration. Categories are `community`, `contributors`, `treasury`,
`validators`, and `liquidity`. Validation refuses a blank beneficiary, an
unknown category, a zero amount, and a total above the cap.

`Vesting` gates one schedule:

- Nothing is transferable before `cliff`.
- Between `cliff` and `cliff + duration`, the unlocked amount grows linearly;
  a duration of zero unlocks the whole schedule at the cliff.
- A transfer above the unlocked amount is refused with `locked`.
- When the schedule has fully elapsed, `Advance` records one `vested` event and
  the remaining units become transferable.

```go
schedule := economics.Schedule{
    Beneficiary: "arce1team", Category: "contributors", Amount: 300,
    Cliff: launch + 365*24*60*60, Duration: 2 * 365 * 24 * 60 * 60,
}
vesting, err := economics.NewVesting(schedule)
err = vesting.Transfer(100, now)   // locked: 0 unlocked before the cliff
vesting.Unlocked(now)              // linear after the cliff
vesting.Advance(now)               // true once, when fully vested
```

The ledger stores genesis balances today; the vesting gate is not wired into
transfer validation yet.

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

### Base-fee market

`internal/economics` (mirrored by `python/arne/economics.py`) implements the
deterministic congestion rule:

```go
market, err := economics.NewFeeMarket(base, min, max, targetUnits, maxChangeBps)
next := market.Next(unitsUsed)          // the base fee for the next block
fee, err := economics.EffectiveFee(next, priority, maxPriority, minFee)
split, err := economics.SplitFee(fee, burnBps, validatorBps)
```

- If a block uses exactly `targetUnits`, the base fee is unchanged.
- Otherwise the fee moves by `|used - target| / target` of the current base fee,
  in the direction of the imbalance.
- One block moves the fee by at most `maxChangeBps` (default 1250, 12.5%), and
  the result is clamped to `[min, max]`. So a completely empty block lowers the
  fee by at most 12.5%, not to the floor in one step.
- `EffectiveFee` adds the priority fee, refuses a priority above
  `max_priority_fee` with `fee`, and applies `min_fee` as a floor.
- `SplitFee` divides the paid fee by basis points. The treasury receives the
  rounding remainder so burn + validators + treasury always equals the fee. A
  split whose basis points exceed 10,000 is refused.

### Fee sponsorship

A sponsorship pays fees for another account from a declared budget:

```go
sponsorship, err := economics.NewSponsorship(
    sponsor, sponsored, "arce", budget, expiresAt,
)
err = sponsorship.Pay(fee, now)         // charged to the sponsor's budget
sponsorship.Remaining(now)
```

- Sponsor, sponsored, and asset are required, and an account cannot sponsor
  itself.
- The budget is a hard cap. An over-budget payment is refused with `budget`; a
  payment at or after `expires_at` is refused with `expired`; a non-positive
  amount with `invalid`.
- The type has no method that moves the sponsored account's funds. A sponsored
  transaction still needs its own signature; sponsorship only covers the fee.

`shortcut:` the fee market, split, and sponsorship are libraries. The ledger
still uses the genesis `base_fee` and its own split parameters, and no CLI or API
creates a sponsorship yet.

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
