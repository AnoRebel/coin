// Package snapshot creates, verifies, and restores app-store snapshots.
//
// A snapshot is a gzipped JSON document containing a digest, the blocks since
// the snapshot base, and the full state at the snapshot height. The digest
// covers every field, so any edit is detected before a restore. Restores write
// into a fresh sibling directory and rename it into place only after the whole
// snapshot verifies.
package snapshot

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/smt"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

// Format identifies the snapshot document version.
const Format = "arne-snapshot/1"

const (
	KindFull        = "full"
	KindIncremental = "incremental"
)

// ErrInvalid means the snapshot is malformed, tampered with, or does not chain
// from its base.
var ErrInvalid = errors.New("snapshot: invalid snapshot")

// File is one snapshot document.
type File struct {
	Format        string            `json:"format"`
	Kind          string            `json:"kind"`
	Network       string            `json:"network"`
	Chain         string            `json:"chain"`
	SchemaVersion uint32            `json:"schema_version"`
	Height        uint64            `json:"height"`
	StateRoot     string            `json:"state_root"`
	BaseHeight    uint64            `json:"base_height,omitempty"`
	BaseStateRoot string            `json:"base_state_root,omitempty"`
	Blocks        []ledger.Block    `json:"blocks"`
	State         map[string]string `json:"state"`
	Digest        string            `json:"sha256"`
}

// Options configures Create.
type Options struct {
	Network string
	Chain   string
	// Base makes the snapshot incremental. It must be a verified full snapshot
	// below the store height.
	Base *File
}

// Create reads a stopped store and returns a stamped snapshot.
func Create(store storage.Store, opts Options) (*File, error) {
	if _, err := protocol.NetworkClass(opts.Network); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if want, err := protocol.ChainID(opts.Network); err != nil || want != opts.Chain {
		return nil, fmt.Errorf("%w: chain %q does not match %q", ErrInvalid, opts.Chain, opts.Network)
	}
	last, ok, err := store.LastBlock()
	if err != nil {
		return nil, err
	}
	if !ok || last.Height == 0 {
		return nil, fmt.Errorf("%w: store has no blocks", ErrInvalid)
	}
	state, height, ok, err := store.LoadState()
	if err != nil {
		return nil, err
	}
	if !ok || height != last.Height {
		return nil, fmt.Errorf("%w: state height %d does not match block %d", ErrInvalid, height, last.Height)
	}
	from := uint64(1)
	kind := KindFull
	f := &File{
		Format: Format, Kind: kind, Network: opts.Network, Chain: opts.Chain,
		SchemaVersion: storage.SchemaVersion, Height: last.Height,
		StateRoot: last.StateRoot,
	}
	if opts.Base != nil {
		if opts.Base.Kind != KindFull {
			return nil, fmt.Errorf("%w: base is not a full snapshot", ErrInvalid)
		}
		if err := Verify(opts.Base, nil); err != nil {
			return nil, err
		}
		if opts.Base.Height >= last.Height {
			return nil, fmt.Errorf("%w: base height %d is not below %d", ErrInvalid, opts.Base.Height, last.Height)
		}
		if opts.Base.Network != opts.Network || opts.Base.Chain != opts.Chain {
			return nil, fmt.Errorf("%w: base is for another network", ErrInvalid)
		}
		kind = KindIncremental
		from = opts.Base.Height + 1
		f.Kind = kind
		f.BaseHeight = opts.Base.Height
		f.BaseStateRoot = opts.Base.StateRoot
	}
	for h := from; h <= last.Height; h++ {
		b, ok, err := store.Block(h)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: block %d is missing", ErrInvalid, h)
		}
		f.Blocks = append(f.Blocks, b)
	}
	f.State = make(map[string]string, len(state))
	for k, v := range state {
		f.State[k] = hex.EncodeToString(v)
	}
	if err := stamp(f); err != nil {
		return nil, err
	}
	if err := Verify(f, opts.Base); err != nil {
		return nil, err
	}
	return f, nil
}

