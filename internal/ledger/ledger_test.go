package ledger

import (
	"strings"
	"testing"

	arnememo "github.com/anorebel/anorebelcoin/internal/memo"
	"github.com/anorebel/anorebelcoin/internal/protocol"
)

const testNet = "arne:devnet"

type fixture struct {
	state *State
	alice *protocol.KeyPair
	bob   *protocol.KeyPair
	carol *protocol.KeyPair
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gen := func() *protocol.KeyPair {
		kp, _, err := protocol.NewKeyPair(testNet)
		if err != nil {
			t.Fatal(err)
		}
		return kp
	}
	f := &fixture{alice: gen(), bob: gen(), carol: gen()}
	params := DefaultParams()
	g := Genesis{
		Network: testNet, Chain: "arne-devnet-1", Time: 1_700_000_000, Params: params,
		Allocations: []Allocation{
			{Address: f.alice.Address, Asset: protocol.NativeAsset(testNet), Amount: 1_000_000_000},
			{Address: f.bob.Address, Asset: protocol.NativeAsset(testNet), Amount: 100_000_000},
			{Address: f.carol.Address, Asset: protocol.NativeAsset(testNet), Amount: 100_000_000},
		},
		Pools: map[string]map[string]uint64{
			PoolFeeSwap: {protocol.NativeAsset(testNet): 1_000_000_000},
			PoolFaucet:  {protocol.NativeAsset(testNet): 1_000_000_000},
		},
	}
	s, err := NewGenesis(g)
	if err != nil {
		t.Fatal(err)
	}
	f.state = s
	return f
}

func (f *fixture) tx(t *testing.T, kp *protocol.KeyPair, typ string, payload map[string]any, nonce uint64) protocol.Tx {
	t.Helper()
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNet, Chain: "arne-devnet-1",
		Type: typ, Signer: kp.Address, Nonce: nonce, CreatedAt: 1_700_000_000,
		ExpiresAt: 1_700_003_600, FeeAsset: protocol.NativeAsset(testNet),
		MaxFee: 10_000, Payload: payload,
	}
	if err := tx.Sign(kp); err != nil {
		t.Fatal(err)
	}
	return tx
}

func (f *fixture) apply(t *testing.T, tx protocol.Tx) Receipt {
	t.Helper()
	return f.state.ApplyTx(tx, 1_700_000_100, 1)
}

func (f *fixture) applyAt(t *testing.T, tx protocol.Tx, now uint64) Receipt {
	t.Helper()
	return f.state.ApplyTx(tx, now, 1)
}

func TestTransferAndFees(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	before := f.state.Balance(f.alice.Address, native)

	r := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(5_000_000),
	}, 0))
	if !r.Success {
		t.Fatalf("transfer failed: %s %s", r.Code, r.Error)
	}
	if r.Fee != f.state.Params.BaseFee {
		t.Fatalf("fee = %d", r.Fee)
	}
	if got := f.state.Balance(f.bob.Address, native); got != 105_000_000 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := f.state.Balance(f.alice.Address, native); got != before-5_000_000-r.Fee {
		t.Fatalf("alice balance = %d", got)
	}
	if f.state.Burned(native) != r.Fee/2 {
		t.Fatalf("burn = %d", f.state.Burned(native))
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestRefundLifecycle(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	pay := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(5_000_000),
	}, 0))
	if !pay.Success {
		t.Fatalf("payment failed: %+v", pay)
	}
	original, ok, err := f.state.Transfer(pay.Hash)
	if err != nil || !ok {
		t.Fatalf("transfer not indexed: %v %v", ok, err)
	}
	if original.From != f.alice.Address || original.To != f.bob.Address ||
		original.Amount != 5_000_000 || original.RefundOf != "" {
		t.Fatalf("unexpected index entry: %+v", original)
	}

	aliceBefore := f.state.Balance(f.alice.Address, native)
	partial := f.apply(t, f.tx(t, f.bob, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.alice.Address, "amount": uint64(2_000_000), "refund_of": pay.Hash,
	}, 0))
	if !partial.Success {
		t.Fatalf("partial refund failed: %+v", partial)
	}
	if got := f.state.Refunded(pay.Hash); got != 2_000_000 {
		t.Fatalf("refunded = %d", got)
	}
	if got := f.state.Balance(f.alice.Address, native); got != aliceBefore+2_000_000 {
		t.Fatalf("alice balance = %d", got)
	}
	if len(partial.Events) != 2 || partial.Events[1].Type != "refund" ||
		partial.Events[1].Attrs["remaining"] != "3000000" ||
		partial.Events[1].Attrs["refund_of"] != pay.Hash {
		t.Fatalf("refund events = %+v", partial.Events)
	}
	reversal, ok, err := f.state.Transfer(partial.Hash)
	if err != nil || !ok || reversal.RefundOf != pay.Hash {
		t.Fatalf("refund not indexed as a reversal: %+v %v", reversal, err)
	}

	last := f.apply(t, f.tx(t, f.bob, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.alice.Address, "amount": uint64(3_000_000), "refund_of": pay.Hash,
	}, 1))
	if !last.Success {
		t.Fatalf("final refund failed: %+v", last)
	}
	if got := f.state.Refunded(pay.Hash); got != 5_000_000 {
		t.Fatalf("refunded = %d", got)
	}
	root := f.state.Root()
	over := f.apply(t, f.tx(t, f.bob, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.alice.Address, "amount": uint64(1), "refund_of": pay.Hash,
	}, 2))
	if over.Success || over.Code != CodeRefundExceeded {
		t.Fatalf("over-refund accepted: %+v", over)
	}
	if f.state.Root() != root {
		t.Fatal("rejected refund changed state")
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestRefundRejections(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	pay := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(5_000_000),
	}, 0))
	if !pay.Success {
		t.Fatalf("payment failed: %+v", pay)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxRegisterAsset, map[string]any{"asset": map[string]any{
		"id": "usd:devnet", "class": "fiat", "symbol": "USD", "name": "US Dollar",
		"decimals": uint64(2), "policy": "capped", "max_supply": uint64(1_000_000),
		"issuer": f.alice.Address, "registered": uint64(0), "meta": map[string]any{"iso4217": "USD"},
	}}, 1)); !r.Success {
		t.Fatalf("register: %+v", r)
	}

	cases := []struct {
		name   string
		kp     *protocol.KeyPair
		nonce  uint64
		change map[string]any
		hash   string
		code   Code
	}{
		{"unknown hash", f.bob, 0, nil, strings.Repeat("a", 64), CodeRefundNotFound},
		{"malformed hash", f.bob, 0, nil, "deadbeef", CodeInvalidPayload},
		{"payer cannot refund", f.alice, 2, nil, "", CodeUnauthorized},
		{"stranger cannot refund", f.carol, 0, nil, "", CodeUnauthorized},
		{"wrong destination", f.bob, 0, map[string]any{"to": f.carol.Address}, "", CodeRefundMismatch},
		{"wrong asset", f.bob, 0, map[string]any{"asset": "usd:devnet"}, "", CodeRefundMismatch},
	}
	for _, tc := range cases {
		payload := map[string]any{"asset": native, "to": f.alice.Address, "amount": uint64(1)}
		for k, v := range tc.change {
			payload[k] = v
		}
		if tc.hash != "" {
			payload["refund_of"] = tc.hash
		} else {
			payload["refund_of"] = pay.Hash
		}
		r := f.apply(t, f.tx(t, tc.kp, protocol.TxTransfer, payload, tc.nonce))
		if r.Success || r.Code != tc.code {
			t.Fatalf("%s: %+v", tc.name, r)
		}
	}

	// A refund cannot itself be refunded, and intent binding is exclusive.
	first := f.apply(t, f.tx(t, f.bob, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.alice.Address, "amount": uint64(1_000_000), "refund_of": pay.Hash,
	}, 0))
	if !first.Success {
		t.Fatalf("refund failed: %+v", first)
	}
	again := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(1_000_000), "refund_of": first.Hash,
	}, 2))
	if again.Success || again.Code != CodeRefundMismatch {
		t.Fatalf("refund of refund accepted: %+v", again)
	}
}

