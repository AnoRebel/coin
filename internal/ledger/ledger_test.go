package ledger

import (
	"strings"
	"testing"

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
