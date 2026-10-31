"""Command-line interface: run a node, manage wallets, and submit transactions."""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import sys
import threading
import time
from decimal import Decimal, InvalidOperation
from typing import Any

import httpx

from . import cbor, ledger, paymenturi, preview, types
from . import memo as memo_codec
from .address import validate_address
from .keys import keypair_from_mnemonic, new_keypair
from .node import Config, FileStore, MemoryStore, Node, devnet_genesis

DEFAULT_URL = os.environ.get("ARNE_API", "http://127.0.0.1:8080")


def _client(url: str) -> httpx.Client:
    return httpx.Client(base_url=url.rstrip("/"), timeout=30.0)


def _fail(response: httpx.Response) -> str:
    try:
        payload = response.json()
    except ValueError:
        return f"{response.status_code}: {response.text.strip()}"
    error = payload.get("error") if isinstance(payload, dict) else None
    if isinstance(error, dict):
        return f"{response.status_code}: {error.get('code')}: {error.get('message')}"
    return f"{response.status_code}: {response.text.strip()}"


def _get(url: str, path: str) -> Any:
    with _client(url) as client:
        response = client.get(path)
        if response.status_code >= 300:
            raise SystemExit(_fail(response))
        return response.json()


def _post(url: str, path: str, payload: dict) -> Any:
    with _client(url) as client:
        response = client.post(path, json=payload)
        if response.status_code >= 300:
            raise SystemExit(_fail(response))
        return response.json()


def parse_amount(text: str, decimals: int) -> int:
    try:
        value = Decimal(text)
    except InvalidOperation as exc:
        raise ValueError(f"invalid amount {text!r}") from exc
    if value < 0:
        raise ValueError("amount must not be negative")
    units = value * (10**decimals)
    if units != units.to_integral_value():
        raise ValueError(f"amount has more than {decimals} decimals")
    return int(units)


def format_amount(value: int, decimals: int) -> str:
    if decimals == 0:
        return str(value)
    scale = 10**decimals
    return f"{value // scale}.{value % scale:0{decimals}d}".rstrip("0").rstrip(".")


def parse_api_keys(specs: list[str]) -> dict[str, list[str]]:
    keys: dict[str, list[str]] = {}
    for spec in specs:
        key, _, scopes = spec.partition("=")
        if not key or not scopes:
            raise ValueError("--api-key must be key=scope[,scope]")
        keys[key] = scopes.split(",")
    return keys


# --- serve / devnet ---


def cmd_serve(args: argparse.Namespace) -> int:
    network = args.network
    chain = types.chain_id(network)
    mnemonic = args.mnemonic
    generated = False
    if not mnemonic:
        _, mnemonic = new_keypair(network)
        generated = True
    funder = keypair_from_mnemonic(mnemonic, "", 0, 0, 0, network)
    faucet = keypair_from_mnemonic(mnemonic, "", 0, 0, 1, network)
    store = MemoryStore() if args.ephemeral else FileStore(args.data_dir)
    seed = None
    if args.webhook:
        try:
            seed = bytes.fromhex(args.webhook_key)
        except ValueError:
            seed = b""
        if len(seed) != 32:
            raise SystemExit("--webhook-key must be 32 bytes of hex when --webhook is set")
    config = Config(
        network=network, chain=chain, params=ledger.default_params(),
        proposer=funder.address, faucet=faucet,
        faucet_amount=100 * 100_000_000, devnet=True,
        webhooks=args.webhook, webhook_seed=seed,
    )
    node = Node.open(config, devnet_genesis(network, int(time.time()), funder, faucet), store)
    if generated:
        print("Generated devnet mnemonic (write it down):")
        print("  " + mnemonic)
    print(
        f"network:  {network}\nchain:    {chain}\nfunder:   {funder.address}\n"
        f"faucet:   {faucet.address}\napi:      http://{args.listen.replace('0.0.0.0', '127.0.0.1')}"
    )
    if args.webhook:
        print(f"webhooks: {len(args.webhook)} endpoint(s), signing key {node.webhook_public_key}")

    from .api import ApiConfig, create_app

    app = create_app(node, ApiConfig(
        devnet=True, rate_limit=args.rate_limit,
        api_keys=parse_api_keys(args.api_key),
        allowed_origins=args.allow_origin,
    ))

    def producer() -> None:
        last = time.monotonic()
        while True:
            time.sleep(1)
            if node.status()["mempool"] == 0 and time.monotonic() - last < args.block_interval:
                continue
            try:
                block = node.produce_block()
            except Exception as exc:  # keep serving so operators can inspect the halt
                print(f"block: {exc}", file=sys.stderr)
                continue
            last = time.monotonic()
            if block.tx_hashes:
                print(f"block {block.height} {block.hash()[:12]} txs={len(block.tx_hashes)}")

    threading.Thread(target=producer, daemon=True).start()

    import uvicorn

    host, _, port = args.listen.rpartition(":")
    try:
        uvicorn.run(app, host=host or "127.0.0.1", port=int(port), log_level=args.log_level)
    finally:
        node.close_webhooks()
    return 0


