// Package arne is a small Go client and type bridge for an ARNE node.
//
// It is safe to import from other modules. Consensus types are re-exported from
// the canonical implementation so a caller signs exactly what the node expects.
package arne

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Consensus types, re-exported for callers that build transactions locally.
type (
	Tx           = protocol.Tx
	KeyPair      = protocol.KeyPair
	Asset        = protocol.Asset
	Receipt      = ledger.Receipt
	Escrow       = protocol.Escrow
	Intent       = protocol.Intent
	Subscription = protocol.Subscription
	Transfer     = protocol.Transfer
)

// AllowanceInfo mirrors GET /v1/allowances/{owner}/{spender}/{asset}.
type AllowanceInfo struct {
	Owner     string `json:"owner"`
	Spender   string `json:"spender"`
	Asset     string `json:"asset"`
	Remaining uint64 `json:"remaining"`
	ExpiresAt uint64 `json:"expires_at"`
}

// Supply mirrors GET /v1/supply/{asset}.
type Supply struct {
	Asset  string `json:"asset"`
	Supply uint64 `json:"supply"`
	Burned uint64 `json:"burned"`
}

// TransferDetail is a transfer plus how much of it has been refunded.
type TransferDetail struct {
	Hash string `json:"hash"`
	Transfer
	Refunded   uint64 `json:"refunded"`
	Refundable uint64 `json:"refundable"`
}

// Block mirrors one item from GET /v1/blocks or GET /v1/blocks/{height}.
type Block struct {
	Height    uint64   `json:"height"`
	Hash      string   `json:"hash"`
	Time      uint64   `json:"time"`
	Proposer  string   `json:"proposer"`
	PrevRoot  string   `json:"prev_root"`
	StateRoot string   `json:"state_root"`
	TxRoot    string   `json:"tx_root"`
	TxHashes  []string `json:"tx_hashes"`
	Cursor    uint64   `json:"cursor"`
}

// BlockPage is one page of blocks; NextCursor is zero on the last page.
type BlockPage struct {
	Items      []Block `json:"items"`
	NextCursor uint64  `json:"next_cursor"`
}

var (
	// NewKeyPair generates a mnemonic and key pair for a network.
	NewKeyPair = protocol.NewKeyPair
	// KeyPairFromMnemonic derives a key pair at m/44'/9999'/account'/change'/index.
	KeyPairFromMnemonic = protocol.KeyPairFromMnemonic
	// Encode canonical CBOR.
	Encode = protocol.Encode
	// ChainID derives the chain ID for a network.
	ChainID = protocol.ChainID
	// NativeAsset returns the ARCE asset ID for a network.
	NativeAsset = protocol.NativeAsset
)

// Transaction type names accepted by Wallet.Send.
const (
	TxTransfer        = protocol.TxTransfer
	TxBatch           = protocol.TxBatch
	TxRegisterAsset   = protocol.TxRegisterAsset
	TxCreateEscrow    = protocol.TxCreateEscrow
	TxReleaseEscrow   = protocol.TxReleaseEscrow
	TxRefundEscrow    = protocol.TxRefundEscrow
	TxApprove         = protocol.TxApprove
	TxTransferFrom    = protocol.TxTransferFrom
	TxRevokeAllowance = protocol.TxRevokeAllowance
	TxMint            = protocol.TxMint
	TxBurn            = protocol.TxBurn
	TxCreateIntent    = protocol.TxCreateIntent
	TxCreateSub       = protocol.TxCreateSub
	TxChargeSub       = protocol.TxChargeSub
	TxCancelSub       = protocol.TxCancelSub
)

// Client talks to the REST /v1 surface.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	APIKey  string
}

// New returns a client with a 30 second timeout.
func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Status mirrors GET /v1/status.
type Status struct {
	Network         string `json:"network"`
	Chain           string `json:"chain"`
	Height          uint64 `json:"height"`
	StateRoot       string `json:"state_root"`
	Time            uint64 `json:"time"`
	Mempool         int    `json:"mempool"`
	Halted          string `json:"halted,omitempty"`
	Devnet          bool   `json:"devnet"`
	ProtocolVersion string `json:"protocol_version"`
}