func TestRefundAndIntentAreExclusive(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	pay := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(5_000_000),
	}, 0))
	if !pay.Success {
		t.Fatalf("payment failed: %+v", pay)
	}
	r := f.apply(t, f.tx(t, f.bob, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.alice.Address, "amount": uint64(1),
		"refund_of": pay.Hash, "intent": strings.Repeat("0", 32),
	}, 0))
	if r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("exclusive fields accepted: %+v", r)
	}
}

func TestInsufficientFundsLeavesStateUnchanged(t *testing.T) {
	f := newFixture(t)
	root := f.state.Root()
	r := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": protocol.NativeAsset(testNet), "to": f.bob.Address, "amount": uint64(999_999_999_999),
	}, 0))
	if r.Success || r.Code != CodeInsufficientFunds {
		t.Fatalf("expected insufficient funds, got %+v", r)
	}
	if f.state.Root() != root {
		t.Fatal("failed tx changed state")
	}
}

func TestNonceAndReplay(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	tx := f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(10),
	}, 0)
	if r := f.apply(t, tx); !r.Success {
		t.Fatalf("first apply: %+v", r)
	}
	if r := f.apply(t, tx); r.Success || r.Code != CodeBadNonce {
		t.Fatalf("replay accepted: %+v", r)
	}
}

func TestExpiredTxRejected(t *testing.T) {
	f := newFixture(t)
	tx := f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": protocol.NativeAsset(testNet), "to": f.bob.Address, "amount": uint64(10),
	}, 0)
	tx.ExpiresAt = 1_700_000_000 // before f.apply's block time
	tx.Signatures = nil
	if err := tx.Sign(f.alice); err != nil {
		t.Fatal(err)
	}
	root := f.state.Root()
	if r := f.apply(t, tx); r.Success || r.Code != CodeExpired {
		t.Fatalf("expired tx accepted: %+v", r)
	}
	if f.state.Root() != root {
		t.Fatal("expired tx changed state")
	}
}

func TestWrongNetworkRejected(t *testing.T) {
	f := newFixture(t)
	tx := f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": protocol.NativeAsset(testNet), "to": f.bob.Address, "amount": uint64(1),
	}, 0)
	tx.Network = "arne:mainnet"
	tx.Chain = "arne-mainnet-1"
	if r := f.apply(t, tx); r.Success || r.Code != CodeWrongNetwork && r.Code != CodeBadSignature {
		t.Fatalf("cross-network tx accepted: %+v", r)
	}
}