// Verify checks the digest, block chain, and state root. Incrementals require
// their full base.
func Verify(f *File, base *File) error {
	if f == nil || f.Format != Format {
		return fmt.Errorf("%w: unsupported format", ErrInvalid)
	}
	if f.Digest == "" {
		return fmt.Errorf("%w: missing digest", ErrInvalid)
	}
	want, err := digest(f)
	if err != nil {
		return err
	}
	if f.Digest != want {
		return fmt.Errorf("%w: checksum mismatch", ErrInvalid)
	}
	if f.Network == "" || f.Chain == "" {
		return fmt.Errorf("%w: network and chain are required", ErrInvalid)
	}
	if want, err := protocol.ChainID(f.Network); err != nil || want != f.Chain {
		return fmt.Errorf("%w: chain %q does not match %q", ErrInvalid, f.Chain, f.Network)
	}
	if f.SchemaVersion > storage.SchemaVersion {
		return fmt.Errorf("%w: schema %d is newer than this binary", ErrInvalid, f.SchemaVersion)
	}
	if f.Height == 0 || len(f.Blocks) == 0 {
		return fmt.Errorf("%w: empty snapshot", ErrInvalid)
	}
	first := f.Blocks[0].Height
	switch f.Kind {
	case KindFull:
		if f.BaseHeight != 0 || f.BaseStateRoot != "" {
			return fmt.Errorf("%w: full snapshot has a base", ErrInvalid)
		}
		if first != 1 {
			return fmt.Errorf("%w: full snapshot starts at %d", ErrInvalid, first)
		}
	case KindIncremental:
		if base == nil {
			return fmt.Errorf("%w: incremental snapshot requires a base", ErrInvalid)
		}
		if base.Kind != KindFull {
			return fmt.Errorf("%w: base is not a full snapshot", ErrInvalid)
		}
		if err := Verify(base, nil); err != nil {
			return err
		}
		if f.BaseHeight != base.Height || f.BaseStateRoot != base.StateRoot {
			return fmt.Errorf("%w: base does not match", ErrInvalid)
		}
		if f.Network != base.Network || f.Chain != base.Chain {
			return fmt.Errorf("%w: base is for another network", ErrInvalid)
		}
		if first != base.Height+1 || first > f.Height {
			return fmt.Errorf("%w: incremental starts at %d, base is %d", ErrInvalid, first, base.Height)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, f.Kind)
	}
	prev := ""
	if f.Kind == KindIncremental && base != nil {
		prev = base.StateRoot
	}
	for i, b := range f.Blocks {
		if b.Height != first+uint64(i) {
			return fmt.Errorf("%w: block %d out of order", ErrInvalid, b.Height)
		}
		if b.StateRoot == "" || b.TxRoot != ledger.TxRoot(b.TxHashes) {
			return fmt.Errorf("%w: block %d is malformed", ErrInvalid, b.Height)
		}
		if prev != "" && b.PrevRoot != prev {
			return fmt.Errorf("%w: block %d does not link to its predecessor", ErrInvalid, b.Height)
		}
		prev = b.StateRoot
	}
	if f.Blocks[len(f.Blocks)-1].Height != f.Height ||
		f.Blocks[len(f.Blocks)-1].StateRoot != f.StateRoot {
		return fmt.Errorf("%w: last block does not match the manifest", ErrInvalid)
	}
	root, err := stateRoot(f.State)
	if err != nil {
		return err
	}
	if root != f.StateRoot {
		return fmt.Errorf("%w: state root %s does not match manifest %s", ErrInvalid, root, f.StateRoot)
	}
	return nil
}

func stateRoot(state map[string]string) (string, error) {
	values := make(map[string][]byte, len(state))
	for k, v := range state {
		raw, err := hex.DecodeString(v)
		if err != nil {
			return "", fmt.Errorf("%w: state key %q: %v", ErrInvalid, k, err)
		}
		values[k] = raw
	}
	tree := smt.New()
	tree.Load(values)
	root := tree.Root()
	return hex.EncodeToString(root[:]), nil
}

func stamp(f *File) error {
	d, err := digest(f)
	if err != nil {
		return err
	}
	f.Digest = d
	return nil
}

func digest(f *File) (string, error) {
	cp := *f
	cp.Digest = ""
	raw, err := json.Marshal(cp)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// WriteFile writes a stamped snapshot atomically as gzipped JSON.
func WriteFile(path string, f *File) error {
	if f.Digest == "" {
		return fmt.Errorf("%w: missing digest", ErrInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	gz := gzip.NewWriter(out)
	enc := json.NewEncoder(gz)
	if err := enc.Encode(f); err != nil {
		out.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadFile reads a snapshot document without verifying it.
func ReadFile(path string) (*File, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	defer gz.Close()
	var f File
	if err := json.NewDecoder(io.LimitReader(gz, 1<<31)).Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return &f, nil
}

// Restore writes a verified snapshot into a new directory. Incrementals also
// require their full base. The destination must not exist.
func Restore(f *File, base *File, dir string) error {
	if err := Verify(f, base); err != nil {
		return err
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%w: destination %s already exists", ErrInvalid, dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".arne-snapshot-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	store, err := storage.OpenPebble(tmp)
	if err != nil {
		return err
	}
	if base != nil {
		if err := writeStore(store, base); err != nil {
			store.Close()
			return err
		}
	}
	if err := writeStore(store, f); err != nil {
		store.Close()
		return err
	}
	if err := store.Migrate(storage.SchemaVersion); err != nil {
		store.Close()
		return err
	}
	last, ok, err := store.LastBlock()
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if !ok || last.Height != f.Height || last.StateRoot != f.StateRoot {
		return fmt.Errorf("%w: restored store does not match the snapshot", ErrInvalid)
	}
	return os.Rename(tmp, dir)
}

func writeStore(store storage.Store, f *File) error {
	state := make(map[string][]byte, len(f.State))
	for k, v := range f.State {
		raw, err := hex.DecodeString(v)
		if err != nil {
			return fmt.Errorf("%w: state key %q: %v", ErrInvalid, k, err)
		}
		state[k] = raw
	}
	for _, b := range f.Blocks {
		if err := store.SaveBlock(b); err != nil {
			return err
		}
	}
	return store.SaveCommit(f.Blocks[len(f.Blocks)-1], state)
}