// Account mirrors GET /v1/accounts/{address}.
type Account struct {
	Address  string            `json:"address"`
	Exists   bool              `json:"exists"`
	Nonce    uint64            `json:"nonce"`
	Auth     string            `json:"auth,omitempty"`
	Balances map[string]uint64 `json:"balances"`
}

// AssetInfo is a registered asset plus its live supply.
type AssetInfo struct {
	Asset
	Supply uint64 `json:"supply"`
	Burned uint64 `json:"burned"`
}

// Proof mirrors a sparse Merkle proof.
type Proof struct {
	Key      string   `json:"key"`
	Value    string   `json:"value"`
	Included bool     `json:"included"`
	Depth    int      `json:"depth"`
	Terminal string   `json:"terminal"`
	Siblings []string `json:"siblings"`
}

// Error is a node error with the stable ledger/API code.
type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func (c *Client) do(ctx context.Context, method, path string, body []byte, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var wrapper struct {
			Error Error `json:"error"`
		}
		if json.Unmarshal(raw, &wrapper) == nil && wrapper.Error.Code != "" {
			wrapper.Error.Status = resp.StatusCode
			return &wrapper.Error
		}
		return &Error{Status: resp.StatusCode, Code: "HTTP_ERROR", Message: strings.TrimSpace(string(raw))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Status fetches node status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, "", &out)
	return out, err
}

// Account fetches balances and the next nonce.
func (c *Client) Account(ctx context.Context, address string) (Account, error) {
	var out Account
	err := c.do(ctx, http.MethodGet, "/v1/accounts/"+url.PathEscape(address), nil, "", &out)
	return out, err
}

