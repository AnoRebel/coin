package api

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/anorebel/anorebelcoin/internal/workspace"
)

// credential is the authenticated caller. Exactly one source wins: an API key
// or a JWT. A verified client certificate only unlocks admin routes; it is not
// a general purpose read/write credential.
type credential struct {
	Subject   string
	Scopes    []string
	Via       string
	MTLSAdmin bool
}

func (c *credential) hasScope(scope string) bool {
	for _, granted := range c.Scopes {
		if granted == scope || granted == "admin" {
			return true
		}
	}
	return false
}

// authenticator resolves the Authorization header and verified client
// certificate. Construction validates the static configuration once.
type authenticator struct {
	keys      map[string][]string
	oidc      *OIDCConfig
	workspace *workspace.Workspace
	adminMTLS bool
}

func newAuthenticator(cfg Config) *authenticator {
	return &authenticator{
		keys: cfg.APIKeys, oidc: cfg.OIDC, workspace: cfg.Workspace, adminMTLS: cfg.AdminMTLS,
	}
}

// enabled reports whether any credential is required or accepted. With no
// credential configured the API stays open, which is the devnet default.
func (a *authenticator) enabled() bool {
	return len(a.keys) > 0 || a.oidc != nil || a.adminMTLS
}

// Authenticate returns nil for an anonymous request when no credential was
// presented. A presented but invalid credential is an error, never a silent
// downgrade to anonymous.
func (a *authenticator) Authenticate(r *http.Request) (*credential, error) {
	header := r.Header.Get("Authorization")
	token := ""
	if header != "" {
		if !strings.HasPrefix(header, "Bearer ") {
			return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "Authorization must be a Bearer token")
		}
		token = strings.TrimPrefix(header, "Bearer ")
		if token == "" {
			return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "empty bearer token")
		}
	}

	var cred *credential
	if token != "" {
		if scopes, ok := a.lookupKey(token); ok {
			cred = &credential{Subject: "api-key:" + keyPrefix(token), Scopes: scopes, Via: "api-key"}
		} else if a.oidc != nil {
			jwt, err := a.oidc.Verify(token, time.Now())
			if err != nil {
				return nil, err
			}
			cred = jwt
		} else if a.workspace != nil {
			// Workspace principals are keys with a role; the role check still
			// happens in workspaceAction. They never carry the admin scope.
			if actor, ok := a.workspace.Actor(token); ok {
				cred = &credential{Subject: actor, Scopes: []string{"read", "write"}, Via: "workspace"}
			}
		}
		if cred == nil && a.enabled() {
			return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid API key")
		}
	}

	if verifiedCertificate(r) {
		if cred == nil {
			cred = &credential{Subject: certificateSubject(r), Via: "mtls"}
		}
		cred.MTLSAdmin = a.adminMTLS
	}
	return cred, nil
}

func (a *authenticator) lookupKey(token string) ([]string, bool) {
	scopes, ok := a.keys[token]
	if !ok {
		return nil, false
	}
	return scopes, true
}

// keyPrefix keeps a key out of logs while still letting an operator tell two
// keys apart.
func keyPrefix(key string) string {
	if len(key) > 6 {
		return key[:6] + "…"
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

func verifiedCertificate(r *http.Request) bool {
	return r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.PeerCertificates) > 0
}

func certificateSubject(r *http.Request) string {
	cert := r.TLS.PeerCertificates[0]
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName
	}
	return cert.SerialNumber.Text(16)
}

// authenticate middleware resolves the credential once for the whole request.
func (s *Server) authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		cred, err := s.auth.Authenticate(c.Request())
		if err != nil {
			return err
		}
		if cred != nil {
			c.Set("credential", cred)
		}
		return next(c)
	}
}

func credentialFrom(c *echo.Context) *credential {
	cred, _ := c.Get("credential").(*credential)
	return cred
}

