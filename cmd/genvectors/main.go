// Command genvectors writes the shared conformance vectors in spec/vectors.
// Run it from the repository root: go run ./cmd/genvectors
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	arnememo "github.com/anorebel/anorebelcoin/internal/memo"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/smt"
)

const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func main() {
	dir := "spec/vectors"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	write(dir, "cbor.json", cborVectors())
	write(dir, "keys.json", keyVectors())
	write(dir, "smt.json", smtVectors())
	write(dir, "ledger.json", ledgerVectors())
	write(dir, "tx.json", txVectors())
	write(dir, "memo.json", memoVectors())
	fmt.Println("wrote vectors to", dir)
}

func write(dir, name string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
		panic(err)
	}
}

// txVectors pins the transaction envelope bytes, hash, and closed-schema
// rejection cases shared by both implementations.
func txVectors() map[string]any {
	const network = "arne:devnet"
	chain, _ := protocol.ChainID(network)
	kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
	if err != nil {
		panic(err)
	}
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: network, Chain: chain,
		Type: protocol.TxTransfer, Signer: kp.Address, Nonce: 0,
		CreatedAt: 1_700_000_000, ExpiresAt: 1_700_003_600,
		FeeAsset: protocol.NativeAsset(network), MaxFee: 1_000,
		PriorityFee: 0,
		Payload:     map[string]any{"to": kp.Address, "asset": protocol.NativeAsset(network), "amount": uint64(1_000_000)},
	}
	encode := func(m map[string]any) string {
		b, err := protocol.Encode(m)
		if err != nil {
			panic(err)
		}
		return hex.EncodeToString(b)
	}
	unsigned := encode(tx.ToCBOR())
	unsignedHash, err := tx.Hash()
	if err != nil {
		panic(err)
	}
	if err := tx.Sign(kp); err != nil {
		panic(err)
	}
	signed := encode(tx.ToCBOR())
	signedHash, err := tx.Hash()
	if err != nil {
		panic(err)
	}

	withField := func(m map[string]any, key string, value any) string {
		m[key] = value
		return encode(m)
	}
	badSignature := tx.ToCBOR()
	badSignature["signatures"] = []any{map[string]any{
		"suite": tx.Signatures[0].Suite, "pubkey": tx.Signatures[0].PubKey,
		"sig": tx.Signatures[0].Sig, "kid": "key-1",
	}}
	return map[string]any{
		"envelope": map[string]any{
			"name": "transfer", "network": network, "chain": chain,
			"signer": kp.Address,
		},
		"valid": []map[string]any{
			{"name": "unsigned", "cbor": unsigned, "hash": unsignedHash, "signed": false},
			{"name": "signed", "cbor": signed, "hash": signedHash, "signed": true},
		},
		"invalid": []map[string]any{
			{"name": "unknown-envelope-field", "cbor": withField(tx.ToCBOR(), "crit", []any{"ext"})},
			{"name": "unknown-signature-field", "cbor": encode(badSignature)},
		},
	}
}

// memoVectors pins key derivation and ciphertext bytes with fixed entropy so
// both implementations can decrypt and reproduce the same sealed memo.
func memoVectors() map[string]any {
	const network = "arne:devnet"
	recipient, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 1, network)
	if err != nil {
		panic(err)
	}
	seed := recipient.Private.Seed()
	pub, err := arnememo.PublicKey(seed)
	if err != nil {
		panic(err)
	}
	seq := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(i + 1)
		}
		return b
	}
	ephemeral, nonce := seq(32), seq(12)
	sealed, err := arnememo.Seal(pub, []byte("invoice 42"), bytes.NewReader(append(ephemeral, nonce...)))
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"mnemonic": mnemonic, "network": network, "recipient_index": 1,
		"recipient_public": hex.EncodeToString(pub),
		"plaintext":        "invoice 42",
		"ephemeral_seed":   hex.EncodeToString(ephemeral),
		"nonce":            hex.EncodeToString(nonce),
		"sealed":           hex.EncodeToString(sealed),
	}
}

