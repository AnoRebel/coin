package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrHash means the running artifact is not the one governance approved.
var ErrHash = errors.New("hash")

// Upgrade is the approved code-upgrade plan carried by a code_upgrade action.
// Target is "binary" or "wasm"; Hash is the SHA-256 of the artifact.
type Upgrade struct {
	Proposal   string `json:"proposal,omitempty"`
	Target     string `json:"target"`
	Hash       string `json:"hash"`
	Previous   string `json:"previous_hash"`
	Migration  string `json:"migration"`
	Rollback   string `json:"rollback"`
	Version    string `json:"version,omitempty"`
	Activation uint64 `json:"activation_height"`
}

// NewUpgrade validates and reads a code_upgrade action. Every field the spec
// requires is mandatory: a plan without a rollback or a migration is refused
// before it can be voted on.
func NewUpgrade(action Action) (Upgrade, error) {
	if action.Type != "code_upgrade" {
		return Upgrade{}, fmt.Errorf("%w: action %s is not a code upgrade", ErrInvalid, action.Type)
	}
	get := func(name string) string {
		value, _ := action.Params[name].(string)
		return strings.TrimSpace(value)
	}
	u := Upgrade{
		Target:    get("target"),
		Hash:      get("hash"),
		Previous:  get("previous_hash"),
		Migration: get("migration"),
		Rollback:  get("rollback"),
		Version:   get("version"),
	}
	if u.Target != "binary" && u.Target != "wasm" {
		return Upgrade{}, fmt.Errorf("%w: target must be binary or wasm", ErrInvalid)
	}
	for name, hash := range map[string]string{"hash": u.Hash, "previous_hash": u.Previous} {
		if !isHash(hash) {
			return Upgrade{}, fmt.Errorf("%w: %s must be 64 lowercase hex characters", ErrInvalid, name)
		}
	}
	if u.Migration == "" {
		return Upgrade{}, fmt.Errorf("%w: migration plan is required", ErrInvalid)
	}
	if u.Rollback == "" {
		return Upgrade{}, fmt.Errorf("%w: rollback plan is required", ErrInvalid)
	}
	return u, nil
}

func isHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

// Verify checks the artifact the node would run against the approved hash.
// A mismatch is refused; the node must not apply the upgrade.
func Verify(u Upgrade, running string) error {
	if strings.ToLower(running) != u.Hash {
		return fmt.Errorf("%w: %s hash is %s, governance approved %s", ErrHash, u.Target, running, u.Hash)
	}
	return nil
}

// Upgrader registers the code_upgrade action. It verifies the running
// artifact at execution, which is already behind the timelock, and records the
// approved plan for operators. Running defaults to SelfHash.
type Upgrader struct {
	Running func() (string, error)
	Applied *Upgrade
}

// Register adds the code_upgrade handler to a registry.
func (u *Upgrader) Register(r *Registry) error {
	return r.Register("code_upgrade", func(action Action, p *Proposal) (func(), error) {
		plan, err := NewUpgrade(action)
		if err != nil {
			return nil, err
		}
		running := u.Running
		if running == nil {
			running = SelfHash
		}
		got, err := running()
		if err != nil {
			return nil, err
		}
		if err := Verify(plan, got); err != nil {
			return nil, err
		}
		plan.Proposal = p.ID
		plan.Activation = p.ActivationHeight
		previous := u.Applied
		u.Applied = &plan
		return func() { u.Applied = previous }, nil
	})
}

// SelfHash returns the SHA-256 of the running executable.
func SelfHash() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return FileHash(path)
}

// FileHash returns the lowercase hex SHA-256 of a file.
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validateCodeUpgrade is the Submit check: a code-upgrade proposal must carry
// at least one well-formed code_upgrade action.
func validateCodeUpgrade(p Proposal) error {
	found := false
	for _, action := range p.Actions {
		if action.Type != "code_upgrade" {
			continue
		}
		if _, err := NewUpgrade(action); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fmt.Errorf("%w: a code upgrade proposal needs a code_upgrade action", ErrInvalid)
	}
	return nil
}