# --- wallet ---


def cmd_wallet_new(args: argparse.Namespace) -> int:
    kp, mnemonic = new_keypair(args.network)
    print(json.dumps({
        "network": args.network, "address": kp.address, "mnemonic": mnemonic,
    }, indent=2))
    return 0


def cmd_wallet_address(args: argparse.Namespace) -> int:
    kp = keypair_from_mnemonic(args.mnemonic, "", 0, 0, args.index, args.network)
    print(kp.address)
    return 0


# --- queries ---


def cmd_status(args: argparse.Namespace) -> int:
    print(json.dumps(_get(args.url, "/v1/status"), indent=2))
    return 0


def cmd_balance(args: argparse.Namespace) -> int:
    account = _get(args.url, f"/v1/accounts/{args.address}")
    units = account.get("balances", {}).get(args.asset, 0)
    asset = _get(args.url, f"/v1/assets/{args.asset}")
    print(format_amount(units, asset["decimals"]))
    return 0


def cmd_proof(args: argparse.Namespace) -> int:
    print(json.dumps(_get(args.url, f"/v1/proofs/{args.key}"), indent=2))
    return 0


# --- writes ---


def _wait_for_receipt(url: str, tx_hash: str, timeout: float = 30.0) -> int:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            receipt = _get(url, f"/v1/transactions/{tx_hash}")
        except SystemExit:
            time.sleep(0.7)
            continue
        if receipt["success"]:
            print(
                f"confirmed in block {receipt['height']}, "
                f"fee {receipt['fee']} {receipt['fee_asset']}"
            )
            return 0
        print(f"transaction failed: {receipt.get('code')}: {receipt.get('error')}")
        return 1
    print("timed out waiting for receipt", file=sys.stderr)
    return 1


def _submit(url: str, tx: types.Tx, wait: bool) -> int:
    with _client(url) as client:
        response = client.post(
            "/v1/transactions", content=cbor.encode(tx.to_cbor()),
            headers={"Content-Type": "application/cbor"},
        )
        if response.status_code >= 300:
            raise SystemExit(_fail(response))
        tx_hash = response.json()["hash"]
    print(tx_hash)
    return _wait_for_receipt(url, tx_hash) if wait else 0


def _signed_tx(
    args: argparse.Namespace, tx_type: str, payload: dict, expires_at: int | None = None
) -> types.Tx:
    kp = keypair_from_mnemonic(args.mnemonic, "", 0, 0, args.index, args.network)
    account = _get(args.url, f"/v1/accounts/{kp.address}")
    now = int(time.time())
    tx = types.Tx(
        version=1, network=args.network, chain=types.chain_id(args.network),
        tx_type=tx_type, signer=kp.address, nonce=account.get("nonce", 0),
        created_at=now, expires_at=min(expires_at or now + 3600, now + 3600),
        fee_asset=args.fee_asset or types.native_asset(args.network),
        max_fee=args.max_fee, payload=payload,
    )
    tx.sign(kp)
    return tx


