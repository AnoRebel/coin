"""FATF, MiCA, and ISO 20022 reference profiles.

Mirrors internal/compliance/profile. Profiles are selectable field sets plus a
disclaimer; validation reports the first missing or malformed field.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from urllib.parse import urlparse

FATF = "fatf"
MICA = "mica"
ISO20022 = "iso20022"

DEFAULT_FATF_THRESHOLD = 1_000

_DTI = re.compile(r"^[A-Z0-9]{9,32}$")
_LEI = re.compile(r"^[A-Z0-9]{20}$")
_UETR = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
_HASH = re.compile(r"^[0-9a-f]{64}$")
_PURPOSE = re.compile(r"^[A-Z]{1,4}$")
_REF = re.compile(r"^[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]{1,512}$")


class ProfileError(Exception):
    pass


class ValidationError(ProfileError):
    def __init__(self, profile: str, field_name: str, message: str) -> None:
        super().__init__(f"{profile}: {field_name}: {message}")
        self.profile = profile
        self.field = field_name
        self.message = message


@dataclass(frozen=True)
class Profile:
    kind: str
    title: str
    disclaimer: str
    required: tuple[str, ...]
    optional: tuple[str, ...]
    retention_days: int = 0


_PROFILES = {
    FATF: Profile(
        kind=FATF,
        title="FATF Travel Rule reference",
        disclaimer=(
            "Reference profile only. Thresholds, record-keeping, and Travel Rule obligations "
            "vary by jurisdiction; the deployer remains responsible for legal compliance."
        ),
        required=("originator_name", "originator_account", "beneficiary_name", "beneficiary_account"),
        optional=("originator_address", "beneficiary_address", "counterparty_vasp", "purpose"),
        retention_days=1825,
    ),
    MICA: Profile(
        kind=MICA,
        title="MiCA crypto-asset reference",
        disclaimer=(
            "Reference profile only. It is not legal advice and does not make a deployment "
            "compliant with MiCA; the deployer remains responsible for registrations, white "
            "papers, and disclosures."
        ),
        required=("iso24165", "iso17442", "white_paper"),
        optional=("white_paper_hash", "casp_lei", "order_book_venue"),
    ),
    ISO20022: Profile(
        kind=ISO20022,
        title="ISO 20022 payment reference",
        disclaimer=(
            "Field mapping is informational. ISO 20022 message construction, validation, and "
            "scheme compliance remain the deployer's responsibility."
        ),
        required=("uetr", "end_to_end_id", "instruction_id"),
        optional=("purpose_code", "remittance_information"),
    ),
}


def get(kind: str) -> Profile | None:
    return _PROFILES.get(kind)


def all_profiles() -> list[Profile]:
    return [_PROFILES[FATF], _PROFILES[MICA], _PROFILES[ISO20022]]


@dataclass
class Config:
    network: str
    profiles: list[str] = field(default_factory=list)
    fatf_threshold: int = DEFAULT_FATF_THRESHOLD
    jurisdiction_threshold: dict[str, int] = field(default_factory=dict)

    def validate(self) -> None:
        if not self.network.strip():
            raise ProfileError("network is required")
        seen: set[str] = set()
        for kind in self.profiles:
            if kind not in _PROFILES:
                raise ProfileError(f"unknown profile {kind!r}")
            if kind in seen:
                raise ProfileError(f"duplicate profile {kind!r}")
            seen.add(kind)
        if not seen:
            raise ProfileError("at least one profile is required")
        if self.fatf_threshold <= 0:
            raise ProfileError("fatf_threshold must be positive")
        for jurisdiction, threshold in self.jurisdiction_threshold.items():
            if not jurisdiction.strip() or threshold <= 0:
                raise ProfileError(f"bad jurisdiction threshold {jurisdiction!r}")

    def enabled(self, kind: str) -> bool:
        return kind in self.profiles

    def selected(self) -> list[Profile]:
        return [_PROFILES[kind] for kind in self.profiles]

    def disclaimers(self) -> list[str]:
        return [p.disclaimer for p in self.selected()]

    def threshold(self, jurisdiction: str) -> int:
        return self.jurisdiction_threshold.get(jurisdiction, self.fatf_threshold)

    def validate_asset(self, metadata: dict[str, str]) -> None:
        if not self.enabled(MICA):
            return
        _require(MICA, metadata, _PROFILES[MICA].required)
        if not _DTI.match(metadata["iso24165"]):
            raise ValidationError(MICA, "iso24165", "must be a 9-32 character ISO 24165 DTI")
        if not _LEI.match(metadata["iso17442"]):
            raise ValidationError(MICA, "iso17442", "must be a 20 character ISO 17442 LEI")
        paper = metadata["white_paper"]
        parsed = urlparse(paper)
        if len(paper) > 512 or not _REF.match(paper) or parsed.scheme not in ("http", "https") or not parsed.netloc:
            raise ValidationError(MICA, "white_paper", "must be an http(s) reference up to 512 characters")
        if "white_paper_hash" in metadata and not _HASH.match(metadata["white_paper_hash"]):
            raise ValidationError(MICA, "white_paper_hash", "must be 64 lowercase hex characters")
        if "casp_lei" in metadata and not _LEI.match(metadata["casp_lei"]):
            raise ValidationError(MICA, "casp_lei", "must be a 20 character ISO 17442 LEI")

    def validate_transfer(self, jurisdiction: str, amount: int, fields: dict[str, str]) -> None:
        if not self.enabled(FATF) or amount <= self.threshold(jurisdiction):
            return
        _require(FATF, fields, _PROFILES[FATF].required)

    def validate_payment(self, fields: dict[str, str]) -> None:
        if not self.enabled(ISO20022):
            return
        _require(ISO20022, fields, _PROFILES[ISO20022].required)
        if not _UETR.match(fields["uetr"]):
            raise ValidationError(ISO20022, "uetr", "must be a lowercase UUIDv4 UETR")
        for name in ("end_to_end_id", "instruction_id"):
            if not 1 <= len(fields[name]) <= 35:
                raise ValidationError(ISO20022, name, "must be 1-35 characters")
        if "purpose_code" in fields and not _PURPOSE.match(fields["purpose_code"]):
            raise ValidationError(ISO20022, "purpose_code", "must be 1-4 uppercase letters")
        if "remittance_information" in fields and len(fields["remittance_information"]) > 140:
            raise ValidationError(ISO20022, "remittance_information", "must be at most 140 characters")


def _require(kind: str, values: dict[str, str], fields: tuple[str, ...]) -> None:
    for name in fields:
        if not values.get(name, "").strip():
            raise ValidationError(kind, name, "missing required field")
    if kind == FATF:
        for name in ("originator_name", "beneficiary_name"):
            if len(values[name]) > 140:
                raise ValidationError(kind, name, "must be at most 140 characters")
        for name in ("originator_account", "beneficiary_account"):
            if len(values[name]) > 35:
                raise ValidationError(kind, name, "must be at most 35 characters")
