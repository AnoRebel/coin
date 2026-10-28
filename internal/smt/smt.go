// Package smt implements the canonical ARNE sparse Merkle tree.
//
// Keys are hashed with SHA-256 and placed at the 256-bit path. Leaf hashes bind
// the path and the value hash, so an inclusion proof cannot be moved to another
// key. Empty subtrees are the 32-byte zero hash. Go and Python implement the
// same algorithm; spec/vectors pins the roots.
package smt

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
)

// Hash is a 32-byte digest.
type Hash [32]byte

// Empty is the hash of an empty subtree.
var Empty Hash

var (
	ErrCollision = errors.New("smt: key collision")
	ErrProof     = errors.New("smt: invalid proof")
)

func hashLeaf(path, value Hash) Hash {
	var buf [65]byte
	buf[0] = 0x00
	copy(buf[1:33], path[:])
	copy(buf[33:], value[:])
	return sha256.Sum256(buf[:])
}

func hashNode(left, right Hash) Hash {
	var buf [65]byte
	buf[0] = 0x01
	copy(buf[1:33], left[:])
	copy(buf[33:], right[:])
	return sha256.Sum256(buf[:])
}

// HashValue returns the value digest used in leaves.
func HashValue(v []byte) Hash { return sha256.Sum256(v) }

// KeyPath returns the 256-bit path of a namespaced state key.
func KeyPath(key string) Hash { return sha256.Sum256([]byte(key)) }

type node struct {
	leaf  bool
	path  Hash
	val   Hash
	left  *node
	right *node
}

// Tree is a sparse Merkle tree over string keys.
type Tree struct {
	values map[string][]byte
	trie   *node
	root   Hash
	dirty  bool
}

// New returns an empty tree.
func New() *Tree {
	return &Tree{values: map[string][]byte{}, root: Empty, trie: nil}
}