def _apply_uri(args: argparse.Namespace) -> paymenturi.Payment | None:
    if not args.uri:
        args.network = args.network or "arne:devnet"
        return None
    payment = paymenturi.parse(args.uri)
    if args.network is not None:
        payment.check_network(args.network)
    payment.check_recipient(args.to)
    payment.check_amount(args.amount)
    payment.check_intent(args.intent)
    if args.asset and args.asset != payment.asset:
        raise paymenturi.URIError("uri asset conflicts with --asset")
    if args.memo and payment.memo and args.memo != payment.memo:
        raise SystemExit("uri memo conflicts with --memo")
    args.network, args.to, args.asset = payment.network, payment.to, payment.asset
    args.amount = payment.amount
    args.intent = payment.intent
    args.memo = args.memo or payment.memo
    return payment


def _transfer_payload(args: argparse.Namespace, units: int) -> dict[str, Any]:
    payload: dict[str, Any] = {"asset": args.asset, "to": args.to, "amount": units}
    if args.intent:
        payload["intent"] = args.intent
    if args.refund_of:
        payload["refund_of"] = args.refund_of
    if args.memo and args.memo_to:
        payload["memo_enc"] = memo_codec.seal(bytes.fromhex(args.memo_to), args.memo.encode())
    elif args.memo:
        print("warning: --memo is public on chain; use --memo-to to encrypt it", file=sys.stderr)
        payload["memo"] = args.memo
    return payload


def _fetch_intent(url: str, network: str, intent_id: str) -> types.Intent:
    if re.fullmatch(r"[0-9a-f]{32}", intent_id) is None:
        raise ValueError("intent ID must be 32 lowercase hex characters")
    actual = _get(url, "/v1/status")["network"]
    if actual != network:
        raise ValueError(f"API network {actual} conflicts with {network}")
    intent = types.Intent.from_cbor(_get(url, f"/v1/intents/{intent_id}"))
    if intent.id != intent_id or types.intent_id(network, intent.payee, intent.reference) != intent_id:
        raise ValueError("API returned a non-canonical intent")
    return intent


def _resolve_intent(args: argparse.Namespace, uri: paymenturi.Payment | None) -> paymenturi.Payment | None:
    if not args.intent:
        return uri
    intent = _fetch_intent(args.url, args.network, args.intent)
    if intent.status != types.INTENT_OPEN:
        raise ValueError(f"intent is {intent.status}; payment transaction: {intent.tx_hash}")
    if intent.expires_at and int(time.time()) > intent.expires_at:
        raise ValueError("intent expired")
    if (args.to and args.to != intent.payee) or (args.asset and args.asset != intent.asset):
        raise ValueError("recipient or asset conflicts with intent")
    decimals = _get(args.url, f"/v1/assets/{intent.asset}")["decimals"]
    if args.amount and parse_amount(args.amount, decimals) != intent.amount:
        raise ValueError("amount conflicts with intent")
    args.to, args.asset = intent.payee, intent.asset
    args.amount, args.memo = format_amount(intent.amount, decimals), args.memo or intent.memo
    expiry = uri.expires if uri else 0
    if intent.expires_at and (not expiry or intent.expires_at < expiry):
        expiry = intent.expires_at
    payment = paymenturi.Payment(
        scheme=uri.scheme if uri else "arne", network=args.network, to=args.to,
        asset=args.asset, amount=args.amount, memo=args.memo, intent=args.intent, expires=expiry,
    )
    return paymenturi.parse(str(payment))


