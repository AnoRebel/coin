// Package vc verifies W3C Verifiable Credentials 2.0 with selective
// disclosure, using the SD-JWT compact form.
//
// An issuer signs a payload whose claims are salted, hashed into `_sd`, and
// sent separately as disclosures. A holder presents only the disclosures a
// verifier needs, so a jurisdiction check does not reveal a name or an
// accreditation flag. Nothing here writes to the ledger; a caller stores only
// credential references and hashes on-chain.
package vc

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// headerJSON is fixed so Go and Python sign identical bytes.
const headerJSON = `{"alg":"EdDSA","typ":"vc+sd-jwt"}`

var (
	ErrInvalid       = errors.New("vc: invalid credential")
	ErrSignature     = errors.New("vc: bad signature")
	ErrExpired       = errors.New("vc: expired")
	ErrRevoked       = errors.New("vc: revoked")
	ErrClaimMissing  = errors.New("vc: required claim missing")
	ErrClaimMismatch = errors.New("vc: required claim mismatch")
)

// IssueOptions is the registered part of a credential.
type IssueOptions struct {
	Issuer    string            `json:"issuer"`
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	IssuedAt  int64             `json:"issued_at"`
	ExpiresAt int64             `json:"expires_at"`
	Salts     map[string][]byte `json:"salts,omitempty"`
}

// VerifyOptions is what the verifier asks for and checks.
type VerifyOptions struct {
	Now     int64          `json:"now"`
	Require map[string]any `json:"require,omitempty"`
	Revoked []string       `json:"revoked,omitempty"`
	// Disclose limits the claims copied into the result. Nil means all
	// disclosed claims.
	Disclose []string `json:"disclose,omitempty"`
}

// Result is the verified, minimal view of a credential.
type Result struct {
	Issuer    string         `json:"issuer"`
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	IssuedAt  int64          `json:"issued_at"`
	ExpiresAt int64          `json:"expires_at"`
	Claims    map[string]any `json:"claims"`
}

// Issue signs a credential. Every claim becomes a salted disclosure; Salts may
// pin them for reproducible input, and missing salts are generated.
func Issue(claims map[string]any, opts IssueOptions, key *protocol.KeyPair) (string, error) {
	if key == nil || len(key.Private) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("%w: signing key is required", ErrInvalid)
	}
	if opts.Issuer == "" || opts.ID == "" || opts.Type == "" {
		return "", fmt.Errorf("%w: issuer, id, and type are required", ErrInvalid)
	}
	if opts.ExpiresAt <= opts.IssuedAt {
		return "", fmt.Errorf("%w: expires_at must be after issued_at", ErrInvalid)
	}

	names := make([]string, 0, len(claims))
	for name := range claims {
		names = append(names, name)
	}
	sort.Strings(names)

	disclosures := make([]string, 0, len(names))
	digests := make([]any, 0, len(names))
	for _, name := range names {
		if name == "" {
			return "", fmt.Errorf("%w: empty claim name", ErrInvalid)
		}
		value, ok := normalizeValue(claims[name])
		if !ok {
			return "", fmt.Errorf("%w: claim %q must be a string, bool, or integer", ErrInvalid, name)
		}
		salt := opts.Salts[name]
		if len(salt) == 0 {
			salt = make([]byte, 16)
			if _, err := rand.Read(salt); err != nil {
				return "", err
			}
		}
		if len(salt) < 16 || len(salt) > 64 {
			return "", fmt.Errorf("%w: salt for %q must be 16..64 bytes", ErrInvalid, name)
		}
		disclosure, err := marshalJSON([]any{base64.RawURLEncoding.EncodeToString(salt), name, value})
		if err != nil {
			return "", err
		}
		disclosures = append(disclosures, disclosure)
		digests = append(digests, digest(disclosure))
	}

	payload, err := marshalJSON(map[string]any{
		"_sd": digests,
		"exp": opts.ExpiresAt,
		"iat": opts.IssuedAt,
		"iss": opts.Issuer,
		"jti": opts.ID,
		"vct": opts.Type,
	})
	if err != nil {
		return "", err
	}
	signing := encode(headerJSON) + "." + encode(payload)
	signature := ed25519.Sign(key.Private, []byte(signing))
	credential := signing + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(disclosures) > 0 {
		credential += "~" + strings.Join(disclosures, "~")
	}
	return credential, nil
}

// Present keeps only the named disclosures. An unavailable name is omitted, so
// the verifier's Require check reports it missing rather than the wrong value.
func Present(credential string, names ...string) string {
	parts := strings.Split(credential, "~")
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	kept := []string{parts[0]}
	for _, raw := range parts[1:] {
		if raw == "" {
			continue
		}
		if name, ok := disclosureName(raw); ok && want[name] {
			kept = append(kept, raw)
		}
	}
	return strings.Join(kept, "~")
}

