package ilp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

type caseVector struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Now         int64     `json:"now"`
	Preimage    string    `json:"preimage"`
	Destination string    `json:"destination"`
	Check       []string  `json:"check"`
	Code        string    `json:"code"`
	TriggeredBy string    `json:"triggered_by"`
	Message     string    `json:"message"`
	Address     string    `json:"address"`
	StreamID    string    `json:"stream_id"`
	ReceiptID   string    `json:"receipt_stream_id"`
	Receipts    []Receipt `json:"receipts"`
	Expect      struct {
		Error       string `json:"error"`
		Fulfillment string `json:"fulfillment"`
		Status      string `json:"status"`
		Total       uint64 `json:"total"`
	} `json:"expect"`
}

type vectorFile struct {
	Fulfillment string       `json:"fulfillment"`
	Condition   string       `json:"condition"`
	Quote       Quote        `json:"quote"`
	Prepare     prepareInput `json:"prepare"`
	Grant       Grant        `json:"grant"`
	Escrow      escrowInput  `json:"escrow"`
	Cases       []caseVector `json:"cases"`
}

type prepareInput struct {
	Amount      uint64 `json:"amount"`
	ExpiresAt   int64  `json:"expires_at"`
	Destination string `json:"destination"`
	DataHex     string `json:"data_hex"`
}

type escrowInput struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Receiver  string `json:"receiver"`
	Amount    Amount `json:"amount"`
	ExpiresAt int64  `json:"expires_at"`
}

func load(t *testing.T) (vectorFile, []byte, []byte) {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/vectors/ilp.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file, mustHex(t, file.Fulfillment), mustHex(t, file.Condition)
}

func mustHex(t *testing.T, raw string) []byte {
	t.Helper()
	value, err := hex.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f vectorFile) escrow(condition []byte) *Escrow {
	return &Escrow{
		ID: f.Escrow.ID, Sender: f.Escrow.Sender, Receiver: f.Escrow.Receiver,
		Amount: f.Escrow.Amount, Condition: condition, ExpiresAt: f.Escrow.ExpiresAt, Status: Held,
	}
}

func TestVectors(t *testing.T) {
	file, fulfillment, condition := load(t)
	computed, err := Condition(fulfillment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(computed, condition) {
		t.Fatalf("condition = %x, want %x", computed, condition)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := runCase(file, fulfillment, condition, c)
			if c.Expect.Error != "" {
				if err == nil || !strings.Contains(err.Error(), c.Expect.Error) {
					t.Fatalf("error = %v, want %q", err, c.Expect.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Expect.Fulfillment != "" && got != c.Expect.Fulfillment {
				t.Fatalf("fulfillment = %s, want %s", got, c.Expect.Fulfillment)
			}
			if c.Expect.Status != "" && got != c.Expect.Status {
				t.Fatalf("status = %s, want %s", got, c.Expect.Status)
			}
			if c.Expect.Total != 0 && got != strconv.FormatUint(c.Expect.Total, 10) {
				t.Fatalf("total = %s, want %d", got, c.Expect.Total)
			}
		})
	}
}

func runCase(file vectorFile, fulfillment, condition []byte, c caseVector) (string, error) {
	preimage := fulfillment
	if c.Preimage != "" {
		decoded, err := hex.DecodeString(c.Preimage)
		if err != nil {
			return "", err
		}
		preimage = decoded
	}
	destination := c.Destination
	if destination == "" {
		destination = file.Prepare.Destination
	}
	prepare := Prepare{
		Amount: file.Prepare.Amount, ExpiresAt: file.Prepare.ExpiresAt,
		Condition: condition, Destination: destination,
	}

	switch c.Kind {
	case "quote":
		return "", file.Quote.Validate(c.Now)
	case "prepare":
		revealed, err := prepare.Fulfill(c.Now, preimage)
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(revealed), nil
	case "grant":
		return "", file.Grant.Check(c.Now, c.Check[0], c.Check[1])
	case "claim":
		e := file.escrow(condition)
		if err := e.Claim(c.Now, preimage); err != nil {
			return "", err
		}
		return string(e.Status), nil
	case "claim_twice":
		e := file.escrow(condition)
		if err := e.Claim(c.Now, preimage); err != nil {
			return "", err
		}
		return string(e.Status), e.Claim(c.Now, preimage)
	case "refund":
		e := file.escrow(condition)
		if err := e.Refund(c.Now); err != nil {
			return "", err
		}
		return string(e.Status), nil
	case "refund_after_claim":
		e := file.escrow(condition)
		if err := e.Claim(file.Escrow.ExpiresAt-1, preimage); err != nil {
			return "", err
		}
		return "", e.Refund(c.Now)
	case "stream":
		id := c.StreamID
		if id == "" {
			id = file.Escrow.ID
		}
		receiptID := c.ReceiptID
		if receiptID == "" {
			receiptID = id
		}
		stream, err := NewStream(id)
		if err != nil {
			return "", err
		}
		for _, receipt := range c.Receipts {
			receipt.StreamID = receiptID
			if err := stream.Apply(receipt); err != nil {
				return "", err
			}
		}
		return strconv.FormatUint(stream.Total(), 10), nil
	case "reject":
		_, err := NewReject(c.Code, c.TriggeredBy, c.Message)
		return "", err
	case "address":
		return "", ValidateAddress(c.Address)
	default:
		return "", errors.New("unknown case kind " + c.Kind)
	}
}

func TestEscrowClaimWinsBeforeExpiry(t *testing.T) {
	file, fulfillment, condition := load(t)
	e := file.escrow(condition)
	if err := e.Claim(file.Escrow.ExpiresAt-1, fulfillment); err != nil {
		t.Fatal(err)
	}
	if err := e.Refund(file.Escrow.ExpiresAt); !errors.Is(err, ErrState) {
		t.Fatalf("refund after claim = %v, want ErrState", err)
	}
	if file.Grant.Check(0, "quote", "read") != nil {
		t.Fatal("grant should allow quote read")
	}
}

func TestNewEscrowRejectsSameAccount(t *testing.T) {
	_, err := NewEscrow("e", "a", "a", Amount{Asset: "arce:testnet/arce", Value: 1}, make([]byte, 32), 10)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}