def _fetch_transfer(url: str, network: str, tx_hash: str) -> dict[str, Any]:
    if not types.valid_tx_hash(tx_hash):
        raise ValueError("--refund-of must be a 64-character lowercase hex transaction hash")
    actual = _get(url, "/v1/status")["network"]
    if actual != network:
        raise ValueError(f"API network {actual} conflicts with {network}")
    return _get(url, f"/v1/transfers/{tx_hash}")


def _resolve_refund(args: argparse.Namespace) -> None:
    """Fill a refund from the original transfer; the ledger repeats every check."""
    if not args.refund_of:
        return
    if args.intent:
        raise ValueError("use --intent or --refund-of, not both")
    transfer = _fetch_transfer(args.url, args.network, args.refund_of)
    if transfer["hash"] != args.refund_of:
        raise ValueError("API returned a different transfer")
    if transfer["refund_of"]:
        raise ValueError("cannot refund a refund")
    if not transfer["refundable"]:
        raise ValueError("transfer is already fully refunded")
    if (args.to and args.to != transfer["from"]) or (args.asset and args.asset != transfer["asset"]):
        raise ValueError("recipient or asset conflicts with --refund-of")
    decimals = _get(args.url, f"/v1/assets/{transfer['asset']}")["decimals"]
    if args.amount and parse_amount(args.amount, decimals) > transfer["refundable"]:
        raise ValueError(f"refund exceeds remaining {format_amount(transfer['refundable'], decimals)}")
    args.to, args.asset = transfer["from"], transfer["asset"]
    if not args.amount:
        args.amount = format_amount(transfer["refundable"], decimals)


def cmd_intent_create(args: argparse.Namespace) -> int:
    if not types.valid_intent_reference(args.reference):
        raise ValueError("reference must be 1-64 letters, digits, _ or -")
    asset = _get(args.url, f"/v1/assets/{args.asset}")
    payload = {"asset": args.asset, "amount": parse_amount(args.amount, asset["decimals"]),
               "reference": args.reference}
    if args.expires:
        payload["expires_at"] = args.expires
    if args.memo:
        payload["memo"] = args.memo
    tx = _signed_tx(args, types.TX_CREATE_INTENT, payload)
    print(f"intent: {types.intent_id(args.network, tx.signer, args.reference)}")
    return _submit(args.url, tx, args.wait)


def cmd_intent_show(args: argparse.Namespace) -> int:
    print(json.dumps(_fetch_intent(args.url, args.network, args.id).to_cbor(), indent=2))
    return 0


def cmd_intent_link(args: argparse.Namespace) -> int:
    args.intent = args.id
    payment = _resolve_intent(args, None)
    print(str(payment))
    if args.qr:
        _write_intent_qr(args.qr, str(payment))
    return 0


def _write_intent_qr(path: str, uri: str) -> None:
    import io

    import segno

    image = io.BytesIO()
    segno.make_qr(uri, error="m", boost_error=False).save(image, kind="png", scale=8)
    with open(path, "xb") as output:
        output.write(image.getvalue())


def cmd_pay(args: argparse.Namespace) -> int:
    uri = _apply_uri(args)
    _resolve_refund(args)
    uri = _resolve_intent(args, uri)
    if uri:
        print(uri.summary())
        if not args.yes:
            print("payment not sent: review the summary above and re-run with --yes", file=sys.stderr)
            return 1
    if not (args.to and args.asset and args.amount):
        raise SystemExit("--to, --asset, and --amount are required without --uri")
    validate_address(args.to, args.network)
    asset = _get(args.url, f"/v1/assets/{args.asset}")
    units = parse_amount(args.amount, asset["decimals"])
    payload = _transfer_payload(args, units)
    expires = uri.expires if uri and uri.expires else None
    return _submit(args.url, _signed_tx(args, types.TX_TRANSFER, payload, expires), args.wait)


def _asset_optional(url: str, asset_id: str) -> dict | None:
    with _client(url) as client:
        response = client.get(f"/v1/assets/{asset_id}")
        if response.status_code == 404:
            return None
        if response.status_code >= 300:
            raise SystemExit(_fail(response))
        return response.json()


