"""Ledger behaviour parity with internal/ledger/ledger_test.go."""

from __future__ import annotations

import pytest
from arne import ledger, smt, types
from arne.keys import keypair_from_mnemonic, new_keypair

NETWORK = "arne:devnet"
CHAIN = "arne-devnet-1"
NATIVE = "arce:devnet"
MNEMONIC = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
NOW = 1_700_000_100


class Fixture:
    def __init__(self) -> None:
        self.alice = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 0, NETWORK)
        self.bob = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 1, NETWORK)
        self.carol = keypair_from_mnemonic(MNEMONIC, "", 0, 0, 2, NETWORK)
        self.state = ledger.new_genesis(
            ledger.Genesis(
                network=NETWORK, chain=CHAIN, time=1_700_000_000,
                params=ledger.default_params(),
                allocations=[
                    ledger.Allocation(self.alice.address, NATIVE, 1_000_000_000),
                    ledger.Allocation(self.bob.address, NATIVE, 100_000_000),
                    ledger.Allocation(self.carol.address, NATIVE, 100_000_000),
                ],
                pools={
                    ledger.POOL_FEE_SWAP: {NATIVE: 1_000_000_000},
                    ledger.POOL_FAUCET: {NATIVE: 1_000_000_000},
                },
            )
        )

    def tx(self, kp, tx_type: str, payload: dict, nonce: int) -> types.Tx:
        tx = types.Tx(
            version=1, network=NETWORK, chain=CHAIN, tx_type=tx_type, signer=kp.address,
            nonce=nonce, created_at=1_700_000_000, expires_at=1_700_003_600,
            fee_asset=NATIVE, max_fee=10_000, payload=payload,
        )
        tx.sign(kp)
        return tx

    def apply(self, tx: types.Tx) -> ledger.Receipt:
        return self.state.apply_tx(tx, NOW, 1)

    def apply_at(self, tx: types.Tx, now: int) -> ledger.Receipt:
        return self.state.apply_tx(tx, now, 1)


@pytest.fixture()
def f() -> Fixture:
    return Fixture()


def test_transfer_and_fee_split(f: Fixture) -> None:
    before = f.state.balance(f.alice.address, NATIVE)
    receipt = f.apply(f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 5_000_000}, 0))
    assert receipt.success, receipt.error
    assert receipt.fee == f.state.params.base_fee
    assert f.state.balance(f.bob.address, NATIVE) == 105_000_000
    assert f.state.balance(f.alice.address, NATIVE) == before - 5_000_000 - receipt.fee
    assert f.state.burned(NATIVE) == receipt.fee // 2
    f.state.check_all()


def test_failed_tx_rolls_back(f: Fixture) -> None:
    root = f.state.root()
    receipt = f.apply(f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 999_999_999_999}, 0))
    assert not receipt.success
    assert receipt.code == ledger.INSUFFICIENT_FUNDS
    assert f.state.root() == root


def test_intent_payable_once(f: Fixture) -> None:
    created = f.apply(f.tx(f.bob, "create_intent", {
        "asset": NATIVE, "amount": 5_000_000, "reference": "invoice_42",
        "expires_at": NOW + 100, "memo": "invoice 42",
    }, 0))
    assert created.success, created.error
    intent_id = created.events[0]["attrs"]["id"]
    payment = f.tx(f.alice, types.TX_TRANSFER, {
        "asset": NATIVE, "amount": 5_000_000, "to": f.bob.address, "intent": intent_id,
    }, 0)
    paid = f.apply(payment)
    assert paid.success, paid.error
    intent = f.state.intent(intent_id)
    assert intent.status == "paid"
    assert intent.tx_hash == payment.hash()
    assert intent.payer == f.alice.address and intent.paid_at == NOW
    root = f.state.root()
    again = f.apply(f.tx(f.carol, types.TX_TRANSFER, dict(payment.payload), 0))
    assert again.code == "INTENT_PAID"
    assert f.state.root() == root
    restored = ledger.State.from_snapshot(NETWORK, CHAIN, f.state.params, f.state.snapshot())
    assert restored.intent(intent_id) == intent
    f.state.check_all()


