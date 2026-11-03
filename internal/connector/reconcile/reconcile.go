// Package reconcile matches connector operations with external confirmations
// and reports the ones that did not line up.
//
// An operation carries an idempotency key (the caller's retry identity) and a
// correlation identifier (the cross-rail identity). Re-submitting the same key
// returns the first operation unchanged. A confirmation is matched by
// correlation: the first matching confirmation settles the operation exactly
// once, an identical repeat is ignored, and a mismatching one puts the
// operation into conflict for a human to resolve.
//
// A reconciliation run ("Reconcile") also ages pending operations: past their
// deadline they become failed or stay pending according to policy, and either
// way they are reported as a missing-confirmation exception.
package reconcile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Outcome is what happened to a submission or confirmation.
type Outcome string

const (
	Accepted  Outcome = "accepted"
	Duplicate Outcome = "duplicate"
	Conflict  Outcome = "conflict"
	Unknown   Outcome = "unknown"
	Invalid   Outcome = "invalid"
)

// Status is an operation's settlement state.
type Status string

const (
	Pending    Status = "pending"
	Settled    Status = "settled"
	Failed     Status = "failed"
	InConflict Status = "conflicting"
)

// Policy decides what a reconciliation run does with an operation that passed
// its deadline without a confirmation.
type Policy struct {
	FailOnTimeout bool `json:"fail_on_timeout"`
}

// Operation is one connector instruction.
type Operation struct {
	Key         string `json:"key"`
	Correlation string `json:"correlation"`
	Asset       string `json:"asset"`
	Amount      uint64 `json:"amount"`
	CreatedAt   int64  `json:"created_at"`
	Timeout     int64  `json:"timeout"`
}

// Confirmation is one external settlement notice.
type Confirmation struct {
	Correlation string `json:"correlation"`
	Asset       string `json:"asset"`
	Amount      uint64 `json:"amount"`
	Reference   string `json:"reference"`
	At          int64  `json:"at"`
}

// State is the published view of one operation.
type State struct {
	Operation      Operation `json:"operation"`
	Status         Status    `json:"status"`
	Reference      string    `json:"reference,omitempty"`
	ConfirmedAt    int64     `json:"confirmed_at,omitempty"`
	ConflictDetail string    `json:"conflict_detail,omitempty"`
}

// Exception is one unmatched or conflicting thing a run found.
type Exception struct {
	Kind        string `json:"kind"`
	Correlation string `json:"correlation"`
	Detail      string `json:"detail,omitempty"`
}

// Total is the amount of one asset per status bucket.
type Total struct {
	Asset       string `json:"asset"`
	Settled     uint64 `json:"settled"`
	Failed      uint64 `json:"failed"`
	Pending     uint64 `json:"pending"`
	Conflicting uint64 `json:"conflicting"`
}

// Report is the result of one reconciliation run. Matched + Unmatched +
// Conflicting equals the number of stored operations.
type Report struct {
	Matched     int         `json:"matched"`
	Unmatched   int         `json:"unmatched"`
	Conflicting int         `json:"conflicting"`
	Totals      []Total     `json:"totals"`
	Exceptions  []Exception `json:"exceptions"`
}

// Ledger is the reconciliation state. It is not safe for concurrent use.
type Ledger struct {
	policy   Policy
	byKey    map[string]*entry
	byCorr   map[string]*entry
	order    []*entry
	excepted []Exception
}

type entry struct {
	state            State
	timedOutReported bool
}

// New starts an empty ledger.
func New(policy Policy) *Ledger {
	return &Ledger{
		policy: policy,
		byKey:  map[string]*entry{},
		byCorr: map[string]*entry{},
	}
}

// Submit records an operation. A repeated key returns the first operation
// unchanged; reusing a correlation under a different key is a conflict.
func (l *Ledger) Submit(op Operation) (State, Outcome) {
	if err := op.validate(); err != nil {
		return State{}, Invalid
	}
	if existing, ok := l.byKey[op.Key]; ok {
		return existing.state, Duplicate
	}
	if existing, ok := l.byCorr[op.Correlation]; ok {
		l.record(Exception{
			Kind:        "correlation_reused",
			Correlation: op.Correlation,
			Detail:      fmt.Sprintf("key %s conflicts with %s", op.Key, existing.state.Operation.Key),
		})
		return existing.state, Conflict
	}
	e := &entry{state: State{Operation: op, Status: Pending}}
	l.byKey[op.Key] = e
	l.byCorr[op.Correlation] = e
	l.order = append(l.order, e)
	return e.state, Accepted
}

