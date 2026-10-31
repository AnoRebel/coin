"""Workspace roles, permissions, and audit log (server-side enforcement).

Mirrors internal/workspace. A workspace file maps API keys to member IDs and
member IDs to roles. Membership changes are written back atomically and every
privileged attempt is appended to "<path>.audit.jsonl".
"""

from __future__ import annotations

import json
import os
import tempfile
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

OWNER = "owner"
ADMIN = "admin"
MEMBER = "member"
AUDITOR = "auditor"

CHANNEL_READ = "channel.read"
CHANNEL_WRITE = "channel.write"
CHANNEL_MANAGE = "channel.manage"
PAYMENT_READ = "payment.read"
PAYMENT_WRITE = "payment.write"
MEMBER_READ = "member.read"
MEMBER_MANAGE = "member.manage"
SETTINGS = "settings.manage"
AUDIT_READ = "audit.read"

ALL_ACTIONS = (
    CHANNEL_READ, CHANNEL_WRITE, CHANNEL_MANAGE,
    PAYMENT_READ, PAYMENT_WRITE, MEMBER_READ, MEMBER_MANAGE, SETTINGS, AUDIT_READ,
)

PERMISSIONS: dict[str, frozenset[str]] = {
    OWNER: frozenset(ALL_ACTIONS),
    ADMIN: frozenset({
        CHANNEL_READ, CHANNEL_WRITE, CHANNEL_MANAGE,
        PAYMENT_READ, PAYMENT_WRITE, MEMBER_READ, MEMBER_MANAGE, AUDIT_READ,
    }),
    MEMBER: frozenset({CHANNEL_READ, CHANNEL_WRITE, PAYMENT_READ, PAYMENT_WRITE, MEMBER_READ}),
    AUDITOR: frozenset({CHANNEL_READ, PAYMENT_READ, MEMBER_READ, AUDIT_READ}),
}


class WorkspaceError(Exception):
    pass


class Forbidden(WorkspaceError):
    pass


class UnknownMember(WorkspaceError):
    pass


class LastOwner(WorkspaceError):
    pass


@dataclass
class Entry:
    seq: int
    time: int
    actor: str
    action: str
    target: str
    result: str
    detail: str = ""

    def to_dict(self) -> dict[str, Any]:
        out = {
            "seq": self.seq, "time": self.time, "actor": self.actor,
            "action": self.action, "result": self.result,
        }
        if self.target:
            out["target"] = self.target
        if self.detail:
            out["detail"] = self.detail
        return out