def _simulate(url: str, tx: types.Tx) -> dict:
    with _client(url) as client:
        response = client.post(
            "/v1/transactions/simulate", content=cbor.encode(tx.to_cbor()),
            headers={"Content-Type": "application/cbor"},
        )
        if response.status_code >= 300:
            try:
                return {"code": response.json()["error"]["code"]}
            except (ValueError, KeyError, TypeError):
                raise SystemExit(_fail(response)) from None
        return response.json()


def cmd_preview(args: argparse.Namespace) -> int:
    uri = _apply_uri(args)
    _resolve_refund(args)
    uri = _resolve_intent(args, uri)
    if not (args.to and args.asset and args.amount):
        raise SystemExit("--to, --asset, and --amount are required without --uri")
    validate_address(args.to, args.network)
    asset = _asset_optional(args.url, args.asset)
    decimals = asset["decimals"] if asset else 8
    units = parse_amount(args.amount, decimals)
    payload = _transfer_payload(args, units)
    expires = uri.expires if uri and uri.expires else None
    tx = _signed_tx(args, types.TX_TRANSFER, payload, expires)
    receipt = _simulate(args.url, tx)
    kp = keypair_from_mnemonic(args.mnemonic, "", 0, 0, args.index, args.network)
    fee = receipt.get("fee", 0)  # failed transactions are free in v1
    result = preview.build(preview.Input(
        network=args.network, signer=kp.address, to=args.to, asset=args.asset,
        symbol=asset["symbol"] if asset else "", decimals=decimals,
        amount=units, fee=fee, memo=args.memo,
        public_memo=bool(args.memo and not args.memo_to),
        asset_known=asset is not None, known=args.known,
        simulated=True, code=receipt.get("code", ""),
    ))
    if args.json:
        payload = {
            "network": result.network, "signer": result.signer, "to": result.to,
            "asset": result.asset, "amount": result.amount, "fee": result.fee,
            "warnings": result.warnings, "ok": result.ok,
        }
        if result.memo:
            payload["memo"] = result.memo
        print(json.dumps(payload, indent=2))
    else:
        print(result.summary())
    return 0


def cmd_memo_pub(args: argparse.Namespace) -> int:
    kp = keypair_from_mnemonic(args.mnemonic, "", 0, 0, args.index, args.network)
    print(memo_codec.public_key(kp.private).hex())
    return 0


def cmd_memo_seal(args: argparse.Namespace) -> int:
    print(memo_codec.seal(bytes.fromhex(args.recipient), args.text.encode()).hex())
    return 0


def cmd_memo_open(args: argparse.Namespace) -> int:
    try:
        raw = bytes.fromhex(args.sealed)
    except ValueError:
        raw = base64.b64decode(args.sealed, validate=True)
    kp = keypair_from_mnemonic(args.mnemonic, "", 0, 0, args.index, args.network)
    print(memo_codec.open_memo(kp.private, raw).decode())
    return 0


def cmd_uri_parse(args: argparse.Namespace) -> int:
    payment = paymenturi.parse(args.uri)
    if args.json:
        print(json.dumps({
            "scheme": payment.scheme, "network": payment.network, "to": payment.to,
            "asset": payment.asset, "amount": payment.amount, "memo": payment.memo,
            "expires": payment.expires, "warnings": payment.warnings,
            "intent": payment.intent,
            "canonical": str(payment),
        }, indent=2))
    else:
        print(payment.summary())
    return 0


def cmd_uri_build(args: argparse.Namespace) -> int:
    from urllib.parse import quote_plus

    raw = (
        f"{args.scheme}:{args.to}?network={quote_plus(args.network)}"
        f"&asset={quote_plus(args.asset)}&amount={quote_plus(args.amount)}"
    )
    payment = paymenturi.parse(raw)
    payment.memo, payment.expires, payment.intent = args.memo, args.expires, args.intent
    if args.memo or args.expires or args.intent:
        paymenturi.parse(str(payment))
    print(str(payment))
    return 0