// Confirm matches a confirmation by correlation. The first matching one settles
// the operation once; an identical repeat is ignored; anything else is a
// conflict and is recorded.
func (l *Ledger) Confirm(c Confirmation, now int64) (State, Outcome) {
	if err := c.validate(); err != nil {
		return State{}, Invalid
	}
	e, ok := l.byCorr[c.Correlation]
	if !ok {
		l.record(Exception{Kind: "unknown_confirmation", Correlation: c.Correlation})
		return State{}, Unknown
	}

	switch e.state.Status {
	case Settled:
		if c.Asset == e.state.Operation.Asset && c.Amount == e.state.Operation.Amount &&
			c.Reference == e.state.Reference {
			return e.state, Duplicate
		}
		return l.conflict(e, fmt.Sprintf("settled as %s/%d from %s, got %s/%d from %s",
			e.state.Operation.Asset, e.state.Operation.Amount, e.state.Reference,
			c.Asset, c.Amount, c.Reference))
	case InConflict:
		return l.conflict(e, "operation is already in conflict")
	case Failed:
		l.record(Exception{
			Kind:        "late_confirmation",
			Correlation: c.Correlation,
			Detail:      fmt.Sprintf("operation failed before confirmation at %d", c.At),
		})
		return e.state, Conflict
	}

	if l.policy.FailOnTimeout && now >= e.state.Operation.CreatedAt+e.state.Operation.Timeout {
		e.state.Status = Failed
		if !e.timedOutReported {
			e.timedOutReported = true
			l.record(Exception{
				Kind:        "missing_confirmation",
				Correlation: c.Correlation,
				Detail:      fmt.Sprintf("no confirmation by %d", e.state.Operation.CreatedAt+e.state.Operation.Timeout),
			})
		}
		l.record(Exception{
			Kind:        "late_confirmation",
			Correlation: c.Correlation,
			Detail:      fmt.Sprintf("confirmation at %d arrived after the deadline", c.At),
		})
		return e.state, Conflict
	}

	if c.Asset != e.state.Operation.Asset || c.Amount != e.state.Operation.Amount {
		return l.conflict(e, fmt.Sprintf("expected %s/%d, got %s/%d",
			e.state.Operation.Asset, e.state.Operation.Amount, c.Asset, c.Amount))
	}
	e.state.Status = Settled
	e.state.Reference = c.Reference
	e.state.ConfirmedAt = c.At
	return e.state, Accepted
}

// Reconcile ages pending operations and reports the current picture. An
// operation past `CreatedAt + Timeout` becomes failed when the policy says so
// (otherwise it stays pending), and either way it is reported once as a
// missing confirmation.
func (l *Ledger) Reconcile(now int64) Report {
	report := Report{}
	totals := map[string]*Total{}

	for _, e := range l.order {
		op := e.state.Operation
		if e.state.Status == Pending && now >= op.CreatedAt+op.Timeout {
			if l.policy.FailOnTimeout {
				e.state.Status = Failed
			}
			if !e.timedOutReported {
				e.timedOutReported = true
				l.record(Exception{
					Kind:        "missing_confirmation",
					Correlation: op.Correlation,
					Detail:      fmt.Sprintf("no confirmation by %d", op.CreatedAt+op.Timeout),
				})
			}
		}

		total := totals[op.Asset]
		if total == nil {
			total = &Total{Asset: op.Asset}
			totals[op.Asset] = total
		}
		switch e.state.Status {
		case Settled:
			report.Matched++
			total.Settled += op.Amount
		case InConflict:
			report.Conflicting++
			total.Conflicting += op.Amount
		default:
			report.Unmatched++
			if e.state.Status == Failed {
				total.Failed += op.Amount
			} else {
				total.Pending += op.Amount
			}
		}
	}

	assets := make([]string, 0, len(totals))
	for asset := range totals {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		report.Totals = append(report.Totals, *totals[asset])
	}
	report.Exceptions = append([]Exception(nil), l.excepted...)
	return report
}

// State returns one operation by correlation.
func (l *Ledger) State(correlation string) (State, bool) {
	e, ok := l.byCorr[correlation]
	if !ok {
		return State{}, false
	}
	return e.state, true
}

func (l *Ledger) conflict(e *entry, detail string) (State, Outcome) {
	e.state.Status = InConflict
	e.state.ConflictDetail = detail
	l.record(Exception{
		Kind:        "conflicting_confirmation",
		Correlation: e.state.Operation.Correlation,
		Detail:      detail,
	})
	return e.state, Conflict
}

func (l *Ledger) record(exception Exception) {
	l.excepted = append(l.excepted, exception)
}

func (op Operation) validate() error {
	if err := identifier("key", op.Key); err != nil {
		return err
	}
	if err := identifier("correlation", op.Correlation); err != nil {
		return err
	}
	if strings.TrimSpace(op.Asset) == "" || utf8.RuneCountInString(op.Asset) > 128 {
		return fmt.Errorf("%w: asset is required", ErrInvalid)
	}
	if op.Amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if op.CreatedAt < 0 {
		return fmt.Errorf("%w: created_at must not be negative", ErrInvalid)
	}
	if op.Timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrInvalid)
	}
	return nil
}

func (c Confirmation) validate() error {
	if err := identifier("correlation", c.Correlation); err != nil {
		return err
	}
	if strings.TrimSpace(c.Asset) == "" || utf8.RuneCountInString(c.Asset) > 128 {
		return fmt.Errorf("%w: asset is required", ErrInvalid)
	}
	if c.Amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if strings.TrimSpace(c.Reference) == "" || utf8.RuneCountInString(c.Reference) > 256 {
		return fmt.Errorf("%w: reference is required", ErrInvalid)
	}
	if c.At < 0 {
		return fmt.Errorf("%w: at must not be negative", ErrInvalid)
	}
	return nil
}

// ErrInvalid is wrapped by a validation failure. Callers mostly see the Invalid
// outcome; the error is kept for direct model checks.
var ErrInvalid = errors.New("reconcile: invalid")

func identifier(field, value string) error {
	if value == "" || len(value) > 128 {
		return fmt.Errorf("%w: %s must be 1..128 characters", ErrInvalid, field)
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '.', r == ':', r == '-':
		default:
			return fmt.Errorf("%w: %s has an invalid character", ErrInvalid, field)
		}
	}
	return nil
}
