package recovery

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type recoveryVectors struct {
	Seeds  map[string]string `json:"seeds"`
	Policy struct {
		Account   string `json:"account"`
		Threshold int    `json:"threshold"`
		Delay     uint64 `json:"delay"`
		Guardians []struct {
			ID   string `json:"id"`
			Seed string `json:"seed"`
		} `json:"guardians"`
	} `json:"policy"`
	NewSeed string `json:"new_seed"`
	Cases   []struct {
		Name      string `json:"name"`
		Nonce     string `json:"nonce"`
		CreatedAt uint64 `json:"created_at"`
		Ops       []struct {
			Op       string `json:"op"`
			Guardian string `json:"guardian"`
			At       uint64 `json:"at"`
		} `json:"ops"`
		Error       string `json:"error"`
		ExpectState string `json:"expect_state"`
		ExpectCount int    `json:"expect_requests"`
	} `json:"cases"`
	InvalidPolicies []struct {
		Name      string   `json:"name"`
		Threshold int      `json:"threshold"`
		Delay     *uint64  `json:"delay"`
		Guardians []string `json:"guardians"`
	} `json:"invalid_policies"`
}

func loadRecoveryVectors(t *testing.T) recoveryVectors {
	t.Helper()
	raw, err := os.ReadFile("../../spec/vectors/recovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors recoveryVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func publicKey(t *testing.T, seedHex string) string {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("bad seed %q", seedHex)
	}
	return hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
}

func seedSign(t *testing.T, seedHex string, payload []byte) string {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("bad seed %q", seedHex)
	}
	return hex.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), payload))
}

func basePolicy(t *testing.T, vectors recoveryVectors) Policy {
	t.Helper()
	policy := Policy{
		Account:        vectors.Policy.Account,
		OwnerPublicKey: publicKey(t, vectors.Seeds["owner"]),
		Threshold:      vectors.Policy.Threshold,
		Delay:          vectors.Policy.Delay,
	}
	for _, guardian := range vectors.Policy.Guardians {
		policy.Guardians = append(policy.Guardians, Guardian{
			ID: guardian.ID, PublicKey: publicKey(t, guardian.Seed),
		})
	}
	return policy
}

func TestPolicyVectors(t *testing.T) {
	vectors := loadRecoveryVectors(t)
	if _, err := New(basePolicy(t, vectors)); err != nil {
		t.Fatalf("base policy: %v", err)
	}
	for _, test := range vectors.InvalidPolicies {
		policy := Policy{
			Account:        vectors.Policy.Account,
			OwnerPublicKey: publicKey(t, vectors.Seeds["owner"]),
			Threshold:      test.Threshold,
			Delay:          vectors.Policy.Delay,
		}
		if test.Delay != nil {
			policy.Delay = *test.Delay
		}
		for _, id := range test.Guardians {
			policy.Guardians = append(policy.Guardians, Guardian{
				ID: id, PublicKey: publicKey(t, vectors.Seeds[id]),
			})
		}
		if _, err := New(policy); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want invalid, got %v", test.Name, err)
		}
	}
}

func TestRecoveryVectors(t *testing.T) {
	vectors := loadRecoveryVectors(t)
	newKey := publicKey(t, vectors.NewSeed)
	for _, test := range vectors.Cases {
		manager, err := New(basePolicy(t, vectors))
		if err != nil {
			t.Fatal(err)
		}
		request, err := manager.Start(newKey, test.Nonce, test.CreatedAt)
		if err != nil {
			t.Fatalf("%s: start: %v", test.Name, err)
		}
		var failure error
		for _, op := range test.Ops {
			if failure != nil {
				break
			}
			var next Request
			switch op.Op {
			case "start":
				next, failure = manager.Start(newKey, test.Nonce, test.CreatedAt)
			case "approve", "approve_bad":
				seed := vectors.Seeds[op.Guardian]
				payload := ApprovalPayload(request.ID, request.Account, request.NewPublicKey)
				if op.Op == "approve_bad" {
					seed = vectors.Seeds["owner"]
				}
				next, failure = manager.Approve(request.ID, op.Guardian, seedSign(t, seed, payload))
			case "execute":
				next, failure = manager.Execute(request.ID, op.At)
			case "cancel":
				next, failure = manager.Cancel(request.ID,
					seedSign(t, vectors.Seeds["owner"], CancelPayload(request.ID, request.Account)), op.At)
			default:
				t.Fatalf("%s: unknown op %q", test.Name, op.Op)
			}
			if failure == nil {
				request = next
			}
		}
		if test.Error != "" {
			if failure == nil || !errors.Is(failure, errorFor(test.Error)) {
				t.Fatalf("%s: want %s, got %v", test.Name, test.Error, failure)
			}
		} else if failure != nil {
			t.Fatalf("%s: %v", test.Name, failure)
		}
		stored, ok := manager.Get(request.ID)
		if !ok {
			t.Fatalf("%s: request disappeared", test.Name)
		}
		if stored.State != test.ExpectState {
			t.Fatalf("%s: state %s, want %s", test.Name, stored.State, test.ExpectState)
		}
		if test.ExpectCount > 0 && len(manager.Requests()) != test.ExpectCount {
			t.Fatalf("%s: %d requests, want %d", test.Name, len(manager.Requests()), test.ExpectCount)
		}
	}
}

func errorFor(code string) error {
	switch code {
	case "invalid":
		return ErrInvalid
	case "guardian":
		return ErrGuardian
	case "threshold":
		return ErrThreshold
	case "timelock":
		return ErrTimelock
	case "signature":
		return ErrSignature
	case "duplicate":
		return ErrDuplicate
	case "state":
		return ErrState
	case "unknown":
		return ErrUnknown
	}
	return nil
}
