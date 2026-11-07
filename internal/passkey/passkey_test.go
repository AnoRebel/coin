package passkey

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type passkeyVectors struct {
	Challenges []struct {
		Scope     Scope  `json:"scope"`
		Challenge string `json:"challenge"`
	} `json:"challenges"`
	InvalidScopes []struct {
		Name  string `json:"name"`
		Scope Scope  `json:"scope"`
	} `json:"invalid_scopes"`
	InvalidCredentials []struct {
		Name       string     `json:"name"`
		Credential Credential `json:"credential"`
	} `json:"invalid_credentials"`
	Cases []struct {
		Name              string     `json:"name"`
		Credential        Credential `json:"credential"`
		Scope             Scope      `json:"scope"`
		At                uint64     `json:"at"`
		ClientData        string     `json:"client_data"`
		AuthenticatorData string     `json:"authenticator_data"`
		Signature         string     `json:"signature"`
		Error             string     `json:"error"`
		ExpectCounter     *uint32    `json:"expect_counter"`
	} `json:"cases"`
}

func loadVectors(t *testing.T) passkeyVectors {
	t.Helper()
	raw, err := os.ReadFile("../../spec/vectors/passkey.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors passkeyVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func decodeVector(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestChallengeVectors(t *testing.T) {
	for _, test := range loadVectors(t).Challenges {
		got, err := Challenge(test.Scope)
		if err != nil {
			t.Fatalf("%s: %v", test.Scope.Purpose, err)
		}
		if got != test.Challenge {
			t.Fatalf("%s: %s, want %s", test.Scope.Purpose, got, test.Challenge)
		}
	}
}

func TestInvalidScopesAndCredentials(t *testing.T) {
	vectors := loadVectors(t)
	for _, test := range vectors.InvalidScopes {
		if _, err := Challenge(test.Scope); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want invalid, got %v", test.Name, err)
		}
	}
	for _, test := range vectors.InvalidCredentials {
		manager, err := NewManager()
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Enroll(test.Credential); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want invalid, got %v", test.Name, err)
		}
	}
}

func TestAssertionVectors(t *testing.T) {
	for _, test := range loadVectors(t).Cases {
		manager, err := NewManager()
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Enroll(test.Credential); err != nil {
			t.Fatalf("%s: enroll: %v", test.Name, err)
		}
		assertion := Assertion{
			ClientDataJSON:    decodeVector(t, test.ClientData),
			AuthenticatorData: decodeVector(t, test.AuthenticatorData),
			Signature:         decodeVector(t, test.Signature),
		}
		credential, err := manager.Authorize(test.Credential.ID, test.Scope, assertion, test.At)
		if test.Error != "" {
			if !errors.Is(err, errorFor(test.Error)) {
				t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
			}
			if stored, _ := manager.Get(test.Credential.ID); stored.Counter != test.Credential.Counter {
				t.Fatalf("%s: failed authorization advanced the counter", test.Name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", test.Name, err)
		}
		if test.ExpectCounter == nil || credential.Counter != *test.ExpectCounter {
			t.Fatalf("%s: counter %d", test.Name, credential.Counter)
		}
	}
}

func TestEnrollmentLifecycle(t *testing.T) {
	vectors := loadVectors(t)
	credential := vectors.Cases[0].Credential
	manager, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Enroll(credential); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enroll(credential); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second enroll: %v", err)
	}
	if err := manager.Revoke(credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authorize(credential.ID, vectors.Cases[0].Scope, Assertion{}, 1); !errors.Is(err, ErrCredential) {
		t.Fatalf("revoked credential: %v", err)
	}
	if _, err := manager.Authorize("missing", vectors.Cases[0].Scope, Assertion{}, 1); !errors.Is(err, ErrCredential) {
		t.Fatalf("unknown credential: %v", err)
	}
}

func errorFor(code string) error {
	switch code {
	case "invalid":
		return ErrInvalid
	case "credential":
		return ErrCredential
	case "expired":
		return ErrExpired
	case "client_data":
		return ErrClientData
	case "challenge":
		return ErrChallenge
	case "origin":
		return ErrOrigin
	case "rp_id":
		return ErrRPID
	case "flags":
		return ErrFlags
	case "counter":
		return ErrCounter
	case "signature":
		return ErrSignature
	case "authenticator_data":
		return ErrAuthenticatorData
	}
	return nil
}