func TestEscrowLifecycle(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	r := f.apply(t, f.tx(t, f.alice, protocol.TxCreateEscrow, map[string]any{
		"asset": native, "amount": uint64(2_000_000), "beneficiary": f.bob.Address,
		"arbiter": f.carol.Address, "timeout": uint64(1_800_000_000),
	}, 0))
	if !r.Success {
		t.Fatalf("create escrow: %+v", r)
	}
	var id string
	for _, e := range r.Events {
		if e.Type == "escrow_created" {
			id = e.Attrs["id"]
		}
	}
	if id == "" {
		t.Fatal("no escrow id")
	}
	// Only payer or arbiter may close.
	if rr := f.apply(t, f.tx(t, f.bob, protocol.TxReleaseEscrow, map[string]any{"id": id}, 0)); rr.Success || rr.Code != CodeUnauthorized {
		t.Fatalf("beneficiary closed escrow: %+v", rr)
	}
	if rr := f.apply(t, f.tx(t, f.carol, protocol.TxReleaseEscrow, map[string]any{"id": id}, 0)); !rr.Success {
		t.Fatalf("arbiter release: %+v", rr)
	}
	if rr := f.apply(t, f.tx(t, f.carol, protocol.TxReleaseEscrow, map[string]any{"id": id}, 1)); rr.Success || rr.Code != CodeEscrowClosed {
		t.Fatalf("double release: %+v", rr)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestEscrowTimeout(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	const timeout = uint64(1_700_000_200)
	r := f.apply(t, f.tx(t, f.alice, protocol.TxCreateEscrow, map[string]any{
		"asset": native, "amount": uint64(2_000_000), "beneficiary": f.bob.Address,
		"arbiter": f.carol.Address, "timeout": timeout,
	}, 0))
	if !r.Success {
		t.Fatalf("create escrow: %+v", r)
	}
	var id string
	for _, e := range r.Events {
		if e.Type == "escrow_created" {
			id = e.Attrs["id"]
		}
	}

	// Before expiry the payer cannot pull the funds back and the beneficiary
	// cannot claim them.
	if rr := f.applyAt(t, f.tx(t, f.alice, protocol.TxRefundEscrow, map[string]any{"id": id}, 1), timeout-50); rr.Success || rr.Code != CodeUnauthorized {
		t.Fatalf("early payer refund: %+v", rr)
	}
	if rr := f.applyAt(t, f.tx(t, f.bob, protocol.TxReleaseEscrow, map[string]any{"id": id}, 0), timeout-50); rr.Success || rr.Code != CodeUnauthorized {
		t.Fatalf("early beneficiary release: %+v", rr)
	}

	// After expiry the beneficiary may claim.
	if rr := f.applyAt(t, f.tx(t, f.bob, protocol.TxReleaseEscrow, map[string]any{"id": id}, 0), timeout); !rr.Success {
		t.Fatalf("beneficiary release after timeout: %+v", rr)
	}
	if got := f.state.Balance(f.bob.Address, native); got != 101_999_000 {
		t.Fatalf("beneficiary balance = %d", got)
	}
	if rr := f.applyAt(t, f.tx(t, f.alice, protocol.TxRefundEscrow, map[string]any{"id": id}, 1), timeout); rr.Success || rr.Code != CodeEscrowClosed {
		t.Fatalf("refund after release: %+v", rr)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestAllowance(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxApprove, map[string]any{
		"spender": f.bob.Address, "asset": native, "amount": uint64(1_000_000), "expires_at": uint64(1_700_003_600),
	}, 0)); !r.Success {
		t.Fatalf("approve: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.bob, protocol.TxTransferFrom, map[string]any{
		"from": f.alice.Address, "to": f.carol.Address, "asset": native, "amount": uint64(400_000),
	}, 0)); !r.Success {
		t.Fatalf("transfer_from: %+v", r)
	}
	if got := f.state.Allowance(f.alice.Address, f.bob.Address, native); got != 600_000 {
		t.Fatalf("allowance = %d", got)
	}
	if r := f.apply(t, f.tx(t, f.bob, protocol.TxTransferFrom, map[string]any{
		"from": f.alice.Address, "to": f.carol.Address, "asset": native, "amount": uint64(700_000),
	}, 1)); r.Success || r.Code != CodeAllowanceExceeded {
		t.Fatalf("over-allowance accepted: %+v", r)
	}

	// Revocation removes the remaining allowance immediately.
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxRevokeAllowance, map[string]any{
		"spender": f.bob.Address, "asset": native,
	}, 1)); !r.Success {
		t.Fatalf("revoke: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.bob, protocol.TxTransferFrom, map[string]any{
		"from": f.alice.Address, "to": f.carol.Address, "asset": native, "amount": uint64(100_000),
	}, 1)); r.Success || r.Code != CodeAllowanceExceeded {
		t.Fatalf("post-revocation transfer accepted: %+v", r)
	}
}

func TestAllowanceExpiry(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxApprove, map[string]any{
		"spender": f.bob.Address, "asset": native, "amount": uint64(1_000_000),
		"expires_at": uint64(1_700_000_200),
	}, 0)); !r.Success {
		t.Fatalf("approve: %+v", r)
	}
	spend := func(now uint64) Receipt {
		return f.applyAt(t, f.tx(t, f.bob, protocol.TxTransferFrom, map[string]any{
			"from": f.alice.Address, "to": f.carol.Address, "asset": native, "amount": uint64(100_000),
		}, 0), now)
	}
	if r := spend(1_700_000_250); r.Success || r.Code != CodeAllowanceExceeded {
		t.Fatalf("expired allowance accepted: %+v", r)
	}
	if r := spend(1_700_000_150); !r.Success {
		t.Fatalf("unexpired allowance rejected: %+v", r)
	}
}

