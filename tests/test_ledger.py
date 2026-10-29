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