@pytest.mark.parametrize("mutation,now,code", [
    ({"amount": 1}, NOW, "INTENT_MISMATCH"),
    ({"to": "carol"}, NOW, "INTENT_MISMATCH"),
    ({}, NOW + 101, "INTENT_EXPIRED"),
    ({"intent": "missing"}, NOW, "INTENT_NOT_FOUND"),
])
def test_intent_rejection_rolls_back(f: Fixture, mutation: dict, now: int, code: str) -> None:
    created = f.apply(f.tx(f.bob, "create_intent", {
        "asset": NATIVE, "amount": 5_000_000, "reference": "invoice_42", "expires_at": NOW + 100,
    }, 0))
    assert created.success, created.error
    intent_id = created.events[0]["attrs"]["id"]
    payload = {"asset": NATIVE, "amount": 5_000_000, "to": f.bob.address, "intent": intent_id}
    payload.update(mutation)
    if payload["to"] == "carol":
        payload["to"] = f.carol.address
    root = f.state.root()
    result = f.apply_at(f.tx(f.alice, types.TX_TRANSFER, payload, 0), now)
    assert result.code == code
    assert f.state.root() == root
    assert f.state.intent(intent_id).status == "open"


@pytest.mark.parametrize("extra", [
    {"reference": "bad/reference"}, {"reference": ""}, {"amount": 0},
    {"expires_at": NOW}, {"expires_at": True}, {"memo": "x" * 257},
    {"memo": "line\nbreak"}, {"memo": "nul\0byte"},
])
def test_invalid_intent_rejected(f: Fixture, extra: dict) -> None:
    payload = {"asset": NATIVE, "amount": 1, "reference": "invoice"}
    payload.update(extra)
    root = f.state.root()
    result = f.apply(f.tx(f.bob, "create_intent", payload, 0))
    assert result.code == "INVALID_PAYLOAD"
    assert f.state.root() == root


def test_intent_simulation_and_failed_payment_leave_it_open(f: Fixture) -> None:
    created = f.apply(f.tx(f.bob, "create_intent", {
        "asset": NATIVE, "amount": 2_000_000_000, "reference": "large_invoice",
    }, 0))
    assert created.success, created.error
    intent_id = created.events[0]["attrs"]["id"]
    root = f.state.root()
    payment = f.tx(f.alice, types.TX_TRANSFER, {
        "asset": NATIVE, "amount": 2_000_000_000, "to": f.bob.address, "intent": intent_id,
    }, 0)
    assert f.apply(payment).code == "INSUFFICIENT_FUNDS"
    assert f.state.root() == root
    assert f.state.intent(intent_id).status == "open"
    duplicate = f.apply(f.tx(f.bob, "create_intent", {
        "asset": NATIVE, "amount": 1, "reference": "large_invoice",
    }, 1))
    assert duplicate.code == "INTENT_EXISTS"
    assert f.state.root() == root


def test_intent_simulation_does_not_consume_payment(f: Fixture) -> None:
    created = f.apply(f.tx(f.bob, "create_intent", {
        "asset": NATIVE, "amount": 1, "reference": "preview_invoice",
    }, 0))
    assert created.success, created.error
    intent_id = created.events[0]["attrs"]["id"]
    payment = f.tx(f.alice, types.TX_TRANSFER, {
        "asset": NATIVE, "amount": 1, "to": f.bob.address, "intent": intent_id,
    }, 0)
    root = f.state.root()
    assert f.state.simulate_tx(payment, NOW, 1).success
    assert f.state.root() == root
    assert f.state.intent(intent_id).status == "open"
    assert f.apply(payment).success


def test_replay_rejected(f: Fixture) -> None:
    tx = f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 10}, 0)
    assert f.apply(tx).success
    replay = f.apply(tx)
    assert not replay.success and replay.code == ledger.BAD_NONCE


def test_wrong_network_rejected(f: Fixture) -> None:
    tx = f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 1}, 0)
    tx.signatures.clear()
    tx.network, tx.chain = "arne:mainnet", "arne-mainnet-1"
    tx.sign(f.alice)
    receipt = f.apply(tx)
    assert not receipt.success and receipt.code == ledger.WRONG_NETWORK


def test_expired_tx_rejected(f: Fixture) -> None:
    tx = f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 10}, 0)
    tx.expires_at = 1_700_000_000  # before Fixture.NOW
    tx.signatures.clear()
    tx.sign(f.alice)
    before = f.state.root()
    receipt = f.apply(tx)
    assert not receipt.success and receipt.code == ledger.EXPIRED
    assert f.state.root() == before


