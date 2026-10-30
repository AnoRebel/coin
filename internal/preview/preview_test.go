package preview

import "testing"

func base() Input {
	return Input{
		Network: "arne:devnet", Signer: "arce-devnet1sender", To: "arce-devnet1recipient",
		Asset: "arce:devnet", Symbol: "ARCE", Decimals: 8, Amount: 1_250_000_000, Fee: 1_000,
		AssetKnown: true,
	}
}

func TestCleanPreview(t *testing.T) {
	p := Build(base())
	if !p.OK || len(p.Warnings) != 0 {
		t.Fatalf("preview = %+v", p)
	}
	if p.Amount != "12.5 ARCE" || p.Fee != "0.00001 ARCE" {
		t.Fatalf("amount/fee = %q / %q", p.Amount, p.Fee)
	}
}

func TestUnknownAsset(t *testing.T) {
	in := base()
	in.AssetKnown, in.Symbol = false, ""
	p := Build(in)
	if p.OK || len(p.Warnings) != 1 {
		t.Fatalf("preview = %+v", p)
	}
}

func TestWarnings(t *testing.T) {
	in := base()
	in.To = in.Signer
	if p := Build(in); len(p.Warnings) != 1 {
		t.Fatalf("self transfer warnings = %v", p.Warnings)
	}

	in = base()
	in.Known = []string{"arce-devnet1AAAAAAxyzABC"}
	in.To = "arce-devnet1BBBBBBxyzABC"
	if p := Build(in); len(p.Warnings) != 1 {
		t.Fatalf("lookalike warnings = %v", p.Warnings)
	}

	in = base()
	in.Memo, in.PublicMemo = "invoice 42", true
	if p := Build(in); len(p.Warnings) != 1 {
		t.Fatalf("public memo warnings = %v", p.Warnings)
	}

	in = base()
	in.Simulated, in.Code = true, "INSUFFICIENT_FUNDS"
	p := Build(in)
	if p.OK || len(p.Warnings) != 1 {
		t.Fatalf("failed simulation preview = %+v", p)
	}
}

func TestLookalikeIgnoresExactContact(t *testing.T) {
	in := base()
	in.Known = []string{in.To}
	if p := Build(in); len(p.Warnings) != 0 {
		t.Fatalf("known contact warned: %v", p.Warnings)
	}
}