// Verify checks the issuer signature, expiry, revocation, required claims, and
// every disclosure's commitment, then returns only the claims in Disclose.
func Verify(credential string, issuer ed25519.PublicKey, opts VerifyOptions) (*Result, error) {
	if len(issuer) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: issuer key is required", ErrInvalid)
	}
	parts := strings.Split(credential, "~")
	jwt := parts[0]
	segments := strings.Split(jwt, ".")
	if len(segments) != 3 {
		return nil, fmt.Errorf("%w: expected header.payload.signature", ErrInvalid)
	}
	header, err := decode(segments[0])
	if err != nil {
		return nil, fmt.Errorf("%w: header", ErrInvalid)
	}
	var headerMap map[string]any
	if err := unmarshalJSON(header, &headerMap); err != nil || headerMap["alg"] != "EdDSA" || headerMap["typ"] != "vc+sd-jwt" {
		return nil, fmt.Errorf("%w: unsupported header", ErrInvalid)
	}
	signature, err := decode(segments[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: signature", ErrInvalid)
	}
	if !ed25519.Verify(issuer, []byte(segments[0]+"."+segments[1]), signature) {
		return nil, ErrSignature
	}
	payloadBytes, err := decode(segments[1])
	if err != nil {
		return nil, fmt.Errorf("%w: payload", ErrInvalid)
	}
	var payload map[string]any
	if err := unmarshalJSON(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("%w: payload", ErrInvalid)
	}

	issuerID, ok := stringField(payload, "iss")
	if !ok {
		return nil, fmt.Errorf("%w: iss", ErrInvalid)
	}
	id, ok := stringField(payload, "jti")
	if !ok {
		return nil, fmt.Errorf("%w: jti", ErrInvalid)
	}
	credentialType, ok := stringField(payload, "vct")
	if !ok {
		return nil, fmt.Errorf("%w: vct", ErrInvalid)
	}
	issuedAt, ok := intField(payload, "iat")
	if !ok {
		return nil, fmt.Errorf("%w: iat", ErrInvalid)
	}
	expiresAt, ok := intField(payload, "exp")
	if !ok {
		return nil, fmt.Errorf("%w: exp", ErrInvalid)
	}

	digests, err := digestSet(payload["_sd"])
	if err != nil {
		return nil, err
	}
	claims := map[string]any{}
	for _, raw := range parts[1:] {
		if raw == "" {
			continue
		}
		_, name, value, err := parseDisclosure(raw)
		if err != nil {
			return nil, err
		}
		if !digests[digest(raw)] {
			return nil, fmt.Errorf("%w: unreferenced disclosure", ErrInvalid)
		}
		delete(digests, digest(raw))
		if _, seen := claims[name]; seen {
			return nil, fmt.Errorf("%w: duplicate claim %q", ErrInvalid, name)
		}
		claims[name] = value
	}

	now := opts.Now
	if now == 0 {
		now = time.Now().Unix()
	}
	if now >= expiresAt {
		return nil, ErrExpired
	}
	for _, revoked := range opts.Revoked {
		if revoked == id {
			return nil, ErrRevoked
		}
	}
	for name, want := range opts.Require {
		got, present := claims[name]
		if !present {
			return nil, fmt.Errorf("%w: %s", ErrClaimMissing, name)
		}
		if !equalValue(got, want) {
			return nil, fmt.Errorf("%w: %s", ErrClaimMismatch, name)
		}
	}

	visible := map[string]any{}
	if opts.Disclose == nil {
		for name, value := range claims {
			visible[name] = value
		}
	} else {
		for _, name := range opts.Disclose {
			if value, ok := claims[name]; ok {
				visible[name] = value
			}
		}
	}
	return &Result{
		Issuer: issuerID, ID: id, Type: credentialType,
		IssuedAt: issuedAt, ExpiresAt: expiresAt, Claims: visible,
	}, nil
}

func digestSet(raw any) (map[string]bool, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: _sd must be an array", ErrInvalid)
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok || len(value) != 43 {
			return nil, fmt.Errorf("%w: _sd entry", ErrInvalid)
		}
		if _, err := decode(value); err != nil {
			return nil, fmt.Errorf("%w: _sd entry", ErrInvalid)
		}
		if set[value] {
			return nil, fmt.Errorf("%w: duplicate _sd entry", ErrInvalid)
		}
		set[value] = true
	}
	return set, nil
}

func parseDisclosure(raw string) (string, string, any, error) {
	var parts []any
	if err := unmarshalJSON([]byte(raw), &parts); err != nil || len(parts) != 3 {
		return "", "", nil, fmt.Errorf("%w: disclosure", ErrInvalid)
	}
	salt, ok := parts[0].(string)
	if !ok {
		return "", "", nil, fmt.Errorf("%w: disclosure salt", ErrInvalid)
	}
	saltBytes, err := decode(salt)
	if err != nil || len(saltBytes) < 16 || len(saltBytes) > 64 {
		return "", "", nil, fmt.Errorf("%w: disclosure salt", ErrInvalid)
	}
	name, ok := parts[1].(string)
	if !ok || name == "" {
		return "", "", nil, fmt.Errorf("%w: disclosure name", ErrInvalid)
	}
	value, ok := normalizeValue(parts[2])
	if !ok {
		return "", "", nil, fmt.Errorf("%w: disclosure value", ErrInvalid)
	}
	return salt, name, value, nil
}

func disclosureName(raw string) (string, bool) {
	_, name, _, err := parseDisclosure(raw)
	return name, err == nil
}

func digest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encode(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }

func decode(raw string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(raw) }

func marshalJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func unmarshalJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func normalizeValue(raw any) (any, bool) {
	switch value := raw.(type) {
	case string:
		return value, true
	case bool:
		return value, true
	case int:
		return int64(value), true
	case int64:
		return value, true
	case json.Number:
		n, err := strconv.ParseInt(string(value), 10, 64)
		return n, err == nil
	default:
		return nil, false
	}
}

func equalValue(got, want any) bool {
	g, ok := normalizeValue(got)
	if !ok {
		return false
	}
	w, ok := normalizeValue(want)
	return ok && g == w
}

func stringField(m map[string]any, name string) (string, bool) {
	value, ok := m[name].(string)
	return value, ok && value != ""
}

func intField(m map[string]any, name string) (int64, bool) {
	value, ok := normalizeValue(m[name])
	if !ok {
		return 0, false
	}
	n, ok := value.(int64)
	return n, ok
}
