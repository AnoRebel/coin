"""Deterministic, versioned jurisdiction policy engine.

Mirrors internal/compliance/policy: policies activate at a block height, the
active version is chosen deterministically, and rules are ordered by descending
priority then ascending rule ID. Unknown fields and invalid decisions are
rejected rather than defaulted.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

ALLOW = "allow"
DENY = "deny"
HOLD = "hold"
_DECISIONS = {ALLOW, DENY, HOLD}
_NO_ACTIVE = "no-active-policy"
_DEFAULT_RULE = "default"


class PolicyError(ValueError):
    """Raised when a policy set is malformed or inconsistent."""


@dataclass(frozen=True)
class Match:
    assets: tuple[str, ...] = ()
    originator_jurisdictions: tuple[str, ...] = ()
    beneficiary_jurisdictions: tuple[str, ...] = ()
    transfer_types: tuple[str, ...] = ()
    min_amount: int = 0
    max_amount: int = 0  # 0 means unbounded

    def matches(self, ctx: Context) -> bool:
        return (
            _in_list(self.assets, ctx.asset)
            and _in_list(self.originator_jurisdictions, ctx.originator_jurisdiction)
            and _in_list(self.beneficiary_jurisdictions, ctx.beneficiary_jurisdiction)
            and _in_list(self.transfer_types, ctx.transfer_type)
            and ctx.amount >= self.min_amount
            and (self.max_amount == 0 or ctx.amount <= self.max_amount)
        )


@dataclass(frozen=True)
class Rule:
    id: str
    priority: int
    decision: str
    reason: str
    requirements: tuple[str, ...]
    match: Match


@dataclass(frozen=True)
class Default:
    decision: str
    reason: str
    requirements: tuple[str, ...]


@dataclass(frozen=True)
class Version:
    version: int
    activation_height: int
    default: Default
    rules: tuple[Rule, ...]


@dataclass(frozen=True)
class Context:
    asset: str = ""
    amount: int = 0
    originator_jurisdiction: str = ""
    beneficiary_jurisdiction: str = ""
    transfer_type: str = ""


@dataclass(frozen=True)
class PolicySet:
    network: str
    versions: tuple[Version, ...] = field(default_factory=tuple)

    def at(self, height: int) -> Version | None:
        active = None
        for version in self.versions:
            if version.activation_height > height:
                continue
            if active is None or (
                version.activation_height,
                version.version,
            ) > (active.activation_height, active.version):
                active = version
        return active

    def history(self) -> tuple[Version, ...]:
        return tuple(sorted(self.versions, key=lambda v: v.activation_height))

    def evaluate(self, height: int, ctx: Context) -> dict[str, Any]:
        active = self.at(height)
        if active is None:
            return {
                "decision": DENY,
                "rule_id": _NO_ACTIVE,
                "policy_version": 0,
                "activation_height": 0,
                "reasons": ["no policy version is active at this height"],
                "requirements": [],
            }
        for rule in sorted(active.rules, key=lambda r: (-r.priority, r.id)):
            if rule.match.matches(ctx):
                return _outcome(active, rule.id, rule.decision, rule.reason, rule.requirements)
        return _outcome(
            active, _DEFAULT_RULE, active.default.decision,
            active.default.reason, active.default.requirements,
        )


def _outcome(
    version: Version, rule_id: str, decision: str, reason: str, requirements: tuple[str, ...]
) -> dict[str, Any]:
    return {
        "decision": decision,
        "rule_id": rule_id,
        "policy_version": version.version,
        "activation_height": version.activation_height,
        "reasons": [reason] if reason else [],
        "requirements": list(requirements),
    }


def _in_list(values: tuple[str, ...], value: str) -> bool:
    return not values or value in values


def _check_fields(obj: dict[str, Any], allowed: set[str], path: str) -> None:
    unknown = set(obj) - allowed
    if unknown:
        raise PolicyError(f"{path}: unknown field(s): {', '.join(sorted(unknown))}")


def _list_of_str(value: Any, path: str) -> tuple[str, ...]:
    if not isinstance(value, list) or any(not isinstance(v, str) for v in value):
        raise PolicyError(f"{path} must be a list of strings")
    return tuple(value)


def _non_negative(value: Any, path: str) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        raise PolicyError(f"{path} must be a non-negative integer")
    return value


def _decision(value: Any, path: str) -> str:
    if value not in _DECISIONS:
        raise PolicyError(f"{path}: unknown decision {value!r}")
    return value


def _parse_match(raw: Any, path: str) -> Match:
    if raw is None:
        raw = {}
    if not isinstance(raw, dict):
        raise PolicyError(f"{path} must be an object")
    _check_fields(
        raw,
        {
            "assets", "originator_jurisdictions", "beneficiary_jurisdictions",
            "transfer_types", "min_amount", "max_amount",
        },
        path,
    )
    match = Match(
        assets=_list_of_str(raw.get("assets", []), f"{path}.assets"),
        originator_jurisdictions=_list_of_str(
            raw.get("originator_jurisdictions", []), f"{path}.originator_jurisdictions"
        ),
        beneficiary_jurisdictions=_list_of_str(
            raw.get("beneficiary_jurisdictions", []), f"{path}.beneficiary_jurisdictions"
        ),
        transfer_types=_list_of_str(raw.get("transfer_types", []), f"{path}.transfer_types"),
        min_amount=_non_negative(raw.get("min_amount", 0), f"{path}.min_amount"),
        max_amount=_non_negative(raw.get("max_amount", 0), f"{path}.max_amount"),
    )
    if match.max_amount != 0 and match.min_amount > match.max_amount:
        raise PolicyError(f"{path}: min_amount exceeds max_amount")
    return match


def _parse_rule(raw: Any, path: str) -> Rule:
    if not isinstance(raw, dict):
        raise PolicyError(f"{path} must be an object")
    _check_fields(
        raw, {"id", "priority", "decision", "reason", "requirements", "match"}, path
    )
    rule_id = raw.get("id", "")
    if not isinstance(rule_id, str) or not rule_id.strip():
        raise PolicyError(f"{path}: id is required")
    return Rule(
        id=rule_id,
        priority=_non_negative(raw.get("priority", 0), f"{path}.priority"),
        decision=_decision(raw.get("decision", ""), f"{path}.decision"),
        reason=raw.get("reason", ""),
        requirements=_list_of_str(raw.get("requirements", []), f"{path}.requirements"),
        match=_parse_match(raw.get("match", {}), f"{path}.match"),
    )


def _parse_default(raw: Any, path: str) -> Default:
    if not isinstance(raw, dict):
        raise PolicyError(f"{path} must be an object")
    _check_fields(raw, {"decision", "reason", "requirements"}, path)
    return Default(
        decision=_decision(raw.get("decision", ""), f"{path}.decision"),
        reason=raw.get("reason", ""),
        requirements=_list_of_str(raw.get("requirements", []), f"{path}.requirements"),
    )


def _parse_version(raw: Any, path: str) -> Version:
    if not isinstance(raw, dict):
        raise PolicyError(f"{path} must be an object")
    _check_fields(raw, {"version", "activation_height", "default", "rules"}, path)
    version = _non_negative(raw.get("version", 0), f"{path}.version")
    if version == 0:
        raise PolicyError(f"{path}.version must be positive")
    if not isinstance(raw.get("rules", []), list):
        raise PolicyError(f"{path}.rules must be a list")
    rules = tuple(_parse_rule(r, f"{path}.rules[{i}]") for i, r in enumerate(raw.get("rules", [])))
    ids = [r.id for r in rules]
    if len(ids) != len(set(ids)):
        raise PolicyError(f"{path}: duplicate rule id")
    return Version(
        version=version,
        activation_height=_non_negative(raw.get("activation_height", 0), f"{path}.activation_height"),
        default=_parse_default(raw.get("default", {}), f"{path}.default"),
        rules=rules,
    )


def parse(raw: dict[str, Any]) -> PolicySet:
    """Validate a decoded policy document. Unknown fields are rejected."""
    if not isinstance(raw, dict):
        raise PolicyError("policy must be an object")
    _check_fields(raw, {"network", "versions"}, "policy")
    network = raw.get("network", "")
    if not isinstance(network, str) or not network.strip():
        raise PolicyError("policy.network is required")
    versions_raw = raw.get("versions", [])
    if not isinstance(versions_raw, list) or not versions_raw:
        raise PolicyError("policy.versions must be a non-empty list")
    versions = tuple(
        _parse_version(v, f"policy.versions[{i}]") for i, v in enumerate(versions_raw)
    )
    if len({v.version for v in versions}) != len(versions):
        raise PolicyError("duplicate version")
    if len({v.activation_height for v in versions}) != len(versions):
        raise PolicyError("duplicate activation height")
    return PolicySet(network=network, versions=versions)
