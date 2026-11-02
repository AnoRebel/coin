package lightclient

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/crypto/bls"
	"github.com/anorebel/anorebelcoin/internal/smt"
)

type blsVector struct {
	Validators []struct {
		ID        string `json:"id"`
		Secret    string `json:"secret"`
		PublicKey string `json:"public_key"`
	} `json:"validators"`
}

type vectorFile struct {
	Network       string `json:"network"`
	TrustedHeight uint64 `json:"trusted_height"`
	Validators    []struct {
		ID    string `json:"id"`
		Power uint64 `json:"power"`
	} `json:"validators"`
	Certificate struct {
		Height    uint64 `json:"height"`
		Round     uint32 `json:"round"`
		BlockHash string `json:"block_hash"`
	} `json:"certificate"`
	Cases []struct {
		Name    string   `json:"name"`
		Height  uint64   `json:"height"`
		Signers []string `json:"signers"`
		Forge   string   `json:"forge"`
		Unknown string   `json:"unknown"`
		Expect  struct {
			Error  string `json:"error"`
			Signed uint64 `json:"signed"`
		} `json:"expect"`
	} `json:"cases"`
}

var secrets map[string][]byte
var publicKeys map[string]string

func TestCertificateVectors(t *testing.T) {
	loadKeys(t)
	var file vectorFile
	loadJSON(t, "../../spec/vectors/lightclient.json", &file)

	trusted := Set{Height: file.TrustedHeight}
	for _, v := range file.Validators {
		trusted.Validators = append(trusted.Validators, Validator{
			ID: v.ID, PublicKey: publicKeys[v.ID], Power: v.Power,
		})
	}
	if err := trusted.Validate(); err != nil {
		t.Fatal(err)
	}

	state := smt.New()
	state.Set("balance/a/arce", []byte("100"))
	stateRoot := state.Root()
	stateRootHex := hex.EncodeToString(stateRoot[:])
	proof := state.Prove("balance/a/arce")

	next := Set{Height: file.Certificate.Height + 1, Validators: trusted.Validators}
	nextRoot := next.Root()

	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			height := file.Certificate.Height
			if c.Height != 0 {
				height = c.Height
			}
			cert := Certificate{
				Height: height, Round: file.Certificate.Round,
				BlockHash: file.Certificate.BlockHash, StateRoot: stateRootHex,
				NextValidatorsRoot: nextRoot,
			}
			for _, signer := range c.Signers {
				cert.Signatures = append(cert.Signatures, Signature{
					Validator: signer, Signature: sign(signer, cert),
				})
			}
			if c.Forge != "" {
				cert.Signatures[len(cert.Signatures)-1].Signature = sign(c.Forge, cert)
			}
			if c.Unknown != "" {
				cert.Signatures = append(cert.Signatures, Signature{
					Validator: c.Unknown, Signature: sign("v1", cert),
				})
			}
			signed, err := Verify(file.Network, trusted, cert)
			if code := errorCode(err); code != c.Expect.Error {
				t.Fatalf("error %q (%v), want %q", code, err, c.Expect.Error)
			}
			if err == nil && signed != c.Expect.Signed {
				t.Fatalf("signed power = %d, want %d", signed, c.Expect.Signed)
			}
			if err == nil {
				if err := VerifyState(cert, "balance/a/arce", []byte("100"), proof); err != nil {
					t.Fatal(err)
				}
				if _, err := Advance(file.Network, trusted, cert, next); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestValidatorSetProof(t *testing.T) {
	loadKeys(t)
	set := Set{Height: 1, Validators: []Validator{
		{ID: "v1", PublicKey: publicKeys["v1"], Power: 2},
		{ID: "v2", PublicKey: publicKeys["v2"], Power: 1},
	}}
	root := set.Root()
	proof, ok := set.Prove("v2")
	if !ok {
		t.Fatal("no proof")
	}
	if err := VerifyMember(root, set.Validators[1], proof); err != nil {
		t.Fatal(err)
	}
	tampered := set.Validators[1]
	tampered.Power = 99
	if err := VerifyMember(root, tampered, proof); !errors.Is(err, ErrProof) {
		t.Fatalf("tampered member = %v", err)
	}
	tamperedRoot := "00" + root[2:]
	if err := VerifyMember(tamperedRoot, set.Validators[1], proof); !errors.Is(err, ErrProof) {
		t.Fatalf("tampered root = %v", err)
	}
}

func TestStateProofRejectsWrongValue(t *testing.T) {
	state := smt.New()
	state.Set("k", []byte("v"))
	root := state.Root()
	cert := Certificate{StateRoot: hex.EncodeToString(root[:])}
	proof := state.Prove("k")
	if err := VerifyState(cert, "k", []byte("v"), proof); err != nil {
		t.Fatal(err)
	}
	if err := VerifyState(cert, "k", []byte("other"), proof); !errors.Is(err, ErrProof) {
		t.Fatalf("wrong value = %v", err)
	}
}

func loadKeys(t *testing.T) {
	t.Helper()
	var keys blsVector
	loadJSON(t, "../../spec/vectors/bls.json", &keys)
	secrets = map[string][]byte{}
	publicKeys = map[string]string{}
	for _, v := range keys.Validators {
		secret, err := hex.DecodeString(v.Secret)
		if err != nil {
			t.Fatal(err)
		}
		secrets[v.ID] = secret
		publicKeys[v.ID] = v.PublicKey
	}
}

func sign(id string, cert Certificate) string {
	sig, err := bls.Sign(secrets[id], Message("arce:devnet", cert))
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(sig)
}

func errorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrOutdated):
		return "outdated"
	case errors.Is(err, ErrQuorum):
		return "insufficient quorum"
	case errors.Is(err, ErrForged):
		return "forged"
	case errors.Is(err, ErrUnknown):
		return "unknown validator"
	case errors.Is(err, ErrDuplicate):
		return "duplicate validator"
	case errors.Is(err, ErrProof):
		return "invalid proof"
	default:
		return "invalid"
	}
}

func loadJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}
