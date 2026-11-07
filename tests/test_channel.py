"""Channel message vectors and delivery semantics."""

from __future__ import annotations

import base64
import json
from pathlib import Path

import pytest
from arne import channel

VECTORS = Path(__file__).resolve().parent.parent / "spec" / "vectors" / "messaging.json"


def build(raw: dict) -> channel.Message:
    data = dict(raw)
    if "ciphertext" in data:
        data["ciphertext"] = base64.b64decode(data["ciphertext"])
    if "file" in data:
        data["file"] = channel.FilePointer(**data["file"])
    return channel.Message(**data)


def test_shared_vectors() -> None:
    doc = json.loads(VECTORS.read_text(encoding="utf-8"))
    for case in doc["identity"]:
        assert channel.message_id(
            case["channel"], case["sender"], case["client_id"]
        ) == case["id"], case["channel"]
    for case in doc["valid"]:
        build(case["message"]).validate()
    for case in doc["invalid"]:
        with pytest.raises(channel.ChannelError) as caught:
            build(case["message"]).validate()
        assert caught.value.code == case["error"], case["name"]


def make_store(retention: float = 3600, now: list[float] | None = None) -> channel.Store:
    clock = now if now is not None else [1000.0]
    return channel.Store(retention, now=lambda: clock[0])


def message(client_id: str, text: str = "hello", sender: str = "alice") -> channel.Message:
    return channel.Message(channel="team", sender=sender, client_id=client_id, text=text)


def test_publish_deduplicates_retry() -> None:
    store = make_store()
    first, duplicate = store.publish(message("c1"))
    assert not duplicate
    second, duplicate = store.publish(message("c1"))
    assert duplicate
    assert (second.id, second.seq) == (first.id, first.seq)
    assert len(store.read("team")) == 1
    with pytest.raises(channel.ChannelError) as caught:
        store.publish(message("c1", "changed"))
    assert caught.value.code == "conflict"


def test_ordering_and_catch_up() -> None:
    store = make_store()
    store.publish(message("a", "one"))
    store.publish(message("b", "two", sender="bob"))
    store.publish(channel.Message(channel="other", sender="carol", client_id="c", text="three"))
    store.publish(message("d", "four"))
    page = store.read("team")
    assert [entry.seq for entry in page] == [1, 2, 3]
    assert [entry.text for entry in page] == ["one", "two", "four"]
    assert [entry.text for entry in store.read("team", after=1, limit=1)] == ["two"]
    assert store.read("team", after=3) == []


def test_retention_expiry_and_cursor() -> None:
    now = [0.0]
    store = make_store(10, now)
    store.publish(message("a", "one"))
    now[0] = 100.0
    store.publish(message("b", "two"))
    assert [entry.text for entry in store.read("team")] == ["two"]
    assert [entry.text for entry in store.read("team", after=1)] == ["two"]
    assert store.prune() == 1
    now[0] = 120.0
    assert store.read("team") == []
    with pytest.raises(channel.ChannelError) as caught:
        store.read("team", after=1)
    assert caught.value.code == "cursor_expired"


def test_per_channel_retention() -> None:
    now = [100.0]
    store = make_store(3600, now)
    store.set_retention("short", 10)
    store.publish(channel.Message(channel="short", sender="alice", client_id="a", text="gone"))
    store.publish(channel.Message(channel="long", sender="alice", client_id="b", text="kept"))
    now[0] = 120.0
    assert store.read("short") == []
    assert [entry.text for entry in store.read("long")] == ["kept"]


def test_subscribe_hints() -> None:
    store = make_store()
    hints, cancel = store.subscribe("team")
    try:
        store.publish(message("a"))
        assert hints.get(timeout=1) == 1
    finally:
        cancel()


def test_oversize_text_and_ciphertext() -> None:
    with pytest.raises(channel.ChannelError) as caught:
        message("a", "a" * (channel.MAX_TEXT_BYTES + 1)).validate()
    assert caught.value.code == "invalid"
    sealed = channel.Message(
        channel="team",
        sender="alice",
        client_id="b",
        kind=channel.TEXT,
        ciphertext=b"x" * (channel.MAX_CIPHERTEXT_BYTES + 1),
    )
    with pytest.raises(channel.ChannelError) as caught:
        sealed.validate()
    assert caught.value.code == "invalid"


def test_text_limit_is_bytes_not_characters() -> None:
    exact = "é" * (channel.MAX_TEXT_BYTES // 2)
    channel.Message(channel="team", sender="alice", client_id="a", text=exact).validate()
    with pytest.raises(channel.ChannelError):
        channel.Message(
            channel="team", sender="alice", client_id="b", text=exact + "a"
        ).validate()
