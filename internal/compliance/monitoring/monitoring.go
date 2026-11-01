// Package monitoring raises transaction-monitoring alerts and records analyst
// dispositions.
//
// A rule can ask for a freeze, but the monitor only applies one when the rule is
// explicitly authorized in the freeze policy. Otherwise the alert is escalated
// for a human instead: no rule freezes funds on its own. Every alert and
// disposition is kept for review.
package monitoring

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Type is a rule kind.
type Type string

const (
	Threshold Type = "threshold"
	Velocity  Type = "velocity"
)

// Severity ranks an alert.
type Severity string

const (
	Low    Severity = "low"
	Medium Severity = "medium"
	High   Severity = "high"
)

// Disposition actions an analyst can record.
const (
	NoAction = "no_action"
	Escalate = "escalate"
	Report   = "report"
)

var (
	ErrInvalid   = errors.New("monitoring: invalid rule, transaction, or disposition")
	ErrUnknown   = errors.New("monitoring: unknown alert")
	ErrClosed    = errors.New("monitoring: alert is already closed")
	ErrDuplicate = errors.New("monitoring: duplicate rule or transaction")
)

// Rule matches transactions. Threshold rules fire at or above Amount; velocity
// rules fire when a sender reaches Count transfers inside Window seconds.
type Rule struct {
	ID       string   `json:"id"`
	Type     Type     `json:"type"`
	Asset    string   `json:"asset,omitempty"`
	Amount   int64    `json:"amount,omitempty"`
	Count    int      `json:"count,omitempty"`
	Window   int64    `json:"window,omitempty"`
	Severity Severity `json:"severity"`
	Freeze   bool     `json:"freeze,omitempty"`
	Enabled  bool     `json:"enabled"`
}

// Transaction is the part of a transfer the monitor sees.
type Transaction struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	Asset  string `json:"asset"`
	Amount int64  `json:"amount"`
	At     int64  `json:"at"`
}

// Alert is one rule hit.
type Alert struct {
	ID              string   `json:"id"`
	Rule            string   `json:"rule"`
	TxID            string   `json:"tx_id"`
	From            string   `json:"from"`
	Asset           string   `json:"asset"`
	Amount          int64    `json:"amount"`
	Severity        Severity `json:"severity"`
	Reason          string   `json:"reason"`
	CreatedAt       int64    `json:"created_at"`
	FreezeRequested bool     `json:"freeze_requested,omitempty"`
	FreezeApplied   bool     `json:"freeze_applied,omitempty"`
	Escalated       bool     `json:"escalated,omitempty"`
	Status          string   `json:"status"`
	Disposition     string   `json:"disposition,omitempty"`
	Reviewer        string   `json:"reviewer,omitempty"`
	ClosedAt        int64    `json:"closed_at,omitempty"`
}

// Monitor evaluates rules and holds alerts.
type Monitor struct {
	mu       sync.Mutex
	rules    []Rule
	freezeOK map[string]bool
	alerts   map[string]Alert
	order    []string
	seenTx   map[string]bool
	history  map[string][]int64
}

// NewMonitor validates rules and the freeze policy. Only rule IDs listed in
// freezeAuthorized may apply a freeze; every other freeze request escalates.
func NewMonitor(rules []Rule, freezeAuthorized []string) (*Monitor, error) {
	m := &Monitor{
		freezeOK: map[string]bool{},
		alerts:   map[string]Alert{},
		seenTx:   map[string]bool{},
		history:  map[string][]int64{},
	}
	for _, id := range freezeAuthorized {
		m.freezeOK[strings.TrimSpace(id)] = true
	}
	ids := map[string]bool{}
	for i, r := range rules {
		r.ID = strings.TrimSpace(r.ID)
		r.Asset = strings.TrimSpace(r.Asset)
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("%w: rules[%d]: %v", ErrInvalid, i, err)
		}
		if ids[r.ID] {
			return nil, fmt.Errorf("%w: duplicate rule %q", ErrDuplicate, r.ID)
		}
		ids[r.ID] = true
		m.rules = append(m.rules, r)
	}
	for id := range m.freezeOK {
		if id != "" && !ids[id] {
			return nil, fmt.Errorf("%w: freeze authority for unknown rule %q", ErrInvalid, id)
		}
	}
	return m, nil
}

