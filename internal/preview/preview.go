// Package preview builds the human-readable confirmation shown before a wallet
// signs, including warnings the recipient cannot suppress.
package preview

import (
	"fmt"
	"strings"
)

// Input is the resolved state a wallet has before signing.
type Input struct {
	Network    string
	Signer     string
	To         string
	Asset      string
	Symbol     string
	Decimals   uint8
	Amount     uint64
	Fee        uint64
	Memo       string
	PublicMemo bool
	AssetKnown bool
	Known      []string // trusted contacts, used for lookalike detection
	Simulated  bool
	Code       string // ledger failure code from simulation, if any
}

// Preview is safe to display and serialize.
type Preview struct {
	Network  string   `json:"network"`
	Signer   string   `json:"signer"`
	To       string   `json:"to"`
	Asset    string   `json:"asset"`
	Amount   string   `json:"amount"`
	Fee      string   `json:"fee"`
	Memo     string   `json:"memo,omitempty"`
	Warnings []string `json:"warnings"`
	OK       bool     `json:"ok"`
}

// Build computes the preview and every warning.
func Build(in Input) Preview {
	p := Preview{
		Network: in.Network, Signer: in.Signer, To: in.To, Asset: in.Asset,
		Amount: format(in.Amount, in.Decimals), Fee: format(in.Fee, 8), Memo: in.Memo,
		Warnings: []string{},
	}
	if !in.AssetKnown || in.Symbol == "" {
		p.Warnings = append(p.Warnings, fmt.Sprintf("asset %s is not registered on %s", in.Asset, in.Network))
	} else {
		p.Amount = format(in.Amount, in.Decimals) + " " + in.Symbol
	}
	p.Fee += " ARCE"
	if strings.EqualFold(in.To, in.Signer) {
		p.Warnings = append(p.Warnings, "recipient is your own address")
	}
	if known := lookalike(in.To, in.Known); known != "" {
		p.Warnings = append(p.Warnings,
			fmt.Sprintf("recipient resembles known address %s; verify the full address", known))
	}
	if in.PublicMemo && in.Memo != "" {
		p.Warnings = append(p.Warnings, "memo is public on chain; use a sealed memo for sensitive text")
	}
	if in.Simulated && in.Code != "" {
		p.Warnings = append(p.Warnings, "transaction would fail: "+in.Code)
	}
	p.OK = in.AssetKnown && in.Symbol != "" && in.Code == ""
	return p
}

// Summary renders the preview as plain text, warnings last.
func (p Preview) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "network: %s\nfrom: %s\nto: %s\nasset: %s\namount: %s\nfee: %s",
		p.Network, p.Signer, p.To, p.Asset, p.Amount, p.Fee)
	if p.Memo != "" {
		fmt.Fprintf(&b, "\nmemo: %q", p.Memo)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s", w)
	}
	return b.String()
}

// lookalike returns a known address that shares a long prefix and suffix with
// addr but is not addr itself.
func lookalike(addr string, known []string) string {
	a := strings.ToLower(addr)
	for _, k := range known {
		b := strings.ToLower(k)
		if b == "" || b == a {
			continue
		}
		prefix := 0
		for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
			prefix++
		}
		suffix := 0
		for suffix < len(a) && suffix < len(b) && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
			suffix++
		}
		if prefix >= 6 && suffix >= 6 {
			return k
		}
	}
	return ""
}

func format(units uint64, decimals uint8) string {
	if decimals == 0 {
		return fmt.Sprint(units)
	}
	scale := uint64(1)
	for i := uint8(0); i < decimals; i++ {
		scale *= 10
	}
	whole, frac := units/scale, units%scale
	digits := fmt.Sprintf("%0*d", int(decimals), frac)
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return fmt.Sprint(whole)
	}
	return fmt.Sprintf("%d.%s", whole, digits)
}
