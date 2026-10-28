"""Example ARNE client: sign transfers and escrows outside the node process.

Run against a devnet:

    uv run python examples/python/client.py pay \
      --mnemonic "$MNEMONIC" --to arce-devnet1... --amount 12.5
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from decimal import Decimal

import httpx
from arne import cbor, types
from arne.keys import keypair_from_mnemonic

NETWORK = "arne:devnet"
NATIVE = "arce:devnet"
FEE_LIMIT = 10_000


def get(base: str, path: str):
    response = httpx.get(base + path, timeout=15)
    response.raise_for_status()
    return response.json()


def account(base: str, address: str) -> dict:
    return get(base, f"/v1/accounts/{address}")


def units(base: str, asset: str, amount: str) -> int:
    info = get(base, f"/v1/assets/{asset}")
    value = Decimal(amount) * (10 ** info["decimals"])
    if value != value.to_integral_value():
        raise SystemExit(f"amount has more than {info['decimals']} decimals")
    return int(value)


def sign(args, tx_type: str, payload: dict) -> bytes:
    kp = keypair_from_mnemonic(args.mnemonic, "", 0, 0, args.index, args.network)
    info = account(args.url, kp.address)
    now = int(time.time())
    tx = types.Tx(
        version=1, network=args.network, chain=types.chain_id(args.network),
        tx_type=tx_type, signer=kp.address, nonce=info["nonce"], created_at=now,
        expires_at=now + 3600, fee_asset=types.native_asset(args.network),
        max_fee=FEE_LIMIT, payload=payload,
    )
    tx.sign(kp)
    return cbor.encode(tx.to_cbor())


def submit(args, raw: bytes) -> dict:
    response = httpx.post(
        args.url + "/v1/transactions", content=raw,
        headers={"Content-Type": "application/cbor"}, timeout=15,
    )
    if response.status_code >= 300:
        raise SystemExit(f"{response.status_code}: {response.text}")
    tx_hash = response.json()["hash"]
    print("submitted", tx_hash)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            receipt = get(args.url, f"/v1/transactions/{tx_hash}")
        except httpx.HTTPStatusError:
            time.sleep(0.5)
            continue
        print(receipt)
        return receipt
    raise SystemExit("timed out waiting for receipt")


def cmd_pay(args) -> None:
    payload = {
        "asset": args.asset, "to": args.to,
        "amount": units(args.url, args.asset, args.amount),
    }
    if args.memo:
        payload["memo"] = args.memo
    submit(args, sign(args, types.TX_TRANSFER, payload))


def cmd_escrow(args) -> None:
    payload = {
        "asset": args.asset, "beneficiary": args.beneficiary,
        "arbiter": args.arbiter, "amount": units(args.url, args.asset, args.amount),
        "timeout": int(time.time()) + args.timeout,
    }
    submit(args, sign(args, types.TX_CREATE_ESCROW, payload))


def cmd_release(args) -> None:
    submit(args, sign(args, types.TX_RELEASE_ESCROW, {"id": args.id}))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default=os.environ.get("ARNE_API", "http://127.0.0.1:8080"))
    parser.add_argument("--network", default=NETWORK)
    parser.add_argument("--mnemonic", default=os.environ.get("ARNE_MNEMONIC", ""))
    parser.add_argument("--index", type=int, default=0)
    sub = parser.add_subparsers(dest="command", required=True)

    pay = sub.add_parser("pay", help="transfer an asset")
    pay.add_argument("--to", required=True)
    pay.add_argument("--asset", default=NATIVE)
    pay.add_argument("--amount", required=True)
    pay.add_argument("--memo", default="")
    pay.set_defaults(func=cmd_pay)

    escrow = sub.add_parser("escrow", help="create an escrow")
    escrow.add_argument("--beneficiary", required=True)
    escrow.add_argument("--arbiter", required=True)
    escrow.add_argument("--asset", default=NATIVE)
    escrow.add_argument("--amount", required=True)
    escrow.add_argument("--timeout", type=int, default=86_400, help="seconds from now")
    escrow.set_defaults(func=cmd_escrow)

    release = sub.add_parser("release", help="release an escrow (payer or arbiter)")
    release.add_argument("--id", required=True)
    release.set_defaults(func=cmd_release)

    args = parser.parse_args()
    if not args.mnemonic:
        parser.error("--mnemonic or ARNE_MNEMONIC is required")
    args.func(args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
