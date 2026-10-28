package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Network classes and canonical identities.
const (
	ClassMainnet    = "mainnet"
	ClassTestnet    = "testnet"
	ClassDevnet     = "devnet"
	ClassConsortium = "consortium"

	NativeAssetSymbol = "ARCE"
	NativeAssetID     = "arce" // followed by ":" + short network id
)

var (
	ErrValidation = errors.New("validation")
	ErrSignature  = errors.New("signature")
	ErrVersion    = errors.New("unsupported version")
	ErrExpired    = errors.New("transaction expired")
	ErrNetwork    = errors.New("network or chain mismatch")
)

var (
	reNetwork = regexp.MustCompile(`^arne:(mainnet|testnet|devnet|consortium/[a-z0-9][a-z0-9-]{1,31})$`)
	reAssetID = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}:[a-z0-9][a-z0-9/-]{0,63}$`)
	reSymbol  = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,15}$`)
	reAddr    = regexp.MustCompile(`^arce(-testnet|-devnet|-consortium)?1[02-9ac-hj-np-z]{38,60}$`)
)

// NetworkClass returns the class of a canonical network ID.
func NetworkClass(network string) (string, error) {
	m := reNetwork.FindStringSubmatch(network)
	if m == nil {
		return "", fmt.Errorf("%w: network id %q", ErrValidation, network)
	}
	if strings.HasPrefix(m[1], "consortium/") {
		return ClassConsortium, nil
	}
	return m[1], nil
}

// ShortNetwork strips the "arne:" prefix: "arne:devnet" -> "devnet".
func ShortNetwork(network string) string { return strings.TrimPrefix(network, "arne:") }

// ChainID maps a network to its CometBFT chain ID.
func ChainID(network string) (string, error) {
	class, err := NetworkClass(network)
	if err != nil {
		return "", err
	}
	if class == ClassConsortium {
		return "arne-consortium-" + strings.ReplaceAll(strings.TrimPrefix(network, "arne:consortium/"), "/", "-") + "-1", nil
	}
	return "arne-" + class + "-1", nil
}

// AssetID builds the canonical asset ID for a symbol on a network.
func AssetID(symbol, network string) string {
	return strings.ToLower(symbol) + ":" + ShortNetwork(network)
}

// NativeAsset returns the canonical ID of ARCE on a network.
func NativeAsset(network string) string { return "arce:" + ShortNetwork(network) }

