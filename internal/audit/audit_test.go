package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type auditVectors struct {
	Genesis  string   `json:"genesis"`
	Inputs   []Entry  `json:"inputs"`
	Records  []Record `json:"records"`
	Head     string   `json:"head"`
	Tampered []struct {
		Name      string   `json:"name"`
		Records   []Record `json:"records"`
		JSONLEdit []string `json:"jsonl_edit"`
		Error     string   `json:"error"`
	} `json:"tampered"`
}

func loadVectors(t *testing.T) auditVectors {
	t.Helper()
	raw, err := os.ReadFile("../../spec/vectors/audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors auditVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func sequentialClock(times ...uint64) func() uint64 {
	index := 0
	return func() uint64 {
		value := times[index]
		index++
		return value
	}
}

func TestAppendVectors(t *testing.T) {
	vectors := loadVectors(t)
	if Genesis != vectors.Genesis {
		t.Fatalf("genesis %s, want %s", Genesis, vectors.Genesis)
	}
	log := New(sequentialClock(1001, 1002, 1003))
	if log.Head() != vectors.Genesis {
		t.Fatalf("new log head %s", log.Head())
	}
	for index, input := range vectors.Inputs {
		record, err := log.Append(input)
		if err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
		if record != vectors.Records[index] {
			t.Fatalf("record %d = %+v, want %+v", index, record, vectors.Records[index])
		}
	}
	if log.Head() != vectors.Head {
		t.Fatalf("head %s, want %s", log.Head(), vectors.Head)
	}
	if !sameRecords(log.Records(), vectors.Records) {
		t.Fatal("records differ from the vectors")
	}
}

func TestExportAndOpen(t *testing.T) {
	vectors := loadVectors(t)
	log := New(sequentialClock(1001, 1002, 1003))
	for _, input := range vectors.Inputs {
		if _, err := log.Append(input); err != nil {
			t.Fatal(err)
		}
	}
	var exported bytes.Buffer
	if err := log.Export(&exported); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(exported.String(), "\n"); count != len(vectors.Records) {
		t.Fatalf("export has %d lines, want %d", count, len(vectors.Records))
	}

	reopened, err := Open(&exported, func() uint64 { return 2000 })
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Head() != vectors.Head || !sameRecords(reopened.Records(), vectors.Records) {
		t.Fatal("reopened log does not match")
	}
	record, err := reopened.Append(Entry{Actor: "dave", Action: "settings.manage", Result: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Seq != 4 || record.Prev != vectors.Head || record.Time != 2000 {
		t.Fatalf("continuation = %+v", record)
	}
}

func TestTamperDetection(t *testing.T) {
	vectors := loadVectors(t)
	for _, test := range vectors.Tampered {
		if len(test.JSONLEdit) == 2 {
			log := New(sequentialClock(1001, 1002, 1003))
			for _, input := range vectors.Inputs {
				if _, err := log.Append(input); err != nil {
					t.Fatal(err)
				}
			}
			var exported bytes.Buffer
			if err := log.Export(&exported); err != nil {
				t.Fatal(err)
			}
			edited := strings.Replace(exported.String(), test.JSONLEdit[0], test.JSONLEdit[1], 1)
			if _, err := Open(strings.NewReader(edited), func() uint64 { return 0 }); !errors.Is(err, errorFor(test.Error)) {
				t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
			}
			continue
		}
		if err := Verify(test.Records); !errors.Is(err, errorFor(test.Error)) {
			t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
		}
	}
}

func TestInvalidEntries(t *testing.T) {
	tests := []Entry{
		{Actor: "", Action: "payment.write", Result: "allow"},
		{Actor: "alice", Action: "", Result: "allow"},
		{Actor: "alice", Action: "payment.write", Result: ""},
		{Actor: "alice\n", Action: "payment.write", Result: "allow"},
		{Actor: "alice", Action: "payment.write", Result: "allow", Detail: strings.Repeat("a", MaxDetail+1)},
		{Actor: "\xff", Action: "payment.write", Result: "allow"},
	}
	for index, entry := range tests {
		log := New(func() uint64 { return 1 })
		if _, err := log.Append(entry); !errors.Is(err, ErrInvalid) {
			t.Fatalf("entry %d: want invalid, got %v", index, err)
		}
	}
}

func TestExportRejectsBrokenLog(t *testing.T) {
	if err := Verify(nil); err != nil {
		t.Fatalf("empty chain: %v", err)
	}
	vectors := loadVectors(t)
	broken := append([]Record(nil), vectors.Records...)
	broken[2].Prev = vectors.Records[0].Hash
	if err := Verify(broken); !errors.Is(err, ErrChain) {
		t.Fatalf("broken chain: %v", err)
	}
}

func sameRecords(left, right []Record) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func errorFor(code string) error {
	switch code {
	case "invalid":
		return ErrInvalid
	case "sequence":
		return ErrSequence
	case "chain":
		return ErrChain
	case "hash":
		return ErrHash
	}
	return nil
}
