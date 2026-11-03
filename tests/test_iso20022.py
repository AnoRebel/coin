"""ISO 20022 mapping: shared fixtures, canonical bytes, and malformed cases."""

from __future__ import annotations

import dataclasses
import json
from pathlib import Path

import pytest
from arne import iso20022

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors"


def _load() -> dict:
    return json.loads((VECTORS / "iso20022.json").read_text(encoding="utf-8"))


def _fixture(name: str) -> bytes:
    return (VECTORS / name).read_bytes()


def _plain(value):
    if dataclasses.is_dataclass(value):
        return {k: _plain(v) for k, v in dataclasses.asdict(value).items()}
    if isinstance(value, dict):
        return {k: _plain(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_plain(v) for v in value]
    return value


def _transfer(raw: dict) -> iso20022.Transfer:
    return iso20022.Transfer(
        message_id=raw["message_id"],
        end_to_end_id=raw["end_to_end_id"],
        created_at=raw["created_at"],
        amount=iso20022.Amount(**raw["amount"]),
        debtor=iso20022.Party(**raw["debtor"]),
        creditor=iso20022.Party(**raw["creditor"]),
        remittance=raw.get("remittance", ""),
    )


def _statement(raw: dict) -> iso20022.Statement:
    return iso20022.Statement(
        message_id=raw["message_id"],
        created_at=raw["created_at"],
        account=raw["account"],
        currency=raw["currency"],
        balances=tuple(
            iso20022.Balance(
                type=b["type"], amount=iso20022.Amount(**b["amount"]), direction=b["direction"]
            )
            for b in raw["balances"]
        ),
        entries=tuple(
            iso20022.Entry(
                amount=iso20022.Amount(**e["amount"]),
                direction=e["direction"],
                status=e["status"],
                booking_date=e["booking_date"],
                end_to_end_id=e.get("end_to_end_id", ""),
                debtor_name=e.get("debtor_name", ""),
                creditor_name=e.get("creditor_name", ""),
            )
            for e in raw["entries"]
        ),
    )


def test_parse_fixtures() -> None:
    doc = _load()
    assert _plain(iso20022.parse_pacs008(_fixture(doc["pacs008"]["file"]))) == doc["pacs008"]["transfer"]
    assert _plain(iso20022.parse_pain001(_fixture(doc["pain001"]["file"]))) == doc["pain001"]["transfer"]
    assert _plain(iso20022.parse_camt053(_fixture(doc["camt053"]["file"]))) == doc["camt053"]["statement"]


def test_build_matches_go_fixture() -> None:
    doc = _load()
    assert iso20022.build_pacs008(_transfer(doc["pacs008"]["transfer"])) == _fixture(doc["pacs008"]["file"])
    assert iso20022.build_pain001(_transfer(doc["pain001"]["transfer"])) == _fixture(doc["pain001"]["file"])
    assert iso20022.build_camt053(_statement(doc["camt053"]["statement"])) == _fixture(doc["camt053"]["file"])


@pytest.mark.parametrize("case", _load()["malformed"], ids=lambda case: case["name"])
def test_malformed(case: dict) -> None:
    parser = {
        "iso20022/pacs008.xml": iso20022.parse_pacs008,
        "iso20022/pain001.xml": iso20022.parse_pain001,
        "iso20022/camt053.xml": iso20022.parse_camt053,
    }[case["file"]]
    original = _fixture(case["file"]).decode()
    assert case["find"] in original, case["name"]
    mutated = original.replace(case["find"], case["replace"])
    with pytest.raises(iso20022.ISO20022Error, match=case["error"]):
        parser(mutated.encode())


def test_count_transactions() -> None:
    doc = _load()
    assert iso20022.count_transactions(_fixture(doc["pacs008"]["file"])) == 1
    assert iso20022.count_transactions(_fixture(doc["pain001"]["file"])) == 1
    batched = _fixture(doc["pacs008"]["file"]).replace(
        b"<NbOfTxs>1</NbOfTxs>", b"<NbOfTxs>3</NbOfTxs>"
    )
    assert iso20022.count_transactions(batched) == 3
    with pytest.raises(iso20022.ISO20022Error, match="unexpected message type"):
        iso20022.count_transactions(_fixture(doc["camt053"]["file"]))


def test_build_rejects_invalid_models() -> None:
    doc = _load()
    good = _transfer(doc["pacs008"]["transfer"])

    with pytest.raises(iso20022.ISO20022Error, match="amount must be positive"):
        iso20022.build_pacs008(
            iso20022.Transfer(
                good.message_id, good.end_to_end_id, good.created_at,
                iso20022.Amount("TZS", "0.00"), good.debtor, good.creditor, good.remittance,
            )
        )
    with pytest.raises(iso20022.ISO20022Error, match="5 fraction digits"):
        iso20022.build_pain001(
            iso20022.Transfer(
                good.message_id, good.end_to_end_id, good.created_at,
                iso20022.Amount("TZS", "1.000001"), good.debtor, good.creditor, good.remittance,
            )
        )
    with pytest.raises(iso20022.ISO20022Error, match="debtor.bic"):
        iso20022.build_pacs008(
            iso20022.Transfer(
                good.message_id, good.end_to_end_id, good.created_at, good.amount,
                iso20022.Party(good.debtor.name, good.debtor.account, "", good.debtor.country),
                good.creditor, good.remittance,
            )
        )
    statement = _statement(doc["camt053"]["statement"])
    with pytest.raises(iso20022.ISO20022Error, match="statement needs an entry"):
        iso20022.build_camt053(
            iso20022.Statement(
                statement.message_id, statement.created_at, statement.account,
                statement.currency, statement.balances, (),
            )
        )