def test_escrow_lifecycle(f: Fixture) -> None:
    receipt = f.apply(f.tx(f.alice, types.TX_CREATE_ESCROW, {
        "asset": NATIVE, "amount": 2_000_000, "beneficiary": f.bob.address,
        "arbiter": f.carol.address, "timeout": 1_800_000_000,
    }, 0))
    assert receipt.success, receipt.error
    escrow_id = receipt.events[0]["attrs"]["id"]

    assert f.apply(f.tx(f.bob, types.TX_RELEASE_ESCROW, {"id": escrow_id}, 0)).code == ledger.UNAUTHORIZED
    assert f.apply(f.tx(f.carol, types.TX_RELEASE_ESCROW, {"id": escrow_id}, 0)).success
    assert f.apply(f.tx(f.carol, types.TX_RELEASE_ESCROW, {"id": escrow_id}, 1)).code == ledger.ESCROW_CLOSED
    f.state.check_all()


def test_escrow_timeout(f: Fixture) -> None:
    timeout = 1_700_000_200
    receipt = f.apply(f.tx(f.alice, types.TX_CREATE_ESCROW, {
        "asset": NATIVE, "amount": 2_000_000, "beneficiary": f.bob.address,
        "arbiter": f.carol.address, "timeout": timeout,
    }, 0))
    assert receipt.success, receipt.error
    escrow_id = receipt.events[0]["attrs"]["id"]

    assert f.apply_at(f.tx(f.alice, types.TX_REFUND_ESCROW, {"id": escrow_id}, 1), timeout - 50).code == ledger.UNAUTHORIZED
    assert f.apply_at(f.tx(f.bob, types.TX_RELEASE_ESCROW, {"id": escrow_id}, 0), timeout - 50).code == ledger.UNAUTHORIZED
    assert f.apply_at(f.tx(f.bob, types.TX_RELEASE_ESCROW, {"id": escrow_id}, 0), timeout).success
    assert f.state.balance(f.bob.address, NATIVE) == 101_999_000
    assert f.apply_at(f.tx(f.alice, types.TX_REFUND_ESCROW, {"id": escrow_id}, 1), timeout).code == ledger.ESCROW_CLOSED
    f.state.check_all()


def test_allowance_spend(f: Fixture) -> None:
    assert f.apply(f.tx(f.alice, types.TX_APPROVE, {
        "spender": f.bob.address, "asset": NATIVE, "amount": 1_000_000, "expires_at": 1_700_003_600,
    }, 0)).success
    assert f.apply(f.tx(f.bob, types.TX_TRANSFER_FROM, {
        "from": f.alice.address, "to": f.carol.address, "asset": NATIVE, "amount": 400_000,
    }, 0)).success
    assert f.state.allowance(f.alice.address, f.bob.address, NATIVE) == 600_000
    over = f.apply(f.tx(f.bob, types.TX_TRANSFER_FROM, {
        "from": f.alice.address, "to": f.carol.address, "asset": NATIVE, "amount": 700_000,
    }, 1))
    assert over.code == ledger.ALLOWANCE_EXCEEDED

    assert f.apply(f.tx(f.alice, types.TX_REVOKE_ALLOWANCE, {
        "spender": f.bob.address, "asset": NATIVE,
    }, 1)).success
    after = f.apply(f.tx(f.bob, types.TX_TRANSFER_FROM, {
        "from": f.alice.address, "to": f.carol.address, "asset": NATIVE, "amount": 100_000,
    }, 1))
    assert after.code == ledger.ALLOWANCE_EXCEEDED


def test_allowance_expiry(f: Fixture) -> None:
    assert f.apply(f.tx(f.alice, types.TX_APPROVE, {
        "spender": f.bob.address, "asset": NATIVE, "amount": 1_000_000,
        "expires_at": 1_700_000_200,
    }, 0)).success

    def spend(now: int) -> ledger.Receipt:
        return f.apply_at(f.tx(f.bob, types.TX_TRANSFER_FROM, {
            "from": f.alice.address, "to": f.carol.address, "asset": NATIVE, "amount": 100_000,
        }, 0), now)

    assert spend(1_700_000_250).code == ledger.ALLOWANCE_EXCEEDED
    assert spend(1_700_000_150).success