def cmd_webhook_keygen(args: argparse.Namespace) -> int:
    import secrets

    from . import webhook as webhook_codec

    seed = secrets.token_bytes(32)
    signer = webhook_codec.Signer(seed)
    print(f"seed:       {seed.hex()}\npublic_key: {signer.public_key}")
    return 0


def cmd_faucet(args: argparse.Namespace) -> int:
    result = _post(args.url, "/v1/faucet", {"address": args.address})
    print(result["hash"])
    return _wait_for_receipt(args.url, result["hash"]) if args.wait else 0


def cmd_asset_list(args: argparse.Namespace) -> int:
    for asset in _get(args.url, "/v1/assets")["items"]:
        print(
            f"{asset['id']:<24} {asset['symbol']:<8} supply={asset['supply']} "
            f"policy={asset['policy']} decimals={asset['decimals']}"
        )
    return 0


def cmd_asset_register(args: argparse.Namespace) -> int:
    asset_id = types.asset_id(args.symbol, args.network)
    payload = {
        "asset": {
            "id": asset_id, "class": args.asset_class, "symbol": args.symbol.upper(),
            "name": args.name, "decimals": args.decimals, "policy": args.policy,
            "max_supply": args.max_supply, "issuer": "", "registered": 0, "meta": {},
        }
    }
    return _submit(args.url, _signed_tx(args, types.TX_REGISTER_ASSET, payload), args.wait)


