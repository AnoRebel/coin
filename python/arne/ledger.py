"""ARNE multi-asset account ledger. Mirrors internal/ledger."""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass, field
from typing import Any

from . import cbor, smt, types
from .address import validate_address
from .types import Tx

# --- error codes (stable API contract, identical to Go) ---

INVALID_SCHEMA = "INVALID_SCHEMA"
INVALID_PAYLOAD = "INVALID_PAYLOAD"
BAD_SIGNATURE = "BAD_SIGNATURE"
WRONG_NETWORK = "WRONG_NETWORK"
EXPIRED = "EXPIRED"
BAD_NONCE = "BAD_NONCE"
FEE_TOO_LOW = "FEE_TOO_LOW"
NO_FEE_ROUTE = "NO_FEE_ROUTE"
INSUFFICIENT_FUNDS = "INSUFFICIENT_FUNDS"
UNKNOWN_ASSET = "UNKNOWN_ASSET"
ASSET_EXISTS = "ASSET_EXISTS"
UNAUTHORIZED = "UNAUTHORIZED"
SUPPLY_LIMIT = "SUPPLY_LIMIT"
ESCROW_NOT_FOUND = "ESCROW_NOT_FOUND"
ESCROW_CLOSED = "ESCROW_CLOSED"
ALLOWANCE_EXCEEDED = "ALLOWANCE_EXCEEDED"
LIMIT_EXCEEDED = "LIMIT_EXCEEDED"
CONSERVATION = "CONSERVATION_VIOLATION"
INVARIANT = "INVARIANT_VIOLATION"