// Get returns the value for key.
func (t *Tree) Get(key string) ([]byte, bool) {
	v, ok := t.values[key]
	if !ok || v == nil {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

// Set stores key=value.
func (t *Tree) Set(key string, value []byte) {
	t.values[key] = append([]byte(nil), value...)
	t.dirty = true
}

// Delete removes key.
func (t *Tree) Delete(key string) {
	t.values[key] = nil
	t.dirty = true
}

// Len returns the number of live keys.
func (t *Tree) Len() int {
	n := 0
	for _, v := range t.values {
		if v != nil {
			n++
		}
	}
	return n
}

// Keys returns live keys in sorted order.
func (t *Tree) Keys() []string {
	keys := make([]string, 0, len(t.values))
	for k, v := range t.values {
		if v != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Snapshot returns a copy of live key/value pairs.
func (t *Tree) Snapshot() map[string][]byte {
	m := make(map[string][]byte, len(t.values))
	for k, v := range t.values {
		if v != nil {
			m[k] = append([]byte(nil), v...)
		}
	}
	return m
}

// Load replaces the tree contents (used for snapshot restore).
func (t *Tree) Load(values map[string][]byte) {
	t.values = map[string][]byte{}
	for k, v := range values {
		t.values[k] = append([]byte(nil), v...)
	}
	t.dirty = true
}

func (t *Tree) rebuild() {
	if !t.dirty {
		return
	}
	keys := t.Keys()
	t.trie = nil
	for _, k := range keys {
		path := KeyPath(k)
		val := HashValue(t.values[k])
		var err error
		t.trie, err = insert(t.trie, path, val, 0)
		if err != nil {
			// A collision is practically impossible; keep the previous root.
			t.dirty = false
			return
		}
	}
	t.root = hashAt(t.trie, 0)
	t.dirty = false
}

// hashAt hashes a subtree rooted at depth. A leaf is padded down to depth 256
// with empty siblings, so every key commits at the same tree depth.
// shortcut: padding is O(256-depth) per leaf; cache subtree hashes if state
// grows beyond devnet scale.
func hashAt(n *node, depth int) Hash {
	if n == nil {
		return Empty
	}
	if n.leaf {
		h := hashLeaf(n.path, n.val)
		for level := 255; level >= depth; level-- {
			if bit(n.path, level) == 0 {
				h = hashNode(h, Empty)
			} else {
				h = hashNode(Empty, h)
			}
		}
		return h
	}
	return hashNode(hashAt(n.left, depth+1), hashAt(n.right, depth+1))
}

func insert(n *node, path Hash, val Hash, depth int) (*node, error) {
	if depth > 256 {
		return nil, ErrCollision
	}
	if n == nil {
		return &node{leaf: true, path: path, val: val}, nil
	}
	if n.leaf {
		if n.path == path {
			n.val = val
			return n, nil
		}
		return combine(
			&node{leaf: true, path: n.path, val: n.val},
			&node{leaf: true, path: path, val: val}, depth)
	}
	var err error
	if bit(path, depth) == 0 {
		n.left, err = insert(n.left, path, val, depth+1)
	} else {
		n.right, err = insert(n.right, path, val, depth+1)
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

// combine builds the smallest branch containing two distinct leaves.
func combine(a, b *node, depth int) (*node, error) {
	if depth >= 256 {
		return nil, ErrCollision
	}
	out := &node{}
	if bit(a.path, depth) == bit(b.path, depth) {
		child, err := combine(a, b, depth+1)
		if err != nil {
			return nil, err
		}
		if bit(a.path, depth) == 0 {
			out.left = child
		} else {
			out.right = child
		}
		return out, nil
	}
	if bit(a.path, depth) == 0 {
		out.left, out.right = a, b
	} else {
		out.left, out.right = b, a
	}
	return out, nil
}

func bit(h Hash, depth int) byte {
	return (h[depth/8] >> uint(7-depth%8)) & 1
}

// Root returns the current state root.
func (t *Tree) Root() Hash {
	t.rebuild()
	return t.root
}

// Proof is a canonical sparse Merkle proof. Depth is the level at which the
// descent stopped: 256 for an empty slot, otherwise the level of the leaf that
// occupies the key's subtree. Terminal is that subtree's hash.
type Proof struct {
	Key      string
	Value    []byte // nil means exclusion
	Depth    int
	Terminal Hash
	Siblings []Hash
}

// Prove returns an inclusion or exclusion proof for key.
func (t *Tree) Prove(key string) Proof {
	t.rebuild()
	path := KeyPath(key)
	p := Proof{
		Key:      key,
		Value:    append([]byte(nil), t.values[key]...),
		Depth:    256,
		Terminal: Empty,
		Siblings: make([]Hash, 256),
	}
	n := t.trie
	for depth := 0; depth < 256; depth++ {
		if n == nil {
			p.Depth = depth
			return p
		}
		if n.leaf {
			p.Depth = depth
			p.Terminal = hashAt(n, depth)
			return p
		}
		if bit(path, depth) == 0 {
			p.Siblings[depth] = hashAt(n.right, depth+1)
			n = n.left
		} else {
			p.Siblings[depth] = hashAt(n.left, depth+1)
			n = n.right
		}
	}
	return p
}

// Verify checks a proof against a root.
func (p Proof) Verify(root Hash) error {
	if len(p.Siblings) != 256 || p.Depth < 0 || p.Depth > 256 {
		return fmt.Errorf("%w: shape", ErrProof)
	}
	path := KeyPath(p.Key)
	cur := p.Terminal
	if p.Value != nil {
		// An inclusion proof must commit to the presented value.
		want := hashLeaf(path, HashValue(p.Value))
		for level := 255; level >= p.Depth; level-- {
			if bit(path, level) == 0 {
				want = hashNode(want, Empty)
			} else {
				want = hashNode(Empty, want)
			}
		}
		if want != p.Terminal {
			return fmt.Errorf("%w: terminal hash", ErrProof)
		}
	} else if p.Depth == 256 && p.Terminal != Empty {
		return fmt.Errorf("%w: terminal hash", ErrProof)
	}
	for depth := p.Depth - 1; depth >= 0; depth-- {
		if bit(path, depth) == 0 {
			cur = hashNode(cur, p.Siblings[depth])
		} else {
			cur = hashNode(p.Siblings[depth], cur)
		}
	}
	if cur != root {
		return fmt.Errorf("%w: root mismatch", ErrProof)
	}
	return nil
}
