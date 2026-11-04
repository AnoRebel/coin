"""Node service: mempool, block production, events, faucet, and persistence.

Mirrors internal/node and internal/storage. The Go node is canonical; this is the
Python parity implementation and must produce the same state roots for the same
transactions (verified by tests/test_vectors.py).
"""

from __future__ import annotations

import hashlib
import json
import os
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from queue import Full, Queue
from typing import Any, Protocol

from . import cbor, ledger, types
from . import webhook as webhook_codec
from .keys import KeyPair

PROTOCOL_VERSION = "arne/1"
MEMPOOL_LIMIT = 10_000
SCHEMA_VERSION = 1
# version -> (name, up, down). Step 1 only records the on-disk version; the
# existing block and state files already use the v1 layout.
_MIGRATIONS: dict[int, tuple[str, Callable[[], None], Callable[[], None]]] = {
    1: ("baseline-version-marker", lambda: None, lambda: None),
}


def read_schema_version(directory: str | Path) -> int:
    """Return the stored schema version without opening or migrating the store."""
    path = Path(directory) / "schema.json"
    return int(json.loads(path.read_text())["version"]) if path.exists() else 0


@dataclass
class Config:
    network: str
    chain: str
    params: ledger.Params
    proposer: str
    max_block_txs: int = 500
    faucet: KeyPair | None = None
    faucet_amount: int = 100 * 100_000_000  # 100 ARCE
    devnet: bool = False
    webhooks: list[str] = field(default_factory=list)
    webhook_seed: bytes | None = None


@dataclass
class Event:
    type: str
    height: int
    time: int
    hash: str
    tx_hashes: list[str] = field(default_factory=list)
    receipt: dict[str, Any] | None = None
    index: int = 0

    @property
    def stream(self) -> str:
        if self.type == "transaction":
            return "transactions"
        if self.type == "message":
            return "messages"
        return "blocks"

    def to_dict(self) -> dict:
        out: dict[str, Any] = {
            "type": self.type, "height": self.height, "time": self.time,
            "hash": self.hash, "tx_hashes": list(self.tx_hashes),
        }
        if self.receipt is not None:
            out["receipt"] = self.receipt
        if self.index:
            out["index"] = self.index
        return out


class Store(Protocol):
    def save_block(self, block: ledger.Block) -> None: ...
    def block(self, height: int) -> ledger.Block | None: ...
    def last_block(self) -> ledger.Block | None: ...
    def save_state(self, height: int, snapshot: dict[str, bytes]) -> None: ...
    def load_state(self) -> tuple[dict[str, bytes], int] | None: ...
    def close(self) -> None: ...


def _checksum(payload: bytes) -> bytes:
    return hashlib.sha256(payload).digest()


class MemoryStore:
    def __init__(self) -> None:
        self.blocks: dict[int, ledger.Block] = {}
        self.state: dict[str, bytes] | None = None
        self.height = 0

    def save_block(self, block: ledger.Block) -> None:
        self.blocks[block.height] = block

    def block(self, height: int) -> ledger.Block | None:
        return self.blocks.get(height)

    def last_block(self) -> ledger.Block | None:
        return self.blocks[max(self.blocks)] if self.blocks else None

    def save_state(self, height: int, snapshot: dict[str, bytes]) -> None:
        self.state = {k: bytes(v) for k, v in snapshot.items()}
        self.height = height

    def load_state(self) -> tuple[dict[str, bytes], int] | None:
        if self.state is None:
            return None
        return dict(self.state), self.height

    def close(self) -> None:
        return None