class LedgerError(Exception):
    """Typed transaction error with the same code strings as Go."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


class ConservationError(LedgerError):
    def __init__(self, message: str) -> None:
        super().__init__(CONSERVATION, message)


class InvariantError(LedgerError):
    def __init__(self, message: str) -> None:
        super().__init__(INVARIANT, message)


def _err(code: str, message: str) -> LedgerError:
    return LedgerError(code, message)


# --- params ---


@dataclass
class Params:
    base_fee: int = 1000
    min_fee: int = 1000
    max_priority_fee: int = 1_000_000_000_000
    fee_burn_bps: int = 5000
    fee_validator_bps: int = 3000
    fee_treasury_bps: int = 2000
    max_memo_bytes: int = 256
    max_batch_items: int = 100
    native_max_supply: int = 100_000_000_000_000_000  # 1e9 ARCE at 8 decimals
    max_tx_validity: int = 30 * 24 * 3600

    def validate(self) -> None:
        if self.fee_burn_bps + self.fee_validator_bps + self.fee_treasury_bps != 10_000:
            raise types.ValidationError("fee split must total 10000 bps")
        if self.native_max_supply == 0 or self.max_batch_items == 0:
            raise types.ValidationError("zero parameter")
        if self.min_fee > self.base_fee:
            raise types.ValidationError("min_fee above base_fee")


def default_params() -> Params:
    return Params()


# --- state keys (consensus-critical, mirrored from Go) ---

K_NONCE = "account/%s/nonce"
K_AUTH = "account/%s/auth"
K_BALANCE = "balance/%s/%s"
K_ASSET = "asset/%s"
K_SUPPLY = "supply/%s"
K_BURNED = "burned/%s"
K_ESCROW = "escrow/%s"
K_ESCROW_AMT = "escrow_held/%s"
K_ALLOWANCE = "allowance/%s/%s/%s"
K_ALLOW_EXP = "allowance_expiry/%s/%s/%s"
K_POOL = "pool/%s/%s"
K_ROUTE = "route/%s"
K_META = "meta/%s"

POOL_FEE_SWAP = "fee_swap"
POOL_VALIDATORS = "validators"
POOL_TREASURY = "treasury"
POOL_FAUCET = "faucet"
POOL_COMMUNITY = "community"

CHECK_TX_WINDOW = 64


def _encode_uint(value: int) -> bytes:
    return value.to_bytes(8, "big")


def _decode_uint(raw: bytes | None) -> int:
    if not raw or len(raw) != 8:
        return 0
    return int.from_bytes(raw, "big")


@dataclass
class Receipt:
    hash: str
    height: int
    success: bool
    fee: int = 0
    fee_asset: str = ""
    code: str = ""
    error: str = ""
    events: list[dict[str, Any]] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {
            "hash": self.hash, "height": self.height, "success": self.success,
            "fee": self.fee, "fee_asset": self.fee_asset,
            "events": self.events or [],
        }
        if not self.success:
            out["code"] = self.code
            out["error"] = self.error
        return out


@dataclass
class AccountInfo:
    address: str
    nonce: int
    auth: str
    balances: dict[str, int]


class State:
    """Deterministic ledger state with a rollback journal."""

    def __init__(self, network: str, chain: str, params: Params) -> None:
        types.network_class(network)
        if types.chain_id(network) != chain:
            raise _err(WRONG_NETWORK, f"chain {chain!r}")
        params.validate()
        self.network = network
        self.chain = chain
        self.params = params
        self.tree = smt.Tree()
        self._journal: list[tuple[str, bytes | None, str]] = []
        self._depth = 0

    @classmethod
    def from_snapshot(
        cls, network: str, chain: str, params: Params, values: dict[str, bytes]
    ) -> State:
        state = cls(network, chain, params)
        state.tree.load(values)
        return state

    # --- journal ---

    def begin(self) -> None:
        self._depth += 1

    def commit(self) -> None:
        if self._depth == 0:
            return
        while self._journal and int(self._journal[-1][2]) >= self._depth:
            self._journal.pop()
        self._depth -= 1

    def rollback(self) -> None:
        if self._depth == 0:
            return
        while self._journal and int(self._journal[-1][2]) >= self._depth:
            key, prev, _ = self._journal.pop()
            if prev is None:
                self.tree.delete(key)
            else:
                self.tree.set(key, prev)
        self._depth -= 1

    def _set(self, key: str, value: bytes) -> None:
        if self._depth > 0:
            prev = self.tree.get(key)
            self._journal.append((key, prev, self._depth))
        self.tree.set(key, value)

    def _del(self, key: str) -> None:
        if self._depth > 0:
            prev = self.tree.get(key)
            if prev is not None:
                self._journal.append((key, prev, self._depth))
        self.tree.delete(key)

    def _get(self, key: str) -> bytes | None:
        return self.tree.get(key)

    def _set_uint(self, key: str, value: int) -> None:
        self._set(key, _encode_uint(value))

    def _get_uint(self, key: str) -> int:
        return _decode_uint(self._get(key))

    # --- meta ---

    def root(self) -> bytes:
        return self.tree.state_root()

    def snapshot(self) -> dict[str, bytes]:
        return self.tree.snapshot()

    def height(self) -> int:
        return self._get_uint(K_META % "height")

    def set_height(self, height: int) -> None:
        self._set_uint(K_META % "height", height)

    def block_time(self) -> int:
        return self._get_uint(K_META % "time")

    def set_block_time(self, when: int) -> None:
        self._set_uint(K_META % "time", when)

    # --- balances ---

    def balance(self, addr: str, asset: str) -> int:
        return self._get_uint(K_BALANCE % (addr, asset))

    def _set_balance(self, addr: str, asset: str, amount: int) -> None:
        if amount < 0:
            raise InvariantError("negative balance")
        self._set_uint(K_BALANCE % (addr, asset), amount)

    def _add_balance(self, addr: str, asset: str, delta: int) -> None:
        self._set_balance(addr, asset, self.balance(addr, asset) + delta)

    def _sub_balance(self, addr: str, asset: str, delta: int) -> None:
        current = self.balance(addr, asset)
        if current < delta:
            raise _err(INSUFFICIENT_FUNDS, f"insufficient {asset} balance")
        self._set_balance(addr, asset, current - delta)

    def pool_balance(self, pool: str, asset: str) -> int:
        return self._get_uint(K_POOL % (pool, asset))

    def _add_pool(self, pool: str, asset: str, delta: int) -> None:
        key = K_POOL % (pool, asset)
        self._set_uint(key, self._get_uint(key) + delta)

    def _sub_pool(self, pool: str, asset: str, delta: int) -> None:
        key = K_POOL % (pool, asset)
        current = self._get_uint(key)
        if current < delta:
            raise _err(INSUFFICIENT_FUNDS, f"pool {pool}/{asset} underfunded")
        self._set_uint(key, current - delta)

    def supply(self, asset: str) -> int:
        return self._get_uint(K_SUPPLY % asset)

    def burned(self, asset: str) -> int:
        return self._get_uint(K_BURNED % asset)

    def _add_burned(self, asset: str, amount: int) -> None:
        key = K_BURNED % asset
        self._set_uint(key, self._get_uint(key) + amount)

    # --- assets ---

    def asset(self, asset_id: str) -> types.Asset | None:
        raw = self._get(K_ASSET % asset_id)
        if raw is None:
            return None
        return types.Asset.from_cbor(cbor.decode_map(raw))

    def _put_asset(self, asset: types.Asset) -> None:
        self._set(K_ASSET % asset.id, cbor.encode(asset.to_cbor()))

    def assets(self) -> list[str]:
        return sorted(k[len("asset/"):] for k in self.tree.keys() if k.startswith("asset/"))

    def _require_asset(self, asset_id: str) -> None:
        if self.asset(asset_id) is None:
            raise _err(UNKNOWN_ASSET, f"asset {asset_id!r}")

    # --- escrow / allowances / routes ---

    def escrow(self, escrow_id: str) -> types.Escrow | None:
        raw = self._get(K_ESCROW % escrow_id)
        if raw is None:
            return None
        return types.Escrow.from_cbor(cbor.decode_map(raw))

    def _put_escrow(self, escrow: types.Escrow) -> None:
        self._set(K_ESCROW % escrow.id, cbor.encode(escrow.to_cbor()))

    def allowance(self, owner: str, spender: str, asset: str) -> int:
        return self._get_uint(K_ALLOWANCE % (owner, spender, asset))

    def allowance_expiry(self, owner: str, spender: str, asset: str) -> int:
        return self._get_uint(K_ALLOW_EXP % (owner, spender, asset))

    def route(self, asset: str) -> int:
        return self._get_uint(K_ROUTE % asset)

    def set_route(self, asset: str, rate: int) -> None:
        if rate == 0:
            self._del(K_ROUTE % asset)
            return
        self._set_uint(K_ROUTE % asset, rate)

    # --- accounts ---

    def account(self, addr: str) -> tuple[AccountInfo, bool]:
        info = AccountInfo(address=addr, nonce=0, auth="", balances={})
        auth = self._get(K_AUTH % addr)
        if auth is not None:
            info.auth = auth.decode()
        info.nonce = self._get_uint(K_NONCE % addr)
        prefix = "balance/" + addr + "/"
        for key in self.tree.keys():
            if key.startswith(prefix):
                info.balances[key[len(prefix):]] = self._get_uint(key)
        exists = auth is not None or info.nonce > 0 or bool(info.balances)
        return info, exists

    def _ensure_account(self, addr: str) -> None:
        if self._get(K_AUTH % addr) is None:
            self._set(K_AUTH % addr, types.SUITE_ED25519.encode())

    # --- conservation ---

    def check_conservation(self, asset: str) -> None:
        total = 0
        for key in self.tree.keys():
            if key.startswith("balance/") and key.endswith("/" + asset):
                total += self._get_uint(key)
            if key.startswith("pool/") and key.endswith("/" + asset):
                total += self._get_uint(key)
        held = self._get_uint(K_ESCROW_AMT % asset)
        burned = self.burned(asset)
        supply = self.supply(asset)
        if total + held + burned != supply:
            raise ConservationError(
                f"asset {asset}: balances({total})+escrow({held})+burned({burned})"
                f"={total + held + burned} != supply({supply})"
            )

    def check_all(self) -> None:
        for asset_id in self.assets():
            self.check_conservation(asset_id)

    # --- transactions ---

    def apply_tx(self, tx: Tx, now: int, height: int) -> Receipt:
        receipt = Receipt(hash=tx.hash(), height=height, success=False, fee_asset=tx.fee_asset)
        self.begin()
        try:
            fee = self._apply(tx, now, receipt)
        except (types.ValidationError, types.SignatureError, LedgerError, ValueError) as exc:
            self.rollback()
            receipt.success = False
            receipt.code = _code_of(exc)
            receipt.error = str(exc)
            receipt.events = []
            return receipt
        self.commit()
        receipt.success = True
        receipt.fee = fee
        return receipt

    def simulate_tx(self, tx: Tx, now: int, height: int) -> Receipt:
        receipt = Receipt(hash=tx.hash(), height=height, success=False, fee_asset=tx.fee_asset)
        self.begin()
        try:
            fee = self._apply(tx, now, receipt)
        except (types.ValidationError, types.SignatureError, LedgerError, ValueError) as exc:
            receipt.success = False
            receipt.code = _code_of(exc)
            receipt.error = str(exc)
            receipt.events = []
        else:
            receipt.success = True
            receipt.fee = fee
        finally:
            self.rollback()
        return receipt

    def check_tx(self, tx: Tx, now: int) -> None:
        self.begin()
        try:
            current = self._get_uint(K_NONCE % tx.signer)
            if tx.nonce < current or tx.nonce > current + CHECK_TX_WINDOW:
                raise _err(
                    BAD_NONCE,
                    f"expected between {current} and {current + CHECK_TX_WINDOW}, got {tx.nonce}",
                )
            self._set_uint(K_NONCE % tx.signer, tx.nonce)
            self._apply(tx, now, Receipt(hash="", height=0, success=False))
        finally:
            self.rollback()

    def _apply(self, tx: Tx, now: int, receipt: Receipt) -> int:
        try:
            tx.validate()
        except types.ValidationError as exc:
            raise _err(INVALID_SCHEMA, str(exc)) from exc
        if tx.network != self.network or tx.chain != self.chain:
            raise _err(WRONG_NETWORK, f"expected {self.network}/{self.chain}")
        if tx.expires_at and now > tx.expires_at:
            raise _err(EXPIRED, f"expired at {tx.expires_at}")
        if self.params.max_tx_validity and tx.expires_at > now + self.params.max_tx_validity:
            raise _err(INVALID_SCHEMA, "expiry too far in the future")
        try:
            tx.verify()
        except (types.SignatureError, types.ValidationError) as exc:
            raise _err(BAD_SIGNATURE, str(exc)) from exc
        expected = self._get_uint(K_NONCE % tx.signer)
        if tx.nonce != expected:
            raise _err(BAD_NONCE, f"expected {expected}, got {tx.nonce}")
        fee = self._charge_fee(tx)
        self._ensure_account(tx.signer)
        self._set_uint(K_NONCE % tx.signer, tx.nonce + 1)
        self._execute(tx, now, receipt)
        self.check_conservation(tx.fee_asset)
        self.check_conservation(types.native_asset(self.network))
        return fee

    def _charge_fee(self, tx: Tx) -> int:
        if tx.priority_fee > self.params.max_priority_fee:
            raise _err(LIMIT_EXCEEDED, "priority fee above cap")
        fee = self.params.base_fee + tx.priority_fee
        if fee < self.params.min_fee:
            fee = self.params.min_fee
        if tx.max_fee < fee:
            raise _err(FEE_TOO_LOW, f"fee {fee} above max_fee {tx.max_fee}")
        native = types.native_asset(self.network)
        if tx.fee_asset == native:
            self._sub_balance(tx.signer, native, fee)
            self._split_fee(native, fee)
            return fee
        if self.asset(tx.fee_asset) is None:
            raise _err(UNKNOWN_ASSET, f"fee asset {tx.fee_asset!r}")
        rate = self.route(tx.fee_asset)
        if rate == 0:
            raise _err(NO_FEE_ROUTE, f"no fee route for {tx.fee_asset!r}")
        arce_fee = fee * rate // 100_000_000
        self._sub_balance(tx.signer, tx.fee_asset, fee)
        self._add_pool(POOL_TREASURY, tx.fee_asset, fee)
        try:
            self._sub_pool(POOL_FEE_SWAP, native, arce_fee)
        except LedgerError as exc:
            raise _err(NO_FEE_ROUTE, "fee-swap reserve underfunded") from exc
        self._split_fee(native, arce_fee)
        return fee

    def _split_fee(self, native: str, fee: int) -> None:
        burn = fee * self.params.fee_burn_bps // 10_000
        validators = fee * self.params.fee_validator_bps // 10_000
        treasury = fee - burn - validators
        self._add_burned(native, burn)
        self._add_pool(POOL_VALIDATORS, native, validators)
        self._add_pool(POOL_TREASURY, native, treasury)

    # --- execution ---

    def _execute(self, tx: Tx, now: int, receipt: Receipt) -> None:
        handler = {
            types.TX_TRANSFER: self._exec_transfer,
            types.TX_BATCH: self._exec_batch,
            types.TX_REGISTER_ASSET: lambda t, r: self._exec_register_asset(t, now, r),
            types.TX_CREATE_ESCROW: lambda t, r: self._exec_create_escrow(t, now, r),
            types.TX_RELEASE_ESCROW: lambda t, r: self._exec_close_escrow(t, True, now, r),
            types.TX_REFUND_ESCROW: lambda t, r: self._exec_close_escrow(t, False, now, r),
            types.TX_APPROVE: self._exec_approve,
            types.TX_TRANSFER_FROM: lambda t, r: self._exec_transfer_from(t, now, r),
            types.TX_REVOKE_ALLOWANCE: self._exec_revoke,
            types.TX_MINT: self._exec_mint,
            types.TX_BURN: self._exec_burn,
        }.get(tx.tx_type)
        if handler is None:
            raise _err(INVALID_PAYLOAD, f"unsupported tx type {tx.tx_type!r}")
        handler(tx, receipt)

    def _event(self, receipt: Receipt, kind: str, **attrs: Any) -> None:
        receipt.events.append({"type": kind, "attrs": {k: str(v) for k, v in attrs.items()}})

    def _require_address(self, addr: str) -> None:
        try:
            validate_address(addr, self.network)
        except Exception as exc:
            raise _err(INVALID_PAYLOAD, str(exc)) from exc

    def _move(self, sender: str, to: str, asset: str, amount: int, receipt: Receipt) -> None:
        self._sub_balance(sender, asset, amount)
        self._ensure_account(to)
        self._add_balance(to, asset, amount)
        self._event(receipt, "transfer", **{"from": sender, "to": to, "asset": asset, "amount": amount})

    def _exec_transfer(self, tx: Tx, receipt: Receipt) -> None:
        asset = _payload_str(tx.payload, "asset")
        to = _payload_str(tx.payload, "to")
        amount = _payload_uint(tx.payload, "amount")
        if "memo" in tx.payload:
            memo = tx.payload["memo"]
            if not isinstance(memo, str) or len(memo.encode()) > self.params.max_memo_bytes:
                raise _err(INVALID_PAYLOAD, "memo too long")
        if amount == 0:
            raise _err(INVALID_PAYLOAD, "amount must be positive")
        self._require_asset(asset)
        self._require_address(to)
        self._move(tx.signer, to, asset, amount, receipt)

    def _exec_batch(self, tx: Tx, receipt: Receipt) -> None:
        items = tx.payload.get("items")
        if not isinstance(items, list) or not items:
            raise _err(INVALID_PAYLOAD, "items must be a non-empty array")
        if len(items) > self.params.max_batch_items:
            raise _err(LIMIT_EXCEEDED, f"batch above {self.params.max_batch_items} items")
        for index, item in enumerate(items):
            if not isinstance(item, dict):
                raise _err(INVALID_PAYLOAD, f"item {index}")
            asset = _payload_str(item, "asset")
            to = _payload_str(item, "to")
            amount = _payload_uint(item, "amount")
            if amount == 0:
                raise _err(INVALID_PAYLOAD, f"item {index} amount")
            self._require_asset(asset)
            self._require_address(to)
            self._move(tx.signer, to, asset, amount, receipt)

    def _exec_register_asset(self, tx: Tx, now: int, receipt: Receipt) -> None:
        raw = tx.payload.get("asset")
        if not isinstance(raw, dict):
            raise _err(INVALID_PAYLOAD, "asset must be a map")
        try:
            asset = types.Asset.from_cbor(raw)
        except types.ValidationError as exc:
            raise _err(INVALID_PAYLOAD, str(exc)) from exc
        if asset.asset_class == "native":
            raise _err(UNAUTHORIZED, "native asset is genesis-defined")
        asset.issuer = tx.signer
        asset.registered = now
        try:
            asset.validate(self.network)
        except types.ValidationError as exc:
            raise _err(INVALID_PAYLOAD, str(exc)) from exc
        if self.asset(asset.id) is not None:
            raise _err(ASSET_EXISTS, f"asset {asset.id!r}")
        self._put_asset(asset)
        self._set_uint(K_SUPPLY % asset.id, 0)
        self._event(receipt, "asset_registered", asset=asset.id)

    def _exec_create_escrow(self, tx: Tx, now: int, receipt: Receipt) -> None:
        asset = _payload_str(tx.payload, "asset")
        beneficiary = _payload_str(tx.payload, "beneficiary")
        arbiter = _payload_str(tx.payload, "arbiter")
        amount = _payload_uint(tx.payload, "amount")
        timeout = _payload_uint(tx.payload, "timeout")
        if amount == 0:
            raise _err(INVALID_PAYLOAD, "amount must be positive")
        if timeout <= now:
            raise _err(INVALID_PAYLOAD, "timeout must be in the future")
        self._require_asset(asset)
        self._require_address(beneficiary)
        self._require_address(arbiter)
        escrow_id = _escrow_id(tx.signer, asset, amount, timeout, tx.nonce, beneficiary)
        self._sub_balance(tx.signer, asset, amount)
        key = K_ESCROW_AMT % asset
        self._set_uint(key, self._get_uint(key) + amount)
        self._put_escrow(types.Escrow(
            id=escrow_id, payer=tx.signer, beneficiary=beneficiary, arbiter=arbiter,
            asset=asset, amount=amount, timeout=timeout, status=types.ESCROW_OPEN,
            created_at=now,
        ))
        self._event(receipt, "escrow_created", id=escrow_id, asset=asset, amount=amount)

    def _exec_close_escrow(self, tx: Tx, release: bool, now: int, receipt: Receipt) -> None:
        escrow_id = _payload_str(tx.payload, "id")
        escrow = self.escrow(escrow_id)
        if escrow is None:
            raise _err(ESCROW_NOT_FOUND, f"escrow {escrow_id!r}")
        if escrow.status != types.ESCROW_OPEN:
            raise _err(ESCROW_CLOSED, f"escrow {escrow_id!r} is {escrow.status}")
        # The payer may release at any time, but may only reclaim after the
        # timeout. The beneficiary may claim after it. The arbiter may always act.
        if not (
            tx.signer == escrow.arbiter
            or (release and tx.signer == escrow.payer)
            or (tx.signer == escrow.beneficiary and now >= escrow.timeout)
            or (not release and tx.signer == escrow.payer and now >= escrow.timeout)
        ):
            raise _err(UNAUTHORIZED, "not allowed to close this escrow yet")
        key = K_ESCROW_AMT % escrow.asset
        self._set_uint(key, self._get_uint(key) - escrow.amount)
        if release:
            escrow.status = types.ESCROW_RELEASED
            self._ensure_account(escrow.beneficiary)
            self._add_balance(escrow.beneficiary, escrow.asset, escrow.amount)
        else:
            escrow.status = types.ESCROW_REFUNDED
            self._add_balance(escrow.payer, escrow.asset, escrow.amount)
        self._put_escrow(escrow)
        self._event(receipt, "escrow_" + escrow.status, id=escrow_id)

    def _exec_approve(self, tx: Tx, receipt: Receipt) -> None:
        spender = _payload_str(tx.payload, "spender")
        asset = _payload_str(tx.payload, "asset")
        amount = _payload_uint(tx.payload, "amount")
        expires = tx.payload.get("expires_at", 0)
        if isinstance(expires, bool) or not isinstance(expires, int) or expires < 0:
            raise _err(INVALID_PAYLOAD, "expires_at must be unsigned")
        self._require_asset(asset)
        self._require_address(spender)
        if amount > 0 and self.balance(tx.signer, asset) == 0:
            raise _err(INSUFFICIENT_FUNDS, f"owner holds no {asset}")
        self._set_uint(K_ALLOWANCE % (tx.signer, spender, asset), amount)
        self._set_uint(K_ALLOW_EXP % (tx.signer, spender, asset), expires)
        self._event(receipt, "allowance_set", owner=tx.signer, spender=spender,
                    asset=asset, amount=amount)

    def _exec_revoke(self, tx: Tx, receipt: Receipt) -> None:
        spender = _payload_str(tx.payload, "spender")
        asset = _payload_str(tx.payload, "asset")
        self._set_uint(K_ALLOWANCE % (tx.signer, spender, asset), 0)
        self._set_uint(K_ALLOW_EXP % (tx.signer, spender, asset), 0)
        self._event(receipt, "allowance_revoked", owner=tx.signer, spender=spender, asset=asset)

    def _exec_transfer_from(self, tx: Tx, now: int, receipt: Receipt) -> None:
        owner = _payload_str(tx.payload, "from")
        to = _payload_str(tx.payload, "to")
        asset = _payload_str(tx.payload, "asset")
        amount = _payload_uint(tx.payload, "amount")
        if amount == 0:
            raise _err(INVALID_PAYLOAD, "amount must be positive")
        self._require_asset(asset)
        self._require_address(to)
        remaining = self.allowance(owner, tx.signer, asset)
        if remaining < amount:
            raise _err(ALLOWANCE_EXCEEDED, f"allowance {remaining} below {amount}")
        expires = self.allowance_expiry(owner, tx.signer, asset)
        if expires != 0 and now > expires:
            raise _err(ALLOWANCE_EXCEEDED, "allowance expired")
        self._move(owner, to, asset, amount, receipt)
        self._set_uint(K_ALLOWANCE % (owner, tx.signer, asset), remaining - amount)

    def _exec_mint(self, tx: Tx, receipt: Receipt) -> None:
        asset_id = _payload_str(tx.payload, "asset")
        to = _payload_str(tx.payload, "to")
        amount = _payload_uint(tx.payload, "amount")
        if amount == 0:
            raise _err(INVALID_PAYLOAD, "amount must be positive")
        asset = self.asset(asset_id)
        if asset is None:
            raise _err(UNKNOWN_ASSET, f"asset {asset_id!r}")
        if tx.signer != asset.issuer:
            raise _err(UNAUTHORIZED, "only the issuer may mint")
        if asset.policy == "fixed":
            raise _err(SUPPLY_LIMIT, f"asset {asset_id!r} is fixed-supply")
        supply = self.supply(asset_id)
        if asset.max_supply > 0 and amount > asset.max_supply - supply:
            raise _err(SUPPLY_LIMIT, "mint exceeds cap")
        self._require_address(to)
        self._set_uint(K_SUPPLY % asset_id, supply + amount)
        self._ensure_account(to)
        self._add_balance(to, asset_id, amount)
        self._event(receipt, "mint", asset=asset_id, to=to, amount=amount)

    def _exec_burn(self, tx: Tx, receipt: Receipt) -> None:
        asset_id = _payload_str(tx.payload, "asset")
        amount = _payload_uint(tx.payload, "amount")
        if amount == 0:
            raise _err(INVALID_PAYLOAD, "amount must be positive")
        asset = self.asset(asset_id)
        if asset is None:
            raise _err(UNKNOWN_ASSET, f"asset {asset_id!r}")
        if asset.policy != "burnable":
            raise _err(UNAUTHORIZED, f"asset {asset_id!r} does not allow burning")
        self._sub_balance(tx.signer, asset_id, amount)
        self._add_burned(asset_id, amount)
        self._event(receipt, "burn", asset=asset_id, amount=amount)


def _payload_str(payload: dict[str, Any], key: str) -> str:
    value = payload.get(key)
    if not isinstance(value, str):
        raise _err(INVALID_PAYLOAD, f"{key} must be text")
    return value


def _payload_uint(payload: dict[str, Any], key: str) -> int:
    value = payload.get(key)
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise _err(INVALID_PAYLOAD, f"{key} must be unsigned")
    return value


def _escrow_id(signer: str, asset: str, amount: int, timeout: int, nonce: int,
               beneficiary: str) -> str:
    body = cbor.encode({
        "dom": "arne-escrow-v1", "signer": signer, "nonce": nonce,
        "asset": asset, "amount": amount, "timeout": timeout, "beneficiary": beneficiary,
    })
    return "esc_" + types.hash_hex(body)[:32]


def _code_of(exc: Exception) -> str:
    if isinstance(exc, LedgerError):
        return exc.code
    if isinstance(exc, types.SignatureError):
        return BAD_SIGNATURE
    return INVALID_PAYLOAD


# --- genesis and blocks ---


@dataclass
class Allocation:
    address: str
    asset: str
    amount: int


@dataclass
class Genesis:
    network: str
    chain: str
    time: int
    params: Params
    assets: list[types.Asset] = field(default_factory=list)
    allocations: list[Allocation] = field(default_factory=list)
    pools: dict[str, dict[str, int]] = field(default_factory=dict)


def new_genesis(genesis: Genesis) -> State:
    state = State(genesis.network, genesis.chain, genesis.params)
    native = types.native_asset(genesis.network)
    arce = types.Asset(
        id=native, asset_class="native", symbol=types.NATIVE_ASSET_SYMBOL,
        name="AnorebelCoin", decimals=8, policy="capped",
        max_supply=genesis.params.native_max_supply, issuer="",
    )
    state._put_asset(arce)
    for asset in genesis.assets:
        asset.validate(genesis.network)
        if asset.asset_class == "native":
            continue
        if state.asset(asset.id) is not None:
            raise InvariantError(f"genesis: duplicate asset {asset.id}")
        state._put_asset(asset)

    supply: dict[str, int] = {}

    def credit(addr: str, asset_id: str, amount: int) -> None:
        if amount == 0:
            return
        state._require_asset(asset_id)
        validate_address(addr, genesis.network)
        state._ensure_account(addr)
        state._add_balance(addr, asset_id, amount)
        supply[asset_id] = supply.get(asset_id, 0) + amount

    for allocation in genesis.allocations:
        credit(allocation.address, allocation.asset, allocation.amount)
    for pool_name in sorted(genesis.pools):
        for asset_id in sorted(genesis.pools[pool_name]):
            amount = genesis.pools[pool_name][asset_id]
            if amount == 0:
                continue
            state._require_asset(asset_id)
            state._add_pool(pool_name, asset_id, amount)
            supply[asset_id] = supply.get(asset_id, 0) + amount
    for asset_id, total in supply.items():
        state._set_uint(K_SUPPLY % asset_id, total)
    if arce.max_supply > 0 and supply.get(native, 0) > arce.max_supply:
        raise InvariantError("genesis ARCE above cap")
    state.set_block_time(genesis.time)
    state.check_all()
    return state


@dataclass
class Block:
    height: int
    time: int
    proposer: str
    prev_root: str
    state_root: str
    tx_root: str
    tx_hashes: list[str] = field(default_factory=list)
    txs: list[bytes] = field(default_factory=list)
    receipts: list[Receipt] = field(default_factory=list)

    def hash(self) -> str:
        payload = cbor.encode({
            "height": self.height, "time": self.time, "proposer": self.proposer,
            "prev_root": self.prev_root, "state_root": self.state_root,
            "tx_root": self.tx_root, "tx_hashes": list(self.tx_hashes),
        })
        return types.hash_hex(payload)

    def to_dict(self) -> dict[str, Any]:
        return {
            "height": self.height, "hash": self.hash(), "time": self.time,
            "proposer": self.proposer, "prev_root": self.prev_root,
            "state_root": self.state_root, "tx_root": self.tx_root,
            "tx_hashes": list(self.tx_hashes),
            "cursor": self.height - 1 if self.height > 1 else 0,
        }


def tx_root(hashes: Iterable[str]) -> str:
    raw = b"".join(bytes.fromhex(h) for h in hashes)
    return types.hash_hex(raw)


def order_txs(txs: list[Tx]) -> list[Tx]:
    groups: dict[str, list[Tx]] = {}
    signers: list[str] = []
    for tx in txs:
        if tx.signer not in groups:
            signers.append(tx.signer)
        groups.setdefault(tx.signer, []).append(tx)
    for signer in signers:
        groups[signer].sort(key=lambda t: (t.nonce, t.hash()))
    signers.sort(key=lambda s: (-max(t.priority_fee for t in groups[s]), s))
    out: list[Tx] = []
    for signer in signers:
        out.extend(groups[signer])
    return out


def apply_block(state: State, txs: list[Tx], now: int, height: int,
                proposer: str) -> tuple[Block, list[Receipt]]:
    if height != state.height() + 1:
        raise InvariantError(f"height {height} after {state.height()}")
    prev = state.root()
    block = Block(height=height, time=now, proposer=proposer, prev_root=prev.hex(),
                  state_root="", tx_root="")
    state.set_height(height)
    state.set_block_time(now)
    for tx in order_txs(txs):
        receipt = state.apply_tx(tx, now, height)
        block.tx_hashes.append(receipt.hash)
        block.txs.append(cbor.encode(tx.to_cbor()))
        block.receipts.append(receipt)
    state.check_all()
    block.state_root = state.root().hex()
    block.tx_root = tx_root(block.tx_hashes)
    return block, block.receipts
