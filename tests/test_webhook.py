"""Webhook signing, replay protection, and retry behaviour."""

from __future__ import annotations

import json

import httpx
import pytest
from arne import webhook

NOW = 1_700_000_000.0


def _signer() -> webhook.Signer:
    return webhook.Signer(bytes(32))


def test_sign_and_verify() -> None:
    signer = _signer()
    body = b'{"hello":"world"}'
    signature = signer.sign(int(NOW), body)
    webhook.verify(signer.public_key, str(int(NOW)), body, signature, now=NOW)
    with pytest.raises(webhook.SignatureError):
        webhook.verify(signer.public_key, str(int(NOW)), b'{"hello":"evil"}', signature, now=NOW)
    with pytest.raises(webhook.ReplayError):
        webhook.verify(signer.public_key, str(int(NOW) - 1000), body, signature, now=NOW)
    with pytest.raises(webhook.ReplayError):
        webhook.verify(signer.public_key, str(int(NOW) + 1000), body, signature, now=NOW)


def test_verify_request() -> None:
    signer = _signer()
    body = b"{}"
    headers = {
        webhook.HEADER_DELIVERY: "d1",
        webhook.HEADER_TIMESTAMP: str(int(NOW)),
        webhook.HEADER_SIGNATURE: signer.sign(int(NOW), body),
    }
    assert webhook.verify_request(headers, body, signer.public_key, now=NOW) == "d1"
    with pytest.raises(webhook.HeaderError):
        webhook.verify_request({}, body, signer.public_key, now=NOW)


def test_retries_reuse_delivery_id() -> None:
    attempts: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        attempts.append(request)
        return httpx.Response(503 if len(attempts) < 3 else 200)

    client = httpx.Client(transport=httpx.MockTransport(handler))
    dispatcher = webhook.Dispatcher(webhook.Config(
        endpoints=["http://example.test/hook"], signer=_signer(),
        max_attempts=3, sleep=lambda _: None, client=client,
    ))
    dispatcher.start()
    assert dispatcher.enqueue("payment", {"tx": "h1"})
    dispatcher.close()

    assert len(attempts) == 3
    ids = {request.headers[webhook.HEADER_DELIVERY] for request in attempts}
    bodies = {request.content for request in attempts}
    assert len(ids) == 1
    assert len(bodies) == 1
    assert json.loads(attempts[0].content)["type"] == "payment"


def test_client_error_not_retried() -> None:
    calls = []

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append(request)
        return httpx.Response(400)

    client = httpx.Client(transport=httpx.MockTransport(handler))
    dispatcher = webhook.Dispatcher(webhook.Config(
        endpoints=["http://example.test/hook"], signer=_signer(),
        max_attempts=5, sleep=lambda _: None, client=client,
    ))
    dispatcher.start()
    dispatcher.enqueue("payment", None)
    dispatcher.close()
    assert len(calls) == 1


def test_queue_full_drops() -> None:
    dispatcher = webhook.Dispatcher(webhook.Config(
        endpoints=["http://example.test/hook"], signer=_signer(), queue_size=1,
    ))
    assert dispatcher.enqueue("a", None)
    assert not dispatcher.enqueue("b", None)
    assert dispatcher.dropped == 1
    dispatcher.close()
    assert not dispatcher.enqueue("c", None)
