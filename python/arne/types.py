"""Canonical ARNE types. Byte-compatible with internal/protocol/types.go."""

from __future__ import annotations

import hashlib
import re
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any

from . import cbor

NATIVE_ASSET_SYMBOL = "ARCE"
CURRENT_TX_VERSION = 1
SUITE_ED25519 = "ed25519-v1"
CHECK_TX_WINDOW = 64

TX_TRANSFER = "transfer"
TX_BATCH = "batch"
TX_REGISTER_ASSET = "register_asset"
TX_CREATE_ESCROW = "create_escrow"
TX_RELEASE_ESCROW = "release_escrow"
TX_REFUND_ESCROW = "refund_escrow"
TX_APPROVE = "approve_allowance"
TX_TRANSFER_FROM = "transfer_from"
TX_REVOKE_ALLOWANCE = "revoke_allowance"
TX_MINT = "mint"
TX_BURN = "burn"

TX_TYPES = {
    TX_TRANSFER, TX_BATCH, TX_REGISTER_ASSET, TX_CREATE_ESCROW, TX_RELEASE_ESCROW,
    TX_REFUND_ESCROW, TX_APPROVE, TX_TRANSFER_FROM, TX_REVOKE_ALLOWANCE, TX_MINT, TX_BURN,
}

ESCROW_OPEN = "open"
ESCROW_RELEASED = "released"
ESCROW_REFUNDED = "refunded"

_NETWORK_RE = re.compile(r"^arne:(mainnet|testnet|devnet|consortium/[a-z0-9][a-z0-9-]{1,31})$")
_ASSET_ID_RE = re.compile(r"^[a-z][a-z0-9]{1,15}:[a-z0-9][a-z0-9/-]{0,63}$")
_SYMBOL_RE = re.compile(r"^[A-Z][A-Z0-9]{1,15}$")
_ADDR_RE = re.compile(r"^arce(-testnet|-devnet|-consortium)?1[02-9ac-hj-np-z]{38,60}$")

ASSET_CLASSES = {
    "native", "fiat", "crypto", "stablecoin", "commodity", "security", "point", "other",
}

# Standards metadata keys and their shapes. Unknown keys are ignored so issuers
# can attach their own references without a protocol change.
_META_PATTERNS = {
    "iso4217": re.compile(r"^[A-Z]{3}$"),
    "iso24165": re.compile(r"^[A-Z0-9]{9,32}$"),
    "iso17442": re.compile(r"^[A-Z0-9]{20}$"),
    "ucum": re.compile(r"^[A-Za-z0-9\[\]{}._/%-]+$"),
}
SUPPLY_POLICIES = {"fixed", "capped", "mintable", "burnable"}


class ValidationError(ValueError):
    pass


class SignatureError(ValueError):
    pass


def network_class(network: str) -> str:
    match = _NETWORK_RE.match(network)
    if not match:
        raise ValidationError(f"network id {network!r}")
    value = match.group(1)
    return "consortium" if value.startswith("consortium/") else value


def short_network(network: str) -> str:
    return network[5:] if network.startswith("arne:") else network


def chain_id(network: str) -> str:
    klass = network_class(network)
    if klass == "consortium":
        suffix = network[len("arne:consortium/") :].replace("/", "-")
        return f"arne-consortium-{suffix}-1"
    return f"arne-{klass}-1"


def native_asset(network: str) -> str:
    return "arce:" + short_network(network)


def asset_id(symbol: str, network: str) -> str:
    return symbol.lower() + ":" + short_network(network)


def hash_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


