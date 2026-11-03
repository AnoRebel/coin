"""Connector handshake: shared vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import connector

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "connector.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


class FakeAdapter:
    def __init__(self, raw: dict) -> None:
        self._name = raw["name"]
        self._versions = tuple(raw["versions"])
        self._assets = tuple(raw["assets"])
        self._health = connector.Health(raw["health"])

    def name(self) -> str:
        return self._name

    def versions(self) -> tuple[int, ...]:
        return self._versions

    def assets(self) -> tuple[str, ...]:
        return self._assets

    def health(self) -> connector.Health:
        return self._health


def test_shared_vectors() -> None:
    for case in _load()["cases"]:
        adapter = FakeAdapter(case["connector"])
        request = connector.Request(
            versions=tuple(case["request"]["versions"]),
            assets=tuple(case["request"]["assets"]),
            network=case["request"].get("network", ""),
        )
        expect = case["expect"]
        if expect.get("error"):
            with pytest.raises(connector.ConnectorError, match=expect["error"]):
                connector.negotiate(adapter, request)
            continue
        session = connector.negotiate(adapter, request)
        assert session.connector == expect["connector"], case["name"]
        assert session.version == expect["version"], case["name"]
        assert list(session.assets) == expect["assets"], case["name"]
        assert session.network == expect.get("network", ""), case["name"]
