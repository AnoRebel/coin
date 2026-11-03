"""Travel Rule collection and authenticated exchange.

Mirrors internal/compliance/travelrule. Data stays off-chain: this module
validates an IVMS101-shaped transfer, signs an envelope with the sender's
Ed25519 key, and hands it to a caller-supplied authenticated transport. A
transport failure means the counterparty is unavailable; the caller holds or
rejects the transfer according to policy.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

from arne import cbor, keys

VERSION = "ivms101"
SIGNATURE_DOMAIN = b"arne-travelrule-v1"


class TravelRuleError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise TravelRuleError(code, message)


@dataclass(frozen=True)
class Party:
    name: str
    account: str
    address: str = ""
    country: str = ""


@dataclass(frozen=True)
class VASP:
    id: str
    name: str = ""


@dataclass(frozen=True)
class Transfer:
    network: str
    asset: str
    amount: int
    reference: str
    originator: Party
    beneficiary: Party
    originating_vasp: VASP
    beneficiary_vasp: VASP


@dataclass(frozen=True)
class Policy:
    threshold: int = 0

    def requires(self, amount: int) -> bool:
        """Strictly above the threshold; exactly at it does not qualify."""
        return amount > self.threshold


EXCHANGED = "exchanged"
BELOW_THRESHOLD = "below_threshold"
UNAVAILABLE = "unavailable"


@dataclass(frozen=True)
class Decision:
    required: bool
    status: str
    threshold: int
    reason: str
    at: int
    detail: str = ""


@dataclass(frozen=True)
class Envelope:
    version: str
    sender: str
    transfer: Transfer
    signature: str


class Transport(Protocol):
    def send(self, envelope: Envelope) -> None: ...


def _text(field_name: str, value: str, minimum: int, maximum: int) -> None:
    if not value.strip():
        _fail("invalid", f"{field_name} is required")
    if not minimum <= len(value) <= maximum:
        _fail("invalid", f"{field_name} must be {minimum}..{maximum} characters")


def _party(label: str, party: Party) -> None:
    _text(f"{label}.name", party.name, 1, 100)
    _text(f"{label}.account", party.account, 1, 100)
    if party.address:
        _text(f"{label}.address", party.address, 1, 200)
    if party.country and (
        len(party.country) != 2 or not all("A" <= c <= "Z" for c in party.country)
    ):
        _fail("invalid", f"{label}.country must be ISO 3166-1 alpha-2")


def _vasp(label: str, vasp: VASP) -> None:
    _text(f"{label}.id", vasp.id, 1, 64)
    if vasp.name:
        _text(f"{label}.name", vasp.name, 1, 100)


def validate(transfer: Transfer) -> None:
    _text("network", transfer.network, 1, 64)
    _text("asset", transfer.asset, 1, 128)
    _text("reference", transfer.reference, 1, 128)
    if transfer.amount <= 0:
        _fail("invalid", "amount must be positive")
    _party("originator", transfer.originator)
    _party("beneficiary", transfer.beneficiary)
    _vasp("originating_vasp", transfer.originating_vasp)
    _vasp("beneficiary_vasp", transfer.beneficiary_vasp)


def _party_map(party: Party) -> dict:
    value = {"name": party.name, "account": party.account}
    if party.address:
        value["address"] = party.address
    if party.country:
        value["country"] = party.country
    return value


def _vasp_map(vasp: VASP) -> dict:
    value = {"id": vasp.id}
    if vasp.name:
        value["name"] = vasp.name
    return value


def message(transfer: Transfer) -> bytes:
    import hashlib

    encoded = cbor.encode(
        {
            "network": transfer.network,
            "asset": transfer.asset,
            "amount": transfer.amount,
            "reference": transfer.reference,
            "originator": _party_map(transfer.originator),
            "beneficiary": _party_map(transfer.beneficiary),
            "originating_vasp": _vasp_map(transfer.originating_vasp),
            "beneficiary_vasp": _vasp_map(transfer.beneficiary_vasp),
        }
    )
    digest = hashlib.sha256()
    digest.update(SIGNATURE_DOMAIN)
    digest.update(b"\x00")
    digest.update(encoded)
    return digest.digest()


def seal(transfer: Transfer, key: keys.KeyPair) -> Envelope:
    validate(transfer)
    signature = key.sign(message(transfer))
    return Envelope(VERSION, key.public.hex(), transfer, signature.hex())


def open_envelope(envelope: Envelope) -> Transfer:
    if envelope.version != VERSION:
        _fail("invalid", f"version {envelope.version!r}")
    try:
        sender = bytes.fromhex(envelope.sender)
        signature = bytes.fromhex(envelope.signature)
    except ValueError:
        _fail("invalid", "sender or signature is not hex")
    if len(sender) != 32:
        _fail("invalid", "sender public key")
    if len(signature) != 64:
        _fail("invalid", "signature")
    if not keys.verify_ed25519(sender, message(envelope.transfer), signature):
        _fail("bad signature", "envelope signature does not verify")
    validate(envelope.transfer)
    return envelope.transfer


def decide(policy: Policy, transfer: Transfer, at: int = 0) -> Decision:
    validate(transfer)
    if policy.requires(transfer.amount):
        return Decision(True, "", policy.threshold, "amount above threshold", at)
    return Decision(False, BELOW_THRESHOLD, policy.threshold, "amount at or below threshold", at)


def exchange(
    policy: Policy,
    transfer: Transfer,
    key: keys.KeyPair,
    transport: Transport | None,
    at: int = 0,
) -> tuple[Decision, Envelope | None]:
    decision = decide(policy, transfer, at)
    if not decision.required:
        return decision, None
    envelope = seal(transfer, key)
    if transport is None:
        return (
            Decision(
                True, UNAVAILABLE, decision.threshold,
                "counterparty unavailable", at, "no transport",
            ),
            envelope,
        )
    try:
        transport.send(envelope)
    except Exception as exc:  # transport-defined failure: hold the transfer
        return (
            Decision(
                True, UNAVAILABLE, decision.threshold,
                "counterparty unavailable", at, str(exc),
            ),
            envelope,
        )
    return (
        Decision(True, EXCHANGED, decision.threshold, "data exchanged", at),
        envelope,
    )
