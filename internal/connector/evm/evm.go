// Package evm implements the EVM side of the ARNE bridge: a lock/mint and
// burn/release attestation with finality, replay protection, per-asset rate
// limits, and an emergency pause.
//
// The bridge never holds keys or talks to a chain. It decides whether an
// observed message may be executed, and records that decision, so a relayer can
// mint or release exactly once. Chain observation, signing, and submission are
// the relayer's job.
package evm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("evm: invalid")
	ErrPaused   = errors.New("evm: bridge is paused")
	ErrFinality = errors.New("evm: not final")
	ErrReplay   = errors.New("evm: replay")
	ErrLimit    = errors.New("evm: rate limit exceeded")
	ErrChain    = errors.New("evm: wrong chain")
)

// Direction is the bridge leg. Inbound mints on ARNE from an EVM lock; outbound
// releases on EVM from an ARNE burn.
type Direction string

const (
	Inbound  Direction = "inbound"
	Outbound Direction = "outbound"
)

// Message is one observed bridge event.
type Message struct {
	ID            string    `json:"id"`
	Direction     Direction `json:"direction"`
	ChainID       uint64    `json:"chain_id"`
	BlockNumber   uint64    `json:"block_number"`
	TxHash        string    `json:"tx_hash"`
	LogIndex      uint32    `json:"log_index"`
	Asset         string    `json:"asset"`
	Amount        uint64    `json:"amount"`
	Sender        string    `json:"sender"`
	Recipient     string    `json:"recipient"`
	Confirmations uint64    `json:"confirmations"`
}

// MessageID is the deterministic identity of an on-chain event: the first 16
// bytes of a domain-separated SHA-256. Two logs in one transaction differ.
func MessageID(chainID uint64, txHash string, logIndex uint32) string {
	h := sha256.New()
	h.Write([]byte("arne-bridge-v1"))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatUint(chainID, 10)))
	h.Write([]byte{0})
	h.Write([]byte(txHash))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatUint(uint64(logIndex), 10)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Limit caps one asset per rolling window, in base units and unix seconds.
type Limit struct {
	Asset  string `json:"asset"`
	Max    uint64 `json:"max"`
	Window int64  `json:"window"`
}

// Config is the bridge policy.
type Config struct {
	ChainID  uint64  `json:"chain_id"`
	Finality uint64  `json:"finality"`
	Limits   []Limit `json:"limits"`
}

type window struct {
	start int64
	used  uint64
}

// Bridge is the execution guard. It is not safe for concurrent use; callers
// serialize submissions because replay and rate-limit decisions are stateful.
type Bridge struct {
	config  Config
	paused  bool
	reason  string
	seen    map[string]bool
	windows map[string]*window
}

// New validates the policy and returns an unpaused bridge.
func New(config Config) (*Bridge, error) {
	if config.ChainID == 0 {
		return nil, fmt.Errorf("%w: chain_id is required", ErrInvalid)
	}
	if config.Finality == 0 {
		return nil, fmt.Errorf("%w: finality must be positive", ErrInvalid)
	}
	seenAssets := map[string]bool{}
	for _, limit := range config.Limits {
		if strings.TrimSpace(limit.Asset) == "" {
			return nil, fmt.Errorf("%w: limit asset is required", ErrInvalid)
		}
		if seenAssets[limit.Asset] {
			return nil, fmt.Errorf("%w: duplicate limit for %q", ErrInvalid, limit.Asset)
		}
		seenAssets[limit.Asset] = true
		if limit.Max == 0 || limit.Window <= 0 {
			return nil, fmt.Errorf("%w: limit for %q needs a positive max and window", ErrInvalid, limit.Asset)
		}
	}
	return &Bridge{
		config:  config,
		seen:    map[string]bool{},
		windows: map[string]*window{},
	}, nil
}

// Pause stops every submission. Reasons are short operator text.
func (b *Bridge) Pause(reason string) error {
	if b.paused {
		return fmt.Errorf("%w: already paused", ErrInvalid)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: pause reason is required", ErrInvalid)
	}
	if utf8.RuneCountInString(reason) > 200 {
		return fmt.Errorf("%w: pause reason is too long", ErrInvalid)
	}
	b.paused, b.reason = true, reason
	return nil
}

// Resume clears the pause. It does not clear replay or rate-limit state.
func (b *Bridge) Resume() error {
	if !b.paused {
		return fmt.Errorf("%w: not paused", ErrInvalid)
	}
	b.paused, b.reason = false, ""
	return nil
}

// Paused reports the pause state and reason.
func (b *Bridge) Paused() (bool, string) { return b.paused, b.reason }

// Submit decides whether a message may execute. On success the message ID is
// recorded and the asset limit is charged; on failure nothing changes.
func (b *Bridge) Submit(msg Message, now int64) error {
	if b.paused {
		return fmt.Errorf("%w: %s", ErrPaused, b.reason)
	}
	if msg.ID == "" {
		msg.ID = MessageID(msg.ChainID, msg.TxHash, msg.LogIndex)
	}
	if err := b.validate(msg); err != nil {
		return err
	}
	if b.seen[msg.ID] {
		return fmt.Errorf("%w: %s", ErrReplay, msg.ID)
	}
	if b.config.Finality > msg.Confirmations {
		return fmt.Errorf("%w: %d of %d confirmations", ErrFinality, msg.Confirmations, b.config.Finality)
	}
	if err := b.charge(msg.Asset, msg.Amount, now); err != nil {
		return err
	}
	b.seen[msg.ID] = true
	return nil
}

func (b *Bridge) validate(msg Message) error {
	if msg.Direction != Inbound && msg.Direction != Outbound {
		return fmt.Errorf("%w: direction %q", ErrInvalid, msg.Direction)
	}
	if msg.ChainID != b.config.ChainID {
		return fmt.Errorf("%w: %d, expected %d", ErrChain, msg.ChainID, b.config.ChainID)
	}
	if len(msg.TxHash) != 66 || !strings.HasPrefix(msg.TxHash, "0x") || !lowerHex(msg.TxHash[2:]) {
		return fmt.Errorf("%w: tx_hash must be 0x followed by 64 lowercase hex characters", ErrInvalid)
	}
	if err := text("asset", msg.Asset, 128); err != nil {
		return err
	}
	if err := text("sender", msg.Sender, 128); err != nil {
		return err
	}
	if err := text("recipient", msg.Recipient, 128); err != nil {
		return err
	}
	if msg.Amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	expected := MessageID(msg.ChainID, msg.TxHash, msg.LogIndex)
	if msg.ID != expected {
		return fmt.Errorf("%w: message id does not match the chain event", ErrInvalid)
	}
	return nil
}

func (b *Bridge) charge(asset string, amount uint64, now int64) error {
	for _, limit := range b.config.Limits {
		if limit.Asset != asset {
			continue
		}
		w := b.windows[asset]
		if w == nil || now >= w.start+limit.Window {
			w = &window{start: now}
			b.windows[asset] = w
		}
		if amount > limit.Max-w.used {
			return fmt.Errorf("%w: %s has %d of %d left", ErrLimit, asset, limit.Max-w.used, limit.Max)
		}
		w.used += amount
		return nil
	}
	return nil
}

func text(field, value string, max int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	if utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%w: %s must be at most %d characters", ErrInvalid, field, max)
	}
	return nil
}

func lowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
