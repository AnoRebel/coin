"""ABCI 2.0 application for the Python parity node.

CometBFT talks to this process over a line-delimited JSON bridge implemented by
``internal/consensus/pythonapp.go``. Every consensus decision is executed by the
Python ledger, so a Go validator and a Python validator produce the same app
hash for the same decided transactions.

Run it with ``python -m arne.abci --genesis <cometbft genesis.json>``. The
genesis file is the same one the Go node uses; its ``app_state`` is the ledger
genesis.
"""

from __future__ import annotations

import argparse
import json
import sys
import threading
from pathlib import Path
from typing import Any

from . import ledger, node, types

ACCEPT = "accept"
REJECT = "reject"


def genesis_from_document(document: dict[str, Any]) -> ledger.Genesis:
    app_state = document.get("app_state") or {}
    params = ledger.Params(**app_state["params"])
    assets = [
        types.Asset(
            id=item["id"], asset_class=item["class"], symbol=item["symbol"],
            name=item["name"], decimals=item["decimals"], policy=item["policy"],
            max_supply=item["max_supply"], issuer=item.get("issuer", ""),
            metadata={k: str(v) for k, v in (item.get("metadata") or {}).items()},
            registered=int(item.get("registered", 0)),
        )
        for item in app_state.get("assets") or []
    ]
    allocations = [
        ledger.Allocation(address=item["address"], asset=item["asset"], amount=item["amount"])
        for item in app_state.get("allocations") or []
    ]
    genesis = ledger.Genesis(
        network=app_state["network"], chain=app_state["chain"], time=app_state["time"],
        params=params, assets=assets, allocations=allocations,
        pools=app_state.get("pools") or {},
    )
    if document.get("chain_id") and document["chain_id"] != genesis.chain:
        raise ValueError(
            f"genesis chain_id {document['chain_id']!r} does not match {genesis.chain!r}"
        )
    return genesis


class App:
    """Minimal ABCI 2.0 app backed by the Python node."""

    def __init__(self, genesis: ledger.Genesis, store: node.Store) -> None:
        cfg = node.Config(
            network=genesis.network, chain=genesis.chain, params=genesis.params,
            proposer="", devnet=types.network_class(genesis.network) == "devnet",
        )
        self.node = node.Node.open(cfg, genesis, store)
        self.lock = threading.RLock()

    # --- ABCI methods ---

    def info(self) -> dict[str, Any]:
        with self.lock:
            return {
                "height": self.node.state.height(),
                "app_hash": self.node.state.root().hex(),
            }

    def init_chain(self, chain_id: str) -> dict[str, Any]:
        with self.lock:
            if chain_id and chain_id != self.node.cfg.chain:
                raise ValueError(
                    f"genesis chain {chain_id!r} does not match node chain "
                    f"{self.node.cfg.chain!r}"
                )
            return {"app_hash": self.node.state.root().hex()}

    def check_tx(self, raw: bytes) -> dict[str, Any]:
        with self.lock:
            try:
                self.node.check_raw(raw)
            except Exception as exc:  # noqa: BLE001 - ABCI turns any error into a code
                return {"code": 1, "codespace": "arne", "log": str(exc)}
            return {"code": 0, "codespace": "arne", "log": ""}

    def prepare_proposal(self, raws: list[bytes], max_tx_bytes: int) -> dict[str, Any]:
        with self.lock:
            txs = self.node.prepare_txs(raws, self.node.cfg.max_block_txs, max_tx_bytes)
            return {"txs": [tx.hex() for tx in txs]}

    def process_proposal(self, raws: list[bytes]) -> dict[str, Any]:
        with self.lock:
            for raw in raws:
                try:
                    self.node.decode_tx(raw)
                except Exception:  # noqa: BLE001 - any malformed tx rejects the proposal
                    return {"status": REJECT}
            return {"status": ACCEPT}

    def finalize_block(
        self, raws: list[bytes], height: int, now: int, proposer: str
    ) -> dict[str, Any]:
        with self.lock:
            _, receipts = self.node.finalize_txs(raws, height, now, proposer)
            results = []
            for receipt in receipts:
                result = {"codespace": "arne", "log": receipt.error}
                if not receipt.success:
                    result["code"] = 1
                    result["info"] = receipt.code
                results.append(result)
            return {
                "app_hash": self.node.state.root().hex(),
                "tx_results": results,
            }

    def commit(self) -> dict[str, Any]:
        with self.lock:
            self.node.commit_block()
            return {}

    def query(self, path: str, data: bytes) -> dict[str, Any]:
        with self.lock:
            if path == "/status":
                status = self.node.status()
                return {
                    "code": 0, "value": json.dumps(status).encode().hex(),
                    "height": status["height"],
                }
            if path == "/balance":
                try:
                    query = json.loads(data)
                    address = query.get("address") or query.get("Address")
                    asset = query.get("asset") or query.get("Asset")
                    if not address or not asset:
                        raise ValueError
                except (ValueError, TypeError):
                    return {"code": 1, "log": "balance query wants JSON {address,asset}"}
                balance = self.node.state.balance(address, asset)
                return {
                    "code": 0,
                    "value": json.dumps({"balance": balance}).encode().hex(),
                    "height": self.node.state.height(),
                }
            return {"code": 1, "log": "unknown query path: " + path}


def _decode(raw: str) -> bytes:
    return bytes.fromhex(raw)


def dispatch(app: App, request: dict[str, Any]) -> dict[str, Any]:
    method = request.get("method")
    if method == "info":
        return app.info()
    if method == "init_chain":
        return app.init_chain(str(request.get("chain_id", "")))
    if method == "check_tx":
        return app.check_tx(_decode(request["tx"]))
    if method == "prepare_proposal":
        return app.prepare_proposal(
            [_decode(tx) for tx in request.get("txs") or []],
            int(request.get("max_tx_bytes", 0)),
        )
    if method == "process_proposal":
        return app.process_proposal([_decode(tx) for tx in request.get("txs") or []])
    if method == "finalize_block":
        return app.finalize_block(
            [_decode(tx) for tx in request.get("txs") or []],
            int(request["height"]), int(request["time"]), str(request.get("proposer", "")),
        )
    if method == "commit":
        return app.commit()
    if method == "query":
        return app.query(str(request.get("path", "")), _decode(request.get("data", "")))
    if method == "stop":
        raise SystemExit(0)
    raise ValueError(f"unknown ABCI method {method!r}")


def serve(app: App, stdin, stdout) -> None:
    for line in stdin:
        line = line.strip()
        if not line:
            continue
        try:
            request = json.loads(line)
            response = dispatch(app, request)
        except SystemExit:
            return
        except Exception as exc:  # noqa: BLE001 - report the failure to the Go bridge
            response = {"error": str(exc)}
        stdout.write(json.dumps(response, separators=(",", ":")).encode() + b"\n")
        stdout.flush()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="arne.abci")
    parser.add_argument("--genesis", required=True, help="CometBFT genesis.json")
    parser.add_argument("--data-dir", default="", help="optional persistent store directory")
    args = parser.parse_args(argv)

    document = json.loads(Path(args.genesis).read_text())
    genesis = genesis_from_document(document)
    store: node.Store
    if args.data_dir:
        store = node.FileStore(args.data_dir)
    else:
        store = node.MemoryStore()
    app = App(genesis, store)
    serve(app, sys.stdin.buffer, sys.stdout.buffer)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