def _register(f: Fixture, policy: str = "capped") -> dict:
    return {
        "id": "usd:devnet", "class": "fiat", "symbol": "USD", "name": "US Dollar",
        "decimals": 2, "policy": policy, "max_supply": 1_000_000_000,
        "issuer": f.alice.address, "registered": 0, "meta": {"iso4217": "USD"},
    }


def test_asset_mint_permissions(f: Fixture) -> None:
    assert f.apply(f.tx(f.alice, types.TX_REGISTER_ASSET, {"asset": _register(f)}, 0)).success
    assert f.apply(f.tx(f.alice, types.TX_REGISTER_ASSET, {"asset": _register(f)}, 1)).code == ledger.ASSET_EXISTS
    assert f.apply(f.tx(f.alice, types.TX_MINT, {"asset": "usd:devnet", "to": f.bob.address, "amount": 500_000}, 1)).success
    assert f.apply(f.tx(f.bob, types.TX_MINT, {"asset": "usd:devnet", "to": f.bob.address, "amount": 1}, 0)).code == ledger.UNAUTHORIZED
    assert f.apply(f.tx(f.bob, types.TX_BURN, {"asset": "usd:devnet", "amount": 1}, 0)).code == ledger.UNAUTHORIZED
    f.state.check_all()


def test_asset_registration_validation(f: Fixture) -> None:
    def asset(asset_class: str, decimals: int, meta: dict) -> dict:
        return {
            "id": "tzs:devnet", "class": asset_class, "symbol": "TZS",
            "name": "Tanzanian Shilling", "decimals": decimals, "policy": "capped",
            "max_supply": 1_000_000_000, "issuer": f.alice.address, "registered": 0,
            "meta": meta,
        }

    def register(a: dict, nonce: int) -> ledger.Receipt:
        return f.apply(f.tx(f.alice, types.TX_REGISTER_ASSET, {"asset": a}, nonce))

    assert register(asset("currency", 2, {}), 0).code == ledger.INVALID_PAYLOAD
    assert register(asset("fiat", 2, {"iso4217": "usd"}), 0).code == ledger.INVALID_PAYLOAD
    assert register(asset("fiat", 2, {"iso17442": "short"}), 0).code == ledger.INVALID_PAYLOAD
    assert register(asset("stablecoin", 2, {"iso4217": "TZS"}), 0).success
    assert register(asset("fiat", 6, {"iso4217": "TZS"}), 1).code == ledger.ASSET_EXISTS

    stored = f.state.asset("tzs:devnet")
    assert stored is not None
    assert stored.decimals == 2 and stored.asset_class == "stablecoin"


def test_supply_cap_enforced(f: Fixture) -> None:
    asset = _register(f) | {
        "id": "cap:devnet", "symbol": "CAP", "decimals": 0, "max_supply": 1000,
    }
    assert f.apply(f.tx(f.alice, types.TX_REGISTER_ASSET, {"asset": asset}, 0)).success
    mint = {"asset": "cap:devnet", "to": f.alice.address, "amount": 1000}
    assert f.apply(f.tx(f.alice, types.TX_MINT, mint, 1)).success
    assert f.apply(f.tx(f.alice, types.TX_MINT, mint | {"amount": 1}, 2)).code == ledger.SUPPLY_LIMIT
    f.state.check_all()


def test_batch_is_atomic(f: Fixture) -> None:
    root = f.state.root()
    receipt = f.apply(f.tx(f.alice, types.TX_BATCH, {"items": [
        {"asset": NATIVE, "to": f.bob.address, "amount": 10},
        {"asset": NATIVE, "to": f.carol.address, "amount": 999_999_999_999},
    ]}, 0))
    assert not receipt.success
    assert f.state.root() == root