// Evaluate records a transaction and returns the alerts it raised. Re-evaluating
// the same transaction returns nothing new.
func (m *Monitor) Evaluate(tx Transaction) ([]Alert, error) {
	if strings.TrimSpace(tx.ID) == "" || strings.TrimSpace(tx.From) == "" ||
		strings.TrimSpace(tx.Asset) == "" || tx.Amount <= 0 || tx.At <= 0 {
		return nil, fmt.Errorf("%w: id, from, asset, amount, and at are required", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seenTx[tx.ID] {
		return []Alert{}, nil
	}
	m.seenTx[tx.ID] = true
	m.history[tx.From] = append(m.history[tx.From], tx.At)

	var out []Alert
	for _, r := range m.rules {
		if !r.Enabled {
			continue
		}
		reason, hit := r.match(tx, m.history[tx.From])
		if !hit {
			continue
		}
		a := Alert{
			ID: alertID(r.ID, tx.ID), Rule: r.ID, TxID: tx.ID, From: tx.From,
			Asset: tx.Asset, Amount: tx.Amount, Severity: r.Severity,
			Reason: reason, CreatedAt: tx.At, FreezeRequested: r.Freeze,
			Status: "open",
		}
		if r.Freeze {
			if m.freezeOK[r.ID] {
				a.FreezeApplied = true
			} else {
				a.Escalated = true
				a.Reason += "; freeze requires policy authorization, escalated"
			}
		}
		if _, dup := m.alerts[a.ID]; dup {
			continue
		}
		m.alerts[a.ID] = a
		m.order = append(m.order, a.ID)
		out = append(out, a)
	}
	return out, nil
}

// Alerts returns alerts in creation order.
func (m *Monitor) Alerts() []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Alert, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.alerts[id])
	}
	return out
}

// Alert returns one alert.
func (m *Monitor) Alert(id string) (Alert, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.alerts[id]
	return a, ok
}

// Dispose records an analyst's decision. Only an open alert can be closed, and
// the reason and reviewer are required so a case is always explainable.
func (m *Monitor) Dispose(alertID, action, reason, reviewer string, at int64) (Alert, error) {
	action = strings.TrimSpace(action)
	reason = strings.TrimSpace(reason)
	reviewer = strings.TrimSpace(reviewer)
	switch action {
	case NoAction, Escalate, Report:
	default:
		return Alert{}, fmt.Errorf("%w: unknown disposition %q", ErrInvalid, action)
	}
	if reason == "" || reviewer == "" {
		return Alert{}, fmt.Errorf("%w: reason and reviewer are required", ErrInvalid)
	}
	if at <= 0 {
		at = now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.alerts[alertID]
	if !ok {
		return Alert{}, fmt.Errorf("%w: %q", ErrUnknown, alertID)
	}
	if a.Status != "open" {
		return Alert{}, fmt.Errorf("%w: %q", ErrClosed, alertID)
	}
	a.Status = "closed"
	a.Disposition = action
	a.Reviewer = reviewer
	a.ClosedAt = at
	if action == Escalate {
		a.Escalated = true
	}
	m.alerts[alertID] = a
	return a, nil
}

func (r Rule) validate() error {
	if r.ID == "" {
		return fmt.Errorf("id is required")
	}
	switch r.Severity {
	case Low, Medium, High:
	default:
		return fmt.Errorf("severity must be low, medium, or high")
	}
	switch r.Type {
	case Threshold:
		if r.Amount <= 0 {
			return fmt.Errorf("threshold rule needs a positive amount")
		}
	case Velocity:
		if r.Count < 2 || r.Window <= 0 {
			return fmt.Errorf("velocity rule needs count >= 2 and a positive window")
		}
	default:
		return fmt.Errorf("type must be threshold or velocity")
	}
	return nil
}

func (r Rule) match(tx Transaction, times []int64) (string, bool) {
	if r.Asset != "" && r.Asset != tx.Asset {
		return "", false
	}
	switch r.Type {
	case Threshold:
		if tx.Amount < r.Amount {
			return "", false
		}
		return fmt.Sprintf("amount %d at or above %d", tx.Amount, r.Amount), true
	case Velocity:
		from := tx.At - r.Window
		count := 0
		for _, t := range times {
			if t > from && t <= tx.At {
				count++
			}
		}
		if count < r.Count {
			return "", false
		}
		return fmt.Sprintf("%d transfers in %ds", count, r.Window), true
	}
	return "", false
}

func alertID(rule, tx string) string {
	sum := sha256.Sum256([]byte("arne-alert-v1\x00" + rule + "\x00" + tx))
	return hex.EncodeToString(sum[:16])
}

func now() int64 { return time.Now().Unix() }
