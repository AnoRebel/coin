// Package iso20022 maps ARNE transfers to and from ISO 20022 messages:
// pacs.008 (customer credit transfer), pain.001 (payment initiation), and
// camt.053 (bank statement).
//
// The mapping is deliberately strict: ISO 20022 text limits, currency and BIC
// shapes, positive decimal amounts, and RFC 3339 timestamps are checked before
// a message is built or accepted. Amounts stay decimal strings; no float ever
// touches a payment.
package iso20022

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Pinned schema versions. Bump these only with a vector update.
const (
	Pacs008Namespace = "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.13"
	Pain001Namespace = "urn:iso:std:iso:20022:tech:xsd:pain.001.001.12"
	Camt053Namespace = "urn:iso:std:iso:20022:tech:xsd:camt.053.001.13"
)

var (
	ErrInvalid     = errors.New("iso20022: invalid")
	ErrMessageType = errors.New("iso20022: unexpected message type")
)

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	bicPattern      = regexp.MustCompile(`^[A-Z0-9]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$`)
	accountPattern  = regexp.MustCompile(`^[A-Z0-9]{5,34}$`)
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	amountPattern   = regexp.MustCompile(`^[0-9]{1,13}(\.[0-9]{1,5})?$`)
	datePattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// Party is a debtor or creditor.
type Party struct {
	Name    string `json:"name"`
	Account string `json:"account"`
	Agent   string `json:"agent,omitempty"`
	Country string `json:"country,omitempty"`
}

// Amount is a decimal string with an ISO 4217 currency.
type Amount struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

// Transfer is the common mapping for pacs.008 and pain.001.
type Transfer struct {
	MessageID  string `json:"message_id"`
	EndToEndID string `json:"end_to_end_id"`
	CreatedAt  string `json:"created_at"`
	Amount     Amount `json:"amount"`
	Debtor     Party  `json:"debtor"`
	Creditor   Party  `json:"creditor"`
	Remittance string `json:"remittance,omitempty"`
}

// Entry is one camt.053 statement line.
type Entry struct {
	Amount     Amount `json:"amount"`
	Direction  string `json:"direction"`
	Status     string `json:"status"`
	BookingDay string `json:"booking_date"`
	EndToEndID string `json:"end_to_end_id,omitempty"`
	DebtorName string `json:"debtor_name,omitempty"`
	Creditor   string `json:"creditor_name,omitempty"`
}

// Balance is one camt.053 opening/closing balance.
type Balance struct {
	Type      string `json:"type"`
	Amount    Amount `json:"amount"`
	Direction string `json:"direction"`
}

// Statement is the camt.053 mapping.
type Statement struct {
	MessageID string    `json:"message_id"`
	CreatedAt string    `json:"created_at"`
	Account   string    `json:"account"`
	Currency  string    `json:"currency"`
	Balances  []Balance `json:"balances"`
	Entries   []Entry   `json:"entries"`
}

// Validate checks every field the mapping depends on.
func (t Transfer) Validate() error {
	if err := text("message_id", t.MessageID, 1, 35); err != nil {
		return err
	}
	if err := text("end_to_end_id", t.EndToEndID, 1, 35); err != nil {
		return err
	}
	if err := timestamp("created_at", t.CreatedAt); err != nil {
		return err
	}
	if err := validateAmount(t.Amount); err != nil {
		return err
	}
	if err := validateParty("debtor", t.Debtor, true); err != nil {
		return err
	}
	if err := validateParty("creditor", t.Creditor, true); err != nil {
		return err
	}
	if t.Remittance != "" {
		return text("remittance", t.Remittance, 1, 140)
	}
	return nil
}

