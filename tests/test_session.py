"""Session key scope, expiry, spending limits, and watch-only rejection."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import session

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "session.json"


def load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def grant(doc: dict, spec: dict) -> session.Grant:
    return session.Grant(
        account=doc["account"],
        public_key=session.public_key_from_seed(bytes.fromhex(doc["session_seed"])),
        scopes=tuple(spec["scopes"]),
        assets=tuple(spec.get("assets", ())),
        limit=spec.get("limit", 0),
        expires_at=spec["expires_at"],
    )


def request(doc: dict, raw: dict) -> session.Request:
    return session.Request(
        account=raw.get("account", doc["account"]),
        scope=raw["scope"],
        asset=raw.get("asset", ""),
        amount=raw.get("amount", 0),
        at=raw["at"],
    )


def test_invalid_grants() -> None:
    doc = load()
    for case in doc["invalid_grants"]:
        with pytest.raises(session.SessionError) as caught:
            session.Session(grant(doc, case))
        assert caught.value.code == "invalid", case["name"]


def test_authorization_vectors() -> None:
    doc = load()
    for case in doc["cases"]:
        scoped = session.Session(grant(doc, case["grant"]))
        requests = case.get("requests") or [case["request"]]
        failure: session.SessionError | None = None
        for raw in requests:
            if failure is not None:
                break
            try:
                scoped.authorize(request(doc, raw))
            except session.SessionError as error:
                failure = error
        if case.get("error"):
            assert failure is not None, case["name"]
            assert failure.code == case["error"], case["name"]
        else:
            assert failure is None, case["name"]
        if case.get("expect_remaining"):
            assert scoped.remaining == case["expect_remaining"], case["name"]


def test_signing_and_watch_only() -> None:
    doc = load()
    scoped = session.Session(
        session.Grant(
            account=doc["account"],
            public_key=session.public_key_from_seed(bytes.fromhex(doc["session_seed"])),
            scopes=(session.TRANSFER,),
            assets=("arce",),
            limit=100,
            expires_at=100,
        )
    )
    auth = session.Request(
        account=doc["account"], scope=session.TRANSFER, asset="arce", amount=10, at=1
    )
    seed = bytes.fromhex(doc["session_seed"])
    signature = scoped.sign(seed, b"canonical", auth)
    assert session.verify(scoped.grant.public_key, b"canonical", signature)

    other = bytes.fromhex(doc["other_seed"])
    with pytest.raises(session.SessionError) as caught:
        scoped.sign(other, b"canonical", auth)
    assert caught.value.code == "key"
    assert scoped.remaining == 90

    watch = session.Account(doc["account"], scoped.grant.public_key, watch_only=True)
    with pytest.raises(session.SessionError) as caught:
        watch.sign(seed, b"canonical")
    assert caught.value.code == "watch_only"

    signer = session.Account(doc["account"], scoped.grant.public_key)
    assert session.verify(signer.public_key, b"canonical", signer.sign(seed, b"canonical"))
    with pytest.raises(session.SessionError) as caught:
        signer.sign(other, b"canonical")
    assert caught.value.code == "key"
