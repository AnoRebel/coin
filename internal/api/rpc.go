package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/anorebel/anorebelcoin/internal/ledger"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) rpc(c *echo.Context) error {
	body, err := readBody(c)
	if err != nil {
		return err
	}
	var single rpcRequest
	if err := json.Unmarshal(body, &single); err == nil && single.Method != "" {
		resp := s.dispatch(single)
		if len(single.ID) == 0 {
			return c.NoContent(http.StatusNoContent)
		}
		return c.JSON(http.StatusOK, resp)
	}
	var batch []rpcRequest
	if err := json.Unmarshal(body, &batch); err != nil {
		return c.JSON(http.StatusOK, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
	}
	out := make([]rpcResponse, 0, len(batch))
	for _, req := range batch {
		if len(req.ID) == 0 {
			s.dispatch(req)
			continue
		}
		out = append(out, s.dispatch(req))
	}
	if len(out) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) dispatch(req rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" {
		resp.Error = &rpcError{Code: -32600, Message: "invalid request"}
		return resp
	}
	params := map[string]json.RawMessage{}
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "params must be an object"}
			return resp
		}
	}
	result, rerr := s.call(req.Method, params)
	if rerr != nil {
		resp.Error = rerr
		return resp
	}
	resp.Result = result
	return resp
}

func (s *Server) call(method string, p map[string]json.RawMessage) (any, *rpcError) {
	switch method {
	case "arne_status", "arne_chainId":
		st := s.node.Status()
		return map[string]any{
			"network": st.Network, "chain": st.Chain, "height": st.Height,
			"state_root": st.StateRoot, "mempool": st.Mempool, "halted": st.Halted,
		}, nil
	case "arne_blockNumber":
		return s.node.Status().Height, nil
	case "arne_getBlockByNumber":
		var args struct {
			Height uint64 `json:"height"`
		}
		if err := decodeParams(p, &args); err != nil {
			return nil, err
		}
		b, ok := s.node.Block(args.Height)
		if !ok {
			return nil, &rpcError{Code: -32004, Message: "block not found"}
		}
		return blockView(b), nil
	case "arne_getBalance":
		var args struct {
			Address string `json:"address"`
			Asset   string `json:"asset"`
		}
		if err := decodeParams(p, &args); err != nil {
			return nil, err
		}
		return s.node.State().Balance(args.Address, args.Asset), nil
	case "arne_getAccount":
		var args struct {
			Address string `json:"address"`
		}
		if err := decodeParams(p, &args); err != nil {
			return nil, err
		}
		info, exists := s.node.State().Account(args.Address)
		return map[string]any{"address": info.Address, "nonce": info.Nonce, "balances": info.Balances, "exists": exists}, nil
	case "arne_listAssets":
		st := s.node.State()
		out := []string{}
		out = append(out, st.Assets()...)
		return out, nil
	case "arne_sendRawTransaction":
		var args struct {
			Tx string `json:"tx"`
		}
		if err := decodeParams(p, &args); err != nil {
			return nil, err
		}
		raw, err := base64.StdEncoding.DecodeString(args.Tx)
		if err != nil {
			if raw, err = hex.DecodeString(args.Tx); err != nil {
				return nil, &rpcError{Code: -32602, Message: "tx must be base64 or hex"}
			}
		}
		hash, err := s.node.SubmitTx(raw)
		if err != nil {
			return nil, rpcFromError(err)
		}
		return hash, nil
	case "arne_getTransactionReceipt":
		var args struct {
			Hash string `json:"hash"`
		}
		if err := decodeParams(p, &args); err != nil {
			return nil, err
		}
		r, ok := s.node.Receipt(args.Hash)
		if !ok {
			return nil, &rpcError{Code: -32004, Message: "transaction not found"}
		}
		return r, nil
	case "arne_getProof":
		var args struct {
			Key string `json:"key"`
		}
		if err := decodeParams(p, &args); err != nil {
			return nil, err
		}
		proof, err := s.node.Proof(args.Key)
		if err != nil {
			return nil, rpcFromError(err)
		}
		return proofView(proof), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
	}
}

func decodeParams(p map[string]json.RawMessage, out any) *rpcError {
	b, err := json.Marshal(p)
	if err != nil {
		return &rpcError{Code: -32602, Message: "invalid params"}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	return nil
}

func rpcFromError(err error) *rpcError {
	var le *ledger.Error
	if ok := asLedgerError(err, &le); ok {
		return &rpcError{Code: -32000, Message: le.Message, Data: string(le.Code)}
	}
	return &rpcError{Code: -32603, Message: err.Error()}
}

func asLedgerError(err error, target **ledger.Error) bool {
	if e, ok := err.(*ledger.Error); ok {
		*target = e
		return true
	}
	return false
}

var _ = fmt.Sprintf
