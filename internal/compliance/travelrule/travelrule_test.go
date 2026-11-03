package travelrule

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

type vectorFile struct {
	Seed    string       `json:"seed"`
	Network string       `json:"network"`
	Cases   []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name      string   `json:"name"`
	Threshold uint64   `json:"threshold"`
	Transport string   `json:"transport"`
	Transfer  Transfer `json:"transfer"`
	Expect    struct {
		Required  bool   `json:"required"`
		Status    Status `json:"status"`
		Reason    string `json:"reason"`
		Error     string `json:"error"`
		Sender    string `json:"sender"`
		Signature string `json:"signature"`
	} `json:"expect"`
}

type recorder struct {
	sent []Envelope
	err  error
}

func (r *recorder) Send(_ context.Context, e Envelope) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, e)
	return nil
}

func loadVectors(t *testing.T) (vectorFile, *protocol.KeyPair) {
	t.Helper()
	var file vectorFile
	raw, err := os.ReadFile("../../../spec/vectors/travelrule.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(file.Seed)
	if err != nil {
		t.Fatal(err)
	}
	key, err := protocol.KeyPairFromPrivate(seed, file.Network)
	if err != nil {
		t.Fatal(err)
	}
	return file, key
}

func TestVectors(t *testing.T) {
	file, key := loadVectors(t)
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Expect.Error != "" {
				_, err := Seal(c.Transfer, key)
				if err == nil || !strings.Contains(err.Error(), c.Expect.Error) {
					t.Fatalf("error = %v, want %q", err, c.Expect.Error)
				}
				return
			}

			transport := &recorder{}
			if c.Transport == "unavailable" {
				transport.err = errors.New("timeout")
			}
			decision, err := Exchange(context.Background(), Policy{Threshold: c.Threshold}, c.Transfer, key, transport, 42)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Required != c.Expect.Required ||
				decision.Status != c.Expect.Status ||
				decision.Reason != c.Expect.Reason {
				t.Fatalf("decision = %+v, want required=%v status=%q reason=%q",
					decision, c.Expect.Required, c.Expect.Status, c.Expect.Reason)
			}
			if decision.At != 42 || decision.Threshold != c.Threshold {
				t.Fatalf("decision = %+v", decision)
			}

			if c.Expect.Sender == "" {
				if len(transport.sent) != 0 {
					t.Fatalf("sent %d envelopes below threshold", len(transport.sent))
				}
				return
			}
			if len(transport.sent) != 1 {
				t.Fatalf("sent %d envelopes, want 1", len(transport.sent))
			}
			envelope := transport.sent[0]
			if envelope.Sender != c.Expect.Sender || envelope.Signature != c.Expect.Signature {
				t.Fatalf("envelope = %s/%s, want %s/%s",
					envelope.Sender, envelope.Signature, c.Expect.Sender, c.Expect.Signature)
			}
			opened, err := Open(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(opened, c.Transfer) {
				t.Fatalf("opened transfer = %+v", opened)
			}
		})
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	file, key := loadVectors(t)
	c := file.Cases[0]
	envelope, err := Seal(c.Transfer, key)
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(envelope.Signature)
	if sig[0] == '0' {
		sig[0] = '1'
	} else {
		sig[0] = '0'
	}
	envelope.Signature = string(sig)
	if _, err := Open(*envelope); !errors.Is(err, ErrSignature) {
		t.Fatalf("error = %v, want ErrSignature", err)
	}

	envelope.Signature = c.Expect.Signature
	envelope.Version = "ivms999"
	if _, err := Open(*envelope); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestSealRejectsMissingKey(t *testing.T) {
	file, _ := loadVectors(t)
	if _, err := Seal(file.Cases[0].Transfer, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if _, err := Seal(file.Cases[0].Transfer, &protocol.KeyPair{Private: ed25519.PrivateKey{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}
