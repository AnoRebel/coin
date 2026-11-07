"""Epoch reward vectors: stake weight, commission, jail exclusion, dust."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import staking

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "rewards.json"


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["invalid"]:
        with pytest.raises(staking.StakingError) as caught:
            state = staking.State(
                network="arne:devnet",
                params=staking.Params(1, 1, 0, 10, 20),
            )
            staking.distribute_rewards(state, case["budget"], case["treasury_bps"])
        assert caught.value.code == case["error"], case["name"]

    for case in doc["cases"]:
        state = staking.State(
            network="arne:devnet",
            params=staking.Params(1, 1, 0, 10, 20),
            epoch=case.get("epoch", 0),
        )
        for entry in case["validators"]:
            state.validators[entry["address"]] = staking.Validator(
                address=entry["address"],
                consensus_key="",
                pop="",
                commission_bps=entry["commission_bps"],
                self_bond=entry["delegations"].get(entry["address"], 0),
                status=entry["status"],
            )
            state.delegations[entry["address"]] = dict(entry["delegations"])

        result = staking.distribute_rewards(
            state, case["budget"], case["treasury_bps"]
        )
        expect = case["expect"]
        assert result.treasury == expect["treasury"], case["name"]
        assert result.total == expect["total"], case["name"]
        assert result.total == case["budget"], case["name"]
        rewards = {reward.validator: reward for reward in result.validators}
        assert set(rewards) == set(expect["validators"]), case["name"]
        for address, wanted in expect["validators"].items():
            reward = rewards[address]
            assert reward.stake == wanted["stake"], case["name"]
            assert reward.commission == wanted["commission"], case["name"]
            assert reward.total == wanted["total"], case["name"]
            shares = {share.address: share.amount for share in reward.delegators}
            assert shares == wanted["delegators"], case["name"]
            assert (
                reward.commission + sum(shares.values()) == reward.total
            ), case["name"]