// Asset is an immutable registered asset.
type Asset struct {
	ID         string            `json:"id"`
	Class      string            `json:"class"`
	Symbol     string            `json:"symbol"`
	Name       string            `json:"name"`
	Decimals   uint8             `json:"decimals"`
	Policy     string            `json:"policy"`
	MaxSupply  uint64            `json:"max_supply"`
	Issuer     string            `json:"issuer"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Registered uint64            `json:"registered"`
}

var assetClasses = map[string]bool{
	"native": true, "fiat": true, "crypto": true,
	"commodity": true, "security": true, "point": true,
}

var supplyPolicies = map[string]bool{
	"fixed": true, "capped": true, "mintable": true, "burnable": true,
}

func (a Asset) Validate(network string) error {
	if !reAssetID.MatchString(a.ID) {
		return fmt.Errorf("%w: asset id %q", ErrValidation, a.ID)
	}
	if !strings.HasSuffix(a.ID, ":"+ShortNetwork(network)) {
		return fmt.Errorf("%w: asset id outside network %q", ErrValidation, a.ID)
	}
	if !assetClasses[a.Class] {
		return fmt.Errorf("%w: asset class %q", ErrValidation, a.Class)
	}
	if !reSymbol.MatchString(a.Symbol) {
		return fmt.Errorf("%w: symbol %q", ErrValidation, a.Symbol)
	}
	if a.Name == "" || len(a.Name) > 64 {
		return fmt.Errorf("%w: name length", ErrValidation)
	}
	if a.Decimals > 18 {
		return fmt.Errorf("%w: decimals", ErrValidation)
	}
	if !supplyPolicies[a.Policy] {
		return fmt.Errorf("%w: policy %q", ErrValidation, a.Policy)
	}
	if (a.Policy == "fixed" || a.Policy == "capped") && a.MaxSupply == 0 {
		return fmt.Errorf("%w: policy %q requires max_supply", ErrValidation, a.Policy)
	}
	if a.Class == "native" && a.ID != NativeAsset(network) {
		return fmt.Errorf("%w: native asset id must be %s", ErrValidation, NativeAsset(network))
	}
	return nil
}

func (a Asset) ToCBOR() map[string]any {
	meta := map[string]any{}
	for k, v := range a.Metadata {
		meta[k] = v
	}
	return map[string]any{
		"id": a.ID, "class": a.Class, "symbol": a.Symbol, "name": a.Name,
		"decimals": uint64(a.Decimals), "policy": a.Policy,
		"max_supply": a.MaxSupply, "issuer": a.Issuer, "meta": meta,
		"registered": a.Registered,
	}
}

// AssetFromCBOR parses a canonical asset map.
func AssetFromCBOR(m map[string]any) (Asset, error) {
	a := Asset{}
	var err error
	if a.ID, err = str(m, "id"); err != nil {
		return a, err
	}
	if a.Class, err = str(m, "class"); err != nil {
		return a, err
	}
	if a.Symbol, err = str(m, "symbol"); err != nil {
		return a, err
	}
	if a.Name, err = str(m, "name"); err != nil {
		return a, err
	}
	var dec uint64
	if dec, err = num(m, "decimals"); err != nil {
		return a, err
	}
	if dec > 18 {
		return a, fmt.Errorf("%w: decimals", ErrValidation)
	}
	a.Decimals = uint8(dec)
	if a.Policy, err = str(m, "policy"); err != nil {
		return a, err
	}
	if a.MaxSupply, err = num(m, "max_supply"); err != nil {
		return a, err
	}
	if raw, ok := m["issuer"]; ok {
		if a.Issuer, ok = raw.(string); !ok {
			return a, fmt.Errorf("%w: issuer", ErrValidation)
		}
	}
	if raw, ok := m["registered"]; ok {
		if a.Registered, ok = raw.(uint64); !ok {
			return a, fmt.Errorf("%w: registered", ErrValidation)
		}
	}
	if raw, ok := m["meta"]; ok {
		meta, ok := raw.(map[string]any)
		if !ok {
			return a, fmt.Errorf("%w: meta", ErrValidation)
		}
		a.Metadata = map[string]string{}
		for k, v := range meta {
			s, ok := v.(string)
			if !ok {
				return a, fmt.Errorf("%w: meta value", ErrValidation)
			}
			a.Metadata[k] = s
		}
	}
	return a, nil
}

// Escrow holds funds until release, refund, or timeout.
type Escrow struct {
	ID          string `json:"id"`
	Payer       string `json:"payer"`
	Beneficiary string `json:"beneficiary"`
	Arbiter     string `json:"arbiter"`
	Asset       string `json:"asset"`
	Amount      uint64 `json:"amount"`
	Timeout     uint64 `json:"timeout"`
	Status      string `json:"status"`
	CreatedAt   uint64 `json:"created_at"`
}

const (
	EscrowOpen     = "open"
	EscrowReleased = "released"
	EscrowRefunded = "refunded"
)

func (e Escrow) ToCBOR() map[string]any {
	return map[string]any{
		"id": e.ID, "payer": e.Payer, "beneficiary": e.Beneficiary,
		"arbiter": e.Arbiter, "asset": e.Asset, "amount": e.Amount,
		"timeout": e.Timeout, "status": e.Status, "created_at": e.CreatedAt,
	}
}

func EscrowFromCBOR(m map[string]any) (Escrow, error) {
	e := Escrow{}
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{"id", &e.ID}, {"payer", &e.Payer}, {"beneficiary", &e.Beneficiary},
		{"arbiter", &e.Arbiter}, {"asset", &e.Asset}, {"status", &e.Status}} {
		if *f.dst, err = str(m, f.key); err != nil {
			return e, err
		}
	}
	if e.Amount, err = num(m, "amount"); err != nil {
		return e, err
	}
	if e.Timeout, err = num(m, "timeout"); err != nil {
		return e, err
	}
	if e.CreatedAt, err = num(m, "created_at"); err != nil {
		return e, err
	}
	return e, nil
}

// Signature is a detached signature over the canonical signing preimage.
type Signature struct {
	Suite  string `json:"suite"`
	PubKey []byte `json:"pubkey"`
	Sig    []byte `json:"sig"`
}

func (s Signature) ToCBOR() map[string]any {
	return map[string]any{"suite": s.Suite, "pubkey": s.PubKey, "sig": s.Sig}
}

// Transaction types.
const (
	TxTransfer        = "transfer"
	TxBatch           = "batch"
	TxRegisterAsset   = "register_asset"
	TxCreateEscrow    = "create_escrow"
	TxReleaseEscrow   = "release_escrow"
	TxRefundEscrow    = "refund_escrow"
	TxApprove         = "approve_allowance"
	TxTransferFrom    = "transfer_from"
	TxRevokeAllowance = "revoke_allowance"
	TxMint            = "mint"
	TxBurn            = "burn"
)

var txTypes = map[string]bool{
	TxTransfer: true, TxBatch: true, TxRegisterAsset: true,
	TxCreateEscrow: true, TxReleaseEscrow: true, TxRefundEscrow: true,
	TxApprove: true, TxTransferFrom: true, TxRevokeAllowance: true,
	TxMint: true, TxBurn: true,
}

// Tx is the versioned transaction envelope. Signatures are excluded from the
// signed body.
type Tx struct {
	Version     uint32         `json:"version"`
	Network     string         `json:"network"`
	Chain       string         `json:"chain"`
	Type        string         `json:"type"`
	Signer      string         `json:"signer"`
	Nonce       uint64         `json:"nonce"`
	CreatedAt   uint64         `json:"created_at"`
	ExpiresAt   uint64         `json:"expires_at"`
	FeeAsset    string         `json:"fee_asset"`
	MaxFee      uint64         `json:"max_fee"`
	PriorityFee uint64         `json:"priority_fee"`
	Payload     map[string]any `json:"payload"`
	Signatures  []Signature    `json:"signatures"`
}

// CurrentTxVersion is the only accepted version.
const CurrentTxVersion uint32 = 1

func (t Tx) body() map[string]any {
	p := map[string]any{}
	for k, v := range t.Payload {
		p[k] = v
	}
	return map[string]any{
		"v": uint64(t.Version), "network": t.Network, "chain": t.Chain,
		"type": t.Type, "signer": t.Signer, "nonce": t.Nonce,
		"created_at": t.CreatedAt, "expires_at": t.ExpiresAt,
		"fee_asset": t.FeeAsset, "max_fee": t.MaxFee,
		"priority_fee": t.PriorityFee, "payload": p,
	}
}

func (t Tx) ToCBOR() map[string]any {
	m := t.body()
	sigs := make([]any, 0, len(t.Signatures))
	for _, s := range t.Signatures {
		sigs = append(sigs, s.ToCBOR())
	}
	m["signatures"] = sigs
	return m
}

// TxFromCBOR parses a transaction envelope.
func TxFromCBOR(m map[string]any) (Tx, error) {
	t := Tx{Payload: map[string]any{}}
	var err error
	var v uint64
	if v, err = num(m, "v"); err != nil {
		return t, err
	}
	if v > 0xffffffff {
		return t, ErrVersion
	}
	t.Version = uint32(v)
	for _, f := range []struct {
		key string
		dst *string
	}{{"network", &t.Network}, {"chain", &t.Chain}, {"type", &t.Type}} {
		if *f.dst, err = str(m, f.key); err != nil {
			return t, err
		}
	}
	if t.Signer, err = str(m, "signer"); err != nil {
		return t, err
	}
	if t.Nonce, err = num(m, "nonce"); err != nil {
		return t, err
	}
	if t.CreatedAt, err = num(m, "created_at"); err != nil {
		return t, err
	}
	if t.ExpiresAt, err = num(m, "expires_at"); err != nil {
		return t, err
	}
	if t.FeeAsset, err = str(m, "fee_asset"); err != nil {
		return t, err
	}
	if t.MaxFee, err = num(m, "max_fee"); err != nil {
		return t, err
	}
	if t.PriorityFee, err = num(m, "priority_fee"); err != nil {
		return t, err
	}
	if raw, ok := m["payload"]; ok {
		p, ok := raw.(map[string]any)
		if !ok {
			return t, fmt.Errorf("%w: payload", ErrValidation)
		}
		t.Payload = p
	}
	if raw, ok := m["signatures"]; ok {
		arr, ok := raw.([]any)
		if !ok {
			return t, fmt.Errorf("%w: signatures", ErrValidation)
		}
		for _, item := range arr {
			sm, ok := item.(map[string]any)
			if !ok {
				return t, fmt.Errorf("%w: signature", ErrValidation)
			}
			var s Signature
			if s.Suite, err = str(sm, "suite"); err != nil {
				return t, err
			}
			if s.PubKey, err = bytesField(sm, "pubkey"); err != nil {
				return t, err
			}
			if s.Sig, err = bytesField(sm, "sig"); err != nil {
				return t, err
			}
			t.Signatures = append(t.Signatures, s)
		}
	}
	return t, nil
}

// ValidationError aggregates non-fatal schema problems.
func (t Tx) Validate() error {
	if t.Version != CurrentTxVersion {
		return ErrVersion
	}
	if _, err := NetworkClass(t.Network); err != nil {
		return err
	}
	if want, err := ChainID(t.Network); err != nil || want != t.Chain {
		return fmt.Errorf("%w: chain %q", ErrNetwork, t.Chain)
	}
	if !txTypes[t.Type] {
		return fmt.Errorf("%w: tx type %q", ErrValidation, t.Type)
	}
	if !reAddr.MatchString(t.Signer) {
		return fmt.Errorf("%w: signer address", ErrValidation)
	}
	if t.ExpiresAt != 0 && t.ExpiresAt < t.CreatedAt {
		return fmt.Errorf("%w: expires_at before created_at", ErrValidation)
	}
	if t.FeeAsset == "" {
		return fmt.Errorf("%w: fee_asset required", ErrValidation)
	}
	if t.MaxFee > 1_000_000_000_000 {
		return fmt.Errorf("%w: max_fee too large", ErrValidation)
	}
	return nil
}

// Hash returns the lowercase hex SHA-256 of the full canonical envelope.
func (t Tx) Hash() (string, error) {
	b, err := Encode(t.ToCBOR())
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// SigningPayload returns the canonical preimage that signatures cover.
func (t Tx) SigningPayload(suite, purpose string) ([]byte, error) {
	return Encode(map[string]any{
		"dom": "arne-sig-v1", "network": t.Network, "chain": t.Chain,
		"suite": suite, "purpose": purpose, "expires_at": t.ExpiresAt,
		"body": t.body(),
	})
}

// Sign appends a signature for the given Ed25519 key.
func (t *Tx) Sign(kp *KeyPair) error {
	return t.SignPurpose(kp, "tx")
}

// SignPurpose signs with an explicit purpose tag.
func (t *Tx) SignPurpose(kp *KeyPair, purpose string) error {
	payload, err := t.SigningPayload(SuiteEd25519, purpose)
	if err != nil {
		return err
	}
	sig := kp.Sign(payload)
	t.Signatures = append(t.Signatures, Signature{Suite: SuiteEd25519, PubKey: kp.Public, Sig: sig})
	return nil
}

// Verify checks every signature, binding to the signer address.
func (t Tx) Verify() error {
	if len(t.Signatures) == 0 {
		return fmt.Errorf("%w: missing signature", ErrSignature)
	}
	for _, s := range t.Signatures {
		if s.Suite != SuiteEd25519 {
			return fmt.Errorf("%w: unsupported suite %q", ErrSignature, s.Suite)
		}
		if len(s.PubKey) != 32 || len(s.Sig) != 64 {
			return fmt.Errorf("%w: malformed key or signature", ErrSignature)
		}
		if AddressFromPubKey(s.PubKey, t.Network) != t.Signer {
			return fmt.Errorf("%w: signer does not match key", ErrSignature)
		}
		payload, err := t.SigningPayload(s.Suite, "tx")
		if err != nil {
			return err
		}
		if !VerifyEd25519(s.PubKey, payload, s.Sig) {
			return fmt.Errorf("%w: bad signature", ErrSignature)
		}
	}
	return nil
}

func str(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("%w: missing %q", ErrValidation, key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: %q must be text", ErrValidation, key)
	}
	return s, nil
}

func num(m map[string]any, key string) (uint64, error) {
	v, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("%w: missing %q", ErrValidation, key)
	}
	u, ok := v.(uint64)
	if !ok {
		return 0, fmt.Errorf("%w: %q must be unsigned", ErrValidation, key)
	}
	return u, nil
}

func bytesField(m map[string]any, key string) ([]byte, error) {
	v, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("%w: missing %q", ErrValidation, key)
	}
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("%w: %q must be bytes", ErrValidation, key)
	}
	return b, nil
}

// SortedKeys is exported for deterministic serialization helpers.
func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HashHex is the lowercase hex SHA-256 of b.
func HashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
