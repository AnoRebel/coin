"""W3C Verifiable Credentials 2.0 with selective disclosure (SD-JWT).

Mirrors internal/compliance/vc. The issuer signs a payload whose claims are
salted and committed into ``_sd``; the holder presents only the disclosures a
verifier needs, so a jurisdiction check reveals no name or accreditation flag.
Nothing here writes to the ledger.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import time
from dataclasses import dataclass, field

from arne import keys

HEADER_JSON = '{"alg":"EdDSA","typ":"vc+sd-jwt"}'


class VCError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise VCError(code, message)


@dataclass(frozen=True)
class IssueOptions:
    issuer: str
    id: str
    type: str
    issued_at: int
    expires_at: int
    salts: dict[str, bytes] = field(default_factory=dict)


@dataclass(frozen=True)
class VerifyOptions:
    now: int | None = None
    require: dict[str, object] = field(default_factory=dict)
    revoked: tuple[str, ...] = ()
    disclose: tuple[str, ...] | None = None


@dataclass(frozen=True)
class Verified:
    issuer: str
    id: str
    type: str
    issued_at: int
    expires_at: int
    claims: dict[str, object]


def _b64(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def _unb64(raw: str) -> bytes:
    return base64.urlsafe_b64decode(raw + "=" * (-len(raw) % 4))


def _canonical(value: object) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def _normalize(value: object) -> object | None:
    if isinstance(value, bool) or isinstance(value, str):
        return value
    if isinstance(value, int):
        return value
    return None


def issue(claims: dict[str, object], opts: IssueOptions, key: keys.KeyPair) -> str:
    if not opts.issuer or not opts.id or not opts.type:
        _fail("invalid", "issuer, id, and type are required")
    if opts.expires_at <= opts.issued_at:
        _fail("invalid", "expires_at must be after issued_at")

    disclosures: list[str] = []
    digests: list[str] = []
    for name in sorted(claims):
        if not name:
            _fail("invalid", "empty claim name")
        value = _normalize(claims[name])
        if value is None:
            _fail("invalid", f"claim {name!r} must be a string, bool, or integer")
        salt = opts.salts.get(name) or os.urandom(16)
        if not 16 <= len(salt) <= 64:
            _fail("invalid", f"salt for {name!r} must be 16..64 bytes")
        disclosure = _canonical([_b64(salt), name, value])
        disclosures.append(disclosure)
        digests.append(_b64(hashlib.sha256(disclosure.encode()).digest()))

    payload = _canonical(
        {
            "_sd": digests,
            "exp": opts.expires_at,
            "iat": opts.issued_at,
            "iss": opts.issuer,
            "jti": opts.id,
            "vct": opts.type,
        }
    )
    signing = f"{_b64(HEADER_JSON.encode())}.{_b64(payload.encode())}"
    signature = key.sign(signing.encode())
    credential = f"{signing}.{_b64(signature)}"
    if disclosures:
        credential += "~" + "~".join(disclosures)
    return credential


def _disclosure_name(raw: str) -> str | None:
    try:
        parts = json.loads(raw)
    except ValueError:
        return None
    if not isinstance(parts, list) or len(parts) != 3 or not isinstance(parts[1], str):
        return None
    return parts[1] or None


def present(credential: str, *names: str) -> str:
    parts = credential.split("~")
    wanted = set(names)
    kept = [parts[0]]
    for raw in parts[1:]:
        if raw and _disclosure_name(raw) in wanted:
            kept.append(raw)
    return "~".join(kept)


def _digest_set(raw: object) -> set[str]:
    if not isinstance(raw, list):
        _fail("invalid", "_sd must be an array")
    result: set[str] = set()
    for item in raw:
        if not isinstance(item, str) or len(item) != 43:
            _fail("invalid", "_sd entry")
        try:
            _unb64(item)
        except ValueError:
            _fail("invalid", "_sd entry")
        if item in result:
            _fail("invalid", "duplicate _sd entry")
        result.add(item)
    return result


def _parse_disclosure(raw: str) -> tuple[str, object]:
    try:
        parts = json.loads(raw)
    except ValueError:
        _fail("invalid", "disclosure")
    if not isinstance(parts, list) or len(parts) != 3:
        _fail("invalid", "disclosure")
    salt, name, value = parts
    if not isinstance(salt, str):
        _fail("invalid", "disclosure salt")
    try:
        salt_bytes = _unb64(salt)
    except ValueError:
        _fail("invalid", "disclosure salt")
    if not 16 <= len(salt_bytes) <= 64:
        _fail("invalid", "disclosure salt")
    if not isinstance(name, str) or not name:
        _fail("invalid", "disclosure name")
    normalized = _normalize(value)
    if normalized is None:
        _fail("invalid", "disclosure value")
    return name, normalized


def verify(credential: str, issuer: bytes, opts: VerifyOptions) -> Verified:
    if len(issuer) != 32:
        _fail("invalid", "issuer key is required")
    parts = credential.split("~")
    segments = parts[0].split(".")
    if len(segments) != 3:
        _fail("invalid", "expected header.payload.signature")
    try:
        header = json.loads(_unb64(segments[0]))
        signature = _unb64(segments[2])
    except (ValueError, TypeError):
        _fail("invalid", "header or signature")
    if header != {"alg": "EdDSA", "typ": "vc+sd-jwt"}:
        _fail("invalid", "unsupported header")
    if len(signature) != 64:
        _fail("invalid", "signature")
    if not keys.verify_ed25519(
        issuer, f"{segments[0]}.{segments[1]}".encode(), signature
    ):
        _fail("bad signature", "issuer signature does not verify")
    try:
        payload = json.loads(_unb64(segments[1]))
    except ValueError:
        _fail("invalid", "payload")
    if not isinstance(payload, dict):
        _fail("invalid", "payload")
    for name in ("iss", "jti", "vct"):
        if not isinstance(payload.get(name), str) or not payload[name]:
            _fail("invalid", name)
    for name in ("iat", "exp"):
        if not isinstance(payload.get(name), int) or isinstance(payload[name], bool):
            _fail("invalid", name)
    issuer_id, credential_id, credential_type = payload["iss"], payload["jti"], payload["vct"]
    issued_at, expires_at = payload["iat"], payload["exp"]

    digests = _digest_set(payload.get("_sd"))
    claims: dict[str, object] = {}
    for raw in parts[1:]:
        if not raw:
            continue
        name, value = _parse_disclosure(raw)
        commitment = _b64(hashlib.sha256(raw.encode()).digest())
        if commitment not in digests:
            _fail("invalid", "unreferenced disclosure")
        digests.discard(commitment)
        if name in claims:
            _fail("invalid", f"duplicate claim {name!r}")
        claims[name] = value

    now = opts.now if opts.now is not None else int(time.time())
    if now >= expires_at:
        _fail("expired", "credential has expired")
    if credential_id in opts.revoked:
        _fail("revoked", "credential has been revoked")
    for name, want in opts.require.items():
        if name not in claims:
            _fail("required claim missing", name)
        if _normalize(claims[name]) != _normalize(want):
            _fail("required claim mismatch", name)

    if opts.disclose is None:
        visible = dict(claims)
    else:
        visible = {name: claims[name] for name in opts.disclose if name in claims}
    return Verified(
        issuer=issuer_id,
        id=credential_id,
        type=credential_type,
        issued_at=issued_at,
        expires_at=expires_at,
        claims=visible,
    )
