"""Scoped, time-bounded auditor access grants.

Mirrors internal/compliance/access. A grant names one auditor, the assets they
may read, and a half-open window [from, until). Every check is logged, whether
it succeeds or fails.
"""

from __future__ import annotations

import hashlib
import json
import os
import tempfile
import threading
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

NO_GRANT = "no-grant"
OUT_OF_SCOPE = "out-of-scope"
EXPIRED = "expired"
NOT_YET_ACTIVE = "not-yet-active"
ALLOWED = "allowed"


class AccessError(Exception):
    pass


class InvalidGrant(AccessError):
    pass


class GrantExists(AccessError):
    pass


class UnknownGrant(AccessError):
    pass


@dataclass
class Grant:
    auditor: str
    assets: list[str]
    from_ts: int
    until: int
    purpose: str
    id: str = ""

    def to_dict(self) -> dict[str, Any]:
        out = asdict(self)
        out["from"] = out.pop("from_ts")
        return out

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> Grant:
        return cls(
            auditor=raw.get("auditor", ""), assets=list(raw.get("assets", [])),
            from_ts=int(raw.get("from", 0)), until=int(raw.get("until", 0)),
            purpose=raw.get("purpose", ""), id=raw.get("id", ""),
        )


@dataclass
class Event:
    seq: int
    time: int
    auditor: str
    action: str
    result: str
    grant: str = ""
    asset: str = ""
    purpose: str = ""

    def to_dict(self) -> dict[str, Any]:
        out = {
            "seq": self.seq, "time": self.time, "auditor": self.auditor,
            "action": self.action, "result": self.result,
        }
        if self.grant:
            out["grant"] = self.grant
        if self.asset:
            out["asset"] = self.asset
        if self.purpose:
            out["purpose"] = self.purpose
        return out