@dataclass
class Asset:
    id: str
    asset_class: str
    symbol: str
    name: str
    decimals: int
    policy: str
    max_supply: int
    issuer: str = ""
    metadata: dict[str, str] = field(default_factory=dict)
    registered: int = 0

    def validate(self, network: str) -> None:
        if not _ASSET_ID_RE.match(self.id):
            raise ValidationError(f"asset id {self.id!r}")
        if not self.id.endswith(":" + short_network(network)):
            raise ValidationError("asset id outside network")
        if self.asset_class not in ASSET_CLASSES:
            raise ValidationError(f"asset class {self.asset_class!r}")
        if not _SYMBOL_RE.match(self.symbol):
            raise ValidationError(f"symbol {self.symbol!r}")
        if not self.name or len(self.name) > 64:
            raise ValidationError("name length")
        if self.decimals < 0 or self.decimals > 18:
            raise ValidationError("decimals")
        if self.policy not in SUPPLY_POLICIES:
            raise ValidationError(f"policy {self.policy!r}")
        if self.policy in ("fixed", "capped") and self.max_supply == 0:
            raise ValidationError("policy requires max_supply")
        if self.asset_class == "native" and self.id != native_asset(network):
            raise ValidationError("native asset id")
        for key, pattern in _META_PATTERNS.items():
            value = self.metadata.get(key)
            if value is not None and not pattern.match(value):
                raise ValidationError(f"malformed {key} metadata {value!r}")

    def to_cbor(self) -> dict[str, Any]:
        return {
            "id": self.id, "class": self.asset_class, "symbol": self.symbol,
            "name": self.name, "decimals": self.decimals, "policy": self.policy,
            "max_supply": self.max_supply, "issuer": self.issuer,
            "meta": dict(self.metadata), "registered": self.registered,
        }

    @classmethod
    def from_cbor(cls, m: Mapping[str, Any]) -> Asset:
        meta = m.get("meta") or {}
        if not isinstance(meta, dict):
            raise ValidationError("meta")
        return cls(
            id=_text(m, "id"), asset_class=_text(m, "class"), symbol=_text(m, "symbol"),
            name=_text(m, "name"), decimals=_uint(m, "decimals"), policy=_text(m, "policy"),
            max_supply=_uint(m, "max_supply"),
            issuer=str(m.get("issuer", "")), metadata={k: str(v) for k, v in meta.items()},
            registered=int(m.get("registered", 0)),
        )


@dataclass
class Escrow:
    id: str
    payer: str
    beneficiary: str
    arbiter: str
    asset: str
    amount: int
    timeout: int
    status: str
    created_at: int

    def to_cbor(self) -> dict[str, Any]:
        return {
            "id": self.id, "payer": self.payer, "beneficiary": self.beneficiary,
            "arbiter": self.arbiter, "asset": self.asset, "amount": self.amount,
            "timeout": self.timeout, "status": self.status, "created_at": self.created_at,
        }

    @classmethod
    def from_cbor(cls, m: Mapping[str, Any]) -> Escrow:
        return cls(
            id=_text(m, "id"), payer=_text(m, "payer"), beneficiary=_text(m, "beneficiary"),
            arbiter=_text(m, "arbiter"), asset=_text(m, "asset"), amount=_uint(m, "amount"),
            timeout=_uint(m, "timeout"), status=_text(m, "status"), created_at=_uint(m, "created_at"),
        )


@dataclass
class Signature:
    suite: str
    pubkey: bytes
    sig: bytes

    def to_cbor(self) -> dict[str, Any]:
        return {"suite": self.suite, "pubkey": self.pubkey, "sig": self.sig}

    @classmethod
    def from_cbor(cls, m: Mapping[str, Any]) -> Signature:
        _reject_unknown("signature", m, _SIGNATURE_FIELDS)
        return cls(suite=_text(m, "suite"), pubkey=_bytes(m, "pubkey"), sig=_bytes(m, "sig"))


