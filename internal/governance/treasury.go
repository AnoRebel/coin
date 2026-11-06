package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	// ErrOverspend means the treasury does not hold the requested amount.
	ErrOverspend = errors.New("overspend")
	// ErrBudget means the spend would exceed its category's limit.
	ErrBudget = errors.New("budget")
	// ErrReconcile means a report did not reconcile.
	ErrReconcile = errors.New("reconcile")
)

// TreasuryEntry is one recorded inflow or outflow.
type TreasuryEntry struct {
	ID        string `json:"id"`
	Asset     string `json:"asset"`
	Amount    uint64 `json:"amount"`
	Direction string `json:"direction"`
	Source    string `json:"source,omitempty"`
	Recipient string `json:"recipient,omitempty"`
	Purpose   string `json:"purpose"`
	Category  string `json:"category,omitempty"`
	Proposal  string `json:"proposal,omitempty"`
	At        uint64 `json:"at"`
}

// TreasuryReport reconciles one half-open window [From, To).
type TreasuryReport struct {
	From       uint64            `json:"from"`
	To         uint64            `json:"to"`
	Inflows    map[string]uint64 `json:"inflows"`
	Outflows   map[string]uint64 `json:"outflows"`
	Categories map[string]uint64 `json:"categories"`
	Opening    map[string]uint64 `json:"opening"`
	Closing    map[string]uint64 `json:"closing"`
}

// Treasury holds asset balances, category limits, and the flow journal.
// Limits are cumulative caps per category and asset.
type TreasuryBook struct {
	Balances map[string]uint64            `json:"balances"`
	Limits   map[string]map[string]uint64 `json:"limits,omitempty"`
	Entries  []TreasuryEntry              `json:"entries"`
}

// NewTreasuryBook returns an empty treasury.
func NewTreasuryBook() *TreasuryBook {
	return &TreasuryBook{Balances: map[string]uint64{}, Limits: map[string]map[string]uint64{}}
}

// SetBudget gives a category a cumulative limit in one asset.
func (t *TreasuryBook) SetBudget(category, asset string, limit uint64) error {
	if category == "" || asset == "" {
		return fmt.Errorf("%w: category and asset are required", ErrInvalid)
	}
	if t.Limits == nil {
		t.Limits = map[string]map[string]uint64{}
	}
	if t.Limits[category] == nil {
		t.Limits[category] = map[string]uint64{}
	}
	t.Limits[category][asset] = limit
	return nil
}

// Deposit records an inflow. The treasury is the recipient by definition.
func (t *TreasuryBook) Deposit(asset string, amount uint64, source, purpose string, at uint64) (TreasuryEntry, error) {
	if asset == "" || amount == 0 || strings.TrimSpace(source) == "" ||
		strings.TrimSpace(purpose) == "" {
		return TreasuryEntry{}, fmt.Errorf("%w: asset, amount, source and purpose are required", ErrInvalid)
	}
	e := TreasuryEntry{
		ID:    TreasuryEntryID("", "in", asset, amount, at, len(t.Entries)),
		Asset: asset, Amount: amount, Direction: "in", Source: source,
		Purpose: purpose, At: at,
	}
	if t.Balances == nil {
		t.Balances = map[string]uint64{}
	}
	t.Balances[asset] += amount
	t.Entries = append(t.Entries, e)
	return e, nil
}

// Spend pays a recipient from one asset. It refuses a category without a
// limit, a spend above that limit, and a spend above the balance; nothing is
// recorded when any check fails.
func (t *TreasuryBook) Spend(proposal, asset string, amount uint64, recipient, purpose, category string, at uint64) (TreasuryEntry, error) {
	if proposal == "" || asset == "" || amount == 0 ||
		strings.TrimSpace(recipient) == "" || strings.TrimSpace(purpose) == "" ||
		strings.TrimSpace(category) == "" {
		return TreasuryEntry{}, fmt.Errorf("%w: proposal, asset, amount, recipient, purpose and category are required", ErrInvalid)
	}
	limit, ok := t.Limits[category][asset]
	if !ok {
		return TreasuryEntry{}, fmt.Errorf("%w: category %s has no limit in %s", ErrBudget, category, asset)
	}
	var spent uint64
	for _, e := range t.Entries {
		if e.Direction == "out" && e.Category == category && e.Asset == asset {
			spent += e.Amount
		}
	}
	if spent+amount > limit {
		return TreasuryEntry{}, fmt.Errorf("%w: %s would spend %d of %d in %s", ErrBudget, category, spent+amount, limit, asset)
	}
	if t.Balances[asset] < amount {
		return TreasuryEntry{}, fmt.Errorf("%w: %d available, %d requested", ErrOverspend, t.Balances[asset], amount)
	}
	e := TreasuryEntry{
		ID:    TreasuryEntryID(proposal, "out", asset, amount, at, len(t.Entries)),
		Asset: asset, Amount: amount, Direction: "out", Recipient: recipient,
		Purpose: purpose, Category: category, Proposal: proposal, At: at,
	}
	t.Balances[asset] -= amount
	t.Entries = append(t.Entries, e)
	return e, nil
}

