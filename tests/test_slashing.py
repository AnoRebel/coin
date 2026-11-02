"""Equivocation and downtime evidence conformance vectors."""

from __future__ import annotations

import json
from pathlib import Path

from arne import slashing, staking

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _load() -> tuple[dict, dict]:
    bls_doc = json.loads((VECTORS / "bls.json").read_text())
    doc = json.loads((VECTORS / "slashing.json").read_text())
    keys = {v["id"]: (v["public_key"], v["pop"]) for v in bls_doc["validators"]}
    return doc, keys


def _vote(raw: dict) -> slashing.Vote:
    return slashing.Vote(
        height=raw["height"], round=raw["round"],
        block_hash=raw["block_hash"], signature=raw["signature"],
    )


def test_shared_vectors() -> None:
    doc, keys = _load()
    staking_params = staking.Params(**doc["staking_params"])
    slashing_params = slashing.Params(**doc["slashing_params"])

    for case in doc["cases"]:
        state = staking.new(doc["network"], staking_params)
        engine = slashing.new(doc["network"], slashing_params)
        for setup in case["setup"]:
            if setup["op"] == "register":
                key, pop = keys[setup["validator"]]
                staking.register(state, staking.Validator(
                    address=setup["validator"], consensus_key=key, pop=pop,
                    commission_bps=setup["commission_bps"], self_bond=setup["self_bond"],
                ), setup["at"])
            else:
                staking.advance_epoch(state, setup["at"])

        for index, step in enumerate(case["steps"]):
            error = None
            result = None
            try:
                if step["op"] == "double_sign":
                    result = slashing.double_sign(state, engine, slashing.DoubleSignEvidence(
                        validator=step["validator"], a=_vote(step["a"]), b=_vote(step["b"]),
                    ), step["at"])
                else:
                    result = slashing.downtime(state, engine, slashing.DowntimeEvidence(
                        validator=step["validator"], from_=step["from"], to=step["to"],
                        missed=step["missed"], total=step["total"],
                    ), step["at"])
            except slashing.SlashingError as exc:
                error = exc

            expect = step["expect"]
            if "error" in expect:
                assert error is not None, (case["name"], index, step)
                assert error.code == expect["error"], (case["name"], index, error)
                continue
            if error is not None:
                raise AssertionError((case["name"], index, step, error)) from error

            wanted = expect["result"]
            assert result.kind == wanted["kind"]
            assert result.slashed == wanted["slashed"]
            assert result.reporter == wanted["reporter"]
            assert result.treasury == wanted["treasury"]
            assert result.jail_until == wanted["jail_until"]
            assert result.delegators == wanted["delegators"]

            want = expect["validator"]
            view = next(
                v for v in staking.snapshot(state)["validators"]
                if v["address"] == step["validator"]
            )
            for field, value in want.items():
                assert view[field] == value, (case["name"], index, field, view)


def test_vote_message_matches_go_shape() -> None:
    # Stable domain and NUL-separated fields; the vector signatures prove it.
    msg = slashing.vote_message("arce:devnet", "v1", 11, 1, "block-a")
    assert len(msg) == 32
    assert msg != slashing.vote_message("arce:devnet", "v1", 11, 1, "block-b")
    assert msg != slashing.vote_message("arce:devnet", "v1", 11, 2, "block-a")
