package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/workspace"
)

func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func oidcFor(t *testing.T, key *rsa.PrivateKey) *OIDCConfig {
	t.Helper()
	return &OIDCConfig{
		Issuer:   "https://id.example.test",
		Audience: "arne-node",
		Keys:     map[string]*rsa.PublicKey{"k1": &key.PublicKey},
	}
}

func claims(overrides map[string]any) map[string]any {
	now := time.Now()
	base := map[string]any{
		"iss": "https://id.example.test", "aud": "arne-node",
		"sub": "alice", "exp": now.Add(time.Hour).Unix(), "scope": "write",
	}
	for key, value := range overrides {
		base[key] = value
	}
	return base
}

func authRequest(t *testing.T, handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.Header.Set("Authorization", token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAPIKeyScopes(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{APIKeys: map[string][]string{
		"read-only": {"read"},
		"writer":    {"write"},
		"operator":  {"admin"},
	}})

	cases := []struct {
		name   string
		token  string
		status int
	}{
		{"read key cannot produce", "Bearer read-only", http.StatusForbidden},
		{"write key cannot produce", "Bearer writer", http.StatusForbidden},
		{"admin key produces", "Bearer operator", http.StatusOK},
		{"missing key", "", http.StatusUnauthorized},
		{"unknown key", "Bearer nope", http.StatusUnauthorized},
		{"write key passes the write gate", "Bearer writer", http.StatusBadRequest},
	}
	paths := map[string]string{
		"read key cannot produce":         http.MethodPost + " /v1/blocks/produce",
		"write key cannot produce":        http.MethodPost + " /v1/blocks/produce",
		"admin key produces":              http.MethodPost + " /v1/blocks/produce",
		"missing key":                     http.MethodPost + " /v1/blocks/produce",
		"unknown key":                     http.MethodPost + " /v1/blocks/produce",
		"write key passes the write gate": http.MethodPost + " /v1/transactions",
	}
	for _, tc := range cases {
		method, path, _ := strings.Cut(paths[tc.name], " ")
		recorder := authRequest(t, e, method, path, tc.token)
		if recorder.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.name, recorder.Code, recorder.Body)
		}
		if tc.status == http.StatusForbidden {
			var body struct{ Error struct{ Code string } }
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			if body.Error.Code != "FORBIDDEN" {
				t.Fatalf("%s code = %q", tc.name, body.Error.Code)
			}
		}
	}
}

func TestOIDCTokenValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := newTestNode(t, 0)
	e := New(n, Config{OIDC: oidcFor(t, key)})
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	valid := signJWT(t, key, "k1", claims(map[string]any{"scope": "admin"}))
	if recorder := authRequest(t, e, http.MethodPost, "/v1/blocks/produce", "Bearer "+valid); recorder.Code != http.StatusOK {
		t.Fatalf("valid token: %d %s", recorder.Code, recorder.Body)
	}

	for name, token := range map[string]string{
		"wrong key":       signJWT(t, other, "k1", claims(nil)),
		"unknown kid":     signJWT(t, key, "k2", claims(nil)),
		"wrong issuer":    signJWT(t, key, "k1", claims(map[string]any{"iss": "https://evil.example.test"})),
		"wrong audience":  signJWT(t, key, "k1", claims(map[string]any{"aud": "someone-else"})),
		"expired":         signJWT(t, key, "k1", claims(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})),
		"no expiry":       signJWT(t, key, "k1", claims(map[string]any{"exp": nil})),
		"not yet active":  signJWT(t, key, "k1", claims(map[string]any{"nbf": time.Now().Add(time.Hour).Unix()})),
		"missing subject": signJWT(t, key, "k1", claims(map[string]any{"sub": ""})),
		"malformed":       "not-a-jwt",
		"unsigned":        base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://id.example.test","aud":"arne-node","sub":"alice","exp":`+big.NewInt(time.Now().Add(time.Hour).Unix()).String()+`}`)) + ".",
	} {
		recorder := authRequest(t, e, http.MethodPost, "/v1/blocks/produce", "Bearer "+token)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, recorder.Code, recorder.Body)
		}
	}
}

func TestOIDCScopeEnforcement(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := newTestNode(t, 0)
	e := New(n, Config{OIDC: oidcFor(t, key)})

	readOnly := signJWT(t, key, "k1", claims(map[string]any{"scope": "read"}))
	recorder := authRequest(t, e, http.MethodPost, "/v1/blocks/produce", "Bearer "+readOnly)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("read-only token: %d %s", recorder.Code, recorder.Body)
	}

	scpArray := signJWT(t, key, "k1", claims(map[string]any{
		"scope": nil, "scp": []string{"read", "write"},
	}))
	recorder = authRequest(t, e, http.MethodPost, "/v1/transactions", "Bearer "+scpArray)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("scp array token: %d %s", recorder.Code, recorder.Body)
	}
}

