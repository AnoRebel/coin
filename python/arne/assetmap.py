"""Versioned external-to-canonical asset mapping.

Mirrors internal/connector/assetmap. A mapping set is versioned so a resolution
can be audited against the table that was current at the time. Within a set a
normalized external reference may appear once; duplicates are ambiguous and are
refused rather than resolved by order.
"""

from __future__ import annotations

from dataclasses import dataclass, replace

EVM = "evm"
FIAT = "fiat"

_TX_ADDRESS_CHARS = set("0123456789abcdef")
_UPPER_ALPHA = set("ABCDEFGHIJKLMNOPQRSTUVWXYZ")


class AssetMapError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"assetmap: {code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise AssetMapError(code, message)


@dataclass(frozen=True)
class External:
    rail: str
    chain_id: int = 0
    address: str = ""
    scheme: str = ""
    code: str = ""

    def normalize(self) -> External:
        if self.rail == EVM:
            return replace(self, address=self.address.strip().lower())
        return replace(self, code=self.code.strip().upper())

    def key(self) -> str:
        value = self.normalize()
        if value.rail == EVM:
            return f"evm:{value.chain_id}:{value.address}"
        if value.rail == FIAT:
            return f"fiat:{value.scheme}:{value.code}"
        return value.rail

    def validate(self) -> None:
        if self.rail == EVM:
            if self.chain_id <= 0:
                _fail("invalid", "evm chain_id is required")
            if (
                len(self.address) != 42
                or not self.address.startswith("0x")
                or any(char not in _TX_ADDRESS_CHARS for char in self.address[2:])
            ):
                _fail("invalid", "evm address must be 0x followed by 40 hex characters")
            if self.scheme or self.code:
                _fail("invalid", "evm mapping has no scheme or code")
            return
        if self.rail == FIAT:
            if self.scheme != "iso4217":
                _fail("invalid", "fiat scheme must be iso4217")
            if len(self.code) != 3 or any(char not in _UPPER_ALPHA for char in self.code):
                _fail("invalid", "fiat code must be an ISO 4217 code")
            if self.chain_id or self.address:
                _fail("invalid", "fiat mapping has no chain or address")
            return
        _fail("invalid", f"rail {self.rail!r}")


@dataclass(frozen=True)
class Mapping:
    external: External
    canonical: str
    decimals: int
    since: int = 0
    merge_policy: str = ""


@dataclass(frozen=True)
class AssetSet:
    version: int
    mappings: tuple[Mapping, ...]
    index: dict[str, int]


def build_set(version: int, mappings: tuple[Mapping, ...]) -> AssetSet:
    if version == 0:
        _fail("invalid", "version starts at 1")
    if not mappings:
        _fail("invalid", "a set needs a mapping")
    index: dict[str, int] = {}
    canonical_seen: set[str] = set()
    normalized: list[Mapping] = []
    for position, mapping in enumerate(mappings):
        if not mapping.canonical.strip() or len(mapping.canonical) > 128:
            _fail("invalid", "canonical asset is required")
        if mapping.decimals > 36:
            _fail("invalid", "decimals must be at most 36")
        external = mapping.external.normalize()
        try:
            external.validate()
        except AssetMapError as exc:
            _fail("invalid", f"mapping {position}: {exc.code}: {exc}")
        key = external.key()
        if key in index:
            _fail("duplicate external reference", key)
        if mapping.canonical in canonical_seen and not mapping.merge_policy.strip():
            _fail(
                "ambiguous canonical asset",
                f"mapping {position} needs a merge policy for {mapping.canonical}",
            )
        if len(mapping.merge_policy) > 128:
            _fail("invalid", "merge policy must be at most 128 characters")
        canonical_seen.add(mapping.canonical)
        index[key] = position
        normalized.append(replace(mapping, external=external))
    return AssetSet(version, tuple(normalized), index)


class Registry:
    """Resolves against the newest applied set and keeps every prior set."""

    def __init__(self, version: int, mappings: tuple[Mapping, ...]) -> None:
        self._sets = [build_set(version, mappings)]

    def apply(self, version: int, mappings: tuple[Mapping, ...]) -> None:
        if version != self.current + 1:
            _fail("unexpected version", f"{version} after {self.current}")
        self._sets.append(build_set(version, mappings))

    @property
    def current(self) -> int:
        return self._sets[-1].version

    @property
    def versions(self) -> tuple[int, ...]:
        return tuple(entry.version for entry in self._sets)

    def resolve(self, external: External) -> Mapping:
        return self.resolve_version(self.current, external)

    def resolve_version(self, version: int, external: External) -> Mapping:
        for entry in self._sets:
            if entry.version != version:
                continue
            key = external.normalize().key()
            position = entry.index.get(key)
            if position is None:
                _fail("unknown external reference", key)
            return entry.mappings[position]
        _fail("unexpected version", f"{version} is not applied")

    def reverse(self, canonical: str) -> tuple[Mapping, ...]:
        return tuple(
            mapping for mapping in self._sets[-1].mappings if mapping.canonical == canonical
        )
