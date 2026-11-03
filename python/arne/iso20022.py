"""ISO 20022 mapping for pacs.008, pain.001, and camt.053.

Mirrors internal/connector/iso20022. The mapping is strict: text limits, ISO
4217 currencies, BICs, accounts, positive decimal amounts, and RFC 3339
timestamps are validated before a message is built or accepted. Amounts stay
decimal strings, so no float ever touches a payment.

Each message carries exactly one transfer or statement; batches are refused so
a caller cannot silently settle part of a message.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import datetime
from xml.etree import ElementTree

PACS008_NAMESPACE = "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.13"
PAIN001_NAMESPACE = "urn:iso:std:iso:20022:tech:xsd:pain.001.001.12"
CAMT053_NAMESPACE = "urn:iso:std:iso:20022:tech:xsd:camt.053.001.13"

_CURRENCY = re.compile(r"^[A-Z]{3}$")
_BIC = re.compile(r"^[A-Z0-9]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$")
_ACCOUNT = re.compile(r"^[A-Z0-9]{5,34}$")
_COUNTRY = re.compile(r"^[A-Z]{2}$")
_AMOUNT = re.compile(r"^[0-9]{1,13}(\.[0-9]{1,5})?$")
_DATE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}$")
_TIMESTAMP = re.compile(
    r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$"
)


class ISO20022Error(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"iso20022: {code}: {message}")
        self.code = code


def _fail(code: str, message: str) -> None:
    raise ISO20022Error(code, message)


@dataclass(frozen=True)
class Party:
    name: str
    account: str
    agent: str = ""
    country: str = ""


@dataclass(frozen=True)
class Amount:
    currency: str
    value: str


@dataclass(frozen=True)
class Transfer:
    message_id: str
    end_to_end_id: str
    created_at: str
    amount: Amount
    debtor: Party
    creditor: Party
    remittance: str = ""


@dataclass(frozen=True)
class Entry:
    amount: Amount
    direction: str
    status: str
    booking_date: str
    end_to_end_id: str = ""
    debtor_name: str = ""
    creditor_name: str = ""


@dataclass(frozen=True)
class Balance:
    type: str
    amount: Amount
    direction: str


@dataclass(frozen=True)
class Statement:
    message_id: str
    created_at: str
    account: str
    currency: str
    balances: tuple[Balance, ...] = ()
    entries: tuple[Entry, ...] = field(default_factory=tuple)


def _text(field_name: str, value: str, minimum: int, maximum: int) -> None:
    if not value:
        _fail("invalid", f"{field_name} is required")
    if not minimum <= len(value) <= maximum:
        _fail("invalid", f"{field_name} must be {minimum}..{maximum} characters")


def _party(label: str, party: Party) -> None:
    _text(f"{label}.name", party.name, 1, 140)
    _account(f"{label}.account", party.account)
    if not _BIC.match(party.agent):
        _fail("invalid", f"{label}.bic must be 8 or 11 characters")
    if party.country and not _COUNTRY.match(party.country):
        _fail("invalid", f"{label}.country must be ISO 3166-1 alpha-2")


def _validate_amount(amount: Amount) -> None:
    _currency("currency", amount.currency)
    if not _AMOUNT.match(amount.value):
        _fail("invalid", "amount must be a positive decimal with at most 5 fraction digits")
    if not amount.value.replace(".", "", 1).strip("0"):
        _fail("invalid", "amount must be positive")


def _currency(field_name: str, value: str) -> None:
    if not _CURRENCY.match(value):
        _fail("invalid", f"{field_name} must be ISO 4217")


def _account(field_name: str, value: str) -> None:
    if not _ACCOUNT.match(value):
        _fail("invalid", f"{field_name} must be 5..34 uppercase alphanumeric characters")


def _timestamp(field_name: str, value: str) -> None:
    if not _TIMESTAMP.match(value):
        _fail("invalid", f"{field_name} must be RFC 3339")
    try:
        datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        _fail("invalid", f"{field_name} must be RFC 3339")


def validate_transfer(transfer: Transfer) -> None:
    _text("message_id", transfer.message_id, 1, 35)
    _text("end_to_end_id", transfer.end_to_end_id, 1, 35)
    _timestamp("created_at", transfer.created_at)
    _validate_amount(transfer.amount)
    _party("debtor", transfer.debtor)
    _party("creditor", transfer.creditor)
    if transfer.remittance:
        _text("remittance", transfer.remittance, 1, 140)


def validate_statement(statement: Statement) -> None:
    _text("message_id", statement.message_id, 1, 35)
    _timestamp("created_at", statement.created_at)
    _account("account", statement.account)
    _currency("currency", statement.currency)
    for index, balance in enumerate(statement.balances):
        if balance.type not in ("OPBD", "CLBD", "ITBD"):
            _fail("invalid", f"balance {index} type must be OPBD, CLBD, or ITBD")
        try:
            _validate_amount(balance.amount)
        except ISO20022Error as exc:
            _fail("invalid", f"balance {index}: {exc}")
        if balance.amount.currency != statement.currency:
            _fail("invalid", f"balance {index} currency must match the account")
        if balance.direction not in ("CRDT", "DBIT"):
            _fail("invalid", f"balance {index} direction must be CRDT or DBIT")
    if not statement.entries:
        _fail("invalid", "statement needs an entry")
    for index, entry in enumerate(statement.entries):
        try:
            _validate_amount(entry.amount)
        except ISO20022Error as exc:
            _fail("invalid", f"entry {index}: {exc}")
        if entry.amount.currency != statement.currency:
            _fail("invalid", f"entry {index} currency must match the account")
        if entry.direction not in ("CRDT", "DBIT"):
            _fail("invalid", f"entry {index} direction must be CRDT or DBIT")
        if entry.status not in ("BOOK", "PDNG"):
            _fail("invalid", f"entry {index} status must be BOOK or PDNG")
        if not _DATE.match(entry.booking_date):
            _fail("invalid", f"entry {index} booking_date must be YYYY-MM-DD")
        try:
            datetime.strptime(entry.booking_date, "%Y-%m-%d")
        except ValueError:
            _fail("invalid", f"entry {index} booking_date is not a date")
        if entry.end_to_end_id:
            _text("end_to_end_id", entry.end_to_end_id, 1, 35)
        if entry.debtor_name and entry.creditor_name:
            _fail("invalid", f"entry {index} cannot have both debtor and creditor")


def _sub(parent: ElementTree.Element, name: str, text: str | None = None) -> ElementTree.Element:
    element = ElementTree.SubElement(parent, name)
    if text is not None:
        element.text = text
    return element


def _amount_element(parent: ElementTree.Element, name: str, amount: Amount) -> None:
    element = _sub(parent, name, amount.value)
    element.set("Ccy", amount.currency)


def _party_element(parent: ElementTree.Element, name: str, party: Party) -> None:
    element = _sub(parent, name)
    _sub(element, "Nm", party.name)
    if party.country:
        _sub(_sub(element, "PstlAdr"), "Ctry", party.country)


def _account_element(parent: ElementTree.Element, name: str, account: str) -> None:
    _sub(_sub(_sub(parent, name), "Id"), "IBAN", account)


def _agent_element(parent: ElementTree.Element, name: str, bic: str) -> None:
    _sub(_sub(_sub(parent, name), "FinInstnId"), "BICFI", bic)


def _header(
    parent: ElementTree.Element, message: Transfer | Statement, count: int, control_sum: str = ""
) -> None:
    header = _sub(parent, "GrpHdr")
    _sub(header, "MsgId", message.message_id)
    _sub(header, "CreDtTm", message.created_at)
    _sub(header, "NbOfTxs", str(count))
    if control_sum:
        _sub(header, "CtrlSum", control_sum)


def _serialize(root: ElementTree.Element) -> bytes:
    ElementTree.indent(root, space="  ")
    body = ElementTree.tostring(root, encoding="unicode")
    return ('<?xml version="1.0" encoding="UTF-8"?>\n' + body).encode()


def build_pacs008(transfer: Transfer) -> bytes:
    validate_transfer(transfer)
    root = ElementTree.Element("Document")
    root.set("xmlns", PACS008_NAMESPACE)
    body = _sub(root, "FIToFICstmrCdtTrf")
    _header(body, transfer, 1)
    tx = _sub(body, "CdtTrfTxInf")
    _sub(_sub(tx, "PmtId"), "EndToEndId", transfer.end_to_end_id)
    _amount_element(tx, "IntrBkSttlmAmt", transfer.amount)
    _party_element(tx, "Dbtr", transfer.debtor)
    _account_element(tx, "DbtrAcct", transfer.debtor.account)
    _agent_element(tx, "DbtrAgt", transfer.debtor.agent)
    _party_element(tx, "Cdtr", transfer.creditor)
    _account_element(tx, "CdtrAcct", transfer.creditor.account)
    _agent_element(tx, "CdtrAgt", transfer.creditor.agent)
    if transfer.remittance:
        _sub(_sub(tx, "RmtInf"), "Ustrd", transfer.remittance)
    return _serialize(root)


def build_pain001(transfer: Transfer) -> bytes:
    validate_transfer(transfer)
    root = ElementTree.Element("Document")
    root.set("xmlns", PAIN001_NAMESPACE)
    body = _sub(root, "CstmrCdtTrfInitn")
    _header(body, transfer, 1, transfer.amount.value)
    payment = _sub(body, "PmtInf")
    _sub(payment, "PmtInfId", transfer.message_id)
    _sub(payment, "NbOfTxs", "1")
    _sub(payment, "CtrlSum", transfer.amount.value)
    _party_element(payment, "Dbtr", transfer.debtor)
    _account_element(payment, "DbtrAcct", transfer.debtor.account)
    _agent_element(payment, "DbtrAgt", transfer.debtor.agent)
    tx = _sub(payment, "CdtTrfTxInf")
    _sub(_sub(tx, "PmtId"), "EndToEndId", transfer.end_to_end_id)
    _amount_element(_sub(tx, "Amt"), "InstdAmt", transfer.amount)
    _party_element(tx, "Cdtr", transfer.creditor)
    _account_element(tx, "CdtrAcct", transfer.creditor.account)
    _agent_element(tx, "CdtrAgt", transfer.creditor.agent)
    if transfer.remittance:
        _sub(_sub(tx, "RmtInf"), "Ustrd", transfer.remittance)
    return _serialize(root)


def build_camt053(statement: Statement) -> bytes:
    validate_statement(statement)
    root = ElementTree.Element("Document")
    root.set("xmlns", CAMT053_NAMESPACE)
    body = _sub(root, "BkToCstmrStmt")
    _header(body, statement, len(statement.entries))
    stmt = _sub(body, "Stmt")
    _sub(stmt, "Id", statement.message_id)
    account = _sub(stmt, "Acct")
    _sub(_sub(account, "Id"), "IBAN", statement.account)
    _sub(account, "Ccy", statement.currency)
    for balance in statement.balances:
        element = _sub(stmt, "Bal")
        _sub(_sub(_sub(element, "Tp"), "CdOrPrtry"), "Cd", balance.type)
        _amount_element(element, "Amt", balance.amount)
        _sub(element, "CdtDbtInd", balance.direction)
    for entry in statement.entries:
        element = _sub(stmt, "Ntry")
        _amount_element(element, "Amt", entry.amount)
        _sub(element, "CdtDbtInd", entry.direction)
        _sub(element, "Sts", entry.status)
        _sub(_sub(element, "BookgDt"), "Dt", entry.booking_date)
        if entry.end_to_end_id or entry.debtor_name or entry.creditor_name:
            details = _sub(_sub(element, "NtryDtls"), "TxDtls")
            if entry.end_to_end_id:
                _sub(_sub(details, "Refs"), "EndToEndId", entry.end_to_end_id)
            if entry.debtor_name or entry.creditor_name:
                parties = _sub(details, "RltdPties")
                if entry.debtor_name:
                    _sub(_sub(parties, "Dbtr"), "Nm", entry.debtor_name)
                if entry.creditor_name:
                    _sub(_sub(parties, "Cdtr"), "Nm", entry.creditor_name)
    return _serialize(root)


def _local(tag: str) -> str:
    return tag.rsplit("}", 1)[-1]


def _child(parent: ElementTree.Element | None, name: str) -> ElementTree.Element | None:
    if parent is None:
        return None
    for element in parent:
        if _local(element.tag) == name:
            return element
    return None


def _path(parent: ElementTree.Element, path: str) -> ElementTree.Element | None:
    element: ElementTree.Element | None = parent
    for name in path.split("/"):
        element = _child(element, name)
        if element is None:
            return None
    return element


def _value(parent: ElementTree.Element, path: str) -> str:
    element = _path(parent, path)
    if element is None or element.text is None:
        return ""
    return element.text.strip()


def _children(parent: ElementTree.Element | None, name: str) -> list[ElementTree.Element]:
    if parent is None:
        return []
    return [element for element in parent if _local(element.tag) == name]


def _load(raw: bytes, body_name: str) -> ElementTree.Element:
    try:
        root = ElementTree.fromstring(raw)
    except ElementTree.ParseError as exc:
        _fail("invalid", str(exc))
    if _local(root.tag) != "Document":
        raise ISO20022Error("unexpected message type", body_name)
    body = _child(root, body_name)
    if body is None:
        raise ISO20022Error("unexpected message type", body_name)
    return body


def _amount(parent: ElementTree.Element, path: str) -> Amount:
    element = _path(parent, path)
    if element is None:
        return Amount("", "")
    return Amount(element.get("Ccy", ""), (element.text or "").strip())


def _read_party(parent: ElementTree.Element, name: str) -> Party:
    element = _child(parent, name)
    return Party(
        name=_value(element, "Nm") if element is not None else "",
        account=_value(element, "Id/IBAN") if element is not None else "",
        agent=_value(element, "FinInstnId/BICFI") if element is not None else "",
        country=_value(element, "PstlAdr/Ctry") if element is not None else "",
    )


def parse_pacs008(raw: bytes) -> Transfer:
    body = _load(raw, "FIToFICstmrCdtTrf")
    header = _child(body, "GrpHdr")
    if header is None or not _value(header, "MsgId"):
        raise ISO20022Error("unexpected message type", "pacs.008")
    if _value(header, "NbOfTxs") != "1":
        _fail("invalid", "pacs.008 mapping expects one transaction")
    tx = _child(body, "CdtTrfTxInf")
    if tx is None:
        _fail("invalid", "pacs.008 mapping expects one transaction")
    transfer = Transfer(
        message_id=_value(header, "MsgId"),
        end_to_end_id=_value(tx, "PmtId/EndToEndId"),
        created_at=_value(header, "CreDtTm"),
        amount=_amount(tx, "IntrBkSttlmAmt"),
        debtor=_party_with_account(tx, "Dbtr", "DbtrAcct", "DbtrAgt"),
        creditor=_party_with_account(tx, "Cdtr", "CdtrAcct", "CdtrAgt"),
        remittance=_value(tx, "RmtInf/Ustrd"),
    )
    validate_transfer(transfer)
    return transfer


def _party_with_account(
    parent: ElementTree.Element, party_name: str, account_name: str, agent_name: str
) -> Party:
    party = _read_party(parent, party_name)
    return Party(
        name=party.name,
        account=_value(parent, f"{account_name}/Id/IBAN"),
        agent=_value(parent, f"{agent_name}/FinInstnId/BICFI"),
        country=party.country,
    )


def parse_pain001(raw: bytes) -> Transfer:
    body = _load(raw, "CstmrCdtTrfInitn")
    header = _child(body, "GrpHdr")
    payment = _child(body, "PmtInf")
    if header is None or not _value(header, "MsgId"):
        raise ISO20022Error("unexpected message type", "pain.001")
    if payment is None or _value(header, "NbOfTxs") != "1" or _value(payment, "NbOfTxs") != "1":
        _fail("invalid", "pain.001 mapping expects one transaction")
    tx = _child(payment, "CdtTrfTxInf")
    if tx is None:
        _fail("invalid", "pain.001 mapping expects one transaction")
    transfer = Transfer(
        message_id=_value(header, "MsgId"),
        end_to_end_id=_value(tx, "PmtId/EndToEndId"),
        created_at=_value(header, "CreDtTm"),
        amount=_amount(tx, "Amt/InstdAmt"),
        debtor=_party_with_account(payment, "Dbtr", "DbtrAcct", "DbtrAgt"),
        creditor=_party_with_account(tx, "Cdtr", "CdtrAcct", "CdtrAgt"),
        remittance=_value(tx, "RmtInf/Ustrd"),
    )
    control_sum = _value(payment, "CtrlSum")
    if control_sum and control_sum != transfer.amount.value:
        _fail("invalid", "control sum does not match the amount")
    validate_transfer(transfer)
    return transfer


def parse_camt053(raw: bytes) -> Statement:
    body = _load(raw, "BkToCstmrStmt")
    header = _child(body, "GrpHdr")
    statement = _child(body, "Stmt")
    if header is None or not _value(header, "MsgId") or statement is None or not _value(statement, "Id"):
        raise ISO20022Error("unexpected message type", "camt.053")
    parsed = Statement(
        message_id=_value(header, "MsgId"),
        created_at=_value(header, "CreDtTm"),
        account=_value(statement, "Acct/Id/IBAN"),
        currency=_value(statement, "Acct/Ccy"),
    )
    balances = []
    for element in _children(statement, "Bal"):
        balances.append(
            Balance(
                type=_value(element, "Tp/CdOrPrtry/Cd"),
                amount=_amount(element, "Amt"),
                direction=_value(element, "CdtDbtInd"),
            )
        )
    entries = []
    for element in _children(statement, "Ntry"):
        entries.append(
            Entry(
                amount=_amount(element, "Amt"),
                direction=_value(element, "CdtDbtInd"),
                status=_value(element, "Sts"),
                booking_date=_value(element, "BookgDt/Dt"),
                end_to_end_id=_value(element, "NtryDtls/TxDtls/Refs/EndToEndId"),
                debtor_name=_value(element, "NtryDtls/TxDtls/RltdPties/Dbtr/Nm"),
                creditor_name=_value(element, "NtryDtls/TxDtls/RltdPties/Cdtr/Nm"),
            )
        )
    parsed = Statement(
        message_id=parsed.message_id,
        created_at=parsed.created_at,
        account=parsed.account,
        currency=parsed.currency,
        balances=tuple(balances),
        entries=tuple(entries),
    )
    validate_statement(parsed)
    return parsed


def count_transactions(raw: bytes) -> int:
    try:
        root = ElementTree.fromstring(raw)
    except ElementTree.ParseError as exc:
        _fail("invalid", str(exc))
    if _local(root.tag) != "Document":
        raise ISO20022Error("unexpected message type", "unknown")
    pacs = _child(root, "FIToFICstmrCdtTrf")
    if pacs is not None:
        value = _value(pacs, "GrpHdr/NbOfTxs")
        return int(value) if value.isdigit() else 0
    pain = _child(root, "CstmrCdtTrfInitn")
    if pain is not None:
        value = _value(pain, "PmtInf/NbOfTxs")
        return int(value) if value.isdigit() else 0
    raise ISO20022Error("unexpected message type", "unknown")
