"""Connector transport security: allowlists, mTLS credentials, rotation, redaction.

Mirrors internal/connector/security. The TLS handshake belongs to the
transport; this module makes the authorization decision after it and refuses a
peer that presented no client certificate.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field, replace
from urllib.parse import quote_plus, unquote_plus, urlsplit

REDACTED = "REDACTED"

_IDENTIFIER = re.compile(r"^[A-Za-z0-9_.:-]{1,128}$")
_LOWER_HEX = set("0123456789abcdef")

_SENSITIVE_QUERY = {
    "token", "access_token", "api_key", "apikey", "key", "secret",
    "client_secret", "password", "passwd", "sig", "signature", "auth",
}
_SENSITIVE_HEADERS = {
    "authorization", "proxy-authorization", "cookie", "set-cookie",
    "x-api-key", "x-arne-signature", "x-auth-token",
}


class ConnectorSecurityError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"connector security: {code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise ConnectorSecurityError(code, message)


class Allowlist:
    def __init__(self, hosts: list[str]) -> None:
        self.exact: set[str] = set()
        self.wildcard: list[str] = []
        for raw in hosts:
            host = raw.strip().lower()
            if not host:
                _fail("invalid", "empty host")
            if any(char in host for char in "/:@ "):
                _fail("invalid", f"host {raw!r} must be a bare hostname")
            if host.startswith("*."):
                suffix = host[2:]
                if not suffix or "*" in suffix:
                    _fail("invalid", f"wildcard {raw!r}")
                if suffix in self.wildcard:
                    _fail("duplicate", f"duplicate host {raw!r}")
                self.wildcard.append(suffix)
                continue
            if "*" in host:
                _fail("invalid", f"wildcard {raw!r} must be the whole label")
            if host in self.exact:
                _fail("duplicate", f"duplicate host {raw!r}")
            self.exact.add(host)
        if not self.exact and not self.wildcard:
            _fail("invalid", "allowlist is empty")

    def allows(self, host: str) -> bool:
        host = host.strip().lower()
        if host.endswith("."):
            host = host[:-1]
        if host in self.exact:
            return True
        return any(
            host.endswith("." + suffix) and len(host) > len(suffix) + 1
            for suffix in self.wildcard
        )


@dataclass(frozen=True)
class Endpoint:
    scheme: str
    host: str
    port: str = ""
    path: str = ""


def parse_endpoint(raw: str, allow: Allowlist | None = None) -> Endpoint:
    try:
        parsed = urlsplit(raw)
    except ValueError:
        _fail("endpoint rejected", f"invalid URL {raw!r}")
    try:
        port = str(parsed.port) if parsed.port else ""
    except ValueError:
        _fail("endpoint rejected", f"invalid port in {raw!r}")
    if parsed.scheme != "https":
        _fail("endpoint rejected", "scheme must be https")
    if "@" in parsed.netloc:
        _fail("endpoint rejected", "credentials must not appear in the URL")
    if parsed.fragment:
        _fail("endpoint rejected", "fragment is not allowed")
    host = (parsed.hostname or "").lower()
    if not host or any(char.isspace() for char in host):
        _fail("endpoint rejected", "host is required")
    if allow is not None and not allow.allows(host):
        _fail("host is not allowlisted", host)
    if port:
        _port(port)
    return Endpoint("https", host, port, parsed.path)


def _port(value: str) -> None:
    if not value.isdigit() or len(value) > 5:
        _fail("endpoint rejected", f"invalid port {value!r}")
    if not 1 <= int(value) <= 65535:
        _fail("endpoint rejected", f"port {value!r} out of range")


@dataclass(frozen=True)
class Credential:
    id: str
    fingerprint: str
    not_before: int
    not_after: int = 0

    def validate(self) -> None:
        if not _IDENTIFIER.match(self.id):
            _fail("invalid", "id must be 1..128 characters of [A-Za-z0-9_.:-]")
        if len(self.fingerprint) != 64 or any(
            char not in _LOWER_HEX for char in self.fingerprint
        ):
            _fail("invalid", "fingerprint must be 64 lowercase hex characters")
        if self.not_before < 0 or self.not_after < 0:
            _fail("invalid", "validity times must not be negative")
        if self.not_after and self.not_after <= self.not_before:
            _fail("invalid", "not_after must be after not_before")


@dataclass
class Registry:
    by_id: dict[str, Credential] = field(default_factory=dict)
    by_fp: dict[str, Credential] = field(default_factory=dict)
    retire_at: dict[str, int] = field(default_factory=dict)

    def register(self, credential: Credential) -> None:
        credential.validate()
        if credential.id in self.by_id:
            _fail("duplicate", f"id {credential.id!r}")
        if credential.fingerprint in self.by_fp:
            _fail("duplicate", "fingerprint")
        self.by_id[credential.id] = credential
        self.by_fp[credential.fingerprint] = credential

    def rotate(
        self, old_id: str, next_credential: Credential, now: int, overlap: int
    ) -> None:
        if overlap < 0:
            _fail("invalid", "overlap must not be negative")
        if now < 0:
            _fail("invalid", "now must not be negative")
        old = self.by_id.get(old_id)
        if old is None:
            _fail("unknown credential", f"id {old_id!r}")
        if old_id in self.retire_at:
            _fail("wrong credential state", f"{old_id!r} is already retired")
        if next_credential.id == old.id:
            _fail("invalid", "the new credential needs a new id")
        if next_credential.fingerprint == old.fingerprint:
            _fail("invalid", "the new credential needs a new fingerprint")
        if next_credential.not_before == 0:
            next_credential = replace(next_credential, not_before=now)
        if next_credential.not_after and next_credential.not_after <= next_credential.not_before:
            _fail("invalid", "not_after must be after not_before")
        self.register(next_credential)
        self.retire_at[old_id] = now + overlap

    def revoke(self, credential_id: str, at: int) -> None:
        if at < 0:
            _fail("invalid", "at must not be negative")
        if credential_id not in self.by_id:
            _fail("unknown credential", f"id {credential_id!r}")
        if credential_id in self.retire_at:
            _fail("wrong credential state", f"{credential_id!r} is already retired")
        self.retire_at[credential_id] = at

    def authorize(self, fingerprint: str, now: int) -> Credential:
        if not fingerprint:
            _fail("client certificate required", "no fingerprint")
        credential = self.by_fp.get(fingerprint)
        if credential is None:
            _fail("unknown credential", "fingerprint")
        if now < credential.not_before:
            _fail(
                "not yet valid",
                f"{credential.id} valid from {credential.not_before}",
            )
        if credential.not_after and now >= credential.not_after:
            _fail("expired", f"{credential.id} expired at {credential.not_after}")
        retired = self.retire_at.get(credential.id)
        if retired is not None and now >= retired:
            _fail("retired", f"{credential.id} retired at {retired}")
        return credential

    def retires_at(self, credential_id: str) -> int | None:
        return self.retire_at.get(credential_id)


def redact_url(raw: str) -> str:
    try:
        parsed = urlsplit(raw)
        port = str(parsed.port) if parsed.port else ""
    except ValueError:
        return "[invalid-url]"
    if not parsed.scheme or not parsed.hostname:
        return "[invalid-url]"

    host = parsed.hostname.lower()
    if any(char.isspace() for char in host):
        return "[invalid-url]"
    output = f"{parsed.scheme.lower()}://"
    output += f"[{host}]" if ":" in host else host
    if port:
        output += f":{port}"
    output += parsed.path

    if parsed.query:
        values: dict[str, list[str]] = {}
        for pair in parsed.query.split("&"):
            if not pair:
                continue
            key, _, value = pair.partition("=")
            key = unquote_plus(key)
            values.setdefault(key, []).append(unquote_plus(value))
        pairs = []
        for key in sorted(values):
            if key.lower() in _SENSITIVE_QUERY:
                value = REDACTED
            else:
                value = ",".join(values[key])
            pairs.append(f"{quote_plus(key)}={quote_plus(value)}")
        output += "?" + "&".join(pairs)
    return output


def redact_headers(headers: dict[str, str]) -> dict[str, str]:
    return {
        key: REDACTED if key.lower() in _SENSITIVE_HEADERS else value
        for key, value in headers.items()
    }


def redact_account(account: str) -> str:
    if len(account) < 8:
        return "****"
    return "****" + account[-4:]
