"""Wallet intent binding must survive URI resolution and payload construction."""

import argparse

import pytest
from arne import cli, paymenturi
from arne.keys import keypair_from_mnemonic


def test_uri_keeps_intent_in_signed_payload() -> None:
    intent = "0123456789abcdef0123456789abcdef"
    args = argparse.Namespace(
        uri="arne:arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t"
        "?network=arne%3Adevnet&asset=arce%3Adevnet&amount=1&intent=" + intent,
        network="arne:devnet", to="", asset="", amount="", memo="", memo_to="", intent="",
        refund_of="",
    )
    cli._apply_uri(args)
    assert cli._transfer_payload(args, 100_000_000)["intent"] == intent
    args.intent = "f" * 32
    with pytest.raises(paymenturi.URIError):
        cli._apply_uri(args)
    args.intent = intent
    args.asset = "usd:devnet"
    with pytest.raises(paymenturi.URIError):
        cli._apply_uri(args)


def test_explicit_devnet_rejects_mainnet_link() -> None:
    key = keypair_from_mnemonic(
        "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
        "", 0, 0, 0, "arne:mainnet",
    )
    args = cli.build_parser().parse_args([
        "pay", "--mnemonic", "unused", "--network", "arne:devnet",
        "--uri", f"arne:{key.address}?network=arne%3Amainnet&asset=arce%3Amainnet&amount=1",
    ])
    with pytest.raises(paymenturi.URIError):
        cli._apply_uri(args)


def test_resolve_intent(monkeypatch) -> None:
    import httpx
    from arne import types

    payee = "arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t"
    intent_id = types.intent_id("arne:devnet", payee, "invoice_42")
    intent = {"id": intent_id, "payee": payee, "reference": "invoice_42", "asset": "arce:devnet",
              "amount": 1250000000, "expires_at": 0, "status": "open", "created_at": 0}
    network = "arne:devnet"

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/v1/status":
            return httpx.Response(200, json={"network": network})
        if request.url.path.startswith("/v1/intents/"):
            return httpx.Response(200, json=intent)
        return httpx.Response(200, json={"decimals": 8})

    monkeypatch.setattr(cli, "_client", lambda url: httpx.Client(base_url=url, transport=httpx.MockTransport(handler)))
    args = argparse.Namespace(url="http://node.test", network=network, intent=intent_id,
                              to="", asset="", amount="", memo="")
    payment = cli._resolve_intent(args, None)
    assert payment.intent == intent_id and payment.to == payee
    assert args.asset == "arce:devnet" and args.amount == "12.5"
    args.amount = "13"
    with pytest.raises(ValueError):
        cli._resolve_intent(args, None)
    args.amount = "12.5"
    intent["status"] = "paid"
    with pytest.raises(ValueError):
        cli._resolve_intent(args, None)
    intent["status"] = "open"
    network = "arne:mainnet"
    with pytest.raises(ValueError):
        cli._resolve_intent(args, None)


def test_resolve_refund(monkeypatch) -> None:
    import httpx

    payer = "arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t"
    payee = "arce-devnet1q9smssex7dwa052xn57j7dmh7hxu0003x5k2rng8"
    original = "a" * 64
    network = "arne:devnet"
    transfer = {"hash": original, "from": payer, "to": payee, "asset": "arce:devnet",
                "amount": 500_000_000, "refund_of": "", "refunded": 200_000_000, "refundable": 300_000_000}

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/v1/status":
            return httpx.Response(200, json={"network": network})
        if request.url.path.startswith("/v1/transfers/"):
            return httpx.Response(200, json=transfer)
        return httpx.Response(200, json={"decimals": 8})

    monkeypatch.setattr(cli, "_client", lambda url: httpx.Client(base_url=url, transport=httpx.MockTransport(handler)))
    args = argparse.Namespace(url="http://node.test", network=network, to="", asset="", amount="",
                              memo="", intent="", refund_of=original)
    cli._resolve_refund(args)
    assert (args.to, args.asset, args.amount) == (payer, "arce:devnet", "3")
    assert cli._transfer_payload(args, 300_000_000)["refund_of"] == original

    args.amount, args.to = "4", ""
    with pytest.raises(ValueError, match="exceeds remaining"):
        cli._resolve_refund(args)
    args.amount, args.to = "3", "arce-devnet1qxxts70pzcfxpcfrmen2vk0f553wrxt6dc3a6tnl"
    with pytest.raises(ValueError, match="conflicts"):
        cli._resolve_refund(args)
    args.to, args.amount = "", "3"
    args.intent = "0" * 32
    with pytest.raises(ValueError, match="not both"):
        cli._resolve_refund(args)
    args.intent = ""
    transfer["refund_of"] = original
    with pytest.raises(ValueError, match="refund a refund"):
        cli._resolve_refund(args)
    transfer["refund_of"], transfer["refundable"] = "", 0
    with pytest.raises(ValueError, match="fully refunded"):
        cli._resolve_refund(args)
    transfer["refund_of"], transfer["refundable"], transfer["hash"] = "", 300_000_000, "b" * 64
    with pytest.raises(ValueError, match="different transfer"):
        cli._resolve_refund(args)
    transfer["hash"] = original
    network = "arne:mainnet"
    with pytest.raises(ValueError, match="conflicts"):
        cli._resolve_refund(args)