type cborCase struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
	JSON any    `json:"json"`
}

func cborVectors() map[string]any {
	bytesVal := func(b []byte) map[string]any { return map[string]any{"$bytes": hex.EncodeToString(b)} }
	cases := []struct {
		name string
		val  any
	}{
		{"nil", nil},
		{"false", false},
		{"true", true},
		{"uint-0", uint64(0)},
		{"uint-23", uint64(23)},
		{"uint-24", uint64(24)},
		{"uint-255", uint64(255)},
		{"uint-256", uint64(256)},
		{"uint-max", uint64(18446744073709551615)},
		{"neg-1", int64(-1)},
		{"neg-1000", int64(-1000)},
		{"text-empty", ""},
		{"text-arne", "arne"},
		{"text-unicode", "AnorebelCoin ⚡"},
		{"bytes-empty", []byte{}},
		{"bytes", []byte{0, 1, 2, 254, 255}},
		{"array", []any{uint64(1), "two", []byte{3}}},
		{"map-short-keys", map[string]any{"a": uint64(1), "bb": uint64(2), "ccc": uint64(3)}},
		{"map-nested", map[string]any{"k": map[string]any{"x": []any{true, nil}}}},
	}
	var out []cborCase
	for _, c := range cases {
		enc, err := protocol.Encode(c.val)
		if err != nil {
			panic(err)
		}
		var jsonVal any = c.val
		switch v := c.val.(type) {
		case []byte:
			jsonVal = bytesVal(v)
		case []any:
			parts := make([]any, len(v))
			for i, item := range v {
				if b, ok := item.([]byte); ok {
					parts[i] = bytesVal(b)
				} else {
					parts[i] = item
				}
			}
			jsonVal = parts
		}
		out = append(out, cborCase{Name: c.name, Hex: hex.EncodeToString(enc), JSON: jsonVal})
	}
	return map[string]any{"cases": out}
}

type derivation struct {
	Path    []uint32          `json:"path"`
	Private string            `json:"private"`
	Public  string            `json:"public"`
	Address map[string]string `json:"address"`
}

func keyVectors() map[string]any {
	var derivations []derivation
	for _, idx := range []uint32{0, 1, 2} {
		priv, err := protocol.DerivePath(mnemonic, "", 0, 0, idx)
		if err != nil {
			panic(err)
		}
		kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, idx, "arne:devnet")
		if err != nil {
			panic(err)
		}
		derivations = append(derivations, derivation{
			Path: []uint32{44, 9999, 0, 0, idx}, Private: hex.EncodeToString(priv),
			Public: hex.EncodeToString(kp.Public),
			Address: map[string]string{
				"arne:devnet":          kp.Address,
				"arne:mainnet":         protocol.AddressFromPubKey(kp.Public, "arne:mainnet"),
				"arne:consortium/acme": protocol.AddressFromPubKey(kp.Public, "arne:consortium/acme"),
			},
		})
	}
	return map[string]any{"mnemonic": mnemonic, "passphrase": "", "derivations": derivations}
}

func smtVectors() map[string]any {
	sets := []map[string]string{
		{"a": "01"},
		{"a": "01", "b": "02"},
		{"a": "01", "b": "02", "c": "03", "d": "04"},
		{"account/alice/nonce": "0000000000000001", "balance/alice/arce:devnet": "0000000005f5e100", "asset/arce:devnet": "61726365"},
	}
	var cases []map[string]any
	for _, set := range sets {
		tr := smt.New()
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		// deterministic insertion order
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		vals := make([]string, len(keys))
		for i, k := range keys {
			b, _ := hex.DecodeString(set[k])
			tr.Set(k, b)
			vals[i] = set[k]
		}
		root := tr.Root()
		var proofs []map[string]any
		for _, k := range keys {
			p := tr.Prove(k)
			siblings := make([]string, len(p.Siblings))
			for i, s := range p.Siblings {
				siblings[i] = hex.EncodeToString(s[:])
			}
			v := ""
			if p.Value != nil {
				v = hex.EncodeToString(p.Value)
			}
			proofs = append(proofs, map[string]any{
				"key": k, "value": v, "depth": p.Depth,
				"terminal": hex.EncodeToString(p.Terminal[:]), "siblings": siblings,
			})
		}
		cases = append(cases, map[string]any{
			"keys": keys, "values": vals, "root": hex.EncodeToString(root[:]), "proofs": proofs,
		})
	}
	return map[string]any{"cases": cases}
}