func TestAssetIssueMintBurn(t *testing.T) {
	f := newFixture(t)
	assetMap := map[string]any{
		"id": "usd:devnet", "class": "fiat", "symbol": "USD", "name": "US Dollar",
		"decimals": uint64(2), "policy": "capped", "max_supply": uint64(1_000_000_000),
		"issuer": f.alice.Address, "registered": uint64(0), "meta": map[string]any{"iso4217": "USD"},
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxRegisterAsset, map[string]any{"asset": assetMap}, 0)); !r.Success {
		t.Fatalf("register: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxRegisterAsset, map[string]any{"asset": assetMap}, 1)); r.Success || r.Code != CodeAssetExists {
		t.Fatalf("duplicate asset accepted: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxMint, map[string]any{
		"asset": "usd:devnet", "to": f.bob.Address, "amount": uint64(500_000),
	}, 1)); !r.Success {
		t.Fatalf("mint: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.bob, protocol.TxMint, map[string]any{
		"asset": "usd:devnet", "to": f.bob.Address, "amount": uint64(1),
	}, 0)); r.Success || r.Code != CodeUnauthorized {
		t.Fatalf("non-issuer mint: %+v", r)
	}
	// USD is capped, not burnable.
	if r := f.apply(t, f.tx(t, f.bob, protocol.TxBurn, map[string]any{
		"asset": "usd:devnet", "amount": uint64(1),
	}, 0)); r.Success || r.Code != CodeUnauthorized {
		t.Fatalf("burn on capped asset: %+v", r)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestAssetRegistrationValidation(t *testing.T) {
	f := newFixture(t)
	asset := func(id, class string, decimals uint64, meta map[string]any) map[string]any {
		return map[string]any{
			"id": id, "class": class, "symbol": "TZS", "name": "Tanzanian Shilling",
			"decimals": decimals, "policy": "capped", "max_supply": uint64(1_000_000_000),
			"issuer": f.alice.Address, "registered": uint64(0), "meta": meta,
		}
	}
	register := func(a map[string]any, nonce uint64) Receipt {
		return f.apply(t, f.tx(t, f.alice, protocol.TxRegisterAsset, map[string]any{"asset": a}, nonce))
	}

	if r := register(asset("tzs:devnet", "currency", 2, map[string]any{}), 0); r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("unknown class accepted: %+v", r)
	}
	if r := register(asset("tzs:devnet", "fiat", 2, map[string]any{"iso4217": "usd"}), 0); r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("malformed ISO 4217 accepted: %+v", r)
	}
	if r := register(asset("tzs:devnet", "fiat", 2, map[string]any{"iso17442": "short"}), 0); r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("malformed LEI accepted: %+v", r)
	}
	if r := register(asset("tzs:devnet", "stablecoin", 2, map[string]any{"iso4217": "TZS"}), 0); !r.Success {
		t.Fatalf("valid registration: %+v", r)
	}
	// Re-registering the same ID cannot change precision or class.
	if r := register(asset("tzs:devnet", "fiat", 6, map[string]any{"iso4217": "TZS"}), 1); r.Success || r.Code != CodeAssetExists {
		t.Fatalf("duplicate accepted: %+v", r)
	}
	got, ok, err := f.state.Asset("tzs:devnet")
	if err != nil || !ok || got.Decimals != 2 || got.Class != "stablecoin" {
		t.Fatalf("asset mutated: %+v %v %v", got, ok, err)
	}
}

