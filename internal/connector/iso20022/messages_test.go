package iso20022

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type vectorFile struct {
	Pacs008 struct {
		File     string   `json:"file"`
		Transfer Transfer `json:"transfer"`
	} `json:"pacs008"`
	Pain001 struct {
		File     string   `json:"file"`
		Transfer Transfer `json:"transfer"`
	} `json:"pain001"`
	Camt053 struct {
		File      string    `json:"file"`
		Statement Statement `json:"statement"`
	} `json:"camt053"`
	Malformed []struct {
		Name    string `json:"name"`
		File    string `json:"file"`
		Find    string `json:"find"`
		Replace string `json:"replace"`
		Error   string `json:"error"`
	} `json:"malformed"`
}

const vectorsDir = "../../../spec/vectors/"

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(vectorsDir + "iso20022.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(vectorsDir + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseVectors(t *testing.T) {
	file := loadVectors(t)
	for _, c := range []struct {
		name   string
		path   string
		parse  func([]byte) (any, error)
		expect any
	}{
		{"pacs008", file.Pacs008.File, wrap(ParsePacs008), file.Pacs008.Transfer},
		{"pain001", file.Pain001.File, wrap(ParsePain001), file.Pain001.Transfer},
		{"camt053", file.Camt053.File, wrap(ParseCamt053), file.Camt053.Statement},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.parse(fixture(t, c.path))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.expect) {
				t.Fatalf("parsed = %+v, want %+v", got, c.expect)
			}
		})
	}
}

func TestBuildMatchesFixture(t *testing.T) {
	file := loadVectors(t)
	built, err := BuildPacs008(file.Pacs008.Transfer)
	if err != nil {
		t.Fatal(err)
	}
	if string(built) != string(fixture(t, file.Pacs008.File)) {
		t.Fatal("pacs.008 build does not match the shared fixture")
	}
	built, err = BuildPain001(file.Pain001.Transfer)
	if err != nil {
		t.Fatal(err)
	}
	if string(built) != string(fixture(t, file.Pain001.File)) {
		t.Fatal("pain.001 build does not match the shared fixture")
	}
	built, err = BuildCamt053(file.Camt053.Statement)
	if err != nil {
		t.Fatal(err)
	}
	if string(built) != string(fixture(t, file.Camt053.File)) {
		t.Fatal("camt.053 build does not match the shared fixture")
	}
}

func TestMalformedVectors(t *testing.T) {
	file := loadVectors(t)
	parsers := map[string]func([]byte) error{
		"iso20022/pacs008.xml": func(raw []byte) error { _, err := ParsePacs008(raw); return err },
		"iso20022/pain001.xml": func(raw []byte) error { _, err := ParsePain001(raw); return err },
		"iso20022/camt053.xml": func(raw []byte) error { _, err := ParseCamt053(raw); return err },
	}
	for _, c := range file.Malformed {
		t.Run(c.Name, func(t *testing.T) {
			parse, ok := parsers[c.File]
			if !ok {
				t.Fatalf("no parser for %s", c.File)
			}
			mutated := strings.ReplaceAll(string(fixture(t, c.File)), c.Find, c.Replace)
			if mutated == string(fixture(t, c.File)) {
				t.Fatalf("%s: %q does not occur in the fixture", c.Name, c.Find)
			}
			err := parse([]byte(mutated))
			if err == nil || !strings.Contains(err.Error(), c.Error) {
				t.Fatalf("error = %v, want %q", err, c.Error)
			}
		})
	}
}

func TestCountTransactions(t *testing.T) {
	file := loadVectors(t)
	for _, path := range []string{file.Pacs008.File, file.Pain001.File} {
		count, err := CountTransactions(fixture(t, path))
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s: count = %d, want 1", path, count)
		}
	}
	batched := strings.ReplaceAll(string(fixture(t, file.Pacs008.File)), "<NbOfTxs>1</NbOfTxs>", "<NbOfTxs>3</NbOfTxs>")
	count, err := CountTransactions([]byte(batched))
	if err != nil || count != 3 {
		t.Fatalf("count = %d, err = %v, want 3", count, err)
	}
	if _, err := CountTransactions(fixture(t, file.Camt053.File)); !errors.Is(err, ErrMessageType) {
		t.Fatalf("camt.053 count error = %v, want ErrMessageType", err)
	}
}

func TestBuildRejectsInvalidModels(t *testing.T) {
	file := loadVectors(t)
	cases := map[string]func() error{
		"zero amount": func() error {
			transfer := file.Pacs008.Transfer
			transfer.Amount.Value = "0.00"
			_, err := BuildPacs008(transfer)
			return err
		},
		"six fraction digits": func() error {
			transfer := file.Pain001.Transfer
			transfer.Amount.Value = "1.000001"
			_, err := BuildPain001(transfer)
			return err
		},
		"missing agent": func() error {
			transfer := file.Pacs008.Transfer
			transfer.Debtor.Agent = ""
			_, err := BuildPacs008(transfer)
			return err
		},
		"statement without entries": func() error {
			statement := file.Camt053.Statement
			statement.Entries = nil
			_, err := BuildCamt053(statement)
			return err
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			if err := build(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func wrap[T any](parse func([]byte) (T, error)) func([]byte) (any, error) {
	return func(raw []byte) (any, error) {
		value, err := parse(raw)
		return value, err
	}
}