class FileStore:
    """Atomic file-backed store for single-node deployments.

    shortcut: one JSON file per block and one state snapshot file instead of an
    embedded KV engine; upgrade to RocksDB or Postgres when block volume or
    concurrent writers matter.
    """

    def __init__(self, directory: str | Path, *, auto_migrate: bool = True) -> None:
        self.root = Path(directory)
        (self.root / "blocks").mkdir(parents=True, exist_ok=True)
        # Refuse a store written by a newer binary before reading it.
        if auto_migrate:
            self.migrate()

    def schema_version(self) -> int:
        return read_schema_version(self.root)

    def _write_schema(self, version: int) -> None:
        self._write(
            self.root / "schema.json",
            json.dumps({"version": version}, sort_keys=True, separators=(",", ":")).encode(),
        )

    def migrate(self, target: int = SCHEMA_VERSION) -> None:
        """Move one version step at a time; rerunning at the target is a no-op."""
        if not 0 <= target <= SCHEMA_VERSION:
            raise ValueError(f"unsupported schema target {target}")
        current = self.schema_version()
        if current > SCHEMA_VERSION:
            raise ledger.InvariantError(
                f"storage schema {current} is newer than this binary ({SCHEMA_VERSION})"
            )
        if current == target:
            return
        steps = range(current + 1, target + 1) if current < target else range(current, target, -1)
        for version in steps:
            if version not in _MIGRATIONS:
                raise ValueError(f"missing schema migration {version}")
            _, up, down = _MIGRATIONS[version]
            up() if current < target else down()
            self._write_schema(version if current < target else version - 1)

    def _write(self, path: Path, payload: bytes) -> None:
        tmp = path.with_suffix(path.suffix + ".tmp")
        with open(tmp, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, path)

    def save_block(self, block: ledger.Block) -> None:
        record = {
            "height": block.height, "time": block.time, "proposer": block.proposer,
            "prev_root": block.prev_root, "state_root": block.state_root,
            "tx_root": block.tx_root, "tx_hashes": block.tx_hashes,
            "receipts": [r.to_dict() for r in block.receipts],
        }
        payload = json.dumps(record, sort_keys=True, separators=(",", ":")).encode()
        self._write(self.root / "blocks" / f"{block.height:020d}.json", payload)

    def block(self, height: int) -> ledger.Block | None:
        path = self.root / "blocks" / f"{height:020d}.json"
        if not path.exists():
            return None
        record = json.loads(path.read_text())
        return ledger.Block(
            height=record["height"], time=record["time"], proposer=record["proposer"],
            prev_root=record["prev_root"], state_root=record["state_root"],
            tx_root=record["tx_root"], tx_hashes=record["tx_hashes"],
            receipts=[
                ledger.Receipt(
                    hash=r["hash"], height=r["height"], success=r["success"],
                    fee=r.get("fee", 0), fee_asset=r.get("fee_asset", ""),
                    code=r.get("code", ""), error=r.get("error", ""),
                    events=r.get("events", []),
                )
                for r in record["receipts"]
            ],
        )

    def last_block(self) -> ledger.Block | None:
        files = sorted((self.root / "blocks").glob("*.json"))
        return self.block(int(files[-1].stem)) if files else None

    def save_state(self, height: int, snapshot: dict[str, bytes]) -> None:
        body = json.dumps(
            {"height": height, "state": {k: v.hex() for k, v in snapshot.items()}},
            sort_keys=True, separators=(",", ":"),
        ).encode()
        self._write(self.root / "state.json", _checksum(body) + body)

    def load_state(self) -> tuple[dict[str, bytes], int] | None:
        path = self.root / "state.json"
        if not path.exists():
            return None
        data = path.read_bytes()
        if len(data) < 32 or _checksum(data[32:]) != data[:32]:
            raise ledger.InvariantError("storage: corrupt state record")
        record = json.loads(data[32:])
        return {k: bytes.fromhex(v) for k, v in record["state"].items()}, record["height"]

    def close(self) -> None:
        return None


