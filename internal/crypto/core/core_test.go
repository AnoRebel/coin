//go:build cryptocore

package core

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type coreVectors struct {
	SHA256 []struct {
		Input  string `json:"input_hex"`
		Digest string `json:"digest"`
	} `json:"sha256"`
	BLAKE3 []struct {
		Input  string `json:"input_hex"`
		Digest string `json:"digest"`
	} `json:"blake3"`
	Ed25519 struct {
		Seed      string `json:"seed"`
		PublicKey string `json:"public_key"`
		Message   string `json:"message_hex"`
		Signature string `json:"signature"`
	} `json:"ed25519"`
}

type blsVectors struct {
	Message    string `json:"message"`
	Validators []struct {
		ID        string `json:"id"`
		Seed      string `json:"seed"`
		Secret    string `json:"secret"`
		PublicKey string `json:"public_key"`
	} `json:"validators"`
	Aggregate struct {
		Validators []string `json:"validators"`
		Signature  string   `json:"signature"`
	} `json:"aggregate"`
}

type hybridVectors struct {
	Message string `json:"message"`
	Seed    string `json:"seed"`
	Key     struct {
		MLDSA string `json:"ml_dsa_65"`
	} `json:"key"`
	Signature struct {
		MLDSA string `json:"ml_dsa_65"`
	} `json:"signature"`
}

func load[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", name))
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	out, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHashes(t *testing.T) {
	doc := load[coreVectors](t, "core.json")
	for _, c := range doc.SHA256 {
		got, err := SHA256(mustHex(t, c.Input))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got[:]) != c.Digest {
			t.Fatalf("sha256(%s) = %x, want %s", c.Input, got, c.Digest)
		}
	}
	for _, c := range doc.BLAKE3 {
		got, err := BLAKE3(mustHex(t, c.Input))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got[:]) != c.Digest {
			t.Fatalf("blake3(%s) = %x, want %s", c.Input, got, c.Digest)
		}
	}
}

func TestEd25519(t *testing.T) {
	doc := load[coreVectors](t, "core.json").Ed25519
	seed := mustHex(t, doc.Seed)
	publicKey, err := Ed25519PublicKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(publicKey) != doc.PublicKey {
		t.Fatalf("public key = %x, want %s", publicKey, doc.PublicKey)
	}
	message := mustHex(t, doc.Message)
	signature, err := Ed25519Sign(seed, message)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(signature) != doc.Signature {
		t.Fatalf("signature = %x, want %s", signature, doc.Signature)
	}
	ok, err := Ed25519Verify(publicKey, message, signature)
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	ok, err = Ed25519Verify(publicKey, []byte("other"), signature)
	if err != nil || ok {
		t.Fatalf("wrong message verify = %v, %v", ok, err)
	}
}

func TestBLS(t *testing.T) {
	doc := load[blsVectors](t, "bls.json")
	message := []byte(doc.Message)
	secrets := map[string][]byte{}
	publicKeys := map[string][]byte{}
	signatures := map[string][]byte{}
	for _, v := range doc.Validators {
		secret, err := BLSSecretFromSeed([]byte(v.Seed))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(secret) != v.Secret {
			t.Fatalf("%s secret = %x, want %s", v.ID, secret, v.Secret)
		}
		publicKey, err := BLSPublicKey(secret)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(publicKey) != v.PublicKey {
			t.Fatalf("%s public key = %x, want %s", v.ID, publicKey, v.PublicKey)
		}
		signature, err := BLSSign(secret, message)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := BLSVerify(publicKey, message, signature)
		if err != nil || !ok {
			t.Fatalf("%s verify = %v, %v", v.ID, ok, err)
		}
		secrets[v.ID] = secret
		publicKeys[v.ID] = publicKey
		signatures[v.ID] = signature
	}

	var sigs [][]byte
	var pubs [][]byte
	for _, id := range doc.Aggregate.Validators {
		sigs = append(sigs, signatures[id])
		pubs = append(pubs, publicKeys[id])
	}
	aggregate, err := BLSAggregate(sigs)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(aggregate) != doc.Aggregate.Signature {
		t.Fatalf("aggregate = %x, want %s", aggregate, doc.Aggregate.Signature)
	}
	ok, err := BLSAggregateVerify(pubs, message, aggregate)
	if err != nil || !ok {
		t.Fatalf("aggregate verify = %v, %v", ok, err)
	}
	ok, err = BLSAggregateVerify(pubs, []byte("other"), aggregate)
	if err != nil || ok {
		t.Fatalf("wrong message aggregate verify = %v, %v", ok, err)
	}
}

func TestMLDSA(t *testing.T) {
	doc := load[hybridVectors](t, "hybrid.json")
	seed := mustHex(t, doc.Seed)
	publicKey, err := MLDSAPublicKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(publicKey) != doc.Key.MLDSA {
		t.Fatalf("public key = %x, want %s", publicKey, doc.Key.MLDSA)
	}
	message := []byte(doc.Message)
	signature, err := MLDSASign(seed, message, []byte("arne-hybrid-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(signature) != doc.Signature.MLDSA {
		t.Fatalf("signature = %x, want %s", signature, doc.Signature.MLDSA)
	}
	ok, err := MLDSAVerify(publicKey, message, []byte("arne-hybrid-v1"), signature)
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	ok, err = MLDSAVerify(publicKey, message, []byte("other"), signature)
	if err != nil || ok {
		t.Fatalf("wrong context verify = %v, %v", ok, err)
	}
}

func TestInvalidInputIsRejected(t *testing.T) {
	if _, err := Ed25519PublicKey([]byte("short")); err == nil {
		t.Fatal("short ed25519 seed accepted")
	}
	if _, err := BLSSign([]byte("short"), []byte("m")); err == nil {
		t.Fatal("short BLS secret accepted")
	}
	if _, err := BLSAggregate(nil); err == nil {
		t.Fatal("empty aggregate accepted")
	}
	if _, err := MLDSAPublicKey([]byte("short")); err == nil {
		t.Fatal("short ML-DSA seed accepted")
	}
}