def test_intent_qr_does_not_overwrite(tmp_path) -> None:
    path = tmp_path / "intent.png"
    uri = "arne:arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t?amount=1&asset=arce%3Adevnet&intent=0123456789abcdef0123456789abcdef&network=arne%3Adevnet"
    cli._write_intent_qr(path, uri)
    before = path.read_bytes()
    assert before.startswith(b"\x89PNG\r\n\x1a\n")
    with pytest.raises(FileExistsError):
        cli._write_intent_qr(path, "replacement")
    assert path.read_bytes() == before


def test_intent_cli_end_to_end(monkeypatch, capsys, tmp_path) -> None:
    import json
    import time

    from arne import ledger, types
    from arne.api import create_app
    from arne.node import Config, MemoryStore, Node, devnet_genesis
    from fastapi.testclient import TestClient

    mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
    funder = keypair_from_mnemonic(mnemonic, "", 0, 0, 0, "arne:devnet")
    payee = keypair_from_mnemonic(mnemonic, "", 0, 0, 1, "arne:devnet")
    config = Config(network="arne:devnet", chain="arne-devnet-1", params=ledger.default_params(),
                    proposer=funder.address, faucet=payee, devnet=True)
    node = Node.open(config, devnet_genesis("arne:devnet", int(time.time()), funder, payee), MemoryStore())
    app = create_app(node)
    monkeypatch.setattr(cli, "_client", lambda _: TestClient(app))
    assert cli.main(["intent", "create", "--mnemonic", mnemonic, "--index", "1", "--asset", "arce:devnet",
                     "--amount", "1.25", "--reference", "cli_invoice", "--expires", str(int(time.time()) + 365*24*3600)]) == 0
    capsys.readouterr()
    node.produce_block(int(time.time()) + 1)
    intent_id = types.intent_id("arne:devnet", payee.address, "cli_invoice")
    assert cli.main(["intent", "show", "--id", intent_id]) == 0
    assert json.loads(capsys.readouterr().out)["status"] == "open"
    qr_path = tmp_path / "intent.png"
    assert cli.main(["intent", "link", "--id", intent_id, "--qr", str(qr_path)]) == 0
    uri = capsys.readouterr().out.strip()
    assert paymenturi.parse(uri).intent == intent_id and qr_path.is_file()
    assert cli.main(["pay", "--mnemonic", mnemonic, "--uri", uri]) == 1
    assert not node.mempool
    capsys.readouterr()
    assert cli.main(["pay", "--mnemonic", mnemonic, "--uri", uri, "--yes"]) == 0
    node.produce_block(int(time.time()) + 2)
    intent = node.state.intent(intent_id)
    assert intent.status == "paid" and node.receipt(intent.tx_hash).success
    root = node.state.root()
    capsys.readouterr()
    assert cli.main(["pay", "--mnemonic", mnemonic, "--intent", intent_id, "--yes"]) == 2
    assert node.state.root() == root and not node.mempool