type vectorTx struct {
	Signer string `json:"signer"`
	CBOR   string `json:"cbor"`
}

type vectorReceipt struct {
	Success bool   `json:"success"`
	Code    string `json:"code,omitempty"`
	Fee     uint64 `json:"fee"`
}

type vectorStep struct {
	Block     uint64            `json:"block"`
	Time      uint64            `json:"time"`
	SetRoute  map[string]uint64 `json:"set_route,omitempty"`
	Txs       []vectorTx        `json:"txs"`
	Receipts  []vectorReceipt   `json:"receipts"`
	StateRoot string            `json:"state_root"`
	TxRoot    string            `json:"tx_root"`
	BlockHash string            `json:"block_hash"`
}

func ledgerVectors() map[string]any {
	network := "arne:devnet"
	chain, _ := protocol.ChainID(network)
	keys := map[string]*protocol.KeyPair{}
	for i, name := range []string{"alice", "bob", "carol"} {
		kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, uint32(i), network)
		if err != nil {
			panic(err)
		}
		keys[name] = kp
	}
	params := ledger.DefaultParams()
	usdID := "usd:" + protocol.ShortNetwork(network)
	native := protocol.NativeAsset(network)
	genesis := ledger.Genesis{
		Network: network, Chain: chain, Time: 1_700_000_000, Params: params,
		Assets: []protocol.Asset{{
			ID: usdID, Class: "fiat", Symbol: "USD", Name: "Vector Dollar",
			Decimals: 2, Policy: "capped", MaxSupply: 1_000_000_000, Issuer: keys["alice"].Address,
			Metadata: map[string]string{"iso4217": "USD"},
		}},
		Allocations: []ledger.Allocation{
			{Address: keys["alice"].Address, Asset: native, Amount: 100_000_000_000},
			{Address: keys["bob"].Address, Asset: native, Amount: 10_000_000_000},
			{Address: keys["carol"].Address, Asset: native, Amount: 10_000_000_000},
		},
		Pools: map[string]map[string]uint64{
			ledger.PoolFeeSwap: {native: 100_000_000_000},
		},
	}
	st, err := ledger.NewGenesis(genesis)
	if err != nil {
		panic(err)
	}
	nonces := map[string]uint64{}
	next := func(signer string) uint64 {
		n := nonces[signer]
		nonces[signer] = n + 1
		return n
	}
	mk := func(signer, typ string, payload map[string]any, feeAsset string) protocol.Tx {
		tx := protocol.Tx{
			Version: protocol.CurrentTxVersion, Network: network, Chain: chain,
			Type: typ, Signer: keys[signer].Address, Nonce: next(signer),
			CreatedAt: 1_700_000_000, ExpiresAt: 1_700_100_000,
			FeeAsset: feeAsset, MaxFee: 1_000_000, Payload: payload,
		}
		if err := tx.Sign(keys[signer]); err != nil {
			panic(err)
		}
		return tx
	}
	steps := []vectorStep{}
	run := func(blockNo, at uint64, route map[string]uint64, txs ...protocol.Tx) {
		if route != nil {
			for asset, rate := range route {
				st.SetRoute(asset, rate)
			}
		}
		var signed []protocol.Tx
		var vtx []vectorTx
		for _, tx := range txs {
			unsigned := tx
			unsigned.Signatures = nil
			enc, err := protocol.Encode(unsigned.ToCBOR())
			if err != nil {
				panic(err)
			}
			signer := ""
			for name, kp := range keys {
				if kp.Address == tx.Signer {
					signer = name
				}
			}
			vtx = append(vtx, vectorTx{Signer: signer, CBOR: hex.EncodeToString(enc)})
			signed = append(signed, tx)
		}
		blk, receipts, err := st.ApplyBlock(signed, at, blockNo, keys["alice"].Address)
		if err != nil {
			panic(err)
		}
		var vrs []vectorReceipt
		for _, r := range receipts {
			vrs = append(vrs, vectorReceipt{Success: r.Success, Code: string(r.Code), Fee: r.Fee})
		}
		h, _ := blk.Hash()
		steps = append(steps, vectorStep{
			Block: blockNo, Time: at, SetRoute: route, Txs: vtx, Receipts: vrs,
			StateRoot: blk.StateRoot, TxRoot: blk.TxRoot, BlockHash: h,
		})
	}

	run(1, 1_700_000_100, nil,
		mk("alice", protocol.TxTransfer, map[string]any{"asset": native, "to": keys["bob"].Address, "amount": uint64(500_000_000)}, native),
		mk("bob", protocol.TxTransfer, map[string]any{"asset": native, "to": keys["alice"].Address, "amount": uint64(100_000_000), "memo": "thanks"}, native),
		mk("carol", protocol.TxBatch, map[string]any{"items": []any{
			map[string]any{"asset": native, "to": keys["alice"].Address, "amount": uint64(1_000_000)},
			map[string]any{"asset": native, "to": keys["bob"].Address, "amount": uint64(2_000_000)},
		}}, native),
	)
	run(2, 1_700_000_200, nil,
		mk("alice", protocol.TxRegisterAsset, map[string]any{"asset": map[string]any{
			"id": usdID, "class": "fiat", "symbol": "USD", "name": "Vector Dollar",
			"decimals": uint64(2), "policy": "capped", "max_supply": uint64(1_000_000_000),
			"issuer": keys["alice"].Address, "registered": uint64(0),
			"meta": map[string]any{"iso4217": "USD"},
		}}, native),
		mk("alice", protocol.TxMint, map[string]any{"asset": usdID, "to": keys["alice"].Address, "amount": uint64(1_000_000)}, native),
		mk("alice", protocol.TxMint, map[string]any{"asset": usdID, "to": keys["bob"].Address, "amount": uint64(100_000)}, native),
	)
	run(3, 1_700_000_300, nil,
		mk("alice", protocol.TxApprove, map[string]any{"spender": keys["bob"].Address, "asset": usdID, "amount": uint64(200_000), "expires_at": uint64(0)}, native),
		mk("bob", protocol.TxTransferFrom, map[string]any{"from": keys["alice"].Address, "to": keys["carol"].Address, "asset": usdID, "amount": uint64(50_000)}, native),
	)
	run(4, 1_700_000_400, nil,
		mk("alice", protocol.TxCreateEscrow, map[string]any{
			"asset": native, "amount": uint64(3_000_000), "beneficiary": keys["bob"].Address,
			"arbiter": keys["carol"].Address, "timeout": uint64(1_800_000_000),
		}, native),
		mk("carol", protocol.TxReleaseEscrow, map[string]any{"id": escrowIDFor(keys["alice"].Address, native, 3_000_000, 1_800_000_000, 5, keys["bob"].Address)}, native),
	)
	run(5, 1_700_000_500, map[string]uint64{usdID: 100_000_000},
		mk("bob", protocol.TxTransfer, map[string]any{"asset": native, "to": keys["carol"].Address, "amount": uint64(1_000)}, usdID),
	)
	// Read actual committed nonces: earlier steps deliberately contain failures.
	for name, kp := range keys {
		account, _ := st.Account(kp.Address)
		nonces[name] = account.Nonce
	}
	intentID := protocol.IntentID(network, keys["bob"].Address, "invoice_42")
	run(6, 1_700_000_600, nil,
		mk("bob", protocol.TxCreateIntent, map[string]any{
			"asset": native, "amount": uint64(5_000_000), "reference": "invoice_42",
			"expires_at": uint64(1_700_001_000), "memo": "invoice 42",
		}, native),
	)
	run(7, 1_700_000_700, nil,
		mk("alice", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["bob"].Address, "amount": uint64(5_000_000), "intent": intentID,
		}, native),
	)
	run(8, 1_700_000_800, nil,
		mk("carol", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["bob"].Address, "amount": uint64(5_000_000), "intent": intentID,
		}, native),
	)
	payment := mk("alice", protocol.TxTransfer, map[string]any{
		"asset": native, "to": keys["bob"].Address, "amount": uint64(4_000_000),
	}, native)
	paymentHash, err := payment.Hash()
	if err != nil {
		panic(err)
	}
	run(9, 1_700_000_900, nil, payment)
	run(10, 1_700_001_000, nil,
		mk("bob", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["alice"].Address, "amount": uint64(1_000_000), "refund_of": paymentHash,
		}, native),
	)
	run(11, 1_700_001_100, nil,
		mk("bob", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["alice"].Address, "amount": uint64(3_000_001), "refund_of": paymentHash,
		}, native),
		mk("carol", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["alice"].Address, "amount": uint64(1_000), "refund_of": paymentHash,
		}, native),
	)
	// Failed refunds above left the nonce counter ahead; subscription IDs
	// depend on the payer's committed nonce.
	for name, kp := range keys {
		account, _ := st.Account(kp.Address)
		nonces[name] = account.Nonce
	}
	subID := protocol.SubscriptionID(network, keys["alice"].Address, keys["bob"].Address, native, nonces["alice"])
	run(12, 1_700_001_200, nil,
		mk("alice", protocol.TxCreateSub, map[string]any{
			"payee": keys["bob"].Address, "asset": native,
			"amount_per_period": uint64(2_000_000), "period": uint64(3600),
			"total_cap": uint64(5_000_000), "expires_at": uint64(1_700_010_000),
		}, native),
	)
	run(13, 1_700_001_300, nil,
		mk("bob", protocol.TxChargeSub, map[string]any{"id": subID, "amount": uint64(1_000_000)}, native),
		mk("bob", protocol.TxChargeSub, map[string]any{"id": subID, "amount": uint64(1_500_000)}, native),
	)
	for name, kp := range keys {
		account, _ := st.Account(kp.Address)
		nonces[name] = account.Nonce
	}
	run(14, 1_700_004_900, nil,
		mk("bob", protocol.TxChargeSub, map[string]any{"id": subID, "amount": uint64(2_000_000)}, native),
		mk("alice", protocol.TxCancelSub, map[string]any{"id": subID}, native),
		mk("bob", protocol.TxChargeSub, map[string]any{"id": subID, "amount": uint64(1)}, native),
	)
	for name, kp := range keys {
		account, _ := st.Account(kp.Address)
		nonces[name] = account.Nonce
	}
	run(15, 1_700_001_400, map[string]uint64{usdID: 100_000_000},
		mk("bob", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["alice"].Address, "amount": uint64(1_000), "max_arce_fee": uint64(1_000),
		}, usdID),
		mk("bob", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["alice"].Address, "amount": uint64(1_000), "max_arce_fee": uint64(999),
		}, usdID),
		mk("carol", protocol.TxTransfer, map[string]any{
			"asset": native, "to": keys["alice"].Address, "amount": uint64(1_000), "max_arce_fee": uint64(1_000),
		}, native),
	)
	return map[string]any{
		"network": network, "chain": chain, "mnemonic": mnemonic,
		"genesis": map[string]any{
			"time": genesis.Time, "params": params, "assets": genesis.Assets,
			"allocations": genesis.Allocations, "pools": genesis.Pools,
		},
		"steps": steps,
	}
}

// escrowIDFor mirrors the ledger derivation for the vector.
func escrowIDFor(signer, asset string, amount, timeout, nonce uint64, beneficiary string) string {
	body, _ := protocol.Encode(map[string]any{
		"dom": "arne-escrow-v1", "signer": signer, "nonce": nonce,
		"asset": asset, "amount": amount, "timeout": timeout, "beneficiary": beneficiary,
	})
	sum := protocol.HashHex(body)
	return "esc_" + sum[:32]
}