class Node:
    def __init__(self, cfg: Config, store: Store, state: ledger.State) -> None:
        self._lock = threading.RLock()
        self.cfg = cfg
        self.store = store
        self.state = state
        self.mempool: dict[str, types.Tx] = {}
        self.receipts: dict[str, ledger.Receipt] = {}
        self.blocks: dict[int, ledger.Block] = {}
        self._subscribers: list[Queue[Event]] = []
        self.halted = ""
        self._pending: ledger.Block | None = None
        self.webhook_dispatcher: webhook_codec.Dispatcher | None = None
        if cfg.webhooks:
            if cfg.webhook_seed is None:
                raise ValueError("webhooks require a 32-byte signing seed")
            self.webhook_dispatcher = webhook_codec.Dispatcher(webhook_codec.Config(
                endpoints=cfg.webhooks, signer=webhook_codec.Signer(cfg.webhook_seed),
            ))
            self.webhook_dispatcher.start()
        self._load_blocks()

    @classmethod
    def open(cls, cfg: Config, genesis: ledger.Genesis, store: Store) -> Node:
        loaded = store.load_state()
        if loaded and loaded[1] > 0:
            snapshot, _ = loaded
            state = ledger.State.from_snapshot(cfg.network, cfg.chain, cfg.params, snapshot)
            return cls(cfg, store, state)
        state = ledger.new_genesis(genesis)
        store.save_state(0, state.snapshot())
        return cls(cfg, store, state)

    def _load_blocks(self) -> None:
        for height in range(1, self.state.height() + 1):
            block = self.store.block(height)
            if block is None:
                continue
            self.blocks[height] = block
            for index, tx_hash in enumerate(block.tx_hashes):
                if index < len(block.receipts):
                    self.receipts[tx_hash] = block.receipts[index]

    # --- queries ---

    def status(self) -> dict:
        with self._lock:
            return {
                "network": self.cfg.network, "chain": self.cfg.chain,
                "height": self.state.height(), "state_root": self.state.root().hex(),
                "time": self.state.block_time(), "mempool": len(self.mempool),
                "halted": self.halted, "devnet": self.cfg.devnet,
                "protocol_version": PROTOCOL_VERSION,
            }

    def block(self, height: int) -> ledger.Block | None:
        with self._lock:
            return self.blocks.get(height)

    def blocks_page(self, limit: int = 50, before: int = 0) -> list[ledger.Block]:
        with self._lock:
            heights = sorted(
                (h for h in self.blocks if before == 0 or h < before), reverse=True
            )
            if limit <= 0:
                limit = len(heights)
            return [self.blocks[h] for h in heights[:limit]]

    def receipt(self, tx_hash: str) -> ledger.Receipt | None:
        with self._lock:
            return self.receipts.get(tx_hash)

    def proof(self, key: str):
        with self._lock:
            return self.state.tree.prove(key)

    # --- writes ---

    def submit_tx(self, raw: bytes) -> str:
        try:
            tx = types.Tx.from_cbor(cbor.decode_map(raw))
            tx_hash = tx.hash()
            tx.validate()
        except (cbor.CBORError, types.ValidationError, types.SignatureError) as exc:
            raise ledger.LedgerError(ledger.INVALID_SCHEMA, f"decode: {exc}") from exc
        with self._lock:
            if self.halted:
                raise ledger.InvariantError(f"node is halted: {self.halted}")
            if tx_hash in self.mempool:
                return tx_hash
            now = int(time.time())
            self.state.check_tx(tx, now)
            for other_hash, other in list(self.mempool.items()):
                if other.signer == tx.signer and other.nonce == tx.nonce:
                    if other.priority_fee >= tx.priority_fee:
                        raise ledger.LedgerError(ledger.MEMPOOL_CONFLICT,
                                                 "pending transaction at this nonce has equal or higher priority fee")
                    del self.mempool[other_hash]
            if len(self.mempool) >= MEMPOOL_LIMIT:
                raise ledger.LedgerError(ledger.LIMIT_EXCEEDED, "mempool full")
            self.mempool[tx_hash] = tx
            return tx_hash

    def simulate(self, raw: bytes) -> ledger.Receipt:
        try:
            tx = types.Tx.from_cbor(cbor.decode_map(raw))
        except (cbor.CBORError, types.ValidationError) as exc:
            raise ledger.LedgerError(ledger.INVALID_SCHEMA, f"decode: {exc}") from exc
        with self._lock:
            return self.state.simulate_tx(tx, int(time.time()), self.state.height() + 1)

    # --- ABCI app surface (mirrors internal/node) ---

    def decode_tx(self, raw: bytes) -> types.Tx:
        try:
            tx = types.Tx.from_cbor(cbor.decode_map(raw))
        except (cbor.CBORError, types.ValidationError) as exc:
            raise ledger.LedgerError(ledger.INVALID_SCHEMA, f"decode: {exc}") from exc
        if tx.network != self.cfg.network or tx.chain != self.cfg.chain:
            raise ledger.LedgerError(
                ledger.WRONG_NETWORK,
                f"expected {self.cfg.network}/{self.cfg.chain}",
            )
        return tx

    def check_raw(self, raw: bytes) -> None:
        tx = self.decode_tx(raw)
        with self._lock:
            if self.halted:
                raise ledger.InvariantError(f"node is halted: {self.halted}")
            self.state.check_tx(tx, int(time.time()))

    def prepare_txs(
        self, raws: list[bytes], max_txs: int = 500, max_bytes: int = 0
    ) -> list[bytes]:
        with self._lock:
            if self.halted or not raws:
                return []
            decoded: list[types.Tx] = []
            by_hash: dict[str, bytes] = {}
            for raw in raws:
                try:
                    tx = self.decode_tx(raw)
                except ledger.LedgerError:
                    continue
                decoded.append(tx)
                by_hash[tx.hash()] = raw
            ordered = ledger.order_txs(decoded)
            expected: dict[str, int] = {}

            def next_nonce(signer: str) -> int:
                if signer not in expected:
                    info, _ = self.state.account(signer)
                    expected[signer] = info.nonce
                return expected[signer]

            now = int(time.time())
            out: list[bytes] = []
            size = 0
            for tx in ordered:
                if len(out) >= max_txs:
                    break
                if tx.nonce != next_nonce(tx.signer):
                    continue
                if not self.state.simulate_tx(tx, now, self.state.height() + 1).success:
                    continue
                raw = by_hash[tx.hash()]
                if max_bytes > 0 and size + len(raw) > max_bytes:
                    continue
                out.append(raw)
                size += len(raw)
                expected[tx.signer] = tx.nonce + 1
            return out

    def finalize_txs(
        self, raws: list[bytes], height: int, now: int, proposer: str
    ) -> tuple[ledger.Block, list[ledger.Receipt]]:
        with self._lock:
            if self.halted:
                raise ledger.InvariantError(f"node is halted: {self.halted}")
            txs = [self.decode_tx(raw) for raw in raws]
            try:
                block, receipts = ledger.apply_ordered_block(
                    self.state, txs, now, height, proposer
                )
            except (ledger.ConservationError, ledger.InvariantError) as exc:
                self.halted = str(exc)
                raise
            self._pending = block
            return block, receipts

    def commit_block(self) -> None:
        with self._lock:
            block = self._pending
            if block is None:
                return
            try:
                snapshot = self.state.snapshot()
                commit = getattr(self.store, "save_commit", None)
                if commit is not None:
                    commit(block, snapshot)
                else:
                    self.store.save_block(block)
                    self.store.save_state(block.height, snapshot)
            except OSError as exc:
                self.halted = f"storage failure: {exc}"
                raise
            self.blocks[block.height] = block
            for index, tx_hash in enumerate(block.tx_hashes):
                if index < len(block.receipts):
                    self.receipts[tx_hash] = block.receipts[index]
            self._pending = None

    def produce_block(self, now: int | None = None) -> ledger.Block:
        with self._lock:
            if self.halted:
                raise ledger.InvariantError(f"node is halted: {self.halted}")
            now = int(time.time()) if now is None else now
            selected = self._select_txs(now)
            try:
                block, receipts = ledger.apply_block(
                    self.state, selected, now, self.state.height() + 1, self.cfg.proposer
                )
            except (ledger.ConservationError, ledger.InvariantError) as exc:
                self.halted = str(exc)
                raise
            applied = {r.hash for r in receipts if r.success}
            self.mempool = {
                h: tx for h, tx in self.mempool.items() if h not in applied
            }
            try:
                snapshot = self.state.snapshot()
                commit = getattr(self.store, "save_commit", None)
                if commit is not None:
                    commit(block, snapshot)
                else:
                    self.store.save_block(block)
                    self.store.save_state(block.height, snapshot)
            except OSError as exc:
                self.halted = f"storage failure: {exc}"
                raise
            self.blocks[block.height] = block
            for index, tx_hash in enumerate(block.tx_hashes):
                if index < len(receipts):
                    self.receipts[tx_hash] = receipts[index]
            self._broadcast(Event(
                type="block", height=block.height, time=block.time,
                hash=block.hash(), tx_hashes=list(block.tx_hashes),
            ))
            for index, receipt in enumerate(receipts):
                self._broadcast(Event(
                    type="transaction", height=block.height, time=block.time,
                    hash=receipt.hash, receipt=receipt.to_dict(), index=index,
                ))
            self._dispatch_receipts(block, receipts)
            return block

    def _dispatch_receipts(self, block: ledger.Block, receipts: list[ledger.Receipt]) -> None:
        if self.webhook_dispatcher is None:
            return
        block_hash = block.hash()
        for receipt in receipts:
            self.webhook_dispatcher.enqueue("transaction", {
                "block_height": block.height, "block_hash": block_hash,
                "time": block.time, "receipt": receipt.to_dict(),
            })

    @property
    def webhook_public_key(self) -> str:
        if self.webhook_dispatcher is None:
            return ""
        return self.webhook_dispatcher.public_key

    def close_webhooks(self) -> None:
        if self.webhook_dispatcher is not None:
            self.webhook_dispatcher.close()

    def _select_txs(self, now: int) -> list[types.Tx]:
        ordered = ledger.order_txs(list(self.mempool.values()))
        expected: dict[str, int] = {}

        def next_nonce(signer: str) -> int:
            if signer not in expected:
                info, _ = self.state.account(signer)
                expected[signer] = info.nonce
            return expected[signer]

        selected: list[types.Tx] = []
        for tx in ordered:
            if len(selected) >= self.cfg.max_block_txs:
                break
            if tx.nonce != next_nonce(tx.signer):
                continue
            if not self.state.simulate_tx(tx, now, self.state.height() + 1).success:
                continue
            selected.append(tx)
            expected[tx.signer] = tx.nonce + 1
        return selected

    # --- events ---

    def subscribe(self) -> Queue[Event]:
        queue: Queue[Event] = Queue(maxsize=64)
        with self._lock:
            self._subscribers.append(queue)
        return queue

    def unsubscribe(self, queue: Queue[Event]) -> None:
        with self._lock:
            if queue in self._subscribers:
                self._subscribers.remove(queue)

    def _broadcast(self, event: Event) -> None:
        for queue in self._subscribers:
            try:
                queue.put_nowait(event)
            except Full:
                pass  # slow consumer: drop rather than block production

    # --- devnet faucet ---

    def faucet(self, address: str) -> str:
        if self.cfg.faucet is None or not self.cfg.devnet:
            raise ledger.LedgerError(ledger.UNAUTHORIZED, "faucet is only available on devnet")
        from .address import validate_address

        validate_address(address, self.cfg.network)
        with self._lock:
            info, _ = self.state.account(self.cfg.faucet.address)
            native = types.native_asset(self.cfg.network)
            if info.balances.get(native, 0) <= self.cfg.faucet_amount:
                raise ledger.LedgerError(ledger.INSUFFICIENT_FUNDS, "faucet empty")
            now = int(time.time())
            tx = types.Tx(
                version=1, network=self.cfg.network, chain=self.cfg.chain,
                tx_type=types.TX_TRANSFER, signer=self.cfg.faucet.address,
                nonce=info.nonce, created_at=now, expires_at=now + 3600,
                fee_asset=native, max_fee=self.cfg.params.base_fee,
                payload={
                    "asset": native, "to": address,
                    "amount": self.cfg.faucet_amount,
                },
            )
            tx.sign(self.cfg.faucet)
        return self.submit_tx(cbor.encode(tx.to_cbor()))


def devnet_genesis(network: str, now: int, funder: KeyPair, faucet: KeyPair) -> ledger.Genesis:
    native = types.native_asset(network)
    arce = 100_000_000  # 1 ARCE at 8 decimals
    return ledger.Genesis(
        network=network,
        chain=types.chain_id(network),
        time=now,
        params=ledger.default_params(),
        assets=[types.Asset(
            id="usd:" + types.short_network(network), asset_class="fiat", symbol="USD",
            name="Demo US Dollar", decimals=2, policy="capped",
            max_supply=10_000_000_000, issuer=funder.address,
            metadata={"iso4217": "USD", "demo": "true"},
        )],
        allocations=[
            ledger.Allocation(funder.address, native, 1_000 * arce),
            ledger.Allocation(faucet.address, native, 10_000 * arce),
        ],
        pools={ledger.POOL_FEE_SWAP: {native: 1_000_000 * arce}},
    )
