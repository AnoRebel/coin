"""Cross-language conformance: the Python node must reproduce spec/vectors exactly."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import cbor, keys, ledger, smt, types

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def load(name: str) -> dict:
    return json.loads((VECTORS / name).read_text(encoding="utf-8"))


def _from_json(value):
    if isinstance(value, dict):
        if list(value) == ["$bytes"]:
            return bytes.fromhex(value["$bytes"])
        return {k: _from_json(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_from_json(v) for v in value]
    return value


def test_cbor_vectors() -> None:
    for case in load("cbor.json")["cases"]:
        payload = _from_json(case["json"])
        encoded = cbor.encode(payload)
        assert encoded.hex() == case["hex"], case["name"]
        assert cbor.decode(bytes.fromhex(case["hex"])) == payload, case["name"]


def test_cbor_rejects_non_canonical() -> None:
    bad = [
        "1800",  # non-shortest zero
        "1f",  # indefinite array
        "5f",  # indefinite map
        "a2616101616102",  # duplicate key
        "a262616102616201",  # keys out of canonical order
        "fa00000000",  # float
        "c000",  # tag
        "8181",  # truncated
    ]
    for raw in bad:
        with pytest.raises(cbor.CBORError):
            cbor.decode(bytes.fromhex(raw))


def test_key_vectors() -> None:
    data = load("keys.json")
    assert keys.validate_mnemonic(data["mnemonic"]) is None
    for derivation in data["derivations"]:
        path = derivation["path"]
        private = keys.derive_path(data["mnemonic"], data["passphrase"], *path[2:])
        assert private.hex() == derivation["private"]
        for network, address in derivation["address"].items():
            kp = keys.keypair_from_mnemonic(
                data["mnemonic"], data["passphrase"], *path[2:], network
            )
            assert kp.public.hex() == derivation["public"]
            assert kp.address == address


def test_smt_vectors() -> None:
    for case in load("smt.json")["cases"]:
        tree = smt.Tree()
        for key, value in zip(case["keys"], case["values"], strict=True):
            tree.set(key, bytes.fromhex(value))
        root = tree.state_root()
        assert root.hex() == case["root"], case["keys"]
        for expected in case["proofs"]:
            proof = tree.prove(expected["key"])
            assert proof.depth == expected["depth"]
            assert proof.terminal.hex() == expected["terminal"]
            assert [s.hex() for s in proof.siblings] == expected["siblings"]
            proof.verify(root)


def _asset(raw: dict) -> types.Asset:
    """Vector JSON uses "metadata"; canonical CBOR uses "meta"."""
    data = {"meta": raw.get("metadata", {})}
    data.update({k: v for k, v in raw.items() if k != "metadata"})
    return types.Asset.from_cbor(data)


def test_ledger_vectors() -> None:
    fixture = load("ledger.json")
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
    for step in fixture["steps"]:
        for asset, rate in step.get("set_route", {}).items():
            state.set_route(asset, rate)
        txs = []
        for item in step["txs"]:
            tx = types.Tx.from_cbor(cbor.decode_map(bytes.fromhex(item["cbor"])))
            kp = keys.keypair_from_mnemonic(
                fixture["mnemonic"], "", 0, 0, indices[item["signer"]], fixture["network"]
            )
            tx.signatures.clear()
            tx.sign(kp)
            txs.append(tx)
        block, receipts = ledger.apply_block(
            state, txs, step["time"], step["block"], genesis["allocations"][0]["address"]
        )
        assert block.state_root == step["state_root"], step["block"]
        assert block.tx_root == step["tx_root"], step["block"]
        assert block.hash() == step["block_hash"], step["block"]
        assert len(receipts) == len(step["receipts"])
        for got, want in zip(receipts, step["receipts"], strict=True):
            assert got.success == want["success"], (step["block"], want)
            assert got.code == want.get("code", ""), (step["block"], want)
            assert got.fee == want["fee"], (step["block"], want)


def test_tx_vectors() -> None:
    data = load("tx.json")
    for case in data["valid"]:
        raw = bytes.fromhex(case["cbor"])
        tx = types.Tx.from_cbor(cbor.decode_map(raw))
        assert cbor.encode(tx.to_cbor()) == raw, case["name"]
        assert tx.hash() == case["hash"], case["name"]
        assert bool(tx.signatures) == case["signed"], case["name"]
        if case["signed"]:
            tx.verify()
    for case in data["invalid"]:
        raw = bytes.fromhex(case["cbor"])
        with pytest.raises(types.ValidationError):
            types.Tx.from_cbor(cbor.decode_map(raw))


def test_uri_vectors() -> None:
    from arne import paymenturi

    data = load("uri.json")
    for case in data["valid"]:
        payment = paymenturi.parse(case["uri"], now=data["now"])
        assert payment.network == case["network"], case["name"]
        assert payment.to == case["to"], case["name"]
        assert payment.asset == case["asset"], case["name"]
        assert payment.amount == case["amount"], case["name"]
        assert payment.memo == case["memo"], case["name"]
        assert payment.expires == case["expires"], case["name"]
        assert payment.warnings == case["warnings"], case["name"]
        assert str(payment) == case["canonical"], case["name"]
        again = paymenturi.parse(str(payment), now=data["now"])
        assert again.summary() == payment.summary(), case["name"]
    for case in data["invalid"]:
        with pytest.raises(paymenturi.URIError):
            paymenturi.parse(case["uri"], now=data["now"])
