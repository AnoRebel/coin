// Package paymenturi parses and builds arce:/arne: payment URIs. Parsing is
// strict: unknown or duplicate fields, a mismatched address, and malformed
// amounts are rejected before a wallet shows anything to the user.
package paymenturi

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

var (
	ErrURI    = errors.New("payment uri")
	reAmount  = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,18})?$`)
	reScheme  = regexp.MustCompile(`^[a-z]+$`)
	reIntent  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	execHints = []string{"<script", "<iframe", "javascript:", "data:text/html", "onerror=", "<svg"}
)

// Payment is a validated payment URI.
type Payment struct {
	Scheme   string
	Network  string
	To       string
	Asset    string
	Amount   string
	Memo     string
	Intent   string
	Expires  uint64
	Warnings []string
}

// Parse validates raw at the current time.
func Parse(raw string) (Payment, error) { return ParseAt(raw, uint64(time.Now().Unix())) }

// ParseAt validates raw with an explicit clock, for tests and previews.
func ParseAt(raw string, now uint64) (Payment, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "#") {
		return Payment{}, fmt.Errorf("%w: empty or fragment", ErrURI)
	}
	idx := strings.IndexByte(raw, ':')
	if idx <= 0 {
		return Payment{}, fmt.Errorf("%w: scheme", ErrURI)
	}
	scheme := raw[:idx]
	if !reScheme.MatchString(scheme) {
		return Payment{}, fmt.Errorf("%w: scheme %q", ErrURI, scheme)
	}
	if scheme != "arce" && scheme != "arne" {
		return Payment{}, fmt.Errorf("%w: unsupported scheme %q", ErrURI, scheme)
	}
	rest := raw[idx+1:]
	if strings.HasPrefix(rest, "//") {
		return Payment{}, fmt.Errorf("%w: authority is not allowed", ErrURI)
	}
	to, query, _ := strings.Cut(rest, "?")
	if to == "" {
		return Payment{}, fmt.Errorf("%w: recipient required", ErrURI)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return Payment{}, fmt.Errorf("%w: query", ErrURI)
	}
	allowed := map[string]bool{"network": true, "asset": true, "amount": true, "memo": true, "expires": true, "intent": true}
	for key, list := range values {
		if !allowed[key] {
			return Payment{}, fmt.Errorf("%w: unknown field %q", ErrURI, key)
		}
		if len(list) != 1 {
			return Payment{}, fmt.Errorf("%w: duplicate field %q", ErrURI, key)
		}
	}
	p := Payment{
		Scheme: scheme, Network: values.Get("network"), To: to,
		Asset: values.Get("asset"), Amount: values.Get("amount"), Memo: values.Get("memo"),
		Intent: values.Get("intent"),
	}
	if _, present := values["intent"]; present && !reIntent.MatchString(p.Intent) {
		return Payment{}, fmt.Errorf("%w: intent", ErrURI)
	}
	if p.Network == "" {
		return Payment{}, fmt.Errorf("%w: network required", ErrURI)
	}
	if _, err := protocol.NetworkClass(p.Network); err != nil {
		return Payment{}, fmt.Errorf("%w: %v", ErrURI, err)
	}
	if err := protocol.ValidateAddress(p.To, p.Network); err != nil {
		return Payment{}, fmt.Errorf("%w: %v", ErrURI, err)
	}
	if !protocol.ValidAssetID(p.Asset) {
		return Payment{}, fmt.Errorf("%w: asset", ErrURI)
	}
	if !reAmount.MatchString(p.Amount) || !positive(p.Amount) {
		return Payment{}, fmt.Errorf("%w: amount", ErrURI)
	}
	if p.Memo != "" {
		if len(p.Memo) > 256 || !utf8.ValidString(p.Memo) {
			return Payment{}, fmt.Errorf("%w: memo", ErrURI)
		}
		if strings.ContainsAny(p.Memo, "\x00\r\n") {
			return Payment{}, fmt.Errorf("%w: memo control characters", ErrURI)
		}
		if hint := executableHint(p.Memo); hint != "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("memo contains markup (%s); it is shown as inert text", hint))
		}
	}
	if raw, ok := values["expires"]; ok {
		exp := raw[0]
		n, err := strconv.ParseUint(exp, 10, 64)
		if err != nil || n == 0 {
			return Payment{}, fmt.Errorf("%w: expires", ErrURI)
		}
		if n <= now {
			return Payment{}, fmt.Errorf("%w: expired at %d", ErrURI, n)
		}
		p.Expires = n
	}
	return p, nil
}

func positive(amount string) bool {
	for _, r := range amount {
		if r >= '1' && r <= '9' {
			return true
		}
	}
	return false
}

func executableHint(memo string) string {
	lower := strings.ToLower(memo)
	for _, hint := range execHints {
		if strings.Contains(lower, hint) {
			return hint
		}
	}
	return ""
}

// CheckNetwork rejects an explicit network that conflicts with the URI.
func (p Payment) CheckNetwork(network string) error {
	if network != "" && network != p.Network {
		return fmt.Errorf("%w: uri network %s conflicts with %s", ErrURI, p.Network, network)
	}
	return nil
}

// CheckRecipient rejects an explicit recipient that conflicts with the URI.
func (p Payment) CheckRecipient(to string) error {
	if to != "" && to != p.To {
		return fmt.Errorf("%w: uri recipient %s conflicts with %s", ErrURI, p.To, to)
	}
	return nil
}

// CheckAmount rejects an explicit amount that conflicts with the URI.
func (p Payment) CheckAmount(amount string) error {
	if amount != "" && amount != p.Amount {
		return fmt.Errorf("%w: uri amount %s conflicts with %s", ErrURI, p.Amount, amount)
	}
	return nil
}

// CheckIntent rejects an explicit intent that conflicts with the URI.
func (p Payment) CheckIntent(intent string) error {
	if intent != "" && intent != p.Intent {
		return fmt.Errorf("%w: uri intent %s conflicts with %s", ErrURI, p.Intent, intent)
	}
	return nil
}

// String renders a canonical URI with fields in deterministic order.
func (p Payment) String() string {
	values := url.Values{}
	for key, value := range map[string]string{
		"network": p.Network, "asset": p.Asset, "amount": p.Amount,
		"memo": p.Memo, "expires": strconv.FormatUint(p.Expires, 10),
		"intent": p.Intent,
	} {
		if value != "" && value != "0" {
			values.Set(key, value)
		}
	}
	return p.Scheme + ":" + p.To + "?" + values.Encode()
}

// Summary is the human-readable confirmation shown before signing.
func (p Payment) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "network: %s\nrecipient: %s\nasset: %s\namount: %s\n", p.Network, p.To, p.Asset, p.Amount)
	if p.Intent != "" {
		fmt.Fprintf(&b, "intent: %s\n", p.Intent)
	}
	if p.Memo != "" {
		fmt.Fprintf(&b, "memo: %q\n", p.Memo)
	}
	if p.Expires != 0 {
		fmt.Fprintf(&b, "expires: %d (%s)\n", p.Expires, time.Unix(int64(p.Expires), 0).UTC().Format(time.RFC3339))
	}
	for _, warning := range p.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", warning)
	}
	return strings.TrimRight(b.String(), "\n")
}
