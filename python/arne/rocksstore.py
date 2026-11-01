"""RocksDB store for the Python node.

Uses the same key schema as internal/storage: ``b`` + 8-byte big-endian height
for blocks, ``meta/last`` for the latest height, ``state/current`` for the state
snapshot, and ``meta/schema`` for the schema version. Records are a SHA-256
checksum followed by canonical JSON, so corruption is detected on read.
"""

from __future__ import annotations

import base64
import hashlib
import json
from pathlib import Path

from rocksdict import Options, Rdict, WriteBatch, WriteOptions

from . import ledger
from .node import SCHEMA_VERSION

KEY_LAST = b"meta/last"
KEY_STATE = b"state/current"
KEY_SCHEMA = b"meta/schema"


def _checksum(payload: bytes) -> bytes:
    return hashlib.sha256(payload).digest()


def _encode(payload: dict) -> bytes:
    body = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    return _checksum(body) + body


def _decode(data: bytes) -> dict:
    if len(data) < 32 or _checksum(data[32:]) != data[:32]:
        raise ledger.InvariantError("storage: corrupt record")
    return json.loads(data[32:])


def block_key(height: int) -> bytes:
    return b"b" + height.to_bytes(8, "big")


class RocksStore:
    """Atomic RocksDB store. A commit writes block, height, and state together."""

    def __init__(self, directory: str | Path) -> None:
        path = Path(directory)
        path.mkdir(parents=True, exist_ok=True)
        options = Options()
        options.create_if_missing(True)
        self.db = Rdict(str(path), options)
        self._sync = WriteOptions()
        self._sync.sync = True
        self._open_schema()

    def _open_schema(self) -> None:
        raw = self.db.get(KEY_SCHEMA)
        version = int(raw) if raw is not None else 0
        if version > SCHEMA_VERSION:
            raise ledger.InvariantError(
                f"storage schema {version} is newer than this binary ({SCHEMA_VERSION})"
            )
        if raw is None:
            self.db.put(KEY_SCHEMA, str(SCHEMA_VERSION).encode(), write_opt=self._sync)

    def schema_version(self) -> int:
        raw = self.db.get(KEY_SCHEMA)
        return int(raw) if raw is not None else 0

    def _block_record(self, block: ledger.Block) -> dict:
        return {
            "height": block.height, "time": block.time, "proposer": block.proposer,
            "prev_root": block.prev_root, "state_root": block.state_root,
            "tx_root": block.tx_root, "tx_hashes": block.tx_hashes,
            "receipts": [r.to_dict() for r in block.receipts],
        }

    def _state_record(self, height: int, snapshot: dict[str, bytes]) -> dict:
        return {
            "height": height,
            "state": {k: base64.b64encode(v).decode() for k, v in snapshot.items()},
        }

    def _put_block(self, batch: WriteBatch, block: ledger.Block) -> None:
        batch.put(block_key(block.height), _encode(self._block_record(block)))

    def save_block(self, block: ledger.Block) -> None:
        batch = WriteBatch()
        self._put_block(batch, block)
        batch.put(KEY_LAST, json.dumps(block.height).encode())
        self.db.write(batch, self._sync)

    def save_state(self, height: int, snapshot: dict[str, bytes]) -> None:
        self.db.put(KEY_STATE, _encode(self._state_record(height, snapshot)), write_opt=self._sync)

    def save_commit(self, block: ledger.Block, snapshot: dict[str, bytes]) -> None:
        """Write the block, last height, and state snapshot in one batch."""
        batch = WriteBatch()
        self._put_block(batch, block)
        batch.put(KEY_LAST, json.dumps(block.height).encode())
        batch.put(KEY_STATE, _encode(self._state_record(block.height, snapshot)))
        self.db.write(batch, self._sync)

    def block(self, height: int) -> ledger.Block | None:
        raw = self.db.get(block_key(height))
        if raw is None:
            return None
        record = _decode(raw)
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
        raw = self.db.get(KEY_LAST)
        return self.block(json.loads(raw)) if raw is not None else None

    def load_state(self) -> tuple[dict[str, bytes], int] | None:
        raw = self.db.get(KEY_STATE)
        if raw is None:
            return None
        record = _decode(raw)
        return {k: base64.b64decode(v) for k, v in record["state"].items()}, record["height"]

    def close(self) -> None:
        self.db.close()