// Validate checks the statement and its entries.
func (s Statement) Validate() error {
	if err := text("message_id", s.MessageID, 1, 35); err != nil {
		return err
	}
	if err := timestamp("created_at", s.CreatedAt); err != nil {
		return err
	}
	if err := account("account", s.Account); err != nil {
		return err
	}
	if err := currency("currency", s.Currency); err != nil {
		return err
	}
	for i, balance := range s.Balances {
		if balance.Type != "OPBD" && balance.Type != "CLBD" && balance.Type != "ITBD" {
			return fmt.Errorf("%w: balance %d type must be OPBD, CLBD, or ITBD", ErrInvalid, i)
		}
		if err := validateAmount(balance.Amount); err != nil {
			return fmt.Errorf("balance %d: %w", i, err)
		}
		if balance.Amount.Currency != s.Currency {
			return fmt.Errorf("%w: balance %d currency must match the account", ErrInvalid, i)
		}
		if balance.Direction != "CRDT" && balance.Direction != "DBIT" {
			return fmt.Errorf("%w: balance %d direction must be CRDT or DBIT", ErrInvalid, i)
		}
	}
	if len(s.Entries) == 0 {
		return fmt.Errorf("%w: statement needs an entry", ErrInvalid)
	}
	for i, entry := range s.Entries {
		if err := validateAmount(entry.Amount); err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
		if entry.Amount.Currency != s.Currency {
			return fmt.Errorf("%w: entry %d currency must match the account", ErrInvalid, i)
		}
		if entry.Direction != "CRDT" && entry.Direction != "DBIT" {
			return fmt.Errorf("%w: entry %d direction must be CRDT or DBIT", ErrInvalid, i)
		}
		if entry.Status != "BOOK" && entry.Status != "PDNG" {
			return fmt.Errorf("%w: entry %d status must be BOOK or PDNG", ErrInvalid, i)
		}
		if !datePattern.MatchString(entry.BookingDay) {
			return fmt.Errorf("%w: entry %d booking_date must be YYYY-MM-DD", ErrInvalid, i)
		}
		if _, err := time.Parse("2006-01-02", entry.BookingDay); err != nil {
			return fmt.Errorf("%w: entry %d booking_date is not a date", ErrInvalid, i)
		}
		if entry.EndToEndID != "" {
			if err := text("end_to_end_id", entry.EndToEndID, 1, 35); err != nil {
				return err
			}
		}
		if entry.DebtorName != "" && entry.Creditor != "" {
			return fmt.Errorf("%w: entry %d cannot have both debtor and creditor", ErrInvalid, i)
		}
	}
	return nil
}

func validateParty(label string, p Party, requireAgent bool) error {
	if err := text(label+".name", p.Name, 1, 140); err != nil {
		return err
	}
	if err := account(label+".account", p.Account); err != nil {
		return err
	}
	if requireAgent || p.Agent != "" {
		if !bicPattern.MatchString(p.Agent) {
			return fmt.Errorf("%w: %s.bic must be 8 or 11 characters", ErrInvalid, label)
		}
	}
	if p.Country != "" && !countryPattern.MatchString(p.Country) {
		return fmt.Errorf("%w: %s.country must be ISO 3166-1 alpha-2", ErrInvalid, label)
	}
	return nil
}

func validateAmount(a Amount) error {
	if err := currency("currency", a.Currency); err != nil {
		return err
	}
	if !amountPattern.MatchString(a.Value) {
		return fmt.Errorf("%w: amount must be a positive decimal with at most 5 fraction digits", ErrInvalid)
	}
	digits := strings.Replace(a.Value, ".", "", 1)
	if strings.Trim(digits, "0") == "" {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	return nil
}

func text(field, value string, min, max int) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	// Rune count, not byte count: ISO 20022 limits are characters and Python
	// counts code points.
	if n := utf8.RuneCountInString(value); n < min || n > max {
		return fmt.Errorf("%w: %s must be %d..%d characters", ErrInvalid, field, min, max)
	}
	return nil
}

func currency(field, value string) error {
	if !currencyPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must be ISO 4217", ErrInvalid, field)
	}
	return nil
}

func account(field, value string) error {
	if !accountPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must be 5..34 uppercase alphanumeric characters", ErrInvalid, field)
	}
	return nil
}

func timestamp(field, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%w: %s must be RFC 3339", ErrInvalid, field)
	}
	return nil
}