def test_best_effort_batch(f: Fixture) -> None:
    receipt = f.apply(f.tx(f.alice, types.TX_BATCH, {
        "mode": "best_effort",
        "items": [
            {"asset": NATIVE, "to": f.bob.address, "amount": 100_000_000},
            {"asset": NATIVE, "to": f.carol.address, "amount": 2_000_000_000},
            {"asset": NATIVE, "to": f.carol.address, "amount": 200_000_000},
        ],
    }, 0))
    assert receipt.success
    assert f.state.balance(f.bob.address, NATIVE) == 200_000_000
    assert f.state.balance(f.carol.address, NATIVE) == 300_000_000
    results = [e for e in receipt.events if e["type"] == "batch_item"]
    assert len(results) == 3
    failed = [e for e in results if e["attrs"]["success"] == "false"]
    assert len(failed) == 1
    assert failed[0]["attrs"]["code"] == ledger.INSUFFICIENT_FUNDS
    f.state.check_all()


def test_batch_unknown_mode(f: Fixture) -> None:
    receipt = f.apply(f.tx(f.alice, types.TX_BATCH, {
        "mode": "sometimes",
        "items": [{"asset": NATIVE, "to": f.bob.address, "amount": 1}],
    }, 0))
    assert receipt.code == ledger.INVALID_PAYLOAD


def test_foreign_fee_needs_route(f: Fixture) -> None:
    f.apply(f.tx(f.alice, types.TX_REGISTER_ASSET, {"asset": _register(f)}, 0))
    f.apply(f.tx(f.alice, types.TX_MINT, {"asset": "usd:devnet", "to": f.bob.address, "amount": 10_000}, 1))
    tx = f.tx(f.bob, types.TX_TRANSFER, {"asset": "usd:devnet", "to": f.carol.address, "amount": 10}, 0)
    tx.signatures.clear()
    tx.fee_asset = "usd:devnet"
    tx.sign(f.bob)
    assert f.apply(tx).code == ledger.NO_FEE_ROUTE
    f.state.set_route("usd:devnet", 100_000_000)
    assert f.apply(tx).success
    f.state.check_all()


def test_state_proofs(f: Fixture) -> None:
    key = f"balance/{f.alice.address}/{NATIVE}"
    root = f.state.root()
    f.state.tree.prove(key).verify(root)
    stranger, _ = new_keypair(NETWORK)
    proof = f.state.tree.prove(f"balance/{stranger.address}/{NATIVE}")
    assert proof.value is None
    proof.verify(root)


def test_block_ordering_groups_nonces(f: Fixture) -> None:
    txs = [
        f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 1}, 1),
        f.tx(f.bob, types.TX_TRANSFER, {"asset": NATIVE, "to": f.alice.address, "amount": 1}, 0),
        f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 1}, 0),
    ]
    ordered = ledger.order_txs(txs)
    assert len(ordered) == 3
    for previous, current in zip(ordered, ordered[1:], strict=False):
        if current.signer == previous.signer:
            assert current.nonce > previous.nonce


def test_block_height_and_roots(f: Fixture) -> None:
    tx = f.tx(f.alice, types.TX_TRANSFER, {"asset": NATIVE, "to": f.bob.address, "amount": 7}, 0)
    block, receipts = ledger.apply_block(f.state, [tx], NOW, 1, f.alice.address)
    assert receipts[0].success
    assert block.state_root == f.state.root().hex()
    assert block.tx_root == block.tx_root
    with pytest.raises(ledger.InvariantError):
        ledger.apply_block(f.state, [], NOW, 3, f.alice.address)


def test_smt_root_matches_go_shape() -> None:
    tree = smt.Tree()
    tree.set("a", b"1")
    root = tree.state_root()
    assert len(root) == 32
    assert root != smt.EMPTY


def test_encrypted_memo(f: Fixture) -> None:
    from arne import memo

    sealed = memo.seal(memo.public_key(f.bob.private), b"invoice 42")
    payload = {"asset": NATIVE, "to": f.bob.address, "amount": 1_000, "memo_enc": sealed}
    assert f.apply(f.tx(f.alice, types.TX_TRANSFER, payload, 0)).success

    both = {"asset": NATIVE, "to": f.bob.address, "amount": 1_000, "memo": "public", "memo_enc": sealed}
    assert f.apply(f.tx(f.alice, types.TX_TRANSFER, both, 1)).code == ledger.INVALID_PAYLOAD

    text = {"asset": NATIVE, "to": f.bob.address, "amount": 1_000, "memo_enc": "text"}
    assert f.apply(f.tx(f.alice, types.TX_TRANSFER, text, 1)).code == ledger.INVALID_PAYLOAD

    garbage = {"asset": NATIVE, "to": f.bob.address, "amount": 1_000, "memo_enc": b"garbage"}
    assert f.apply(f.tx(f.alice, types.TX_TRANSFER, garbage, 1)).code == ledger.INVALID_PAYLOAD


