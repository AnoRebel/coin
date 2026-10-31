"""Reference profiles: required fields, thresholds, and shared vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import profile

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "profiles.json"


def _config(raw: dict) -> profile.Config:
    return profile.Config(
        network="arne:devnet",
        profiles=list(raw.get("profiles", [])),
        fatf_threshold=raw.get("threshold", profile.DEFAULT_FATF_THRESHOLD),
        jurisdiction_threshold=dict(raw.get("jurisdiction_threshold", {})),
    )


def _run(cfg: profile.Config, case: dict) -> None:
    target = case["target"]
    if target == "asset":
        cfg.validate_asset(case["metadata"])
    elif target == "transfer":
        transfer = case["transfer"]
        cfg.validate_transfer(transfer["jurisdiction"], transfer["amount"], transfer["fields"])
    elif target == "iso20022":
        cfg.validate_payment(case["fields"])
    else:
        raise AssertionError(f"unknown target {target!r}")


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["cases"]:
        cfg = _config(case["config"])
        if case["ok"]:
            _run(cfg, case)
            continue
        with pytest.raises(profile.ValidationError) as exc:
            _run(cfg, case)
        assert exc.value.field == case["field"], case["name"]

    for kind, want in doc["profile_fields"].items():
        definition = profile.get(kind)
        assert definition is not None
        assert list(definition.required) == want["required"]
        assert definition.retention_days == want.get("retention_days", 0)
        assert doc["disclaimer_contains"] in definition.disclaimer.lower()


def test_config_validation() -> None:
    with pytest.raises(profile.ProfileError):
        profile.Config(network="", profiles=[profile.FATF]).validate()
    with pytest.raises(profile.ProfileError):
        profile.Config(network="arne:devnet", profiles=["gdpr"]).validate()
    with pytest.raises(profile.ProfileError):
        profile.Config(network="arne:devnet", profiles=[profile.MICA, profile.MICA]).validate()
    with pytest.raises(profile.ProfileError):
        profile.Config(network="arne:devnet", profiles=[]).validate()

    cfg = _config({"profiles": [profile.FATF]})
    assert cfg.threshold("se") == profile.DEFAULT_FATF_THRESHOLD
    cfg.jurisdiction_threshold["se"] = 5
    assert cfg.threshold("se") == 5
    assert len(cfg.disclaimers()) == 1
    assert len(profile.all_profiles()) == 3
