package signer

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type overrides struct {
	Network   *string `json:"network"`
	Account   *string `json:"account"`
	To        *string `json:"to"`
	Asset     *string `json:"asset"`
	Amount    *uint64 `json:"amount"`
	Fee       *uint64 `json:"fee"`
	FeeAsset  *string `json:"fee_asset"`
	Nonce     *uint64 `json:"nonce"`
	ExpiresAt *uint64 `json:"expires_at"`
	Memo      *string `json:"memo"`
}

type signerVectors struct {
	Details Details `json:"details"`
	Payload string  `json:"payload"`
	Invalid []struct {
		Name      string    `json:"name"`
		Overrides overrides `json:"overrides"`
	} `json:"invalid"`
}

func loadSignerVectors(t *testing.T) signerVectors {
	t.Helper()
	raw, err := os.ReadFile("../../spec/vectors/signer.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors signerVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func TestDetailsVectors(t *testing.T) {
	vectors := loadSignerVectors(t)
	if err := vectors.Details.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := string(vectors.Details.Payload()); got != vectors.Payload {
		t.Fatalf("payload mismatch:\n%s\nwant:\n%s", got, vectors.Payload)
	}
	for _, test := range vectors.Invalid {
		details := vectors.Details
		applyOverrides(&details, test.Overrides)
		if err := details.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want invalid, got %v", test.Name, err)
		}
	}
}

func applyOverrides(details *Details, overrides overrides) {
	if overrides.Network != nil {
		details.Network = *overrides.Network
	}
	if overrides.Account != nil {
		details.Account = *overrides.Account
	}
	if overrides.To != nil {
		details.To = *overrides.To
	}
	if overrides.Asset != nil {
		details.Asset = *overrides.Asset
	}
	if overrides.Amount != nil {
		details.Amount = *overrides.Amount
	}
	if overrides.Fee != nil {
		details.Fee = *overrides.Fee
	}
	if overrides.FeeAsset != nil {
		details.FeeAsset = *overrides.FeeAsset
	}
	if overrides.Nonce != nil {
		details.Nonce = *overrides.Nonce
	}
	if overrides.ExpiresAt != nil {
		details.ExpiresAt = *overrides.ExpiresAt
	}
	if overrides.Memo != nil {
		details.Memo = *overrides.Memo
	}
}

func TestSoftwareSigner(t *testing.T) {
	vectors := loadSignerVectors(t)
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 1
	software, err := NewSoftware(seed)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := software.Sign(vectors.Details)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(software.PublicKey(), vectors.Details, signature) {
		t.Fatal("software signature does not verify")
	}
	bad := append([]byte(nil), signature...)
	bad[0] ^= 0xff
	if Verify(software.PublicKey(), vectors.Details, bad) {
		t.Fatal("mutated signature verified")
	}
}

type fakeDevice struct {
	public     string
	suite      string
	display    string
	displayErr error
	signature  []byte
	signErr    error
}

func (d *fakeDevice) PublicKey() string { return d.public }
func (d *fakeDevice) Suite() string     { return d.suite }
func (d *fakeDevice) Display(text string) error {
	d.display = text
	return d.displayErr
}
func (d *fakeDevice) Sign([]byte) ([]byte, error) {
	return d.signature, d.signErr
}

func TestBadPublicKeys(t *testing.T) {
	vectors := loadSignerVectors(t)
	for _, key := range []string{"", "abcd", strings.Repeat("zz", 16), strings.Repeat("0", 66)} {
		if Verify(key, vectors.Details, make([]byte, ed25519.SignatureSize)) {
			t.Fatalf("%q verified", key)
		}
		if _, err := NewHardware(&fakeDevice{public: key, suite: SuiteEd25519}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q accepted: %v", key, err)
		}
	}
}

func TestHardwareSigner(t *testing.T) {
	vectors := loadSignerVectors(t)
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 2
	key := ed25519.NewKeyFromSeed(seed)
	public := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	payload := vectors.Details.Payload()
	device := &fakeDevice{
		public: public, suite: SuiteEd25519,
		signature: ed25519.Sign(key, payload),
	}
	hardware, err := NewHardware(device)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := hardware.Sign(vectors.Details)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(public, vectors.Details, signature) {
		t.Fatal("hardware signature does not verify")
	}
	if device.display != vectors.Details.Human() {
		t.Fatalf("device displayed %q", device.display)
	}
	if !strings.Contains(device.display, `memo: "invoice 42"`) {
		t.Fatalf("display lacks the memo: %q", device.display)
	}

	device.suite = "secp256k1-v1"
	if _, err := hardware.Sign(vectors.Details); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported suite: %v", err)
	}
	device.suite = SuiteEd25519
	device.displayErr = errors.New("rejected")
	if _, err := hardware.Sign(vectors.Details); !errors.Is(err, ErrDisplay) {
		t.Fatalf("display error: %v", err)
	}
	device.displayErr = nil
	device.signature = ed25519.Sign(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), payload)
	if _, err := hardware.Sign(vectors.Details); !errors.Is(err, ErrSignature) {
		t.Fatalf("bad signature: %v", err)
	}
}
