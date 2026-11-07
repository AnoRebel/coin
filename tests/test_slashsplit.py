"""Slashing split vectors: reporter cap, burn share, treasury remainder."""

from __future__ import annotations

import json
from pathlib import Path

from arne import slashing

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "slashsplit.json"


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["cases"]:
        params = slashing.Params(
            window_blocks=100,
            jail_blocks=50,
            double_sign_bps=1000,
            downtime_bps=500,
            reporter_bps=case["reporter_bps"],
            downtime_threshold_bps=5000,
            downtime_min_blocks=10,
            reporter_cap=case["reporter_cap"],
            burn_bps=case["burn_bps"],
        )
        split = slashing.split_penalty(case["slashed"], params)
        assert (
            split.reporter,
            split.burn,
            split.treasury,
        ) == (
            case["expect"]["reporter"],
            case["expect"]["burn"],
            case["expect"]["treasury"],
        ), case["name"]
        assert split.reporter + split.burn + split.treasury == case["slashed"]
