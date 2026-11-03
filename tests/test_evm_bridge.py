"""EVM bridge guard: finality, replay, rate limits, and pause vectors."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import evm_bridge

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "evm_bridge.json"


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _config(raw: dict) -> evm_bridge.Config:
    return evm_bridge.Config(
        chain_id=raw["chain_id"],
        finality=raw["finality"],
        limits=tuple(evm_bridge.Limit(**limit) for limit in raw["limits"]),
    )


def _message(raw: dict) -> evm_bridge.Message:
    return evm_bridge.Message(**raw)


def test_shared_vectors() -> None:
    doc = _load()
    bridge = evm_bridge.Bridge(_config(doc["config"]))
    base = doc["cases"][0]["message"]
    assert (
        evm_bridge.message_id(base["chain_id"], base["tx_hash"], base["log_index"])
        == doc["message_id"]
    )

    for case in doc["cases"]:
        expect = case.get("expect", {})
        if expect.get("error"):
            with pytest.raises(evm_bridge.EVMError, match=expect["error"]):
                _run(bridge, case)
        else:
            _run(bridge, case)


def _run(bridge: evm_bridge.Bridge, case: dict) -> None:
    if case["kind"] == "pause":
        bridge.pause(case["reason"])
    elif case["kind"] == "resume":
        bridge.resume()
    else:
        bridge.submit(_message(case["message"]), case["now"])


def test_replay_state_survives_resume() -> None:
    doc = _load()
    bridge = evm_bridge.Bridge(_config(doc["config"]))
    first = _message(doc["cases"][0]["message"])
    bridge.submit(first, 100)
    bridge.pause("maintenance")
    bridge.resume()
    with pytest.raises(evm_bridge.EVMError, match="replay"):
        bridge.submit(first, 101)


def test_failed_submit_does_not_charge_limit() -> None:
    bridge = evm_bridge.Bridge(
        evm_bridge.Config(1, 3, (evm_bridge.Limit("a", 100, 60),))
    )
    message = evm_bridge.Message(
        id="", direction=evm_bridge.INBOUND, chain_id=1, block_number=1,
        tx_hash="0x" + "ab" * 32, log_index=0, asset="a", amount=80,
        sender="s", recipient="r", confirmations=3,
    )
    bridge.submit(message, 10)

    late = evm_bridge.Message(
        id="", direction=evm_bridge.INBOUND, chain_id=1, block_number=1,
        tx_hash="0x" + "cd" * 32, log_index=0, asset="a", amount=40,
        sender="s", recipient="r", confirmations=2,
    )
    with pytest.raises(evm_bridge.EVMError, match="not final"):
        bridge.submit(late, 11)
    assert bridge.windows["a"].used == 80


def test_config_validation() -> None:
    for config in (
        evm_bridge.Config(0, 1),
        evm_bridge.Config(1, 0),
        evm_bridge.Config(1, 1, (evm_bridge.Limit("a", 0, 60),)),
        evm_bridge.Config(1, 1, (evm_bridge.Limit("a", 1, 60), evm_bridge.Limit("a", 1, 60))),
    ):
        with pytest.raises(evm_bridge.EVMError, match="invalid"):
            evm_bridge.Bridge(config)