func TestSupplyCapEnforced(t *testing.T) {
	f := newFixture(t)
	assetMap := map[string]any{
		"id": "cap:devnet", "class": "point", "symbol": "CAP", "name": "Capped",
		"decimals": uint64(0), "policy": "capped", "max_supply": uint64(1000),
		"issuer": f.alice.Address, "registered": uint64(0),
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxRegisterAsset, map[string]any{"asset": assetMap}, 0)); !r.Success {
		t.Fatalf("register: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxMint, map[string]any{
		"asset": "cap:devnet", "to": f.alice.Address, "amount": uint64(1000),
	}, 1)); !r.Success {
		t.Fatalf("mint to cap: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxMint, map[string]any{
		"asset": "cap:devnet", "to": f.alice.Address, "amount": uint64(1),
	}, 2)); r.Success || r.Code != CodeSupplyLimit {
		t.Fatalf("mint past cap accepted: %+v", r)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchAtomicity(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	root := f.state.Root()
	r := f.apply(t, f.tx(t, f.alice, protocol.TxBatch, map[string]any{
		"items": []any{
			map[string]any{"asset": native, "to": f.bob.Address, "amount": uint64(10)},
			map[string]any{"asset": native, "to": f.carol.Address, "amount": uint64(999_999_999_999)},
		},
	}, 0))
	if r.Success {
		t.Fatal("failing batch succeeded")
	}
	if f.state.Root() != root {
		t.Fatal("batch failure left partial state")
	}
}

func TestBestEffortBatch(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	r := f.apply(t, f.tx(t, f.alice, protocol.TxBatch, map[string]any{
		"mode": "best_effort",
		"items": []any{
			map[string]any{"asset": native, "to": f.bob.Address, "amount": uint64(100_000_000)},
			map[string]any{"asset": native, "to": f.carol.Address, "amount": uint64(2_000_000_000)},
			map[string]any{"asset": native, "to": f.carol.Address, "amount": uint64(200_000_000)},
		},
	}, 0))
	if !r.Success {
		t.Fatalf("best-effort batch failed: %+v", r)
	}
	if got := f.state.Balance(f.bob.Address, native); got != 200_000_000 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := f.state.Balance(f.carol.Address, native); got != 300_000_000 {
		t.Fatalf("carol balance = %d", got)
	}
	results, failed := 0, 0
	for _, e := range r.Events {
		if e.Type == "batch_item" {
			results++
			if e.Attrs["success"] == "false" {
				failed++
				if e.Attrs["code"] != string(CodeInsufficientFunds) {
					t.Fatalf("failure code = %q", e.Attrs["code"])
				}
			}
		}
	}
	if results != 3 || failed != 1 {
		t.Fatalf("results = %d, failed = %d", results, failed)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchRejectsUnknownMode(t *testing.T) {
	f := newFixture(t)
	r := f.apply(t, f.tx(t, f.alice, protocol.TxBatch, map[string]any{
		"mode":  "sometimes",
		"items": []any{map[string]any{"asset": protocol.NativeAsset(testNet), "to": f.bob.Address, "amount": uint64(1)}},
	}, 0))
	if r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("unknown mode accepted: %+v", r)
	}
}

func TestFeeInForeignAsset(t *testing.T) {
	f := newFixture(t)
	assetMap := map[string]any{
		"id": "usd:devnet", "class": "fiat", "symbol": "USD", "name": "US Dollar",
		"decimals": uint64(2), "policy": "capped", "max_supply": uint64(1_000_000_000),
		"issuer": f.alice.Address, "registered": uint64(0), "meta": map[string]any{},
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxRegisterAsset, map[string]any{"asset": assetMap}, 0)); !r.Success {
		t.Fatalf("register: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxMint, map[string]any{
		"asset": "usd:devnet", "to": f.bob.Address, "amount": uint64(10_000),
	}, 1)); !r.Success {
		t.Fatalf("mint: %+v", r)
	}
	// No route yet: reject.
	tx := f.tx(t, f.bob, protocol.TxTransfer, map[string]any{
		"asset": "usd:devnet", "to": f.carol.Address, "amount": uint64(10),
	}, 0)
	tx.FeeAsset = "usd:devnet"
	// Re-sign with the changed fee asset.
	tx.Signatures = nil
	if err := tx.Sign(f.bob); err != nil {
		t.Fatal(err)
	}
	if r := f.apply(t, tx); r.Success || r.Code != CodeNoFeeRoute {
		t.Fatalf("unrouted fee accepted: %+v", r)
	}
	f.state.SetRoute("usd:devnet", 100_000_000) // 1:1 with ARCE
	if r := f.apply(t, tx); !r.Success {
		t.Fatalf("routed fee: %+v", r)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestFeeSlippageLimit(t *testing.T) {
	f := newFixture(t)
	assetMap := map[string]any{
		"id": "usd:devnet", "class": "fiat", "symbol": "USD", "name": "US Dollar",
		"decimals": uint64(2), "policy": "capped", "max_supply": uint64(1_000_000_000),
		"issuer": f.alice.Address, "registered": uint64(0), "meta": map[string]any{},
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxRegisterAsset, map[string]any{"asset": assetMap}, 0)); !r.Success {
		t.Fatalf("register: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxMint, map[string]any{
		"asset": "usd:devnet", "to": f.bob.Address, "amount": uint64(10_000),
	}, 1)); !r.Success {
		t.Fatalf("mint: %+v", r)
	}
	f.state.SetRoute("usd:devnet", 100_000_000) // 1:1 with ARCE

	pay := func(feeAsset string, cap any) protocol.Tx {
		payload := map[string]any{
			"asset": protocol.NativeAsset(testNet), "to": f.carol.Address, "amount": uint64(10),
		}
		if cap != nil {
			payload["max_arce_fee"] = cap
		}
		acct, _ := f.state.Account(f.bob.Address)
		tx := f.tx(t, f.bob, protocol.TxTransfer, payload, acct.Nonce)
		tx.FeeAsset = feeAsset
		tx.Signatures = nil
		if err := tx.Sign(f.bob); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	if r := f.apply(t, pay("usd:devnet", uint64(999))); r.Success || r.Code != CodeLimitExceeded {
		t.Fatalf("slippage over cap accepted: %+v", r)
	}
	if r := f.apply(t, pay("usd:devnet", uint64(1000))); !r.Success {
		t.Fatalf("slippage at cap rejected: %+v", r)
	}
	if r := f.apply(t, pay(protocol.NativeAsset(testNet), uint64(999))); r.Success || r.Code != CodeLimitExceeded {
		t.Fatalf("native fee over cap accepted: %+v", r)
	}
	if r := f.apply(t, pay(protocol.NativeAsset(testNet), uint64(1000))); !r.Success {
		t.Fatalf("native fee at cap rejected: %+v", r)
	}
	for _, bad := range []any{uint64(0), "1000"} {
		if r := f.apply(t, pay(protocol.NativeAsset(testNet), bad)); r.Success || r.Code != CodeInvalidPayload {
			t.Fatalf("max_arce_fee %v accepted: %+v", bad, r)
		}
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestStateProofs(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	key := "balance/" + f.alice.Address + "/" + native
	p := f.state.Tree().Prove(key)
	if err := p.Verify(f.state.Root()); err != nil {
		t.Fatalf("inclusion proof: %v", err)
	}
	stranger, _, err := protocol.NewKeyPair(testNet)
	if err != nil {
		t.Fatal(err)
	}
	absent := f.state.Tree().Prove("balance/" + stranger.Address + "/" + native)
	if absent.Value != nil {
		t.Fatal("stranger should not exist yet")
	}
	if err := absent.Verify(f.state.Root()); err != nil {
		t.Fatalf("exclusion proof: %v", err)
	}
}

func TestBlockApplyAndDeterminism(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	var txs []protocol.Tx
	for i := uint64(0); i < 5; i++ {
		txs = append(txs, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
			"asset": native, "to": f.bob.Address, "amount": uint64(1000),
		}, i))
	}
	b1, receipts, err := f.state.ApplyBlock(txs, 1_700_000_200, 1, f.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 5 {
		t.Fatalf("receipts = %d", len(receipts))
	}
	for _, r := range receipts {
		if !r.Success {
			t.Fatalf("tx failed: %s", r.Error)
		}
	}
	if len(b1.TxHashes) != 5 || b1.StateRoot == "" {
		t.Fatal("block incomplete")
	}
	h1, _ := b1.Hash()
	if !strings.HasPrefix(h1, "") || len(h1) != 64 {
		t.Fatal("bad block hash")
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderTxsGroupsNonces(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	// Build nonces 0..2 for alice and 0..1 for bob, submitted out of order.
	var txs []protocol.Tx
	txs = append(txs, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{"asset": native, "to": f.bob.Address, "amount": uint64(1)}, 1))
	txs = append(txs, f.tx(t, f.bob, protocol.TxTransfer, map[string]any{"asset": native, "to": f.alice.Address, "amount": uint64(1)}, 0))
	txs = append(txs, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{"asset": native, "to": f.bob.Address, "amount": uint64(1)}, 0))
	ordered := OrderTxs(txs)
	if len(ordered) != 3 {
		t.Fatal("lost transactions")
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Signer == ordered[i-1].Signer && ordered[i].Nonce <= ordered[i-1].Nonce {
			t.Fatalf("nonce order violated at %d", i)
		}
	}
}

func TestEncryptedMemo(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	pub, err := arnememo.PublicKey(f.bob.Private.Seed())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := arnememo.RandomSeal(pub, []byte("invoice 42"))
	if err != nil {
		t.Fatal(err)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(1_000), "memo_enc": sealed,
	}, 0)); !r.Success {
		t.Fatalf("sealed memo transfer: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(1_000),
		"memo": "public", "memo_enc": sealed,
	}, 1)); r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("both memos accepted: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(1_000), "memo_enc": "text",
	}, 1)); r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("non-bytes memo_enc accepted: %+v", r)
	}
	if r := f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(1_000), "memo_enc": []byte("garbage"),
	}, 1)); r.Success || r.Code != CodeInvalidPayload {
		t.Fatalf("garbage memo_enc accepted: %+v", r)
	}
}

