"""Human-readable transaction preview with non-recipient-controlled warnings."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Input:
    network: str
    signer: str
    to: str
    asset: str
    amount: int
    fee: int
    symbol: str = ""
    decimals: int = 0
    memo: str = ""
    public_memo: bool = False
    asset_known: bool = False
    known: list[str] = field(default_factory=list)
    simulated: bool = False
    code: str = ""


@dataclass
class Preview:
    network: str
    signer: str
    to: str
    asset: str
    amount: str
    fee: str
    memo: str
    warnings: list[str]
    ok: bool

    def summary(self) -> str:
        lines = [
            f"network: {self.network}", f"from: {self.signer}", f"to: {self.to}",
            f"asset: {self.asset}", f"amount: {self.amount}", f"fee: {self.fee}",
        ]
        if self.memo:
            lines.append(f"memo: {self.memo!r}")
        lines.extend(f"warning: {warning}" for warning in self.warnings)
        return "\n".join(lines)


def _format(units: int, decimals: int) -> str:
    if decimals == 0:
        return str(units)
    scale = 10**decimals
    whole, frac = divmod(units, scale)
    digits = str(frac).rjust(decimals, "0").rstrip("0")
    return str(whole) if not digits else f"{whole}.{digits}"


def _lookalike(addr: str, known: list[str]) -> str:
    a = addr.lower()
    for candidate in known:
        b = candidate.lower()
        if not b or b == a:
            continue
        prefix = 0
        while prefix < len(a) and prefix < len(b) and a[prefix] == b[prefix]:
            prefix += 1
        suffix = 0
        while suffix < len(a) and suffix < len(b) and a[-1 - suffix] == b[-1 - suffix]:
            suffix += 1
        if prefix >= 6 and suffix >= 6:
            return candidate
    return ""


def build(data: Input) -> Preview:
    amount = _format(data.amount, data.decimals)
    warnings: list[str] = []
    if not data.asset_known or not data.symbol:
        warnings.append(f"asset {data.asset} is not registered on {data.network}")
    else:
        amount += f" {data.symbol}"
    if data.to.lower() == data.signer.lower():
        warnings.append("recipient is your own address")
    if known := _lookalike(data.to, data.known):
        warnings.append(f"recipient resembles known address {known}; verify the full address")
    if data.public_memo and data.memo:
        warnings.append("memo is public on chain; use a sealed memo for sensitive text")
    if data.simulated and data.code:
        warnings.append(f"transaction would fail: {data.code}")
    return Preview(
        network=data.network, signer=data.signer, to=data.to, asset=data.asset,
        amount=amount, fee=f"{_format(data.fee, 8)} ARCE", memo=data.memo,
        warnings=warnings, ok=data.asset_known and bool(data.symbol) and not data.code,
    )


__all__ = ["Input", "Preview", "build"]