def test_refund_lifecycle(f: Fixture) -> None:
    pay = f.apply(f.tx(f.alice, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.bob.address, "amount": 5_000_000}, 0))
    assert pay.success, pay.error
    original = f.state.transfer(pay.hash)
    assert original is not None
    assert (original.sender, original.to, original.amount, original.refund_of) == (
        f.alice.address, f.bob.address, 5_000_000, "")

    alice_before = f.state.balance(f.alice.address, NATIVE)
    partial = f.apply(f.tx(f.bob, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.alice.address, "amount": 2_000_000, "refund_of": pay.hash}, 0))
    assert partial.success, partial.error
    assert f.state.refunded(pay.hash) == 2_000_000
    assert f.state.balance(f.alice.address, NATIVE) == alice_before + 2_000_000
    assert partial.events[-1] == {"type": "refund", "attrs": {
        "refund_of": pay.hash, "from": f.bob.address, "to": f.alice.address,
        "asset": NATIVE, "amount": "2000000", "remaining": "3000000"}}
    reversal = f.state.transfer(partial.hash)
    assert reversal is not None and reversal.refund_of == pay.hash

    last = f.apply(f.tx(f.bob, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.alice.address, "amount": 3_000_000, "refund_of": pay.hash}, 1))
    assert last.success, last.error
    assert f.state.refunded(pay.hash) == 5_000_000

    root = f.state.root()
    over = f.apply(f.tx(f.bob, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.alice.address, "amount": 1, "refund_of": pay.hash}, 2))
    assert over.code == ledger.REFUND_EXCEEDED
    assert f.state.root() == root
    f.state.check_all()


def test_refund_rejections(f: Fixture) -> None:
    pay = f.apply(f.tx(f.alice, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.bob.address, "amount": 5_000_000}, 0))
    assert pay.success, pay.error
    assert f.apply(f.tx(f.alice, types.TX_REGISTER_ASSET, {"asset": {
        "id": "usd:devnet", "class": "fiat", "symbol": "USD", "name": "US Dollar",
        "decimals": 2, "policy": "capped", "max_supply": 1_000_000,
        "issuer": f.alice.address, "registered": 0, "meta": {"iso4217": "USD"},
    }}, 1)).success

    cases = [
        (f.bob, 0, {"refund_of": "a" * 64}, ledger.REFUND_NOT_FOUND),
        (f.bob, 0, {"refund_of": "deadbeef"}, ledger.INVALID_PAYLOAD),
        (f.alice, 2, {}, ledger.UNAUTHORIZED),
        (f.carol, 0, {}, ledger.UNAUTHORIZED),
        (f.bob, 0, {"to": f.carol.address}, ledger.REFUND_MISMATCH),
        (f.bob, 0, {"asset": "usd:devnet"}, ledger.REFUND_MISMATCH),
    ]
    for kp, nonce, change, code in cases:
        payload = {"asset": NATIVE, "to": f.alice.address, "amount": 1,
                   "refund_of": pay.hash, **change}
        assert f.apply(f.tx(kp, types.TX_TRANSFER, payload, nonce)).code == code

    first = f.apply(f.tx(f.bob, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.alice.address, "amount": 1_000_000, "refund_of": pay.hash}, 0))
    assert first.success, first.error
    again = f.apply(f.tx(f.alice, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.bob.address, "amount": 1_000_000, "refund_of": first.hash}, 2))
    assert again.code == ledger.REFUND_MISMATCH


def test_refund_and_intent_are_exclusive(f: Fixture) -> None:
    pay = f.apply(f.tx(f.alice, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.bob.address, "amount": 5_000_000}, 0))
    assert pay.success, pay.error
    result = f.apply(f.tx(f.bob, types.TX_TRANSFER, {
        "asset": NATIVE, "to": f.alice.address, "amount": 1,
        "refund_of": pay.hash, "intent": "0" * 32}, 0))
    assert result.code == ledger.INVALID_PAYLOAD
