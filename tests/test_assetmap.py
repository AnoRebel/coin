"""Asset mapping: versioned snapshots, normalization, ambiguity, reversibility."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import assetmap

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "assetmap.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _external(raw: dict) -> assetmap.External:
    return assetmap.External(
        rail=raw.get("rail", ""),
        chain_id=raw.get("chain_id", 0),
        address=raw.get("address", ""),
        scheme=raw.get("scheme", ""),
        code=raw.get("code", ""),
    )


def _mapping(raw: dict) -> assetmap.Mapping:
    return assetmap.Mapping(
        external=_external(raw["external"]),
        canonical=raw["canonical"],
        decimals=raw["decimals"],
        since=raw.get("since", 0),
        merge_policy=raw.get("merge_policy", ""),
    )


def _run(registry: assetmap.Registry, case: dict):
    kind = case["kind"]
    if kind == "resolve":
        return registry.resolve(_external(case["external"]))
    if kind == "resolve_version":
        return registry.resolve_version(case["version"], _external(case["external"]))
    if kind == "reverse":
        return registry.reverse(case["canonical"])
    if kind == "apply":
        return registry.apply(case["version"], tuple(_mapping(m) for m in case["mappings"]))
    if kind == "new_set":
        return assetmap.Registry(case["version"], tuple(_mapping(m) for m in case["mappings"]))
    raise AssertionError(f"unknown kind {kind}")


def test_shared_vectors() -> None:
    doc = _load()
    registry = assetmap.Registry(doc["version"], tuple(_mapping(m) for m in doc["mappings"]))

    for case in doc["cases"]:
        expect = case.get("expect", {})
        if expect.get("error"):
            with pytest.raises(assetmap.AssetMapError, match=expect["error"]):
                _run(registry, case)
            continue
        result = _run(registry, case)
        if case["kind"] == "reverse":
            assert len(result) == expect["count"], case["name"]
        elif case["kind"] in ("resolve", "resolve_version"):
            assert result.canonical == expect["canonical"], case["name"]
            assert result.decimals == expect["decimals"], case["name"]


def test_apply_requires_sequential_versions() -> None:
    doc = _load()
    registry = assetmap.Registry(doc["version"], tuple(_mapping(m) for m in doc["mappings"]))
    mappings = tuple(_mapping(m) for m in doc["mappings"])
    with pytest.raises(assetmap.AssetMapError, match="unexpected version"):
        registry.apply(3, mappings)
    registry.apply(2, mappings)
    with pytest.raises(assetmap.AssetMapError, match="unexpected version"):
        registry.apply(2, mappings)
    assert registry.versions == (1, 2)


def test_failed_apply_leaves_registry_unchanged() -> None:
    doc = _load()
    registry = assetmap.Registry(doc["version"], tuple(_mapping(m) for m in doc["mappings"]))
    bad = (assetmap.Mapping(external=assetmap.External(rail="swift"), canonical="x", decimals=0),)
    with pytest.raises(assetmap.AssetMapError, match="rail"):
        registry.apply(2, bad)
    assert registry.current == 1