// Assets lists registered assets.
func (c *Client) Assets(ctx context.Context) ([]AssetInfo, error) {
	var out struct {
		Items []AssetInfo `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/assets", nil, "", &out)
	return out.Items, err
}

// Asset fetches one registered asset.
func (c *Client) Asset(ctx context.Context, id string) (AssetInfo, error) {
	var out AssetInfo
	err := c.do(ctx, http.MethodGet, "/v1/assets/"+url.PathEscape(id), nil, "", &out)
	return out, err
}

// Supply fetches an asset's circulating supply and burned total.
func (c *Client) Supply(ctx context.Context, asset string) (Supply, error) {
	var out Supply
	err := c.do(ctx, http.MethodGet, "/v1/supply/"+url.PathEscape(asset), nil, "", &out)
	return out, err
}

// Escrow fetches an escrow by ID.
func (c *Client) Escrow(ctx context.Context, id string) (Escrow, error) {
	var out Escrow
	err := c.do(ctx, http.MethodGet, "/v1/escrows/"+url.PathEscape(id), nil, "", &out)
	return out, err
}

// Intent fetches a payment intent by ID.
func (c *Client) Intent(ctx context.Context, id string) (Intent, error) {
	var out Intent
	err := c.do(ctx, http.MethodGet, "/v1/intents/"+url.PathEscape(id), nil, "", &out)
	return out, err
}

// Subscription fetches a subscription by ID.
func (c *Client) Subscription(ctx context.Context, id string) (Subscription, error) {
	var out Subscription
	err := c.do(ctx, http.MethodGet, "/v1/subscriptions/"+url.PathEscape(id), nil, "", &out)
	return out, err
}

// Transfer fetches a transfer by transaction hash, including refund progress.
func (c *Client) Transfer(ctx context.Context, hash string) (TransferDetail, error) {
	var out TransferDetail
	err := c.do(ctx, http.MethodGet, "/v1/transfers/"+url.PathEscape(hash), nil, "", &out)
	return out, err
}

// AllowanceInfo reads the remaining allowance and its expiry.
func (c *Client) AllowanceInfo(ctx context.Context, owner, spender, asset string) (AllowanceInfo, error) {
	var out AllowanceInfo
	path := "/v1/allowances/" + url.PathEscape(owner) + "/" + url.PathEscape(spender) + "/" + url.PathEscape(asset)
	err := c.do(ctx, http.MethodGet, path, nil, "", &out)
	return out, err
}

// Block fetches one block by height. A pruned block returns a *Error with code
// PRUNED.
func (c *Client) Block(ctx context.Context, height uint64) (Block, error) {
	var out Block
	err := c.do(ctx, http.MethodGet, "/v1/blocks/"+strconv.FormatUint(height, 10), nil, "", &out)
	return out, err
}

// Blocks lists up to limit blocks after cursor, oldest first. Pass a zero cursor
// to start at the oldest retained block.
func (c *Client) Blocks(ctx context.Context, limit, cursor uint64) (BlockPage, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.FormatUint(limit, 10))
	}
	if cursor > 0 {
		query.Set("cursor", strconv.FormatUint(cursor, 10))
	}
	var out BlockPage
	path := "/v1/blocks"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	err := c.do(ctx, http.MethodGet, path, nil, "", &out)
	return out, err
}

// Proof fetches a state proof for a namespaced key.
func (c *Client) Proof(ctx context.Context, key string) (Proof, error) {
	var out Proof
	err := c.do(ctx, http.MethodGet, "/v1/proofs/"+escapePath(key), nil, "", &out)
	return out, err
}

// Submit sends canonical transaction bytes and returns the transaction hash.
func (c *Client) Submit(ctx context.Context, raw []byte) (string, error) {
	var out struct {
		Hash string `json:"hash"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/transactions", raw, "application/cbor", &out)
	return out.Hash, err
}

// SubmitTx encodes and signs nothing: sign the transaction before calling.
func (c *Client) SubmitTx(ctx context.Context, tx Tx) (string, error) {
	raw, err := Encode(tx.ToCBOR())
	if err != nil {
		return "", err
	}
	return c.Submit(ctx, raw)
}

// Simulate dry-runs canonical transaction bytes.
func (c *Client) Simulate(ctx context.Context, raw []byte) (Receipt, error) {
	var out Receipt
	err := c.do(ctx, http.MethodPost, "/v1/transactions/simulate", raw, "application/cbor", &out)
	return out, err
}

// Receipt fetches an included transaction's receipt.
func (c *Client) Receipt(ctx context.Context, hash string) (Receipt, error) {
	var out Receipt
	err := c.do(ctx, http.MethodGet, "/v1/transactions/"+url.PathEscape(hash), nil, "", &out)
	return out, err
}

// WaitReceipt polls until the receipt exists or ctx expires.
func (c *Client) WaitReceipt(ctx context.Context, hash string) (Receipt, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		receipt, err := c.Receipt(ctx, hash)
		if err == nil {
			return receipt, nil
		}
		var apiErr *Error
		if !asError(err, &apiErr) || apiErr.Status != http.StatusNotFound {
			return Receipt{}, err
		}
		select {
		case <-ctx.Done():
			return Receipt{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Faucet requests devnet funds.
func (c *Client) Faucet(ctx context.Context, address string) (string, error) {
	body, _ := json.Marshal(map[string]string{"address": address})
	var out struct {
		Hash string `json:"hash"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/faucet", body, "application/json", &out)
	return out.Hash, err
}

// RPC calls a JSON-RPC method and decodes the result.
func (c *Client) RPC(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	var wrapper struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}
	if err := c.do(ctx, http.MethodPost, "/rpc", body, "application/json", &wrapper); err != nil {
		return err
	}
	if wrapper.Error != nil {
		return &Error{Status: http.StatusBadRequest, Code: wrapper.Error.Data, Message: wrapper.Error.Message}
	}
	return json.Unmarshal(wrapper.Result, out)
}

// FormatAmount renders base units with the asset's decimals.
func FormatAmount(value uint64, decimals uint8) string {
	if decimals == 0 {
		return strconv.FormatUint(value, 10)
	}
	scale := uint64(1)
	for i := uint8(0); i < decimals; i++ {
		scale *= 10
	}
	whole, frac := value/scale, value%scale
	s := strconv.FormatUint(whole, 10) + "." + fmt.Sprintf("%0*d", int(decimals), frac)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

func escapePath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// AsError extracts a node error. Use it with errors.Is-style checks on Code.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsCode reports whether err is a node error with the given stable code.
func IsCode(err error, code string) bool {
	e, ok := AsError(err)
	return ok && e.Code == code
}

func asError(err error, target **Error) bool {
	e, ok := AsError(err)
	if ok {
		*target = e
	}
	return ok
}
