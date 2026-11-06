"""Treasury conformance vectors and spend action."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import governance, treasury

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "treasury.json"


def _code(exc: BaseException) -> str:
    return exc.code if isinstance(exc, governance.GovernanceError) else "other"


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    book = treasury.TreasuryBook()
    for limit in doc["limits"]:
        book.set_budget(limit["category"], limit["asset"], limit["limit"])
    for step in doc["steps"]:
        operation = step["op"]
        error = None
        if operation == "deposit":
            book.deposit(step["asset"], step["amount"], step["source"], step["purpose"], step["at"])
        elif operation == "spend":
            try:
                book.spend(
                    step["proposal"], step["asset"], step["amount"], step["recipient"],
                    step["purpose"], step["category"], step["at"],
                )
            except governance.GovernanceError as exc:
                error = exc
        elif operation == "report":
            report = book.report(step["from"], step["to"])
            expect = step["expect"]
            assert report.inflows == expect["inflows"]
            assert report.outflows == expect["outflows"]
            assert report.categories == expect["categories"]
            assert report.opening == expect["opening"]
            assert report.closing == expect["closing"]
        else:
            raise AssertionError(f"unknown op {operation}")
        if step.get("error"):
            assert error is not None and _code(error) == step["error"], step
        else:
            assert error is None, step


def _state() -> governance.State:
    return governance.new(
        "arne:devnet",
        governance.Params(
            min_deposit=100, deposit_period=0, voting_period=100, timelock=50,
            quorum_bps=5000, threshold_bps=5000,
            constitutional_threshold_bps=6667, constitutional_timelock=500,
            max_actions=8,
        ),
    )


def test_spend_action_and_report() -> None:
    book = treasury.TreasuryBook()
    book.set_budget("grants", "arce", 500)
    book.deposit("arce", 1000, "fees", "fee income", 10)
    state = _state()
    proposal = governance.Proposal(
        kind=governance.TREASURY, proposer="arce-devnet1proposer", nonce=9,
        title="grant", deposit=100, activation_height=600,
        actions=[
            governance.Action(
                "treasury_spend",
                {
                    "asset": "arce", "amount": 250, "recipient": "arce1grant",
                    "purpose": "grant", "category": "grants",
                },
            )
        ],
    )
    governance.submit(state, proposal, 1000, 100)
    pid = governance.proposal_id("arne:devnet", proposal.proposer, 9)
    governance.vote(state, pid, "voter-a", governance.YES, 600, 110)
    governance.advance(state, 200)
    governance.queue(state, pid, 200)
    registry = governance.Registry()
    book.register(registry, lambda: 30)
    governance.execute(state, pid, 250, registry)
    assert book.balances["arce"] == 750
    assert book.entries[-1].proposal == pid
    report = book.report(0, 100)
    assert report.outflows["arce"] == 250
    assert report.categories["grants"] == 250


def test_non_treasury_proposal_is_refused() -> None:
    book = treasury.TreasuryBook()
    book.set_budget("grants", "arce", 500)
    book.deposit("arce", 1000, "fees", "fee income", 10)
    registry = governance.Registry()
    book.register(registry, lambda: 20)
    action = governance.Action(
        "treasury_spend",
        {"asset": "arce", "amount": 100, "recipient": "r", "purpose": "grant", "category": "grants"},
    )
    with pytest.raises(governance.GovernanceError) as caught:
        registry.handlers["treasury_spend"](
            action, governance.Proposal(kind=governance.PARAMETER, proposer="p", nonce=1, actions=[], deposit=1, activation_height=1, id="p")
        )
    assert caught.value.code == "invalid"
    assert book.balances["arce"] == 1000
