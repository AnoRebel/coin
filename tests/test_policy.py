"""Jurisdiction policy engine: determinism, versioning, and shared vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import policy

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "policy.json"


def _policy(**overrides):
    body = {
        "network": "arne:devnet",
        "versions": [
            {
                "version": 1,
                "activation_height": 0,
                "default": {"decision": "deny", "reason": "default"},
                "rules": [
                    {"id": "b", "priority": 10, "decision": "allow", "reason": "b", "match": {}},
                    {"id": "a", "priority": 10, "decision": "hold", "reason": "a", "match": {}},
                ],
            },
            {
                "version": 2,
                "activation_height": 10,
                "default": {"decision": "allow", "reason": "v2"},
                "rules": [],
            },
        ],
    }
    body.update(overrides)
    return policy.parse(body)


def test_priority_tie_break_and_version_history() -> None:
    ps = _policy()
    ctx = policy.Context(asset="arce:devnet", amount=1)
    assert ps.evaluate(0, ctx) == {
        "decision": "hold", "rule_id": "a", "policy_version": 1,
        "activation_height": 0, "reasons": ["a"], "requirements": [],
    }
    assert ps.evaluate(9, ctx)["policy_version"] == 1
    assert ps.evaluate(10, ctx) == {
        "decision": "allow", "rule_id": "default", "policy_version": 2,
        "activation_height": 10, "reasons": ["v2"], "requirements": [],
    }
    assert [v.activation_height for v in ps.history()] == [0, 10]


def test_before_first_activation_denies() -> None:
    ps = policy.parse({
        "network": "n",
        "versions": [{
            "version": 1, "activation_height": 5,
            "default": {"decision": "allow", "reason": "x"}, "rules": [],
        }],
    })
    assert ps.evaluate(4, policy.Context()) == {
        "decision": "deny", "rule_id": "no-active-policy", "policy_version": 0,
        "activation_height": 0,
        "reasons": ["no policy version is active at this height"], "requirements": [],
    }


def test_rule_matching_thresholds() -> None:
    ps = policy.parse({
        "network": "n",
        "versions": [{
            "version": 1, "activation_height": 0,
            "default": {"decision": "allow", "reason": "x"},
            "rules": [{
                "id": "t", "priority": 1, "decision": "hold", "reason": "t",
                "match": {
                    "assets": ["usd:devnet"], "originator_jurisdictions": ["US"],
                    "transfer_types": ["transfer"], "min_amount": 100, "max_amount": 200,
                },
            }],
        }],
    })
    cases = [
        (policy.Context("usd:devnet", 100, "US", "", "transfer"), "hold", "t"),
        (policy.Context("usd:devnet", 200, "US", "", "transfer"), "hold", "t"),
        (policy.Context("usd:devnet", 99, "US", "", "transfer"), "allow", "default"),
        (policy.Context("usd:devnet", 201, "US", "", "transfer"), "allow", "default"),
        (policy.Context("usd:devnet", 100, "CA", "", "transfer"), "allow", "default"),
    ]
    for ctx, decision, rule in cases:
        got = ps.evaluate(0, ctx)
        assert (got["decision"], got["rule_id"]) == (decision, rule)


@pytest.mark.parametrize("body", [
    {"network": "", "versions": []},
    {"network": "n", "versions": []},
    {"network": "n", "versions": [{"version": 0, "activation_height": 0, "default": {"decision": "allow"}, "rules": []}]},
    {"network": "n", "versions": [{"version": 1, "activation_height": 0, "default": {"decision": "maybe"}, "rules": []}]},
    {"network": "n", "versions": [
        {"version": 1, "activation_height": 0, "default": {"decision": "allow"}, "rules": []},
        {"version": 1, "activation_height": 9, "default": {"decision": "allow"}, "rules": []},
    ]},
    {"network": "n", "versions": [
        {"version": 1, "activation_height": 0, "default": {"decision": "allow"}, "rules": []},
        {"version": 2, "activation_height": 0, "default": {"decision": "allow"}, "rules": []},
    ]},
    {"network": "n", "versions": [{"version": 1, "activation_height": 0, "default": {"decision": "allow"}, "rules": [
        {"id": "x", "decision": "allow", "match": {}}, {"id": "x", "decision": "deny", "match": {}},
    ]}]},
    {"network": "n", "versions": [{"version": 1, "activation_height": 0, "default": {"decision": "allow"}, "rules": [
        {"id": "x", "decision": "allow", "match": {"min_amount": 10, "max_amount": 1}},
    ]}]},
    {"network": "n", "versions": [{"version": 1, "activation_height": 0, "default": {"decision": "allow"}, "rules": [
        {"id": "", "decision": "allow", "match": {}},
    ]}]},
    {"network": "n", "extra": True, "versions": [{"version": 1, "activation_height": 0, "default": {"decision": "allow"}, "rules": []}]},
    {"network": "n", "versions": [{"version": 1, "activation_height": 0, "default": {"decision": "allow", "extra": 1}, "rules": []}]},
])
def test_invalid_policies_are_rejected(body) -> None:
    with pytest.raises(policy.PolicyError):
        policy.parse(body)


def test_shared_policy_vectors() -> None:
    doc = json.loads(VECTORS.read_text())
    for set_index, item in enumerate(doc["sets"]):
        ps = policy.parse(item["policy"])
        for case_index, case in enumerate(item["cases"]):
            got = ps.evaluate(case["height"], policy.Context(**case["context"]))
            assert got == case["expected"], (set_index, case_index, got)