func TestIntentPayableOnce(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	id := protocol.IntentID(testNet, f.bob.Address, "invoice-1")

	r := f.apply(t, f.tx(t, f.bob, protocol.TxCreateIntent, map[string]any{
		"reference": "invoice-1", "asset": native, "amount": uint64(5_000_000),
		"expires_at": uint64(1_700_003_000),
	}, 0))
	if !r.Success {
		t.Fatalf("create failed: %s %s", r.Code, r.Error)
	}
	if _, ok := eventOf(r, "intent_created"); !ok {
		t.Fatalf("events = %+v", r.Events)
	}
	in, ok, err := f.state.Intent(id)
	if err != nil || !ok || in.Status != protocol.IntentOpen || in.Payee != f.bob.Address {
		t.Fatalf("intent = %+v ok=%v err=%v", in, ok, err)
	}

	r = f.apply(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(5_000_000), "intent": id,
	}, 0))
	if !r.Success {
		t.Fatalf("pay failed: %s %s", r.Code, r.Error)
	}
	in, _, _ = f.state.Intent(id)
	if in.Status != protocol.IntentPaid || in.Payer != f.alice.Address || in.TxHash != r.Hash {
		t.Fatalf("paid intent = %+v", in)
	}
	bobBefore := f.state.Balance(f.bob.Address, native)

	r = f.apply(t, f.tx(t, f.carol, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(5_000_000), "intent": id,
	}, 0))
	if r.Success || r.Code != CodeIntentPaid {
		t.Fatalf("duplicate = %+v", r)
	}
	if got := f.state.Balance(f.bob.Address, native); got != bobBefore {
		t.Fatalf("duplicate moved value: %d -> %d", bobBefore, got)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestIntentExpiryAndMismatch(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	id := protocol.IntentID(testNet, f.bob.Address, "invoice-2")

	r := f.applyAt(t, f.tx(t, f.bob, protocol.TxCreateIntent, map[string]any{
		"reference": "invoice-2", "asset": native, "amount": uint64(1_000),
		"expires_at": uint64(1_700_000_400),
	}, 0), 1_700_000_100)
	if !r.Success {
		t.Fatalf("create failed: %s %s", r.Code, r.Error)
	}

	r = f.applyAt(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(2_000), "intent": id,
	}, 0), 1_700_000_200)
	if r.Success || r.Code != CodeIntentMismatch {
		t.Fatalf("mismatch = %+v", r)
	}

	r = f.applyAt(t, f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(1_000), "intent": id,
	}, 0), 1_700_000_500)
	if r.Success || r.Code != CodeIntentExpired {
		t.Fatalf("expired = %+v", r)
	}
	in, _, _ := f.state.Intent(id)
	if in.Status != protocol.IntentOpen {
		t.Fatalf("expired intent status = %q", in.Status)
	}
}

