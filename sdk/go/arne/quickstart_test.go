package arne_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/api"
	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
	arne "github.com/anorebel/anorebelcoin/sdk/go/arne"
)

const testNetwork = "arne:devnet"

// TestQuickstart exercises the documented SDK flow against a real devnet: fund
// a wallet, pay, escrow, invoice, subscribe, and read each result back.
func TestQuickstart(t *testing.T) {
	funder, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	faucet, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	genesis := node.DevnetGenesis(testNetwork, uint64(time.Now().Unix()), funder, faucet)
	n, err := node.New(node.Config{
		Network: testNetwork, Chain: genesis.Chain, Params: ledger.DefaultParams(),
		Proposer: funder.Address, Faucet: faucet, Devnet: true,
	}, genesis, storage.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(n, api.Config{Devnet: true}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := arne.New(server.URL)

	// Produce advances the chain; a live network does this on its own.
	height := uint64(time.Now().Unix())
	produce := func() {
		t.Helper()
		if _, err := n.ProduceBlock(height); err != nil {
			t.Fatal(err)
		}
		height++
	}

	payer, _, err := arne.NewWallet(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	payee, _, err := arne.NewWallet(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	native := arne.NativeAsset(testNetwork)

	hash, err := client.Faucet(ctx, payer.Address)
	if err != nil {
		t.Fatal(err)
	}
	produce()
	receipt, err := client.WaitReceipt(ctx, hash)
	if err != nil || !receipt.Success {
		t.Fatalf("faucet receipt: %+v %v", receipt, err)
	}

	payment := arne.Payment{To: payee.Address, Asset: native, Amount: 1_250_000_000}
	hash, err = payer.Send(ctx, client, arne.TxTransfer, payment.Payload())
	if err != nil {
		t.Fatal(err)
	}
	produce()
	if receipt, err = client.WaitReceipt(ctx, hash); err != nil || !receipt.Success {
		t.Fatalf("payment receipt: %+v %v", receipt, err)
	}
	balance, err := client.Account(ctx, payee.Address)
	if err != nil || balance.Balances[native] != 1_250_000_000 {
		t.Fatalf("payee balance: %+v %v", balance, err)
	}

	now := uint64(time.Now().Unix())
	hash, err = payer.Send(ctx, client, arne.TxCreateEscrow, arne.EscrowCreate{
		Beneficiary: payee.Address, Arbiter: faucet.Address, Asset: native,
		Amount: 1_000, Timeout: now + 3600,
	}.Payload())
	if err != nil {
		t.Fatal(err)
	}
	produce()
	escrowID := eventAttr(t, client, ctx, hash, "escrow_created", "id")
	escrow, err := client.Escrow(ctx, escrowID)
	if err != nil || escrow.Status != protocol.EscrowOpen || escrow.Amount != 1_000 {
		t.Fatalf("escrow: %+v %v", escrow, err)
	}
	if _, err := payer.Send(ctx, client, arne.TxReleaseEscrow, arne.EscrowClose{ID: escrowID}.Payload()); err != nil {
		t.Fatal(err)
	}
	produce()
	if escrow, err = client.Escrow(ctx, escrowID); err != nil || escrow.Status != protocol.EscrowReleased {
		t.Fatalf("released escrow: %+v %v", escrow, err)
	}

	hash, err = payee.Send(ctx, client, arne.TxCreateIntent, arne.IntentCreate{
		Reference: "invoice-1001", Asset: native, Amount: 500,
	}.Payload())
	if err != nil {
		t.Fatal(err)
	}
	produce()
	intentID := eventAttr(t, client, ctx, hash, "intent_created", "id")
	if _, err := payer.Send(ctx, client, arne.TxTransfer, arne.Payment{
		To: payee.Address, Asset: native, Amount: 500, Intent: intentID,
	}.Payload()); err != nil {
		t.Fatal(err)
	}
	produce()
	if intent, err := client.Intent(ctx, intentID); err != nil || intent.Status != protocol.IntentPaid {
		t.Fatalf("intent: %+v %v", intent, err)
	}

	hash, err = payer.Send(ctx, client, arne.TxCreateSub, arne.SubscriptionCreate{
		Payee: payee.Address, Asset: native, AmountPerPeriod: 100, Period: 30, TotalCap: 1_000,
	}.Payload())
	if err != nil {
		t.Fatal(err)
	}
	produce()
	subID := eventAttr(t, client, ctx, hash, "subscription_created", "id")
	if _, err := payee.Send(ctx, client, arne.TxChargeSub, arne.SubscriptionCharge{
		ID: subID, Amount: 100,
	}.Payload()); err != nil {
		t.Fatal(err)
	}
	produce()
	if sub, err := client.Subscription(ctx, subID); err != nil || sub.ChargedTotal != 100 {
		t.Fatalf("subscription: %+v %v", sub, err)
	}

	if _, err := payer.Send(ctx, client, arne.TxApprove, arne.Allowance{
		Spender: payee.Address, Asset: native, Amount: 700,
	}.Payload()); err != nil {
		t.Fatal(err)
	}
	produce()
	allowance, err := client.AllowanceInfo(ctx, payer.Address, payee.Address, native)
	if err != nil || allowance.Remaining != 700 {
		t.Fatalf("allowance: %+v %v", allowance, err)
	}
	if _, err := payee.Send(ctx, client, arne.TxTransferFrom, arne.AllowanceTransfer{
		From: payer.Address, To: payee.Address, Asset: native, Amount: 200,
	}.Payload()); err != nil {
		t.Fatal(err)
	}
	produce()
	if allowance, err = client.AllowanceInfo(ctx, payer.Address, payee.Address, native); err != nil || allowance.Remaining != 500 {
		t.Fatalf("spent allowance: %+v %v", allowance, err)
	}

	if _, err := client.Escrow(ctx, "esc_missing"); !arne.IsCode(err, "ESCROW_NOT_FOUND") {
		t.Fatalf("typed error: %v", err)
	} else if apiErr, ok := arne.AsError(err); !ok || apiErr.Status != 404 {
		t.Fatalf("error status: %+v", apiErr)
	}

	blocks, err := client.Blocks(ctx, 2, 0)
	if err != nil || len(blocks.Items) != 2 {
		t.Fatalf("blocks: %+v %v", blocks, err)
	}
}

func eventAttr(t *testing.T, client *arne.Client, ctx context.Context, hash, event, attr string) string {
	t.Helper()
	receipt, err := client.WaitReceipt(ctx, hash)
	if err != nil || !receipt.Success {
		t.Fatalf("receipt %s: %+v %v", hash, receipt, err)
	}
	for _, e := range receipt.Events {
		if e.Type == event {
			if value := e.Attrs[attr]; value != "" {
				return value
			}
		}
	}
	t.Fatalf("receipt %s has no %s.%s", hash, event, attr)
	return ""
}