@dataclass
class Tx:
    version: int
    network: str
    chain: str
    tx_type: str
    signer: str
    nonce: int
    created_at: int
    expires_at: int
    fee_asset: str
    max_fee: int
    payload: dict[str, Any] = field(default_factory=dict)
    priority_fee: int = 0
    signatures: list[Signature] = field(default_factory=list)

    def body(self) -> dict[str, Any]:
        return {
            "v": self.version, "network": self.network, "chain": self.chain,
            "type": self.tx_type, "signer": self.signer, "nonce": self.nonce,
            "created_at": self.created_at, "expires_at": self.expires_at,
            "fee_asset": self.fee_asset, "max_fee": self.max_fee,
            "priority_fee": self.priority_fee, "payload": dict(self.payload),
        }

    def to_cbor(self) -> dict[str, Any]:
        m = self.body()
        m["signatures"] = [s.to_cbor() for s in self.signatures]
        return m

    @classmethod
    def from_cbor(cls, m: Mapping[str, Any]) -> Tx:
        _reject_unknown("envelope", m, _ENVELOPE_FIELDS)
        payload = m.get("payload") or {}
        if not isinstance(payload, dict):
            raise ValidationError("payload")
        signatures = []
        for item in m.get("signatures") or []:
            signatures.append(Signature.from_cbor(item))
        return cls(
            version=_uint(m, "v"), network=_text(m, "network"), chain=_text(m, "chain"),
            tx_type=_text(m, "type"), signer=_text(m, "signer"), nonce=_uint(m, "nonce"),
            created_at=_uint(m, "created_at"), expires_at=_uint(m, "expires_at"),
            fee_asset=_text(m, "fee_asset"), max_fee=_uint(m, "max_fee"),
            priority_fee=_uint(m, "priority_fee"), payload=dict(payload), signatures=signatures,
        )

    def validate(self) -> None:
        if self.version != CURRENT_TX_VERSION:
            raise ValidationError("unsupported version")
        network_class(self.network)
        if chain_id(self.network) != self.chain:
            raise ValidationError(f"chain {self.chain!r}")
        if self.tx_type not in TX_TYPES:
            raise ValidationError(f"tx type {self.tx_type!r}")
        if not _ADDR_RE.match(self.signer):
            raise ValidationError("signer address")
        if self.expires_at and self.expires_at < self.created_at:
            raise ValidationError("expires_at before created_at")
        if not self.fee_asset:
            raise ValidationError("fee_asset required")
        if self.max_fee > 1_000_000_000_000:
            raise ValidationError("max_fee too large")

    def hash(self) -> str:
        return hash_hex(cbor.encode(self.to_cbor()))

    def signing_payload(self, suite: str, purpose: str) -> bytes:
        return cbor.encode({
            "dom": "arne-sig-v1", "network": self.network, "chain": self.chain,
            "suite": suite, "purpose": purpose, "expires_at": self.expires_at,
            "body": self.body(),
        })

    def sign(self, keypair: Any, purpose: str = "tx") -> None:
        payload = self.signing_payload(SUITE_ED25519, purpose)
        self.signatures.append(Signature(SUITE_ED25519, keypair.public, keypair.sign(payload)))

    def verify(self) -> None:
        from .keys import verify_ed25519

        if not self.signatures:
            raise SignatureError("missing signature")
        for sig in self.signatures:
            if sig.suite != SUITE_ED25519:
                raise SignatureError(f"unsupported suite {sig.suite!r}")
            if len(sig.pubkey) != 32 or len(sig.sig) != 64:
                raise SignatureError("malformed key or signature")
            from .address import address_from_pubkey

            if address_from_pubkey(sig.pubkey, self.network) != self.signer:
                raise SignatureError("signer does not match key")
            if not verify_ed25519(sig.pubkey, self.signing_payload(sig.suite, "tx"), sig.sig):
                raise SignatureError("bad signature")


# Envelope and signature maps are closed in v1: a field not listed here needs a
# version bump. The payload map stays open because each transaction type owns it.
_ENVELOPE_FIELDS = frozenset({
    "v", "network", "chain", "type", "signer", "nonce", "created_at",
    "expires_at", "fee_asset", "max_fee", "priority_fee", "payload", "signatures",
})
_SIGNATURE_FIELDS = frozenset({"suite", "pubkey", "sig"})


def _reject_unknown(kind: str, m: Mapping[str, Any], allowed: frozenset[str]) -> None:
    unknown = sorted(set(m) - allowed)
    if unknown:
        raise ValidationError(f"unknown {kind} field {unknown[0]!r}")


def _text(m: Mapping[str, Any], key: str) -> str:
    if key not in m or not isinstance(m[key], str):
        raise ValidationError(f"missing {key!r}")
    return m[key]


def _uint(m: Mapping[str, Any], key: str) -> int:
    if key not in m:
        raise ValidationError(f"missing {key!r}")
    value = m[key]
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValidationError(f"{key!r} must be unsigned")
    return value


def _bytes(m: Mapping[str, Any], key: str) -> bytes:
    if key not in m or not isinstance(m[key], (bytes, bytearray)):
        raise ValidationError(f"missing {key!r}")
    return bytes(m[key])
