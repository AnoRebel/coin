"""Connector CLI: negotiate a handshake from a JSON file."""

from __future__ import annotations

import json

import pytest
from arne import cli


def write_handshake(tmp_path, body: dict) -> str:
    path = tmp_path / "handshake.json"
    path.write_text(json.dumps(body), encoding="utf-8")
    return str(path)


def test_connector_negotiate(tmp_path, capsys) -> None:
    path = write_handshake(tmp_path, {
        "adapter": {
            "name": "evm", "versions": [1, 2],
            "assets": ["arce:devnet", "usd:devnet"], "health": "degraded",
        },
        "request": {"versions": [1, 2], "assets": ["usd:devnet"], "network": "arne:devnet"},
    })
    assert cli.main(["connector", "negotiate", "--file", path]) == 0
    assert json.loads(capsys.readouterr().out) == {
        "connector": "evm", "version": 2, "assets": ["usd:devnet"], "network": "arne:devnet",
    }


@pytest.mark.parametrize("adapter,wanted,error", [
    (
        {"name": "evm", "versions": [1], "assets": ["arce:devnet"], "health": "unhealthy"},
        {"versions": [1], "assets": ["arce:devnet"]}, "unhealthy",
    ),
    (
        {"name": "evm", "versions": [1], "assets": ["arce:devnet"]},
        {"versions": [2], "assets": ["arce:devnet"]}, "unsupported protocol version",
    ),
    (
        {"name": "evm", "versions": [1], "assets": ["arce:devnet"]},
        {"versions": [1], "assets": ["usd:devnet"]}, "unsupported asset",
    ),
])
def test_connector_negotiate_rejects(tmp_path, capsys, adapter, wanted, error) -> None:
    path = write_handshake(tmp_path, {"adapter": adapter, "request": wanted})
    assert cli.main(["connector", "negotiate", "--file", path]) == 2
    assert error in capsys.readouterr().err