class Workspace:
    """Membership, role checks, and an append-only audit log."""

    def __init__(
        self,
        members: dict[str, str],
        principals: dict[str, str],
        audit_path: str = "",
        path: str = "",
    ) -> None:
        if not members:
            raise WorkspaceError("at least one owner is required")
        for member_id, role in members.items():
            if not member_id or role not in PERMISSIONS:
                raise WorkspaceError(f"invalid role {role!r} for {member_id!r}")
        if not any(role == OWNER for role in members.values()):
            raise WorkspaceError("at least one owner is required")
        for key, member_id in principals.items():
            if not key or member_id not in members:
                raise WorkspaceError(f"principal maps to unknown member {member_id!r}")
        self.path = path
        self.audit_path = audit_path
        self._members = dict(members)
        self._principals = dict(principals)
        self._entries: list[Entry] = []
        self._lock = threading.Lock()
        if audit_path:
            Path(audit_path).parent.mkdir(parents=True, exist_ok=True)

    @classmethod
    def load(cls, path: str) -> Workspace:
        raw = json.loads(Path(path).read_text(encoding="utf-8"))
        workspace = cls(
            raw.get("members", {}), raw.get("principals", {}),
            audit_path=path + ".audit.jsonl", path=path,
        )
        workspace._load_audit()
        return workspace

    def actor(self, key: str) -> str | None:
        with self._lock:
            return self._principals.get(key)

    def role_of(self, member_id: str) -> str | None:
        with self._lock:
            return self._members.get(member_id)

    def members(self) -> list[dict[str, str]]:
        with self._lock:
            return [
                {"id": member_id, "role": role}
                for member_id, role in sorted(self._members.items())
            ]

    def authorize(self, member_id: str, action: str) -> bool:
        with self._lock:
            return action in PERMISSIONS.get(self._members.get(member_id, ""), frozenset())

    def require(self, member_id: str, action: str, target: str) -> None:
        if self.authorize(member_id, action):
            return
        self._append(Entry(0, 0, member_id, action, target, "denied"))
        raise Forbidden(f"role cannot {action}")

    def audit(self, actor: str, action: str, target: str, result: str, detail: str = "") -> None:
        self._append(Entry(0, 0, actor, action, target, result, detail))

    def set_role(self, actor: str, member_id: str, role: str) -> None:
        if not member_id or role not in PERMISSIONS:
            raise WorkspaceError(f"invalid role {role!r}")
        with self._lock:
            self._manage(actor, member_id, role)
            previous = self._members.get(member_id)
            self._members[member_id] = role
            try:
                self._save()
            except Exception:
                if previous is None:
                    self._members.pop(member_id, None)
                else:
                    self._members[member_id] = previous
                raise
            detail = "added" if previous is None else f"{previous} -> {role}"
            self._append_locked(
                Entry(0, 0, actor, MEMBER_MANAGE, member_id, "ok", detail)
            )

    def remove(self, actor: str, member_id: str) -> None:
        with self._lock:
            self._manage(actor, member_id, "")
            if member_id not in self._members:
                raise UnknownMember(member_id)
            if self._members[member_id] == OWNER and self._owners() == 1:
                raise LastOwner(member_id)
            previous = self._members.pop(member_id)
            removed = {k: v for k, v in self._principals.items() if v == member_id}
            for key in removed:
                del self._principals[key]
            try:
                self._save()
            except Exception:
                self._members[member_id] = previous
                self._principals.update(removed)
                raise
            self._append_locked(
                Entry(0, 0, actor, MEMBER_MANAGE, member_id, "ok", "removed")
            )

    def entries(self, after: int = 0, limit: int = 100) -> list[dict[str, Any]]:
        limit = 100 if limit <= 0 else min(limit, 1000)
        with self._lock:
            return [
                entry.to_dict() for entry in self._entries
                if entry.seq > after
            ][:limit]

    def _manage(self, actor: str, member_id: str, role: str) -> None:
        actor_role = self._members.get(actor)
        if actor_role is None:
            raise Forbidden("unknown actor")
        if MEMBER_MANAGE not in PERMISSIONS[actor_role]:
            self._append_locked(
                Entry(0, 0, actor, MEMBER_MANAGE, member_id, "denied")
            )
            raise Forbidden("role cannot manage members")
        target_role = self._members.get(member_id)
        owner_target = target_role == OWNER or role == OWNER
        if owner_target and actor_role != OWNER:
            self._append_locked(
                Entry(0, 0, actor, MEMBER_MANAGE, member_id, "denied",
                      "owner change requires owner")
            )
            raise Forbidden("owner change requires owner")
        if target_role == OWNER and role != OWNER and self._owners() == 1:
            raise LastOwner(member_id)

    def _owners(self) -> int:
        return sum(1 for role in self._members.values() if role == OWNER)

    def _append(self, entry: Entry) -> None:
        with self._lock:
            self._append_locked(entry)

    def _append_locked(self, entry: Entry) -> None:
        entry.seq = self._entries[-1].seq + 1 if self._entries else 1
        entry.time = int(time.time())
        self._entries.append(entry)
        if not self.audit_path:
            return
        with open(self.audit_path, "a", encoding="utf-8") as f:
            f.write(json.dumps(entry.to_dict(), separators=(",", ":")) + "\n")
            f.flush()
            os.fsync(f.fileno())

    def _load_audit(self) -> None:
        path = Path(self.audit_path)
        if not self.audit_path or not path.exists():
            return
        for line in path.read_text(encoding="utf-8").splitlines():
            raw = json.loads(line)
            self._entries.append(Entry(
                seq=raw["seq"], time=raw["time"], actor=raw["actor"],
                action=raw["action"], target=raw.get("target", ""),
                result=raw["result"], detail=raw.get("detail", ""),
            ))

    def _save(self) -> None:
        if not self.path:
            return
        raw = json.dumps(
            {"members": self._members, "principals": self._principals},
            indent=2, sort_keys=True,
        ) + "\n"
        directory = str(Path(self.path).parent)
        fd, tmp = tempfile.mkstemp(dir=directory, prefix=".workspace-")
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