class Registry:
    """Grants plus an append-only access log."""

    def __init__(self, path: str = "") -> None:
        self.path = path
        self.log_path = path + ".log.jsonl" if path else ""
        self._grants: dict[str, Grant] = {}
        self._events: list[Event] = []
        self._lock = threading.Lock()

    @classmethod
    def load(cls, path: str) -> Registry:
        registry = cls(path)
        file_path = Path(path)
        if file_path.exists():
            raw = json.loads(file_path.read_text(encoding="utf-8"))
            for item in raw.get("grants", []):
                grant = Grant.from_dict(item)
                _validate(grant)
                grant.id = grant.id or _grant_id(grant)
                if grant.id in registry._grants:
                    raise InvalidGrant(f"duplicate grant {grant.id}")
                registry._grants[grant.id] = grant
        registry._load_log()
        return registry

    def grant(self, grant: Grant) -> Grant:
        grant.auditor = grant.auditor.strip()
        grant.purpose = grant.purpose.strip()
        grant.assets = _normalize_assets(grant.assets)
        _validate(grant)
        grant.id = _grant_id(grant)
        with self._lock:
            if grant.id in self._grants:
                raise GrantExists(f"grant {grant.id} already exists")
            self._grants[grant.id] = grant
            try:
                self._save()
            except Exception:
                del self._grants[grant.id]
                raise
            self._append(Event(0, 0, grant.auditor, "grant", "created",
                               grant=grant.id, purpose=grant.purpose))
        return grant

    def revoke(self, grant_id: str) -> None:
        with self._lock:
            grant = self._grants.get(grant_id)
            if grant is None:
                raise UnknownGrant(grant_id)
            del self._grants[grant_id]
            try:
                self._save()
            except Exception:
                self._grants[grant_id] = grant
                raise
            self._append(Event(0, 0, grant.auditor, "revoke", "revoked",
                               grant=grant.id, purpose=grant.purpose))

    def list(self, auditor: str = "") -> list[dict[str, Any]]:
        with self._lock:
            return [
                g.to_dict() for g in sorted(self._grants.values(), key=lambda g: g.id)
                if not auditor or g.auditor == auditor
            ]

    def check(self, auditor: str, asset: str, at: int) -> Grant:
        auditor, asset = auditor.strip(), asset.strip()
        with self._lock:
            candidates = sorted(
                (g for g in self._grants.values() if g.auditor == auditor),
                key=lambda g: g.id,
            )
            result, reason, grant_id = NO_GRANT, "no-grant", ""
            if candidates:
                result, reason = OUT_OF_SCOPE, "asset outside grant scope"
            for grant in candidates:
                if asset not in grant.assets:
                    continue
                grant_id = grant.id
                if at < grant.from_ts:
                    result, reason = NOT_YET_ACTIVE, "grant not yet active"
                elif at >= grant.until:
                    result, reason = EXPIRED, "grant expired"
                else:
                    self._append(Event(0, 0, auditor, "access", ALLOWED,
                                       grant=grant.id, asset=asset, purpose=grant.purpose))
                    return grant
            self._append(Event(0, 0, auditor, "access", result, grant=grant_id, asset=asset))
            raise AccessError(reason)

    def events(self, after: int = 0, limit: int = 100) -> list[dict[str, Any]]:
        limit = 100 if limit <= 0 else min(limit, 1000)
        with self._lock:
            return [
                e.to_dict() for e in self._events if e.seq > after
            ][:limit]

    def _append(self, event: Event) -> None:
        event.seq = self._events[-1].seq + 1 if self._events else 1
        if not event.time:
            event.time = int(time.time())
        self._events.append(event)
        if not self.log_path:
            return
        with open(self.log_path, "a", encoding="utf-8") as f:
            f.write(json.dumps(event.to_dict(), separators=(",", ":")) + "\n")
            f.flush()
            os.fsync(f.fileno())

    def _load_log(self) -> None:
        path = Path(self.log_path) if self.log_path else None
        if path is None or not path.exists():
            return
        for line in path.read_text(encoding="utf-8").splitlines():
            raw = json.loads(line)
            self._events.append(Event(
                seq=raw["seq"], time=raw["time"], auditor=raw["auditor"],
                action=raw["action"], result=raw["result"],
                grant=raw.get("grant", ""), asset=raw.get("asset", ""),
                purpose=raw.get("purpose", ""),
            ))

    def _save(self) -> None:
        if not self.path:
            return
        grants = [g.to_dict() for g in sorted(self._grants.values(), key=lambda g: g.id)]
        raw = json.dumps({"grants": grants}, indent=2) + "\n"
        fd, tmp = tempfile.mkstemp(dir=str(Path(self.path).parent), prefix=".grants-")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                f.write(raw)
                f.flush()
                os.fsync(f.fileno())
            os.chmod(tmp, 0o600)
            os.replace(tmp, self.path)
        finally:
            if os.path.exists(tmp):
                os.remove(tmp)


def _validate(grant: Grant) -> None:
    if not grant.auditor:
        raise InvalidGrant("auditor is required")
    if not grant.assets:
        raise InvalidGrant("at least one asset is required")
    if len(grant.assets) > 32:
        raise InvalidGrant("at most 32 assets per grant")
    if grant.from_ts < 0 or grant.until <= 0 or grant.until <= grant.from_ts:
        raise InvalidGrant("window must satisfy 0 <= from < until")
    if not grant.purpose or len(grant.purpose) > 256:
        raise InvalidGrant("purpose must be 1-256 characters")


def _grant_id(grant: Grant) -> str:
    parts = [
        grant.auditor, grant.purpose,
        str(grant.from_ts), str(grant.until), "\x00".join(sorted(grant.assets)),
    ]
    digest = hashlib.sha256(b"arne-grant-v1" + b"".join(
        b"\x00" + part.encode() for part in parts
    )).digest()
    return digest[:16].hex()


def _normalize_assets(assets: list[str]) -> list[str]:
    seen: set[str] = set()
    out: list[str] = []
    for asset in assets:
        asset = asset.strip()
        if not asset or asset in seen:
            continue
        seen.add(asset)
        out.append(asset)
    return sorted(out)