// TreasuryEntryID is the first 16 bytes of
// SHA-256("arne-treasury-v1" || 0 || proposal || 0 || direction || 0 || asset ||
// 0 || amount || 0 || at || 0 || sequence), hex encoded.
func TreasuryEntryID(proposal, direction, asset string, amount, at uint64, sequence int) string {
	h := sha256.New()
	for _, part := range []string{
		"arne-treasury-v1", proposal, direction, asset,
		strconv.FormatUint(amount, 10), strconv.FormatUint(at, 10),
		strconv.Itoa(sequence),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Report reconciles inflows, outflows and the closing balance for [from, to).
func (t *TreasuryBook) Report(from, to uint64) (TreasuryReport, error) {
	if to < from {
		return TreasuryReport{}, fmt.Errorf("%w: to is before from", ErrInvalid)
	}
	r := TreasuryReport{
		From: from, To: to,
		Inflows: map[string]uint64{}, Outflows: map[string]uint64{},
		Categories: map[string]uint64{},
		Opening:    cloneBalances(t.Balances), Closing: cloneBalances(t.Balances),
	}
	for i := len(t.Entries) - 1; i >= 0; i-- {
		e := t.Entries[i]
		if e.At >= from {
			if err := reverse(r.Opening, e); err != nil {
				return TreasuryReport{}, err
			}
		}
		if e.At >= to {
			if err := reverse(r.Closing, e); err != nil {
				return TreasuryReport{}, err
			}
		}
	}
	for _, e := range t.Entries {
		if e.At >= from && e.At < to {
			if e.Direction == "in" {
				r.Inflows[e.Asset] += e.Amount
			} else {
				r.Outflows[e.Asset] += e.Amount
				r.Categories[e.Category] += e.Amount
			}
		}
	}
	for asset, closing := range r.Closing {
		want := r.Opening[asset] + r.Inflows[asset] - r.Outflows[asset]
		if want != closing {
			return TreasuryReport{}, fmt.Errorf("%w: %s opening %d + in %d - out %d != closing %d",
				ErrReconcile, asset, r.Opening[asset], r.Inflows[asset], r.Outflows[asset], closing)
		}
	}
	return r, nil
}

func cloneBalances(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for asset, amount := range in {
		out[asset] = amount
	}
	return out
}

func reverse(balances map[string]uint64, e TreasuryEntry) error {
	if e.Direction == "in" {
		if balances[e.Asset] < e.Amount {
			return fmt.Errorf("%w: %s balance below recorded inflow", ErrReconcile, e.Asset)
		}
		balances[e.Asset] -= e.Amount
		return nil
	}
	balances[e.Asset] += e.Amount
	return nil
}

// Register adds the treasury_spend action. Now supplies the block time.
func (t *TreasuryBook) Register(r *Registry, now func() uint64) error {
	if now == nil {
		now = func() uint64 { return 0 }
	}
	return r.Register("treasury_spend", func(action Action, p *Proposal) (func(), error) {
		if p.Kind != Treasury {
			return nil, fmt.Errorf("%w: treasury_spend requires a treasury proposal", ErrInvalid)
		}
		asset, _ := action.Params["asset"].(string)
		recipient, _ := action.Params["recipient"].(string)
		purpose, _ := action.Params["purpose"].(string)
		category, _ := action.Params["category"].(string)
		amount, err := uintParam(action.Params, "amount")
		if err != nil {
			return nil, err
		}
		entry, err := t.Spend(p.ID, asset, amount, recipient, purpose, category, now())
		if err != nil {
			return nil, err
		}
		return func() {
			t.Entries = t.Entries[:len(t.Entries)-1]
			t.Balances[entry.Asset] += entry.Amount
		}, nil
	})
}

func uintParam(params map[string]any, name string) (uint64, error) {
	value, ok := params[name]
	if !ok {
		return 0, fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	switch v := value.(type) {
	case uint64:
		return v, nil
	case uint32:
		return uint64(v), nil
	case int:
		if v < 0 {
			break
		}
		return uint64(v), nil
	case int64:
		if v < 0 {
			break
		}
		return uint64(v), nil
	case float64:
		if v < 0 || v != float64(uint64(v)) {
			break
		}
		return uint64(v), nil
	case json.Number:
		if n, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%w: %s must be a non-negative integer", ErrInvalid, name)
}