func TestIntentReferenceRejected(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	for _, reference := range []string{"", "with space", "slash/name", strings.Repeat("a", 65)} {
		r := f.apply(t, f.tx(t, f.bob, protocol.TxCreateIntent, map[string]any{
			"reference": reference, "asset": native, "amount": uint64(1_000),
		}, 0))
		if r.Success || r.Code != CodeInvalidPayload {
			t.Fatalf("reference %q = %+v", reference, r)
		}
	}
}

func TestIntentMemoMustFitPaymentLink(t *testing.T) {
	f := newFixture(t)
	for _, memo := range []string{"line\nbreak", "nul\x00byte"} {
		r := f.apply(t, f.tx(t, f.bob, protocol.TxCreateIntent, map[string]any{
			"reference": "invoice", "asset": protocol.NativeAsset(testNet), "amount": uint64(1), "memo": memo,
		}, 0))
		if r.Success || r.Code != CodeInvalidPayload {
			t.Fatalf("memo %q: %+v", memo, r)
		}
	}
}

func TestIntentSimulationDoesNotConsumePayment(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	created := f.apply(t, f.tx(t, f.bob, protocol.TxCreateIntent, map[string]any{
		"reference": "preview_invoice", "asset": native, "amount": uint64(1),
	}, 0))
	if !created.Success {
		t.Fatalf("create: %+v", created)
	}
	id := protocol.IntentID(testNet, f.bob.Address, "preview_invoice")
	tx := f.tx(t, f.alice, protocol.TxTransfer, map[string]any{
		"asset": native, "to": f.bob.Address, "amount": uint64(1), "intent": id,
	}, 0)
	before := f.state.Root()
	if r := f.state.SimulateTx(tx, 1_700_000_200, 1); !r.Success {
		t.Fatalf("simulate: %+v", r)
	}
	in, _, _ := f.state.Intent(id)
	if in.Status != protocol.IntentOpen || f.state.Root() != before {
		t.Fatal("simulation consumed intent")
	}
	if r := f.apply(t, tx); !r.Success {
		t.Fatalf("pay after simulation: %+v", r)
	}
}

func eventOf(r Receipt, typ string) (Event, bool) {
	for _, e := range r.Events {
		if e.Type == typ {
			return e, true
		}
	}
	return Event{}, false
}

