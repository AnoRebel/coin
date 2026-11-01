"""Provider-agnostic sanctions screening.

Mirrors internal/compliance/sanctions. A potential match is a hold for review,
never an automatic freeze. A provider error is a deny (fail closed).
"""

from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass, field
from typing import Protocol

ALLOW = "allow"
HOLD = "hold"
DENY = "deny"

CLEAR = "clear"
POTENTIAL_MATCH = "potential_match"
UNAVAILABLE = "unavailable"

_WHITESPACE = re.compile(r"\s+")


class SanctionsError(Exception):
    pass


class InvalidList(SanctionsError):
    pass


class Unavailable(SanctionsError):
    pass


@dataclass
class Subject:
    address: str = ""
    name: str = ""
    jurisdictions: list[str] = field(default_factory=list)


@dataclass
class Match:
    provider: str
    list_version: str
    result: str
    matches: list[str] = field(default_factory=list)
    score: int = 0


@dataclass
class Decision:
    outcome: str
    provider: str
    list_version: str
    result: str
    reason: str
    matches: list[str] = field(default_factory=list)
    score: int = 0
    requires_review: bool = False

    def to_dict(self) -> dict[str, object]:
        out: dict[str, object] = {
            "outcome": self.outcome, "provider": self.provider,
            "list_version": self.list_version, "result": self.result,
            "reason": self.reason, "requires_review": self.requires_review,
        }
        if self.matches:
            out["matches"] = list(self.matches)
        if self.score:
            out["score"] = self.score
        return out


class Provider(Protocol):
    def name(self) -> str: ...

    def screen(self, subject: Subject) -> Match: ...


class Screener:
    def __init__(self, provider: Provider) -> None:
        if provider is None:
            raise InvalidList("provider is required")
        self.provider = provider

    def screen(self, subject: Subject) -> Decision:
        try:
            match = self.provider.screen(subject)
        except Exception as exc:  # fail closed on any provider failure
            return Decision(
                outcome=DENY, provider=self.provider.name(), list_version="",
                result=UNAVAILABLE, reason=f"screening unavailable: {exc}",
            )
        decision = Decision(
            outcome=ALLOW, provider=self.provider.name(),
            list_version=match.list_version, result=match.result,
            reason="no match", matches=list(match.matches), score=match.score,
        )
        if match.result == CLEAR:
            return decision
        if match.result == POTENTIAL_MATCH:
            decision.outcome = HOLD
            decision.reason = "potential match requires review"
            decision.requires_review = True
            return decision
        decision.outcome = DENY
        decision.result = UNAVAILABLE
        decision.reason = "provider returned an unknown result"
        return decision


@dataclass
class Entry:
    id: str = ""
    address: str = ""
    name: str = ""
    aliases: list[str] = field(default_factory=list)


@dataclass
class List:
    name: str
    version: str
    updated_at: int
    entries: list[Entry]


class ListProvider:
    def __init__(self, sanctions_list: List) -> None:
        if not sanctions_list.name.strip():
            raise InvalidList("name is required")
        if not sanctions_list.version.strip():
            raise InvalidList("version is required")
        if not sanctions_list.entries:
            raise InvalidList("at least one entry is required")
        self.list = sanctions_list
        self._entries: list[Entry] = []
        self._by_id: dict[str, Entry] = {}
        for entry in sanctions_list.entries:
            if not entry.address.strip() and not entry.name.strip():
                raise InvalidList("entry needs an address or a name")
            if not entry.id:
                entry.id = _entry_id(entry)
            if entry.id in self._by_id:
                raise InvalidList(f"duplicate entry id {entry.id!r}")
            self._by_id[entry.id] = entry
            self._entries.append(entry)

    def name(self) -> str:
        return "list:" + self.list.name

    def version(self) -> str:
        return self.list.version

    def screen(self, subject: Subject) -> Match:
        address = _normalize_address(subject.address)
        name = _normalize_name(subject.name)
        matches: list[str] = []
        score = 0
        for entry in self._entries:
            hit = False
            entry_score = 0
            if address and _normalize_address(entry.address) == address:
                hit, entry_score = True, 100
            if not hit and name:
                for candidate in [entry.name, *entry.aliases]:
                    if candidate and _normalize_name(candidate) == name:
                        hit, entry_score = True, 80
                        break
            if hit and entry.id not in matches:
                matches.append(entry.id)
                score = max(score, entry_score)
        matches.sort()
        return Match(
            provider=self.name(), list_version=self.list.version,
            result=POTENTIAL_MATCH if matches else CLEAR,
            matches=matches, score=score,
        )


class UnavailableProvider:
    def __init__(self, provider_name: str = "unavailable", reason: str = "") -> None:
        self.provider_name = provider_name
        self.reason = reason

    def name(self) -> str:
        return self.provider_name or "unavailable"

    def screen(self, subject: Subject) -> Match:
        raise Unavailable(self.reason or "no provider configured")


def _normalize_address(value: str) -> str:
    return value.strip().lower()


def _normalize_name(value: str) -> str:
    return _WHITESPACE.sub(" ", value.strip().lower())


def _entry_id(entry: Entry) -> str:
    digest = hashlib.sha256(
        b"arne-sanctions-entry-v1"
        + b"\x00" + _normalize_address(entry.address).encode()
        + b"\x00" + _normalize_name(entry.name).encode()
    ).digest()
    return digest[:8].hex()