func TestOIDCKeySetLoading(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	modulus := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	document := `{"keys":[{"kty":"RSA","kid":"k1","n":"` + modulus + `","e":"` + exponent + `"}]}`
	path := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	oidc, err := LoadOIDC("https://id.example.test", "arne-node", path)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := oidc.Verify(signJWT(t, key, "k1", claims(nil)), time.Now())
	if err != nil || cred.Subject != "alice" {
		t.Fatalf("verify: %v %+v", err, cred)
	}

	if _, err := LoadOIDC("", "arne-node", path); err == nil {
		t.Fatal("missing issuer accepted")
	}
	if _, err := LoadOIDC("https://id.example.test", "arne-node", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing jwks accepted")
	}
	for name, body := range map[string]string{
		"no keys":       `{"keys":[]}`,
		"not rsa":       `{"keys":[{"kty":"EC","kid":"k1","n":"` + modulus + `","e":"` + exponent + `"}]}`,
		"unnamed":       `{"keys":[{"kty":"RSA","n":"` + modulus + `","e":"` + exponent + `"}]}`,
		"duplicate kid": `{"keys":[{"kty":"RSA","kid":"k1","n":"` + modulus + `","e":"` + exponent + `"},{"kty":"RSA","kid":"k1","n":"` + modulus + `","e":"` + exponent + `"}]}`,
		"bad exponent":  `{"keys":[{"kty":"RSA","kid":"k1","n":"` + modulus + `","e":"` + base64.RawURLEncoding.EncodeToString([]byte{4}) + `"}]}`,
	} {
		bad := filepath.Join(t.TempDir(), "jwks.json")
		if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOIDC("https://id.example.test", "arne-node", bad); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func mtlsRequest(t *testing.T, handler http.Handler, cn string, adminMTLS bool) *httptest.ResponseRecorder {
	t.Helper()
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: cn}, SerialNumber: big.NewInt(7)}
	request := httptest.NewRequest(http.MethodPost, "/v1/blocks/produce", nil)
	request.TLS = &tls.ConnectionState{
		VerifiedChains:   [][]*x509.Certificate{{cert}},
		PeerCertificates: []*x509.Certificate{cert},
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestMutualTLSGrantsAdminOnly(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{AdminMTLS: true})
	if recorder := mtlsRequest(t, e, "ops", true); recorder.Code != http.StatusOK {
		t.Fatalf("admin mTLS: %d %s", recorder.Code, recorder.Body)
	}
	// The certificate authenticates the caller but the credential is not a
	// general read/write key.
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "ops"}, SerialNumber: big.NewInt(7)}
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions", nil)
	request.TLS = &tls.ConnectionState{
		VerifiedChains:   [][]*x509.Certificate{{cert}},
		PeerCertificates: []*x509.Certificate{cert},
	}
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("mTLS write: %d %s", recorder.Code, recorder.Body)
	}

	// With auth enabled but AdminMTLS off, the certificate authenticates the
	// caller without granting admin.
	plain := New(n, Config{APIKeys: map[string][]string{"some-key": {"read"}}})
	if recorder := mtlsRequest(t, plain, "ops", false); recorder.Code != http.StatusForbidden {
		t.Fatalf("mTLS without admin: %d %s", recorder.Code, recorder.Body)
	}
}

func TestRateLimitIsPerCredential(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{
		RateLimit: 1,
		APIKeys:   map[string][]string{"key-alpha": {"read"}, "key-bravo": {"read"}},
	})
	for i := 0; i < 2; i++ {
		if recorder := authRequest(t, e, http.MethodGet, "/v1/health", "Bearer key-alpha"); recorder.Code != http.StatusOK {
			t.Fatalf("key alpha request %d: %d %s", i, recorder.Code, recorder.Body)
		}
	}
	if recorder := authRequest(t, e, http.MethodGet, "/v1/health", "Bearer key-alpha"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("key alpha over budget: %d %s", recorder.Code, recorder.Body)
	}
	if recorder := authRequest(t, e, http.MethodGet, "/v1/health", "Bearer key-bravo"); recorder.Code != http.StatusOK {
		t.Fatalf("key bravo shares key alpha's budget: %d %s", recorder.Code, recorder.Body)
	}
}

func TestWorkspacePrincipalNeverGrantsAdmin(t *testing.T) {
	ws, err := workspace.New(
		map[string]workspace.Role{"alice": workspace.RoleOwner},
		map[string]string{"principal-key": "alice"},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := newTestNode(t, 0)
	// An API key makes auth active, so the admin route enforces its scope.
	e := New(n, Config{Workspace: ws, APIKeys: map[string][]string{"service-key": {"read"}}})
	recorder := authRequest(t, e, http.MethodPost, "/v1/blocks/produce", "Bearer principal-key")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("workspace principal: %d %s", recorder.Code, recorder.Body)
	}
}

func TestMalformedAuthorizationHeader(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{APIKeys: map[string][]string{"abc123": {"read"}}})
	for _, header := range []string{"Basic abc", "Bearer ", "bearer abc123"} {
		recorder := authRequest(t, e, http.MethodGet, "/v1/health", header)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%q: %d %s", header, recorder.Code, recorder.Body)
		}
	}
}
