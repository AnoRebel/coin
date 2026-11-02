// Package lightclient verifies ARNE commit certificates, validator-set
// membership, and state proofs without the full chain.
//
// Certificates carry BLS signatures over a canonical message. A verifier
// counts only signatures from its trusted validator set and requires more than
// two-thirds of the trusted voting power.
package lightclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/anorebel/anorebelcoin/internal/crypto/bls"
	"github.com/anorebel/anorebelcoin/internal/smt"
)

var (
	ErrInvalid   = errors.New("invalid")
	ErrOutdated  = errors.New("outdated")
	ErrQuorum    = errors.New("insufficient quorum")
	ErrForged    = errors.New("forged")
	ErrUnknown   = errors.New("unknown validator")
	ErrDuplicate = errors.New("duplicate validator")
	ErrProof     = errors.New("invalid proof")
)

// Validator is one member of a validator set.
type Validator struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
	Power     uint64 `json:"power"`
}

// Set is a validator set at a known height.
type Set struct {
	Height     uint64      `json:"height"`
	Validators []Validator `json:"validators"`
}

// Signature is one validator's certificate signature.
type Signature struct {
	Validator string `json:"validator"`
	Signature string `json:"signature"`
}

// Certificate proves that a block was finalized.
type Certificate struct {
	Height             uint64      `json:"height"`
	Round              uint32      `json:"round"`
	BlockHash          string      `json:"block_hash"`
	StateRoot          string      `json:"state_root"`
	NextValidatorsRoot string      `json:"next_validators_root"`
	Signatures         []Signature `json:"signatures"`
}

