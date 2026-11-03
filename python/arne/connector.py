"""Connector handshake: versions, assets, and health.

Mirrors internal/connector. An adapter advertises the protocol versions and
canonical assets it speaks and reports health. negotiate() picks the highest
shared version and the required assets, and refuses an unhealthy adapter.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

HEALTHY = "healthy"
DEGRADED = "degraded"
UNHEALTHY = "unhealthy"


class ConnectorError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise ConnectorError(code, message)


@dataclass(frozen=True)
class Health:
    status: str
    detail: str = ""


@dataclass(frozen=True)
class Request:
    versions: tuple[int, ...]
    assets: tuple[str, ...]
    network: str = ""


@dataclass(frozen=True)
class Session:
    connector: str
    version: int
    assets: tuple[str, ...]
    network: str = ""


class Adapter(Protocol):
    def name(self) -> str: ...

    def versions(self) -> tuple[int, ...]: ...

    def assets(self) -> tuple[str, ...]: ...

    def health(self) -> Health: ...


def _clean_versions(raw: tuple[int, ...]) -> list[int]:
    seen: set[int] = set()
    out: list[int] = []
    for version in raw:
        if version == 0:
            _fail("invalid", "protocol versions start at 1")
        if version in seen:
            _fail("duplicate", f"version {version}")
        seen.add(version)
        out.append(version)
    out.sort()
    return out


def _clean_assets(raw: tuple[str, ...]) -> list[str]:
    seen: set[str] = set()
    out: list[str] = []
    for asset in raw:
        if not asset:
            _fail("invalid", "empty asset")
        if asset in seen:
            _fail("duplicate", f"asset {asset!r}")
        seen.add(asset)
        out.append(asset)
    out.sort()
    return out


def negotiate(adapter: Adapter, request: Request) -> Session:
    name = adapter.name()
    if not name:
        _fail("invalid", "adapter name is required")
    versions = _clean_versions(tuple(adapter.versions()))
    if not versions:
        _fail("invalid", "no protocol versions")
    adapter_assets = _clean_assets(tuple(adapter.assets()))
    if not adapter_assets:
        _fail("invalid", "no assets")
    if not request.versions:
        _fail("invalid", "request needs a version")
    try:
        wanted = _clean_assets(tuple(request.assets))
    except ConnectorError:
        wanted = []
    if not wanted:
        _fail("invalid", "request needs an asset")

    health = adapter.health()
    if health.status == UNHEALTHY:
        _fail("unhealthy", name)
    if health.status not in (HEALTHY, DEGRADED):
        _fail("invalid", f"health status {health.status!r}")

    common = sorted(set(versions) & set(request.versions))
    if not common:
        _fail("unsupported protocol version", name)
    assets = tuple(asset for asset in adapter_assets if asset in set(wanted))
    if not assets:
        _fail("unsupported asset", name)
    return Session(name, common[-1], assets, request.network)
