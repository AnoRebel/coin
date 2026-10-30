"""Strict arce:/arne: payment URI parsing and canonical rendering."""

from __future__ import annotations

import re
import time
from dataclasses import dataclass, field
from urllib.parse import parse_qs, quote_plus

from .address import validate_address
from .types import network_class, valid_asset_id

_AMOUNT = re.compile(r"^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,18})?$")
_SCHEME = re.compile(r"^[a-z]+$")
_EXEC_HINTS = ("<script", "<iframe", "javascript:", "data:text/html", "onerror=", "<svg")
_ALLOWED = {"network", "asset", "amount", "memo", "expires"}


class URIError(ValueError):
    pass


@dataclass
class Payment:
    scheme: str
    network: str
    to: str
    asset: str
    amount: str
    memo: str = ""
    expires: int = 0
    warnings: list[str] = field(default_factory=list)

    def __str__(self) -> str:
        values = {
            "network": self.network, "asset": self.asset, "amount": self.amount,
            "memo": self.memo, "expires": str(self.expires) if self.expires else "",
        }
        query = "&".join(
            f"{key}={quote_plus(value)}" for key, value in sorted(values.items()) if value
        )
        return f"{self.scheme}:{self.to}?{query}"

    def check_network(self, network: str) -> None:
        if network and network != self.network:
            raise URIError(f"uri network {self.network} conflicts with {network}")

    def check_recipient(self, to: str) -> None:
        if to and to != self.to:
            raise URIError(f"uri recipient {self.to} conflicts with {to}")

    def check_amount(self, amount: str) -> None:
        if amount and amount != self.amount:
            raise URIError(f"uri amount {self.amount} conflicts with {amount}")

    def summary(self) -> str:
        lines = [
            f"network: {self.network}", f"recipient: {self.to}",
            f"asset: {self.asset}", f"amount: {self.amount}",
        ]
        if self.memo:
            lines.append(f"memo: {self.memo!r}")
        if self.expires:
            lines.append(f"expires: {self.expires} ({time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime(self.expires))})")
        lines.extend(f"warning: {warning}" for warning in self.warnings)
        return "\n".join(lines)


def parse(raw: str, now: int | None = None) -> Payment:
    now = int(time.time()) if now is None else now
    raw = raw.strip()
    if not raw or "#" in raw:
        raise URIError("empty or fragment")
    scheme, sep, rest = raw.partition(":")
    if not sep or not _SCHEME.match(scheme):
        raise URIError(f"scheme {scheme!r}")
    if scheme not in ("arce", "arne"):
        raise URIError(f"unsupported scheme {scheme!r}")
    if rest.startswith("//"):
        raise URIError("authority is not allowed")
    to, _, query = rest.partition("?")
    if not to:
        raise URIError("recipient required")
    params = parse_qs(query, strict_parsing=bool(query), keep_blank_values=True)
    for key, values in params.items():
        if key not in _ALLOWED:
            raise URIError(f"unknown field {key!r}")
        if len(values) != 1:
            raise URIError(f"duplicate field {key!r}")

    def one(key: str) -> str:
        return params.get(key, [""])[0]

    payment = Payment(
        scheme=scheme, network=one("network"), to=to, asset=one("asset"),
        amount=one("amount"), memo=one("memo"),
    )
    if not payment.network:
        raise URIError("network required")
    try:
        network_class(payment.network)
    except ValueError as exc:
        raise URIError(str(exc)) from exc
    try:
        validate_address(payment.to, payment.network)
    except ValueError as exc:
        raise URIError(str(exc)) from exc
    if not valid_asset_id(payment.asset):
        raise URIError("asset")
    if not _AMOUNT.match(payment.amount) or not any(c in "123456789" for c in payment.amount):
        raise URIError("amount")
    if payment.memo:
        if len(payment.memo.encode()) > 256:
            raise URIError("memo")
        if any(c in payment.memo for c in "\x00\r\n"):
            raise URIError("memo control characters")
        hint = next((h for h in _EXEC_HINTS if h in payment.memo.lower()), "")
        if hint:
            payment.warnings.append(
                f"memo contains markup ({hint}); it is shown as inert text"
            )
    if "expires" in params:
        try:
            expires = int(one("expires"))
            if expires <= 0 or not one("expires").isdigit():
                raise ValueError
        except ValueError as exc:
            raise URIError("expires") from exc
        if expires <= now:
            raise URIError(f"expired at {expires}")
        payment.expires = expires
    return payment


__all__ = ["Payment", "URIError", "parse"]
