"""Command-line interface: run a node, manage wallets, and submit transactions."""

from __future__ import annotations

import argparse
import base64
import json
import os
import sys
import threading
import time
from decimal import Decimal, InvalidOperation
from typing import Any

import httpx

from . import cbor, ledger, types
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
    config = Config(
        network=network, chain=chain, params=ledger.default_params(),
        proposer=funder.address, faucet=faucet,
        faucet_amount=100 * 100_000_000, devnet=True,
    )
    node = Node.open(config, devnet_genesis(network, int(time.time()), funder, faucet), store)
    if generated:
        print("Generated devnet mnemonic (write it down):")
        print("  " + mnemonic)
    print(
        f"network:  {network}\nchain:    {chain}\nfunder:   {funder.address}\n"
        f"faucet:   {faucet.address}\napi:      http://{args.listen.replace('0.0.0.0', '127.0.0.1')}"
    )

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
    uvicorn.run(app, host=host or "127.0.0.1", port=int(port), log_level=args.log_level)
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


def _signed_tx(args: argparse.Namespace, tx_type: str, payload: dict) -> types.Tx:
    kp = keypair_from_mnemonic(args.mnemonic, "", 0, 0, args.index, args.network)
    account = _get(args.url, f"/v1/accounts/{kp.address}")
    now = int(time.time())
    tx = types.Tx(
        version=1, network=args.network, chain=types.chain_id(args.network),
        tx_type=tx_type, signer=kp.address, nonce=account.get("nonce", 0),
        created_at=now, expires_at=now + 3600,
        fee_asset=args.fee_asset or types.native_asset(args.network),
        max_fee=args.max_fee, payload=payload,
    )
    tx.sign(kp)
    return tx


def cmd_pay(args: argparse.Namespace) -> int:
    validate_address(args.to, args.network)
    asset = _get(args.url, f"/v1/assets/{args.asset}")
    units = parse_amount(args.amount, asset["decimals"])
    payload: dict[str, Any] = {"asset": args.asset, "to": args.to, "amount": units}
    if args.memo and args.memo_to:
        payload["memo_enc"] = memo_codec.seal(bytes.fromhex(args.memo_to), args.memo.encode())
    elif args.memo:
        print("warning: --memo is public on chain; use --memo-to to encrypt it", file=sys.stderr)
        payload["memo"] = args.memo
    return _submit(args.url, _signed_tx(args, types.TX_TRANSFER, payload), args.wait)


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
    pay.add_argument("--to", required=True)
    pay.add_argument("--asset", required=True)
    pay.add_argument("--amount", required=True)
    pay.add_argument("--memo", default="")
    pay.add_argument("--memo-to", default="", help="recipient memo public key (hex)")
    write_flags(pay)
    pay.set_defaults(func=cmd_pay)

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