func TestSubscriptionLifecycleAndLimits(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	start := uint64(1_700_000_100)
	id := protocol.SubscriptionID(testNet, f.alice.Address, f.bob.Address, native, 0)

	r := f.applyAt(t, f.tx(t, f.alice, protocol.TxCreateSub, map[string]any{
		"payee": f.bob.Address, "asset": native, "amount_per_period": uint64(2_000_000),
		"period": uint64(1000), "total_cap": uint64(5_000_000),
	}, 0), start)
	if !r.Success {
		t.Fatalf("create: %s %s", r.Code, r.Error)
	}
	if _, ok := eventOf(r, "subscription_created"); !ok {
		t.Fatalf("events = %+v", r.Events)
	}
	sub, ok, err := f.state.Subscription(id)
	if err != nil || !ok || sub.Status != protocol.SubscriptionActive ||
		sub.AmountPerPeriod != 2_000_000 || sub.TotalCap != 5_000_000 {
		t.Fatalf("subscription = %+v ok=%v err=%v", sub, ok, err)
	}

	charge := func(nonce, amount, at uint64) Receipt {
		return f.applyAt(t, f.tx(t, f.bob, protocol.TxChargeSub, map[string]any{
			"id": id, "amount": amount,
		}, nonce), at)
	}
	bobBefore := f.state.Balance(f.bob.Address, native)
	if r = charge(0, 1_500_000, start+10); !r.Success {
		t.Fatalf("charge: %s %s", r.Code, r.Error)
	}
	if ev, ok := eventOf(r, "subscription_charged"); !ok || ev.Attrs["remaining_total"] != "3500000" {
		t.Fatalf("events = %+v", r.Events)
	}
	// Same period: only 500,000 of the 2,000,000 per-period allowance remains.
	bobAfter := f.state.Balance(f.bob.Address, native)
	if r = charge(1, 1_000_000, start+20); r.Success || r.Code != CodeSubLimit {
		t.Fatalf("over-period = %+v", r)
	}
	if got := f.state.Balance(f.bob.Address, native); got != bobAfter || got <= bobBefore {
		t.Fatalf("failed charge moved value: %d -> %d", bobAfter, got)
	}
	if r = charge(1, 500_000, start+30); !r.Success {
		t.Fatalf("remaining charge: %s %s", r.Code, r.Error)
	}
	if r = charge(2, 1, start+40); r.Success || r.Code != CodeSubLimit {
		t.Fatalf("full-period charge = %+v", r)
	}

	// A new period resets the per-period counter but not the total.
	if r = charge(2, 2_000_000, start+1000); !r.Success {
		t.Fatalf("next period: %s %s", r.Code, r.Error)
	}
	if sub, _, _ = f.state.Subscription(id); sub.ChargedPeriod != 2_000_000 || sub.ChargedTotal != 4_000_000 {
		t.Fatalf("counters = %+v", sub)
	}
	if r = charge(3, 1, start+1010); r.Success || r.Code != CodeSubLimit {
		t.Fatalf("second charge in period = %+v", r)
	}
	// Only 1,000,000 remains under the total cap, regardless of period.
	if r = charge(3, 2_000_000, start+2000); r.Success || r.Code != CodeSubLimit {
		t.Fatalf("over-total = %+v", r)
	}
	if r = charge(3, 1_000_000, start+2010); !r.Success {
		t.Fatalf("final charge: %s %s", r.Code, r.Error)
	}
	if r = charge(4, 1, start+3000); r.Success || r.Code != CodeSubLimit {
		t.Fatalf("cap exhausted = %+v", r)
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionAuthorizationAndExpiry(t *testing.T) {
	f := newFixture(t)
	native := protocol.NativeAsset(testNet)
	start := uint64(1_700_000_100)

	if r := f.applyAt(t, f.tx(t, f.bob, protocol.TxChargeSub, map[string]any{
		"id": strings.Repeat("0", 32), "amount": uint64(1),
	}, 0), start); r.Success || r.Code != CodeSubNotFound {
		t.Fatalf("missing = %+v", r)
	}

	id := protocol.SubscriptionID(testNet, f.alice.Address, f.bob.Address, native, 0)
	r := f.applyAt(t, f.tx(t, f.alice, protocol.TxCreateSub, map[string]any{
		"payee": f.bob.Address, "asset": native, "amount_per_period": uint64(1_000_000),
		"period": uint64(3600), "total_cap": uint64(3_000_000), "expires_at": start + 1000,
	}, 0), start+2)
	if !r.Success {
		t.Fatalf("create: %s %s", r.Code, r.Error)
	}

	if r = f.applyAt(t, f.tx(t, f.alice, protocol.TxChargeSub, map[string]any{
		"id": id, "amount": uint64(1),
	}, 1), start+3); r.Success || r.Code != CodeUnauthorized {
		t.Fatalf("payer charge = %+v", r)
	}
	if r = f.applyAt(t, f.tx(t, f.bob, protocol.TxCancelSub, map[string]any{"id": id}, 0), start+4); r.Success || r.Code != CodeUnauthorized {
		t.Fatalf("payee cancel = %+v", r)
	}
	if r = f.applyAt(t, f.tx(t, f.alice, protocol.TxCancelSub, map[string]any{"id": id}, 1), start+5); !r.Success {
		t.Fatalf("cancel: %s %s", r.Code, r.Error)
	}
	if r = f.applyAt(t, f.tx(t, f.bob, protocol.TxChargeSub, map[string]any{
		"id": id, "amount": uint64(1),
	}, 0), start+6); r.Success || r.Code != CodeSubClosed {
		t.Fatalf("post-cancel charge = %+v", r)
	}
	if r = f.applyAt(t, f.tx(t, f.alice, protocol.TxCancelSub, map[string]any{"id": id}, 2), start+7); r.Success || r.Code != CodeSubClosed {
		t.Fatalf("double cancel = %+v", r)
	}

	id2 := protocol.SubscriptionID(testNet, f.alice.Address, f.bob.Address, native, 2)
	r = f.applyAt(t, f.tx(t, f.alice, protocol.TxCreateSub, map[string]any{
		"payee": f.bob.Address, "asset": native, "amount_per_period": uint64(1_000_000),
		"period": uint64(3600), "total_cap": uint64(3_000_000), "expires_at": start + 200,
	}, 2), start+10)
	if !r.Success {
		t.Fatalf("expiring create: %s %s", r.Code, r.Error)
	}
	if r = f.applyAt(t, f.tx(t, f.bob, protocol.TxChargeSub, map[string]any{
		"id": id2, "amount": uint64(1),
	}, 0), start+50); !r.Success {
		t.Fatalf("pre-expiry charge: %s %s", r.Code, r.Error)
	}
	if r = f.applyAt(t, f.tx(t, f.bob, protocol.TxChargeSub, map[string]any{
		"id": id2, "amount": uint64(1),
	}, 1), start+201); r.Success || r.Code != CodeSubExpired {
		t.Fatalf("post-expiry charge = %+v", r)
	}

	for _, payload := range []map[string]any{
		{"payee": f.alice.Address, "asset": native, "amount_per_period": uint64(1), "period": uint64(3600), "total_cap": uint64(1)},
		{"payee": f.bob.Address, "asset": native, "amount_per_period": uint64(1), "period": uint64(0), "total_cap": uint64(1)},
		{"payee": f.bob.Address, "asset": native, "amount_per_period": uint64(0), "period": uint64(3600), "total_cap": uint64(1)},
	} {
		if r = f.applyAt(t, f.tx(t, f.alice, protocol.TxCreateSub, payload, 3), start+300); r.Success || r.Code != CodeInvalidPayload {
			t.Fatalf("invalid create %+v = %+v", payload, r)
		}
	}
	if err := f.state.CheckAll(); err != nil {
		t.Fatal(err)
	}
}