// Validate checks a set for duplicates and unusable entries.
func (s Set) Validate() error {
	if s.Height == 0 || len(s.Validators) == 0 {
		return fmt.Errorf("%w: set needs a height and at least one validator", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, v := range s.Validators {
		if v.ID == "" || v.Power == 0 {
			return fmt.Errorf("%w: validator needs an id and positive power", ErrInvalid)
		}
		if seen[v.ID] {
			return fmt.Errorf("%w: validator %s", ErrDuplicate, v.ID)
		}
		seen[v.ID] = true
		if key, err := hex.DecodeString(v.PublicKey); err != nil || len(key) == 0 {
			return fmt.Errorf("%w: validator %s key is not hex", ErrInvalid, v.ID)
		}
	}
	return nil
}

// Root returns the set's sparse Merkle root as hex.
func (s Set) Root() string {
	tree := smt.New()
	for _, v := range s.Validators {
		tree.Set(memberKey(v.ID), memberValue(v))
	}
	root := tree.Root()
	return hex.EncodeToString(root[:])
}

// Prove returns an inclusion proof for a validator.
func (s Set) Prove(id string) (smt.Proof, bool) {
	for _, v := range s.Validators {
		if v.ID == id {
			tree := smt.New()
			for _, member := range s.Validators {
				tree.Set(memberKey(member.ID), memberValue(member))
			}
			return tree.Prove(memberKey(id)), true
		}
	}
	return smt.Proof{}, false
}

// VerifyMember proves that a validator belongs to a set root.
func VerifyMember(root string, v Validator, proof smt.Proof) error {
	parsed, err := parseRoot(root)
	if err != nil {
		return err
	}
	if proof.Key != memberKey(v.ID) {
		return fmt.Errorf("%w: proof is for %q", ErrProof, proof.Key)
	}
	if string(proof.Value) != string(memberValue(v)) {
		return fmt.Errorf("%w: validator value mismatch", ErrProof)
	}
	if err := proof.Verify(parsed); err != nil {
		return fmt.Errorf("%w: %v", ErrProof, err)
	}
	return nil
}

// Verify checks a certificate against a trusted set and returns the signed
// power. The certificate must be newer than the trusted set.
func Verify(network string, trusted Set, cert Certificate) (uint64, error) {
	if err := trusted.Validate(); err != nil {
		return 0, err
	}
	if cert.Height <= trusted.Height {
		return 0, fmt.Errorf("%w: certificate height %d, trusted %d", ErrOutdated, cert.Height, trusted.Height)
	}
	if cert.BlockHash == "" || cert.StateRoot == "" || cert.NextValidatorsRoot == "" {
		return 0, fmt.Errorf("%w: certificate fields are empty", ErrInvalid)
	}
	byID := make(map[string]Validator, len(trusted.Validators))
	var total uint64
	for _, v := range trusted.Validators {
		byID[v.ID] = v
		total += v.Power
	}
	msg := Message(network, cert)
	signed := uint64(0)
	seen := map[string]bool{}
	for _, sig := range cert.Signatures {
		v, ok := byID[sig.Validator]
		if !ok {
			return 0, fmt.Errorf("%w: %s", ErrUnknown, sig.Validator)
		}
		if seen[sig.Validator] {
			return 0, fmt.Errorf("%w: %s", ErrDuplicate, sig.Validator)
		}
		seen[sig.Validator] = true
		key, err := hex.DecodeString(v.PublicKey)
		if err != nil {
			return 0, fmt.Errorf("%w: validator key is not hex", ErrInvalid)
		}
		raw, err := hex.DecodeString(sig.Signature)
		if err != nil || len(raw) == 0 {
			return 0, fmt.Errorf("%w: signature is not hex", ErrForged)
		}
		if !bls.Verify(bls.PublicKey(key), msg, bls.Signature(raw)) {
			return 0, fmt.Errorf("%w: signature from %s", ErrForged, sig.Validator)
		}
		signed += v.Power
	}
	if signed*3 <= total*2 {
		return signed, fmt.Errorf("%w: signed %d of %d", ErrQuorum, signed, total)
	}
	return signed, nil
}

// Advance verifies a certificate and returns the next set when its root
// matches the certificate.
func Advance(network string, trusted Set, cert Certificate, next Set) (Set, error) {
	if _, err := Verify(network, trusted, cert); err != nil {
		return Set{}, err
	}
	if err := next.Validate(); err != nil {
		return Set{}, err
	}
	if next.Height != cert.Height+1 {
		return Set{}, fmt.Errorf("%w: next set height %d, certificate %d", ErrInvalid, next.Height, cert.Height)
	}
	if next.Root() != cert.NextValidatorsRoot {
		return Set{}, fmt.Errorf("%w: next set root mismatch", ErrProof)
	}
	return next, nil
}

// VerifyState checks a state proof against a certificate's state root.
func VerifyState(cert Certificate, key string, value []byte, proof smt.Proof) error {
	root, err := parseRoot(cert.StateRoot)
	if err != nil {
		return err
	}
	if proof.Key != key {
		return fmt.Errorf("%w: proof is for %q", ErrProof, proof.Key)
	}
	if string(proof.Value) != string(value) {
		return fmt.Errorf("%w: value mismatch", ErrProof)
	}
	if err := proof.Verify(root); err != nil {
		return fmt.Errorf("%w: %v", ErrProof, err)
	}
	return nil
}

// Message is the canonical bytes every certifying validator signs.
func Message(network string, cert Certificate) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte("arne-cert-v1"))
	for _, part := range [][]byte{
		[]byte(network), []byte(strconv.FormatUint(cert.Height, 10)),
		[]byte(strconv.FormatUint(uint64(cert.Round), 10)),
		[]byte(cert.BlockHash), []byte(cert.StateRoot), []byte(cert.NextValidatorsRoot),
	} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

func memberKey(id string) string { return "validator/" + id }

func memberValue(v Validator) []byte {
	return []byte(v.PublicKey + ":" + strconv.FormatUint(v.Power, 10))
}

func parseRoot(root string) (smt.Hash, error) {
	raw, err := hex.DecodeString(root)
	if err != nil || len(raw) != 32 {
		return smt.Hash{}, fmt.Errorf("%w: root is not 32-byte hex", ErrInvalid)
	}
	var out smt.Hash
	copy(out[:], raw)
	return out, nil
}
