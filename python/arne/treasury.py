"""Treasury balances, category limits, and reconciling flow reports.

Mirrors internal/governance/treasury.go. Amounts are integer base units; a
treasury never invents a balance.
"""

from __future__ import annotations

import hashlib
from collections.abc import Callable
from dataclasses import dataclass, field

from arne import governance

IN = "in"
OUT = "out"


@dataclass(frozen=True)
class Entry:
    id: str
    asset: str
    amount: int
    direction: str
    purpose: str
    source: str = ""
    recipient: str = ""
    category: str = ""
    proposal: str = ""
    at: int = 0


@dataclass
class Report:
    from_: int
    to: int
    inflows: dict[str, int] = field(default_factory=dict)
    outflows: dict[str, int] = field(default_factory=dict)
    categories: dict[str, int] = field(default_factory=dict)
    opening: dict[str, int] = field(default_factory=dict)
    closing: dict[str, int] = field(default_factory=dict)


def entry_id(
    proposal: str, direction: str, asset: str, amount: int, at: int, sequence: int
) -> str:
    h = hashlib.sha256()
    for part in (
        "arne-treasury-v1", proposal, direction, asset,
        str(amount), str(at), str(sequence),
    ):
        h.update(part.encode())
        h.update(b"\x00")
    return h.digest()[:16].hex()


class TreasuryBook:
    """Balances, cumulative category limits, and the flow journal."""

    def __init__(self) -> None:
        self.balances: dict[str, int] = {}
        self.limits: dict[str, dict[str, int]] = {}
        self.entries: list[Entry] = []

    def set_budget(self, category: str, asset: str, limit: int) -> None:
        if not category or not asset:
            governance._fail("invalid", "category and asset are required")
        self.limits.setdefault(category, {})[asset] = limit

    def deposit(self, asset: str, amount: int, source: str, purpose: str, at: int) -> Entry:
        if not asset or amount <= 0 or not source.strip() or not purpose.strip():
            governance._fail("invalid", "asset, amount, source and purpose are required")
        entry = Entry(
            id=entry_id("", IN, asset, amount, at, len(self.entries)),
            asset=asset, amount=amount, direction=IN, source=source,
            purpose=purpose, at=at,
        )
        self.balances[asset] = self.balances.get(asset, 0) + amount
        self.entries.append(entry)
        return entry

    def spend(
        self,
        proposal: str,
        asset: str,
        amount: int,
        recipient: str,
        purpose: str,
        category: str,
        at: int,
    ) -> Entry:
        if (
            not proposal or not asset or amount <= 0 or not recipient.strip()
            or not purpose.strip() or not category.strip()
        ):
            governance._fail(
                "invalid",
                "proposal, asset, amount, recipient, purpose and category are required",
            )
        limit = self.limits.get(category, {}).get(asset)
        if limit is None:
            governance._fail("budget", f"category {category} has no limit in {asset}")
        spent = sum(
            e.amount
            for e in self.entries
            if e.direction == OUT and e.category == category and e.asset == asset
        )
        if spent + amount > limit:
            governance._fail(
                "budget", f"{category} would spend {spent + amount} of {limit} in {asset}"
            )
        if self.balances.get(asset, 0) < amount:
            governance._fail(
                "overspend",
                f"{self.balances.get(asset, 0)} available, {amount} requested",
            )
        entry = Entry(
            id=entry_id(proposal, OUT, asset, amount, at, len(self.entries)),
            asset=asset, amount=amount, direction=OUT, recipient=recipient,
            purpose=purpose, category=category, proposal=proposal, at=at,
        )
        self.balances[asset] -= amount
        self.entries.append(entry)
        return entry

    def report(self, from_: int, to: int) -> Report:
        if to < from_:
            governance._fail("invalid", "to is before from")
        report = Report(
            from_=from_, to=to,
            opening=dict(self.balances), closing=dict(self.balances),
        )
        for entry in reversed(self.entries):
            if entry.at >= from_:
                self._reverse(report.opening, entry)
            if entry.at >= to:
                self._reverse(report.closing, entry)
        for entry in self.entries:
            if from_ <= entry.at < to:
                if entry.direction == IN:
                    report.inflows[entry.asset] = (
                        report.inflows.get(entry.asset, 0) + entry.amount
                    )
                else:
                    report.outflows[entry.asset] = (
                        report.outflows.get(entry.asset, 0) + entry.amount
                    )
                    report.categories[entry.category] = (
                        report.categories.get(entry.category, 0) + entry.amount
                    )
        for asset, closing in report.closing.items():
            want = (
                report.opening.get(asset, 0)
                + report.inflows.get(asset, 0)
                - report.outflows.get(asset, 0)
            )
            if want != closing:
                governance._fail(
                    "reconcile",
                    f"{asset} opening {report.opening.get(asset, 0)} + in "
                    f"{report.inflows.get(asset, 0)} - out "
                    f"{report.outflows.get(asset, 0)} != closing {closing}",
                )
        return report

    @staticmethod
    def _reverse(balances: dict[str, int], entry: Entry) -> None:
        if entry.direction == IN:
            if balances.get(entry.asset, 0) < entry.amount:
                governance._fail("reconcile", f"{entry.asset} balance below recorded inflow")
            balances[entry.asset] -= entry.amount
        else:
            balances[entry.asset] = balances.get(entry.asset, 0) + entry.amount

    def register(
        self, registry: governance.Registry, now: Callable[[], int] | None = None
    ) -> None:
        clock = now or (lambda: 0)

        def handler(action: governance.Action, proposal: governance.Proposal):
            if proposal.kind != governance.TREASURY:
                governance._fail("invalid", "treasury_spend requires a treasury proposal")
            amount = action.params.get("amount")
            if not isinstance(amount, int) or isinstance(amount, bool) or amount < 0:
                governance._fail("invalid", "amount must be a non-negative integer")
            entry = self.spend(
                proposal.id,
                action.params.get("asset", ""),
                amount,
                action.params.get("recipient", ""),
                action.params.get("purpose", ""),
                action.params.get("category", ""),
                clock(),
            )

            def undo() -> None:
                self.entries.pop()
                self.balances[entry.asset] += entry.amount

            return undo

        registry.register("treasury_spend", handler)