// rateKey scopes a rate limit to the credential, so one client cannot spend
// another client's budget from the same address.
func rateKey(c *echo.Context) string {
	if cred := credentialFrom(c); cred != nil && cred.Subject != "" {
		return "credential:" + cred.Subject
	}
	return "ip:" + c.RealIP()
}

// --- OIDC / JWT ---

// OIDCConfig validates RS256 JWTs against a static key set. Keys come from a
// JWKS document; discovery and key rotation are operator responsibilities.
type OIDCConfig struct {
	Issuer   string
	Audience string
	Keys     map[string]*rsa.PublicKey
}

// LoadOIDC reads a JWKS file and validates the issuer and audience.
func LoadOIDC(issuer, audience, jwksPath string) (*OIDCConfig, error) {
	if issuer == "" || audience == "" || jwksPath == "" {
		return nil, errors.New("oidc: issuer, audience, and jwks are required together")
	}
	raw, err := os.ReadFile(jwksPath)
	if err != nil {
		return nil, fmt.Errorf("oidc: %w", err)
	}
	var document struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("oidc: invalid jwks: %w", err)
	}
	if len(document.Keys) == 0 {
		return nil, errors.New("oidc: jwks has no keys")
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		if key.Kty != "RSA" || key.Kid == "" {
			return nil, errors.New("oidc: only named RSA keys are supported")
		}
		if _, exists := keys[key.Kid]; exists {
			return nil, fmt.Errorf("oidc: duplicate kid %q", key.Kid)
		}
		modulus, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil || len(modulus) == 0 {
			return nil, fmt.Errorf("oidc: key %q has an invalid modulus", key.Kid)
		}
		exponent, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil || len(exponent) == 0 || len(exponent) > 4 {
			return nil, fmt.Errorf("oidc: key %q has an invalid exponent", key.Kid)
		}
		e := 0
		for _, b := range exponent {
			e = e<<8 | int(b)
		}
		if e < 3 || e%2 == 0 {
			return nil, fmt.Errorf("oidc: key %q has an invalid exponent", key.Kid)
		}
		keys[key.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
	}
	return &OIDCConfig{Issuer: issuer, Audience: audience, Keys: keys}, nil
}

// Verify checks an RS256 JWT and maps its claims to a credential. Unsigned,
// symmetric, and unknown algorithms are rejected to prevent key confusion.
func (o *OIDCConfig) Verify(token string, now time.Time) (*credential, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "malformed bearer token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "malformed bearer token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil || header.Alg != "RS256" {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "unsupported token algorithm")
	}
	key, ok := o.Keys[header.Kid]
	if !ok {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "unknown signing key")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "malformed bearer token")
	}
	hashed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hashed[:], signature); err != nil {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "invalid token signature")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "malformed bearer token")
	}
	var claims struct {
		Issuer    string          `json:"iss"`
		Audience  json.RawMessage `json:"aud"`
		Subject   string          `json:"sub"`
		Expires   int64           `json:"exp"`
		NotBefore int64           `json:"nbf"`
		Scope     string          `json:"scope"`
		Scopes    json.RawMessage `json:"scp"`
	}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "malformed bearer token")
	}
	if claims.Issuer != o.Issuer {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "untrusted token issuer")
	}
	audience, err := tokenAudience(claims.Audience)
	if err != nil || !contains(audience, o.Audience) {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "token audience mismatch")
	}
	if claims.Subject == "" {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "token has no subject")
	}
	if claims.Expires == 0 || now.Unix() >= claims.Expires {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "token expired")
	}
	if claims.NotBefore != 0 && now.Unix() < claims.NotBefore {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "token not active")
	}
	scopes := strings.Fields(claims.Scope)
	extra, err := tokenScopes(claims.Scopes)
	if err != nil {
		return nil, apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "malformed token scopes")
	}
	return &credential{Subject: claims.Subject, Scopes: dedupe(append(scopes, extra...)), Via: "jwt"}, nil
}

func tokenAudience(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("no audience")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func tokenScopes(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return strings.Fields(single), nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