# --- parser ---


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="arne", description="AnorebelNetwork node and client")
    parser.add_argument("--version", action="version", version="arne 0.1.0")
    sub = parser.add_subparsers(dest="command", required=True)

    serve = sub.add_parser("serve", help="run a node and API server")
    serve.add_argument("--network", default="arne:devnet")
    serve.add_argument("--listen", default="127.0.0.1:8080")
    serve.add_argument("--data-dir", default=".arne-devnet")
    serve.add_argument("--mnemonic", default="")
    serve.add_argument("--block-interval", type=float, default=2.0)
    serve.add_argument("--ephemeral", action="store_true")
    serve.add_argument("--log-level", default="info")
    serve.add_argument("--api-key", action="append", default=[], help="key=read,write[,admin]")
    serve.add_argument("--allow-origin", action="append", default=[], help="CORS origin")
    serve.add_argument("--rate-limit", type=int, default=200, help="requests per second per IP")
    serve.add_argument("--webhook", action="append", default=[], help="webhook endpoint URL")
    serve.add_argument("--webhook-key", default="", help="hex Ed25519 seed for webhooks")
    serve.set_defaults(func=cmd_serve)

    devnet = sub.add_parser("devnet", help="alias for serve with devnet settings")
    devnet.add_argument("--network", default="arne:devnet")
    devnet.add_argument("--listen", default="127.0.0.1:8080")
    devnet.add_argument("--data-dir", default=".arne-devnet")
    devnet.add_argument("--mnemonic", default="")
    devnet.add_argument("--block-interval", type=float, default=2.0)
    devnet.add_argument("--ephemeral", action="store_true")
    devnet.add_argument("--log-level", default="info")
    devnet.add_argument("--api-key", action="append", default=[], help="key=read,write[,admin]")
    devnet.add_argument("--allow-origin", action="append", default=[], help="CORS origin")
    devnet.add_argument("--rate-limit", type=int, default=200, help="requests per second per IP")
    devnet.add_argument("--webhook", action="append", default=[], help="webhook endpoint URL")
    devnet.add_argument("--webhook-key", default="", help="hex Ed25519 seed for webhooks")
    devnet.set_defaults(func=cmd_serve)

    wallet = sub.add_parser("wallet", help="offline key management")
    wallet_sub = wallet.add_subparsers(dest="wallet_command", required=True)
    new = wallet_sub.add_parser("new", help="generate a random mnemonic and address")
    new.add_argument("--network", default="arne:devnet")
    new.set_defaults(func=cmd_wallet_new)
    address = wallet_sub.add_parser("address", help="derive an address from a mnemonic")
    address.add_argument("--mnemonic", required=True)
    address.add_argument("--index", type=int, default=0)
    address.add_argument("--network", default="arne:devnet")
    address.set_defaults(func=cmd_wallet_address)

    status = sub.add_parser("status", help="node status")
    status.add_argument("--url", default=DEFAULT_URL)
    status.set_defaults(func=cmd_status)

    balance = sub.add_parser("balance", help="account balance")
    balance.add_argument("--address", required=True)
    balance.add_argument("--asset", required=True)
    balance.add_argument("--url", default=DEFAULT_URL)
    balance.set_defaults(func=cmd_balance)

    proof = sub.add_parser("proof", help="state proof for a namespaced key")
    proof.add_argument("--key", required=True)
    proof.add_argument("--url", default=DEFAULT_URL)
    proof.set_defaults(func=cmd_proof)

    def write_flags(command: argparse.ArgumentParser) -> None:
        command.add_argument("--mnemonic", required=True)
        command.add_argument("--index", type=int, default=0)
        command.add_argument("--network", default="arne:devnet")
        command.add_argument("--fee-asset", default="")
        command.add_argument("--max-fee", type=int, default=10_000)
        command.add_argument("--url", default=DEFAULT_URL)
        command.add_argument("--wait", action="store_true")

    pay = sub.add_parser("pay", help="sign and submit a transfer")
    pay.add_argument("--to", default="")
    pay.add_argument("--asset", default="")
    pay.add_argument("--amount", default="")
    pay.add_argument("--memo", default="")
    pay.add_argument("--memo-to", default="", help="recipient memo public key (hex)")
    pay.add_argument("--uri", default="", help="arce:/arne: payment URI")
    pay.add_argument("--intent", default="", help="payment intent ID")
    pay.add_argument("--refund-of", default="", help="original transaction hash to refund")
    pay.add_argument("--yes", action="store_true", help="confirm the URI preview")
    write_flags(pay)
    pay.set_defaults(func=cmd_pay, network=None)

    preview_cmd = sub.add_parser("preview", help="show a transfer before signing")
    preview_cmd.add_argument("--to", default="")
    preview_cmd.add_argument("--asset", default="")
    preview_cmd.add_argument("--amount", default="")
    preview_cmd.add_argument("--memo", default="")
    preview_cmd.add_argument("--memo-to", default="", help="recipient memo public key (hex)")
    preview_cmd.add_argument("--uri", default="", help="arce:/arne: payment URI")
    preview_cmd.add_argument("--intent", default="", help="payment intent ID")
    preview_cmd.add_argument("--refund-of", default="", help="original transaction hash to refund")
    preview_cmd.add_argument("--known", action="append", default=[], help="trusted contact")
    preview_cmd.add_argument("--json", action="store_true")
    write_flags(preview_cmd)
    preview_cmd.set_defaults(func=cmd_preview, network=None)

    intent = sub.add_parser("intent", help="create, inspect, and link payment intents")
    intent_sub = intent.add_subparsers(dest="intent_command", required=True)
    intent_create = intent_sub.add_parser("create", help="create an on-chain request signed by the payee")
    intent_create.add_argument("--asset", required=True)
    intent_create.add_argument("--amount", required=True)
    intent_create.add_argument("--reference", required=True)
    intent_create.add_argument("--memo", default="")
    intent_create.add_argument("--expires", type=int, default=0, help="Unix expiry (0 means no expiry)")
    write_flags(intent_create)
    intent_create.set_defaults(func=cmd_intent_create)
    for name, function in (("show", cmd_intent_show), ("link", cmd_intent_link)):
        command = intent_sub.add_parser(name)
        command.add_argument("--id", required=True)
        command.add_argument("--network", default="arne:devnet")
        command.add_argument("--url", default=DEFAULT_URL)
        if name == "link":
            command.add_argument("--qr", default="", help="write a PNG QR code to a new file")
        command.set_defaults(func=function, to="", asset="", amount="", memo="")

    memo = sub.add_parser("memo", help="opt-in encrypted memos")
    memo_sub = memo.add_subparsers(dest="memo_command", required=True)
    memo_pub = memo_sub.add_parser("pub", help="print your memo public key")
    memo_pub.add_argument("--mnemonic", required=True)
    memo_pub.add_argument("--index", type=int, default=0)
    memo_pub.add_argument("--network", default="arne:devnet")
    memo_pub.set_defaults(func=cmd_memo_pub)
    memo_seal = memo_sub.add_parser("seal", help="encrypt memo text to a recipient key")
    memo_seal.add_argument("--recipient", required=True, help="memo public key (hex)")
    memo_seal.add_argument("--text", required=True)
    memo_seal.set_defaults(func=cmd_memo_seal)
    memo_open = memo_sub.add_parser("open", help="decrypt a sealed memo")
    memo_open.add_argument("--mnemonic", required=True)
    memo_open.add_argument("--index", type=int, default=0)
    memo_open.add_argument("--network", default="arne:devnet")
    memo_open.add_argument("--sealed", required=True, help="hex or base64")
    memo_open.set_defaults(func=cmd_memo_open)

    uri = sub.add_parser("uri", help="parse and build payment URIs")
    uri_sub = uri.add_subparsers(dest="uri_command", required=True)
    uri_parse = uri_sub.add_parser("parse", help="validate a payment URI")
    uri_parse.add_argument("--uri", required=True)
    uri_parse.add_argument("--json", action="store_true")
    uri_parse.set_defaults(func=cmd_uri_parse)
    uri_build = uri_sub.add_parser("build", help="build a canonical payment URI")
    uri_build.add_argument("--scheme", default="arce")
    uri_build.add_argument("--network", default="arne:devnet")
    uri_build.add_argument("--to", required=True)
    uri_build.add_argument("--asset", required=True)
    uri_build.add_argument("--amount", required=True)
    uri_build.add_argument("--memo", default="")
    uri_build.add_argument("--intent", default="", help="payment intent ID")
    uri_build.add_argument("--expires", type=int, default=0)
    uri_build.set_defaults(func=cmd_uri_build)

    webhook = sub.add_parser("webhook", help="webhook signing keys")
    webhook_sub = webhook.add_subparsers(dest="webhook_command", required=True)
    webhook_keygen = webhook_sub.add_parser("keygen", help="generate a signing seed")
    webhook_keygen.set_defaults(func=cmd_webhook_keygen)

    faucet = sub.add_parser("faucet", help="request devnet funds")
    faucet.add_argument("--address", required=True)
    faucet.add_argument("--url", default=DEFAULT_URL)
    faucet.add_argument("--wait", action="store_true")
    faucet.set_defaults(func=cmd_faucet)

    asset = sub.add_parser("asset", help="asset registry")
    asset_sub = asset.add_subparsers(dest="asset_command", required=True)
    listing = asset_sub.add_parser("list")
    listing.add_argument("--url", default=DEFAULT_URL)
    listing.set_defaults(func=cmd_asset_list)
    register = asset_sub.add_parser("register")
    register.add_argument("--symbol", required=True)
    register.add_argument("--name", required=True)
    register.add_argument("--decimals", type=int, default=2)
    register.add_argument("--policy", default="capped")
    register.add_argument("--max-supply", type=int, default=0)
    register.add_argument("--class", dest="asset_class", default="fiat")
    write_flags(register)
    register.set_defaults(func=cmd_asset_register)

    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except (ValueError, ledger.LedgerError, types.ValidationError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
