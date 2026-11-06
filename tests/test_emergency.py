"""Emergency committee conformance vectors."""

from __future__ import annotations

import json
from pathlib import Path

from arne import emergency, governance

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "emergency.json"


def _code(exc: BaseException) -> str:
    return exc.code if isinstance(exc, governance.GovernanceError) else "other"


def _last(committee: emergency.Emergency, kind: str) -> emergency.Action | None:
    return next(
        (action for action in reversed(committee.actions) if action.kind == kind), None
    )


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    events: list[emergency.AuditEvent] = []
    committee = emergency.Emergency(
        doc["members"], doc["threshold"], doc["period"], events.append
    )
    for step in doc["steps"]:
        events.clear()
        action = None
        error = None
        operation = step["op"]
        try:
            if operation == "pause":
                action = committee.pause(
                    step["signers"], step["operations"], step["reason"], step["at"]
                )
            elif operation == "veto":
                action = committee.veto(
                    step["signers"], step["proposal"], step["reason"], step["at"]
                )
            elif operation == "advance":
                committee.advance(step["at"])
            elif operation in ("revoke", "ratify"):
                target = _last(committee, step["target_kind"])
                assert target is not None, step
                if operation == "revoke":
                    committee.revoke(
                        target.id, step["signers"], step["reason"], step["at"]
                    )
                else:
                    committee.ratify(target.id, step["proposal"], step["at"])
            else:
                raise AssertionError(f"unknown op {operation}")
        except governance.GovernanceError as exc:
            error = exc
        if step.get("error"):
            assert error is not None and _code(error) == step["error"], step
            continue
        assert error is None, step
        expect = step.get("expect", {})
        if expect.get("kind"):
            assert action.kind == expect["kind"]
        if expect.get("status"):
            assert action.status == expect["status"]
        if expect.get("expires_at"):
            assert action.expires_at == expect["expires_at"]
        if expect.get("signers"):
            assert action.signers == expect["signers"]
        for kind, status in expect.get("statuses", {}).items():
            target = _last(committee, kind)
            assert target is not None and target.status == status, (kind, target)
        if expect.get("paused"):
            assert committee.paused(expect["paused"], step["at"]), step
        if expect.get("not_paused"):
            assert not committee.paused(expect["not_paused"], step["at"]), step
        if expect.get("vetoed"):
            assert committee.vetoed(expect["vetoed"], step["at"]), step
        if expect.get("not_vetoed"):
            assert not committee.vetoed(expect["not_vetoed"], step["at"]), step
        assert sorted(event.action for event in events) == sorted(expect.get("audit", [])), step
