// Package tests runs the shared conformance vectors against the Go stack.
package tests

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/smt"
)

const vectorDir = "../spec/vectors"

func load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(vectorDir + "/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

func fromJSON(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case nil, bool, string:
		return x
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil && i < 0 {
			return i
		}
		u, err := strconv.ParseUint(string(x), 10, 64)
		if err != nil {
			t.Fatalf("bad number %s", x)
		}
		return u
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = fromJSON(t, item)
		}
		return out
	case map[string]any:
		if raw, ok := x["$bytes"].(string); ok && len(x) == 1 {
			b, err := hex.DecodeString(raw)
			if err != nil {
				t.Fatalf("bad $bytes: %v", err)
			}
			return b
		}
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = fromJSON(t, item)
		}
		return out
	default:
		t.Fatalf("unsupported JSON type %T", v)
		return nil
	}
}

func TestCBORVectors(t *testing.T) {
	var f struct {
		Cases []struct {
			Name string `json:"name"`
			Hex  string `json:"hex"`
			JSON any    `json:"json"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile(vectorDir + "/cbor.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		val := fromJSON(t, c.JSON)
		got, err := protocol.Encode(val)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if hex.EncodeToString(got) != c.Hex {
			t.Fatalf("%s: encode %x want %s", c.Name, got, c.Hex)
		}
		raw, _ := hex.DecodeString(c.Hex)
		back, err := protocol.Decode(raw)
		if err != nil {
			t.Fatalf("%s: decode: %v", c.Name, err)
		}
		again, err := protocol.Encode(back)
		if err != nil || hex.EncodeToString(again) != c.Hex {
			t.Fatalf("%s: round-trip %x", c.Name, again)
		}
	}
}

func TestKeyVectors(t *testing.T) {
	var f struct {
		Mnemonic    string `json:"mnemonic"`
		Passphrase  string `json:"passphrase"`
		Derivations []struct {
			Path    []uint32          `json:"path"`
			Private string            `json:"private"`
			Public  string            `json:"public"`
			Address map[string]string `json:"address"`
		} `json:"derivations"`
	}
	load(t, "keys.json", &f)
	for _, d := range f.Derivations {
		if len(d.Path) != 5 {
			t.Fatal("bad path")
		}
		priv, err := protocol.DerivePath(f.Mnemonic, f.Passphrase, d.Path[2], d.Path[3], d.Path[4])
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(priv) != d.Private {
			t.Fatalf("path %v private mismatch", d.Path)
		}
		kp, err := protocol.KeyPairFromPrivate(priv, "arne:devnet")
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(kp.Public) != d.Public {
			t.Fatalf("path %v public mismatch", d.Path)
		}
		for network, want := range d.Address {
			if got := protocol.AddressFromPubKey(kp.Public, network); got != want {
				t.Fatalf("%s address = %s want %s", network, got, want)
			}
		}
	}
}

func TestSMTVectors(t *testing.T) {
	var f struct {
		Cases []struct {
			Keys   []string `json:"keys"`
			Values []string `json:"values"`
			Root   string   `json:"root"`
			Proofs []struct {
				Key      string   `json:"key"`
				Value    string   `json:"value"`
				Depth    int      `json:"depth"`
				Terminal string   `json:"terminal"`
				Siblings []string `json:"siblings"`
			} `json:"proofs"`
		} `json:"cases"`
	}
	load(t, "smt.json", &f)
	for i, c := range f.Cases {
		tr := smt.New()
		for j, k := range c.Keys {
			v, _ := hex.DecodeString(c.Values[j])
			tr.Set(k, v)
		}
		root := tr.Root()
		if hex.EncodeToString(root[:]) != c.Root {
			t.Fatalf("case %d root mismatch", i)
		}
		for _, p := range c.Proofs {
			siblings := make([]smt.Hash, len(p.Siblings))
			for j, s := range p.Siblings {
				b, _ := hex.DecodeString(s)
				copy(siblings[j][:], b)
			}
			term, _ := hex.DecodeString(p.Terminal)
			var terminal smt.Hash
			copy(terminal[:], term)
			var value []byte
			if p.Value != "" {
				value, _ = hex.DecodeString(p.Value)
			}
			proof := smt.Proof{Key: p.Key, Value: value, Depth: p.Depth, Terminal: terminal, Siblings: siblings}
			if err := proof.Verify(root); err != nil {
				t.Fatalf("case %d proof %s: %v", i, p.Key, err)
			}
		}
	}
}

func TestLedgerVectors(t *testing.T) {
	var f struct {
		Network  string `json:"network"`
		Chain    string `json:"chain"`
		Mnemonic string `json:"mnemonic"`
		Genesis  struct {
			Time        uint64                       `json:"time"`
			Params      ledger.Params                `json:"params"`
			Assets      []protocol.Asset             `json:"assets"`
			Allocations []ledger.Allocation          `json:"allocations"`
			Pools       map[string]map[string]uint64 `json:"pools"`
		} `json:"genesis"`
		Steps []struct {
			Block    uint64            `json:"block"`
			Time     uint64            `json:"time"`
			SetRoute map[string]uint64 `json:"set_route"`
			Txs      []struct {
				Signer string `json:"signer"`
				CBOR   string `json:"cbor"`
			} `json:"txs"`
			Receipts []struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
				Fee     uint64 `json:"fee"`
			} `json:"receipts"`
			StateRoot string `json:"state_root"`
			TxRoot    string `json:"tx_root"`
			BlockHash string `json:"block_hash"`
		} `json:"steps"`
	}
	load(t, "ledger.json", &f)
	genesis := ledger.Genesis{
		Network: f.Network, Chain: f.Chain, Time: f.Genesis.Time,
		Params: f.Genesis.Params, Assets: f.Genesis.Assets,
		Allocations: f.Genesis.Allocations, Pools: f.Genesis.Pools,
	}
	st, err := ledger.NewGenesis(genesis)
	if err != nil {
		t.Fatal(err)
	}
	indices := map[string]uint32{"alice": 0, "bob": 1, "carol": 2}
	for _, step := range f.Steps {
		for asset, rate := range step.SetRoute {
			st.SetRoute(asset, rate)
		}
		var txs []protocol.Tx
		for _, vtx := range step.Txs {
			raw, _ := hex.DecodeString(vtx.CBOR)
			m, err := protocol.DecodeMap(raw)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := protocol.TxFromCBOR(m)
			if err != nil {
				t.Fatal(err)
			}
			kp, err := protocol.KeyPairFromMnemonic(f.Mnemonic, "", 0, 0, indices[vtx.Signer], f.Network)
			if err != nil {
				t.Fatal(err)
			}
			tx.Signatures = nil
			if err := tx.Sign(kp); err != nil {
				t.Fatal(err)
			}
			txs = append(txs, tx)
		}
		block, receipts, err := st.ApplyBlock(txs, step.Time, step.Block, f.Genesis.Allocations[0].Address)
		if err != nil {
			t.Fatalf("block %d: %v", step.Block, err)
		}
		if block.StateRoot != step.StateRoot {
			t.Fatalf("block %d root = %s want %s", step.Block, block.StateRoot, step.StateRoot)
		}
		if block.TxRoot != step.TxRoot {
			t.Fatalf("block %d tx root = %s want %s", step.Block, block.TxRoot, step.TxRoot)
		}
		h, _ := block.Hash()
		if h != step.BlockHash {
			t.Fatalf("block %d hash = %s want %s", step.Block, h, step.BlockHash)
		}
		if len(receipts) != len(step.Receipts) {
			t.Fatalf("block %d receipts = %d want %d", step.Block, len(receipts), len(step.Receipts))
		}
		for i, r := range receipts {
			want := step.Receipts[i]
			if r.Success != want.Success || string(r.Code) != want.Code || r.Fee != want.Fee {
				t.Fatalf("block %d receipt %d = %+v want %+v", step.Block, i, r, want)
			}
		}
	}
}

func TestTxVectors(t *testing.T) {
	var f struct {
		Valid []struct {
			Name   string `json:"name"`
			CBOR   string `json:"cbor"`
			Hash   string `json:"hash"`
			Signed bool   `json:"signed"`
		} `json:"valid"`
		Invalid []struct {
			Name string `json:"name"`
			CBOR string `json:"cbor"`
		} `json:"invalid"`
	}
	load(t, "tx.json", &f)
	for _, c := range f.Valid {
		raw, err := hex.DecodeString(c.CBOR)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		m, err := protocol.DecodeMap(raw)
		if err != nil {
			t.Fatalf("%s: decode: %v", c.Name, err)
		}
		tx, err := protocol.TxFromCBOR(m)
		if err != nil {
			t.Fatalf("%s: parse: %v", c.Name, err)
		}
		enc, err := protocol.Encode(tx.ToCBOR())
		if err != nil || hex.EncodeToString(enc) != c.CBOR {
			t.Fatalf("%s: re-encode differs", c.Name)
		}
		if h, err := tx.Hash(); err != nil || h != c.Hash {
			t.Fatalf("%s: hash = %s want %s", c.Name, h, c.Hash)
		}
		if got := len(tx.Signatures) > 0; got != c.Signed {
			t.Fatalf("%s: signed = %v", c.Name, got)
		}
		if c.Signed {
			if err := tx.Verify(); err != nil {
				t.Fatalf("%s: signature: %v", c.Name, err)
			}
		}
	}
	for _, c := range f.Invalid {
		raw, err := hex.DecodeString(c.CBOR)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		m, err := protocol.DecodeMap(raw)
		if err != nil {
			t.Fatalf("%s: decode: %v", c.Name, err)
		}
		if _, err := protocol.TxFromCBOR(m); err == nil {
			t.Fatalf("%s: accepted", c.Name)
		}
	}
}
