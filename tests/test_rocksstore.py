"""RocksDB store: same key schema, atomic commits, and Go-matching state roots."""

from __future__ import annotations

import json
from pathlib import Path

from arne import cbor, keys, ledger, types
from arne.rocksstore import RocksStore

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _load(name: str) -> dict:
    return json.loads((VECTORS / name).read_text(encoding="utf-8"))


def _asset(raw: dict) -> types.Asset:
    data = {"meta": raw.get("metadata", {})}
    data.update({k: v for k, v in raw.items() if k != "metadata"})
    return types.Asset.from_cbor(data)


def test_commit_round_trip(tmp_path) -> None:
    store = RocksStore(tmp_path / "db")
    block = ledger.Block(
        height=1, time=1, proposer="p", prev_root="00", state_root="11",
        tx_root="22", tx_hashes=["aa"], receipts=[],
    )
    store.save_commit(block, {"k": b"v"})
    store.close()

    reopened = RocksStore(tmp_path / "db")
    assert reopened.schema_version() == 1
    loaded = reopened.block(1)
    assert loaded is not None and loaded.state_root == "11" and loaded.tx_hashes == ["aa"]
    assert reopened.last_block() is not None
    state = reopened.load_state()
    assert state is not None and state[0] == {"k": b"v"} and state[1] == 1
    reopened.close()


def test_ledger_vectors_match_go(tmp_path) -> None:
    fixture = _load("ledger.json")
    genesis = fixture["genesis"]
    params = ledger.Params(**genesis["params"])
    state = ledger.new_genesis(
        ledger.Genesis(
            network=fixture["network"],
            chain=fixture["chain"],
            time=genesis["time"],
            params=params,
            assets=[_asset(a) for a in genesis["assets"]],
            allocations=[ledger.Allocation(**a) for a in genesis["allocations"]],
            pools=genesis["pools"],
        )
    )
    indices = {"alice": 0, "bob": 1, "carol": 2}
    store = RocksStore(tmp_path / "db")
    store.save_state(0, state.snapshot())

    for step in fixture["steps"]:
        for asset, rate in step.get("set_route", {}).items():
            state.set_route(asset, rate)
        txs = []
        for item in step["txs"]:
            tx = types.Tx.from_cbor(cbor.decode_map(bytes.fromhex(item["cbor"])))
            keypair = keys.keypair_from_mnemonic(
                fixture["mnemonic"], "", 0, 0, indices[item["signer"]], fixture["network"]
            )
            tx.signatures.clear()
            tx.sign(keypair)
            txs.append(tx)
        block, _ = ledger.apply_block(
            state, txs, step["time"], step["block"], genesis["allocations"][0]["address"]
        )
        assert block.state_root == step["state_root"], step["block"]
        store.save_commit(block, state.snapshot())

    store.close()

    reopened = RocksStore(tmp_path / "db")
    snapshot, height = reopened.load_state()
    assert height == fixture["steps"][-1]["block"]
    restored = ledger.State.from_snapshot(fixture["network"], fixture["chain"], params, snapshot)
    assert restored.root().hex() == fixture["steps"][-1]["state_root"]
    reopened.close()
