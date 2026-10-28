"""Canonical sparse Merkle tree. Byte-compatible with internal/smt."""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field

EMPTY = b"\x00" * 32


class ProofError(ValueError):
    pass


def _sha(data: bytes) -> bytes:
    return hashlib.sha256(data).digest()


def _leaf(path: bytes, value_hash: bytes) -> bytes:
    return _sha(b"\x00" + path + value_hash)


def _node(left: bytes, right: bytes) -> bytes:
    return _sha(b"\x01" + left + right)


def key_path(key: str) -> bytes:
    return _sha(key.encode())


def hash_value(value: bytes) -> bytes:
    return _sha(value)


@dataclass
class Node:
    leaf: bool = False
    path: bytes = b""
    val: bytes = b""
    left: Node | None = None
    right: Node | None = None


def _bit(path: bytes, depth: int) -> int:
    return (path[depth // 8] >> (7 - depth % 8)) & 1


def _hash_at(node: Node | None, depth: int) -> bytes:
    if node is None:
        return EMPTY
    if node.leaf:
        h = _leaf(node.path, node.val)
        for level in range(255, depth - 1, -1):
            h = _node(h, EMPTY) if _bit(node.path, level) == 0 else _node(EMPTY, h)
        return h
    return _node(_hash_at(node.left, depth + 1), _hash_at(node.right, depth + 1))


def _insert(node: Node | None, path: bytes, val: bytes, depth: int) -> Node:
    if depth > 256:
        raise ValueError("key collision")
    if node is None:
        return Node(leaf=True, path=path, val=val)
    if node.leaf:
        if node.path == path:
            node.val = val
            return node
        return _combine(
            Node(leaf=True, path=node.path, val=node.val),
            Node(leaf=True, path=path, val=val),
            depth,
        )
    if _bit(path, depth) == 0:
        node.left = _insert(node.left, path, val, depth + 1)
    else:
        node.right = _insert(node.right, path, val, depth + 1)
    return node


def _combine(a: Node, b: Node, depth: int) -> Node:
    if depth >= 256:
        raise ValueError("key collision")
    out = Node()
    if _bit(a.path, depth) == _bit(b.path, depth):
        child = _combine(a, b, depth + 1)
        if _bit(a.path, depth) == 0:
            out.left = child
        else:
            out.right = child
        return out
    if _bit(a.path, depth) == 0:
        out.left, out.right = a, b
    else:
        out.left, out.right = b, a
    return out


@dataclass
class Proof:
    key: str
    value: bytes | None
    depth: int
    terminal: bytes
    siblings: list[bytes] = field(default_factory=list)

    def verify(self, root: bytes) -> None:
        if len(self.siblings) != 256 or not 0 <= self.depth <= 256:
            raise ProofError("shape")
        path = key_path(self.key)
        cur = self.terminal
        if self.value is not None:
            want = _leaf(path, hash_value(self.value))
            for level in range(255, self.depth - 1, -1):
                want = _node(want, EMPTY) if _bit(path, level) == 0 else _node(EMPTY, want)
            if want != self.terminal:
                raise ProofError("terminal hash")
        elif self.depth == 256 and self.terminal != EMPTY:
            raise ProofError("terminal hash")
        for depth in range(self.depth - 1, -1, -1):
            if _bit(path, depth) == 0:
                cur = _node(cur, self.siblings[depth])
            else:
                cur = _node(self.siblings[depth], cur)
        if cur != root:
            raise ProofError("root mismatch")


class Tree:
    def __init__(self) -> None:
        self.values: dict[str, bytes] = {}
        self.trie: Node | None = None
        self.root: bytes = EMPTY
        self.dirty = False

    def get(self, key: str) -> bytes | None:
        return self.values.get(key)

    def set(self, key: str, value: bytes) -> None:
        self.values[key] = bytes(value)
        self.dirty = True

    def delete(self, key: str) -> None:
        self.values.pop(key, None)
        self.dirty = True

    def keys(self) -> list[str]:
        return sorted(self.values)

    def snapshot(self) -> dict[str, bytes]:
        return {k: bytes(v) for k, v in self.values.items()}

    def load(self, values: dict[str, bytes]) -> None:
        self.values = {k: bytes(v) for k, v in values.items()}
        self.dirty = True

    def _rebuild(self) -> None:
        if not self.dirty:
            return
        self.trie = None
        for key in self.keys():
            self.trie = _insert(self.trie, key_path(key), hash_value(self.values[key]), 0)
        self.root = _hash_at(self.trie, 0)
        self.dirty = False

    def state_root(self) -> bytes:
        self._rebuild()
        return self.root

    def prove(self, key: str) -> Proof:
        self._rebuild()
        path = key_path(key)
        proof = Proof(key=key, value=self.values.get(key), depth=256, terminal=EMPTY,
                      siblings=[EMPTY] * 256)
        node = self.trie
        for depth in range(256):
            if node is None:
                proof.depth = depth
                return proof
            if node.leaf:
                proof.depth = depth
                proof.terminal = _hash_at(node, depth)
                return proof
            if _bit(path, depth) == 0:
                proof.siblings[depth] = _hash_at(node.right, depth + 1)
                node = node.left
            else:
                proof.siblings[depth] = _hash_at(node.left, depth + 1)
                node = node.right
        return proof
