"""Social recovery vectors: threshold, timelock, cancellation, signatures."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from arne import recovery

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "recovery.json"


def load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def public_key(seed: str) -> str:
    return recovery.public_key_from_seed(bytes.fromhex(seed))


def base_policy(doc: dict) -> recovery.Policy:
    return recovery.Policy(
        account=doc["policy"]["account"],
        owner_public_key=public_key(doc["seeds"]["owner"]),
        guardians=tuple(
            recovery.Guardian(id=guardian["id"], public_key=public_key(guardian["seed"]))
            for guardian in doc["policy"]["guardians"]
        ),
        threshold=doc["policy"]["threshold"],
        delay=doc["policy"]["delay"],
    )


def test_policy_vectors() -> None:
    doc = load()
    recovery.Manager(base_policy(doc))
    for case in doc["invalid_policies"]:
        policy = recovery.Policy(
            account=doc["policy"]["account"],
            owner_public_key=public_key(doc["seeds"]["owner"]),
            guardians=tuple(
                recovery.Guardian(id=identifier, public_key=public_key(doc["seeds"][identifier]))
                for identifier in case["guardians"]
            ),
            threshold=case.get("threshold", doc["policy"]["threshold"]),
            delay=case.get("delay", doc["policy"]["delay"]),
        )
        with pytest.raises(recovery.RecoveryError) as caught:
            recovery.Manager(policy)
        assert caught.value.code == "invalid", case["name"]


def test_recovery_vectors() -> None:
    doc = load()
    new_key = public_key(doc["new_seed"])
    for case in doc["cases"]:
        manager = recovery.Manager(base_policy(doc))
        request = manager.start(new_key, case["nonce"], case["created_at"])
        failure: recovery.RecoveryError | None = None
        for op in case["ops"]:
            if failure is not None:
                break
            try:
                if op["op"] == "start":
                    request = manager.start(new_key, case["nonce"], case["created_at"])
                elif op["op"] in ("approve", "approve_bad"):
                    seed = doc["seeds"][op["guardian"]]
                    payload = recovery.approval_payload(
                        request.id, request.account, request.new_public_key
                    )
                    if op["op"] == "approve_bad":
                        seed = doc["seeds"]["owner"]
                    request = manager.approve(
                        request.id, op["guardian"], recovery.sign_with_seed(bytes.fromhex(seed), payload)
                    )
                elif op["op"] == "execute":
                    request = manager.execute(request.id, op["at"])
                elif op["op"] == "cancel":
                    payload = recovery.cancel_payload(request.id, request.account)
                    request = manager.cancel(
                        request.id,
                        recovery.sign_with_seed(bytes.fromhex(doc["seeds"]["owner"]), payload),
                        op["at"],
                    )
                else:
                    raise AssertionError(f"unknown op {op['op']}")
            except recovery.RecoveryError as error:
                failure = error

        if case.get("error"):
            assert failure is not None, case["name"]
            assert failure.code == case["error"], case["name"]
        else:
            assert failure is None, case["name"]

        stored = manager.get(request.id)
        assert stored is not None, case["name"]
        assert stored.state == case["expect_state"], case["name"]
        if case.get("expect_requests"):
            assert len(manager.requests()) == case["expect_requests"], case["name"]


def test_unknown_request() -> None:
    doc = load()
    manager = recovery.Manager(base_policy(doc))
    with pytest.raises(recovery.RecoveryError) as caught:
        manager.execute("missing", 1)
    assert caught.value.code == "unknown"
