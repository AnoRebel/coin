// Package api exposes the ARNE node over REST /v1, JSON-RPC 2.0, and SSE.
package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/anorebel/anorebelcoin/internal/compliance/access"
	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/smt"
	"github.com/anorebel/anorebelcoin/internal/workspace"
)

// Config configures the HTTP surface.
type Config struct {
	APIKeys        map[string][]string // key -> scopes ("read","write","admin")
	Workspace      *workspace.Workspace
	AuditGrants    *access.Registry // scoped auditor grants; nil disables scoping
	AllowedOrigins []string
	RateLimit      int         // requests per second per credential or IP; 0 disables
	OIDC           *OIDCConfig // RS256 JWT validation; nil disables
	AdminMTLS      bool        // a verified client certificate grants admin
	Devnet         bool
}

// Server serves the node API.
type Server struct {
	node        *node.Node
	cfg         Config
	auth        *authenticator
	lim         *limiter
	submissions submissions
	writes      writeCache
}

// New builds an Echo server.
func New(n *node.Node, cfg Config) *echo.Echo {
	if cfg.Workspace != nil && cfg.AuditGrants == nil {
		cfg.AuditGrants = access.New("")
	}
	s := &Server{node: n, cfg: cfg, auth: newAuthenticator(cfg)}
	if cfg.RateLimit > 0 {
		s.lim = newLimiter(cfg.RateLimit, cfg.RateLimit*2)
	}
	e := echo.New()
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
		writeError(c, err)
	}
	e.Use(s.securityHeaders)
	e.Use(s.authenticate)
	e.Use(s.limitMiddleware)
	e.Use(s.corsMiddleware)
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Set("started", time.Now())
			return next(c)
		}
	})

	e.GET("/v1/openapi.json", s.openapi)
	e.GET("/v1/health", s.health)
	e.GET("/v1/version", s.version)
	e.GET("/v1/status", s.status)
	e.GET("/v1/assets", s.listAssets)
	e.GET("/v1/assets/:id", s.getAsset)
	e.GET("/v1/accounts/:address", s.getAccount)
	e.GET("/v1/accounts/:address/proof", s.accountProof)
	e.GET("/v1/supply/:asset", s.supply)
	e.GET("/v1/escrows/:id", s.getEscrow, s.workspaceAction(workspace.ActionPaymentRead))
	e.GET("/v1/intents/:id", s.getIntent, s.workspaceAction(workspace.ActionPaymentRead))
	e.GET("/v1/transfers/:hash", s.getTransfer, s.workspaceAction(workspace.ActionPaymentRead))
	e.GET("/v1/subscriptions/:id", s.getSubscription, s.workspaceAction(workspace.ActionPaymentRead))
	e.GET("/v1/allowances/:owner/:spender/:asset", s.getAllowance)
	e.GET("/v1/blocks", s.listBlocks)
	e.GET("/v1/blocks/:height", s.getBlock)
	e.GET("/v1/transactions/:hash", s.getTx)
	e.GET("/v1/transactions/:hash/status", deprecatedHandler(s.getTx, "Tue, 04 May 2027 00:00:00 GMT", "/v1/transactions/{hash}"))
	e.GET("/v1/events", s.events)
	e.GET("/v1/webhooks/key", s.webhookKey)
	e.GET("/v1/proofs/*", s.keyProof)

	e.POST("/v1/transactions", s.submitTx, s.require("write"), s.workspaceAction(workspace.ActionPaymentWrite))
	e.POST("/v1/transactions/simulate", s.simulateTx, s.workspaceAction(workspace.ActionPaymentRead))
	e.POST("/v1/blocks/produce", s.produceBlock, s.require("admin"), s.idempotent)
	e.POST("/v1/faucet", s.faucet, s.require("write"), s.idempotent)
	e.GET("/v1/workspace", s.getWorkspace)
	e.GET("/v1/workspace/members", s.listWorkspaceMembers, s.workspaceAction(workspace.ActionMemberRead))
	e.PUT("/v1/workspace/members/:id", s.setWorkspaceMember, s.workspaceAction(workspace.ActionMemberManage), s.idempotent)
	e.DELETE("/v1/workspace/members/:id", s.deleteWorkspaceMember, s.workspaceAction(workspace.ActionMemberManage), s.idempotent)
	e.GET("/v1/workspace/audit", s.getWorkspaceAudit, s.workspaceAction(workspace.ActionAuditRead))
	e.GET("/v1/audit/grants", s.listGrants, s.workspaceAction(workspace.ActionAuditRead))
	e.POST("/v1/audit/grants", s.createGrant, s.workspaceAction(workspace.ActionSettings), s.idempotent)
	e.DELETE("/v1/audit/grants/:id", s.revokeGrant, s.workspaceAction(workspace.ActionSettings), s.idempotent)
	e.GET("/v1/audit/grants/log", s.getGrantLog, s.workspaceAction(workspace.ActionSettings))
	e.POST("/rpc", s.rpc)
	return e
}

func (s *Server) securityHeaders(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		w := c.Response()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		return next(c)
	}
}

func (s *Server) corsMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		origin := c.Request().Header.Get("Origin")
		if origin != "" {
			for _, allowed := range s.cfg.AllowedOrigins {
				if allowed == origin {
					w := c.Response()
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
					break
				}
			}
		}
		if c.Request().Method == http.MethodOptions {
			return c.NoContent(http.StatusNoContent)
		}
		return next(c)
	}
}

func (s *Server) limitMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if s.lim != nil && !s.lim.allow(rateKey(c)) {
			return apiErr(http.StatusTooManyRequests, "RATE_LIMITED", "too many requests")
		}
		return next(c)
	}
}

func (s *Server) require(scope string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !s.auth.enabled() {
				return next(c)
			}
			cred := credentialFrom(c)
			if scope == "admin" && cred != nil && cred.MTLSAdmin {
				return next(c)
			}
			if cred == nil {
				return apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			}
			if !cred.hasScope(scope) {
				return apiErr(http.StatusForbidden, "FORBIDDEN", "missing scope "+scope)
			}
			return next(c)
		}
	}
}

// workspaceAction enforces a workspace permission when a workspace is loaded.
// Unknown API keys are rejected; the resolved member ID is stored as "actor".
func (s *Server) workspaceAction(action workspace.Action) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if s.cfg.Workspace == nil {
				return next(c)
			}
			actor, err := s.principal(c)
			if err != nil {
				return err
			}
			if err := s.cfg.Workspace.Require(actor, action, auditTarget(c)); err != nil {
				return apiErr(http.StatusForbidden, "FORBIDDEN", "role cannot "+string(action))
			}
			c.Set("actor", actor)
			return next(c)
		}
	}
}

func auditTarget(c *echo.Context) string {
	if id := c.Param("id"); id != "" {
		return id
	}
	return c.Path()
}

func (s *Server) principal(c *echo.Context) (string, error) {
	if s.cfg.Workspace == nil {
		return "", apiErr(http.StatusNotFound, "NOT_FOUND", "workspace is not configured")
	}
	key := strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
	if actor, ok := s.cfg.Workspace.Actor(key); ok {
		return actor, nil
	}
	if cred := credentialFrom(c); cred != nil && cred.Subject != "" {
		if _, ok := s.cfg.Workspace.RoleOf(cred.Subject); ok {
			return cred.Subject, nil
		}
	}
	return "", apiErr(http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid API key")
}

func (s *Server) getWorkspace(c *echo.Context) error {
	actor, err := s.principal(c)
	if err != nil {
		return err
	}
	role, _ := s.cfg.Workspace.RoleOf(actor)
	return c.JSON(http.StatusOK, map[string]any{
		"actor": actor, "role": role, "permissions": workspace.Permissions(role),
	})
}

func (s *Server) listWorkspaceMembers(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"items": s.cfg.Workspace.Members()})
}

func (s *Server) setWorkspaceMember(c *echo.Context) error {
	body, err := readBody(c)
	if err != nil {
		return err
	}
	var in struct {
		Role workspace.Role `json:"role"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "body must be {\"role\":\"...\"}")
	}
	actor, _ := c.Get("actor").(string)
	id := c.Param("id")
	if err := s.cfg.Workspace.SetRole(actor, id, in.Role); err != nil {
		return workspaceError(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"id": id, "role": in.Role})
}

func (s *Server) deleteWorkspaceMember(c *echo.Context) error {
	actor, _ := c.Get("actor").(string)
	if err := s.cfg.Workspace.Remove(actor, c.Param("id")); err != nil {
		return workspaceError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) getWorkspaceAudit(c *echo.Context) error {
	var after uint64
	if raw := c.QueryParam("after"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "after must be a sequence number")
		}
		after = v
	}
	limit := 100
	if raw := c.QueryParam("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "limit must be positive")
		}
		limit = v
	}
	return c.JSON(http.StatusOK, map[string]any{"items": s.cfg.Workspace.Entries(after, limit)})
}

func workspaceError(err error) error {
	switch {
	case errors.Is(err, workspace.ErrForbidden), errors.Is(err, workspace.ErrUnknownActor):
		return apiErr(http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, workspace.ErrUnknownMember):
		return apiErr(http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, workspace.ErrLastOwner):
		return apiErr(http.StatusConflict, "LAST_OWNER", err.Error())
	case errors.Is(err, workspace.ErrInvalid):
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	default:
		return err
	}
}

// auditScope restricts auditors to grants covering the record's asset. Owners,
// admins, and members are not scoped here.
func (s *Server) auditScope(c *echo.Context, asset string) error {
	if s.cfg.Workspace == nil || s.cfg.AuditGrants == nil {
		return nil
	}
	actor, ok := c.Get("actor").(string)
	if !ok {
		return nil
	}
	role, _ := s.cfg.Workspace.RoleOf(actor)
	if role != workspace.RoleAuditor {
		return nil
	}
	if _, err := s.cfg.AuditGrants.Check(actor, asset, time.Now().Unix()); err != nil {
		return apiErr(http.StatusForbidden, "AUDIT_SCOPE", err.Error())
	}
	return nil
}

func (s *Server) grants(c *echo.Context) (*access.Registry, error) {
	if s.cfg.AuditGrants == nil || s.cfg.Workspace == nil {
		return nil, apiErr(http.StatusNotFound, "NOT_FOUND", "audit grants are not configured")
	}
	return s.cfg.AuditGrants, nil
}

func (s *Server) listGrants(c *echo.Context) error {
	r, err := s.grants(c)
	if err != nil {
		return err
	}
	actor, _ := c.Get("actor").(string)
	role, _ := s.cfg.Workspace.RoleOf(actor)
	if role == workspace.RoleAuditor {
		return c.JSON(http.StatusOK, map[string]any{"items": r.List(actor)})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": r.List("")})
}

func (s *Server) createGrant(c *echo.Context) error {
	r, err := s.grants(c)
	if err != nil {
		return err
	}
	body, err := readBody(c)
	if err != nil {
		return err
	}
	var in struct {
		Auditor string   `json:"auditor"`
		Assets  []string `json:"assets"`
		From    int64    `json:"from"`
		Until   int64    `json:"until"`
		Purpose string   `json:"purpose"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "body must be {auditor, assets, from, until, purpose}")
	}
	g, err := r.Grant(access.Grant{
		Auditor: in.Auditor, Assets: in.Assets, From: in.From, Until: in.Until, Purpose: in.Purpose,
	})
	if err != nil {
		if errors.Is(err, access.ErrExists) {
			return apiErr(http.StatusConflict, "GRANT_EXISTS", err.Error())
		}
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	}
	return c.JSON(http.StatusCreated, g)
}

func (s *Server) revokeGrant(c *echo.Context) error {
	r, err := s.grants(c)
	if err != nil {
		return err
	}
	if err := r.Revoke(c.Param("id")); err != nil {
		if errors.Is(err, access.ErrUnknown) {
			return apiErr(http.StatusNotFound, "NOT_FOUND", "grant not found")
		}
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) getGrantLog(c *echo.Context) error {
	r, err := s.grants(c)
	if err != nil {
		return err
	}
	var after uint64
	if raw := c.QueryParam("after"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "after must be a sequence number")
		}
		after = v
	}
	limit := 100
	if raw := c.QueryParam("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "limit must be positive")
		}
		limit = v
	}
	return c.JSON(http.StatusOK, map[string]any{"items": r.Events(after, limit)})
}

// --- read handlers ---

func (s *Server) health(c *echo.Context) error {
	st := s.node.Status()
	status := http.StatusOK
	if st.Halted != "" {
		status = http.StatusServiceUnavailable
	}
	return c.JSON(status, map[string]any{"status": map[bool]string{true: "halted", false: "ok"}[st.Halted != ""], "height": st.Height})
}

func (s *Server) version(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"protocol": "arne/1", "tx_version": 1, "api": "v1",
		"cbor": "deterministic", "state": "smt-sha256",
	})
}

func (s *Server) status(c *echo.Context) error {
	return c.JSON(http.StatusOK, s.node.Status())
}

func (s *Server) listAssets(c *echo.Context) error {
	if err := checkQuery(c, "limit", "cursor", "order", "class", "issuer"); err != nil {
		return err
	}
	limit, err := pageLimit(c, 100, 1000)
	if err != nil {
		return err
	}
	cursor, err := assetCursor(c)
	if err != nil {
		return err
	}
	descending, err := pageOrder(c)
	if err != nil {
		return err
	}
	class, issuer := c.QueryParam("class"), c.QueryParam("issuer")
	st := s.node.State()
	ids := st.Assets()
	if descending {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
	out := make([]map[string]any, 0, limit)
	next := ""
	for _, id := range ids {
		if cursor != "" {
			if descending && id >= cursor || !descending && id <= cursor {
				continue
			}
		}
		a, _, err := st.Asset(id)
		if err != nil {
			return err
		}
		if class != "" && a.Class != class || issuer != "" && a.Issuer != issuer {
			continue
		}
		if len(out) == limit {
			next = out[len(out)-1]["id"].(string)
			break
		}
		out = append(out, assetView(a, st.Supply(id), st.Burned(id)))
	}
	body := map[string]any{"items": out}
	if next != "" {
		body["next_cursor"] = next
	}
	return c.JSON(http.StatusOK, body)
}

func assetView(a any, supply, burned uint64) map[string]any {
	b, _ := json.Marshal(a)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["supply"] = supply
	m["burned"] = burned
	return m
}

func (s *Server) getAsset(c *echo.Context) error {
	st := s.node.State()
	id := c.Param("id")
	a, ok, err := st.Asset(id)
	if err != nil {
		return err
	}
	if !ok {
		return apiErr(http.StatusNotFound, "UNKNOWN_ASSET", "asset not found")
	}
	return c.JSON(http.StatusOK, assetView(a, st.Supply(id), st.Burned(id)))
}

func (s *Server) getAccount(c *echo.Context) error {
	addr := c.Param("address")
	st := s.node.State()
	info, created := st.Account(addr)
	if !created {
		return c.JSON(http.StatusOK, map[string]any{"address": addr, "exists": false, "balances": map[string]uint64{}, "nonce": 0})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"address": info.Address, "exists": true, "nonce": info.Nonce,
		"auth": info.Auth, "balances": info.Balances,
	})
}

func (s *Server) accountProof(c *echo.Context) error {
	addr := c.Param("address")
	asset := c.QueryParam("asset")
	key := "account/" + addr + "/nonce"
	if asset != "" {
		key = "balance/" + addr + "/" + asset
	}
	p, err := s.node.Proof(key)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, proofView(p))
}

func (s *Server) keyProof(c *echo.Context) error {
	key := c.Param("*")
	p, err := s.node.Proof(key)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, proofView(p))
}

func proofView(p smt.Proof) map[string]any {
	siblings := make([]string, len(p.Siblings))
	for i, sib := range p.Siblings {
		siblings[i] = hex.EncodeToString(sib[:])
	}
	value := ""
	if p.Value != nil {
		value = hex.EncodeToString(p.Value)
	}
	return map[string]any{
		"key": p.Key, "value": value, "included": p.Value != nil,
		"depth": p.Depth, "terminal": hex.EncodeToString(p.Terminal[:]),
		"siblings": siblings,
	}
}

func (s *Server) supply(c *echo.Context) error {
	id := c.Param("asset")
	st := s.node.State()
	if _, ok, _ := st.Asset(id); !ok {
		return apiErr(http.StatusNotFound, "UNKNOWN_ASSET", "asset not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"asset": id, "supply": st.Supply(id), "burned": st.Burned(id)})
}

func (s *Server) getEscrow(c *echo.Context) error {
	e, ok, err := s.node.State().Escrow(c.Param("id"))
	if err != nil {
		return err
	}
	if !ok {
		return apiErr(http.StatusNotFound, "ESCROW_NOT_FOUND", "escrow not found")
	}
	if err := s.auditScope(c, e.Asset); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, e)
}

func (s *Server) getIntent(c *echo.Context) error {
	in, ok, err := s.node.Intent(c.Param("id"))
	if err != nil {
		return err
	}
	if !ok {
		return apiErr(http.StatusNotFound, "INTENT_NOT_FOUND", "intent not found")
	}
	if err := s.auditScope(c, in.Asset); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, in)
}

func (s *Server) getSubscription(c *echo.Context) error {
	sub, ok, err := s.node.Subscription(c.Param("id"))
	if err != nil {
		return err
	}
	if !ok {
		return apiErr(http.StatusNotFound, "SUBSCRIPTION_NOT_FOUND", "subscription not found")
	}
	if err := s.auditScope(c, sub.Asset); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, sub)
}

func (s *Server) getTransfer(c *echo.Context) error {
	hash := c.Param("hash")
	t, ok, err := s.node.Transfer(hash)
	if err != nil {
		return err
	}
	if !ok {
		return apiErr(http.StatusNotFound, "TRANSFER_NOT_FOUND", "transfer not found")
	}
	if err := s.auditScope(c, t.Asset); err != nil {
		return err
	}
	refunded := s.node.Refunded(hash)
	return c.JSON(http.StatusOK, map[string]any{
		"hash": hash, "from": t.From, "to": t.To, "asset": t.Asset,
		"amount": t.Amount, "refund_of": t.RefundOf, "refunded": refunded,
		"refundable": t.Amount - refunded,
	})
}

func (s *Server) getAllowance(c *echo.Context) error {
	st := s.node.State()
	owner, spender, asset := c.Param("owner"), c.Param("spender"), c.Param("asset")
	return c.JSON(http.StatusOK, map[string]any{
		"owner": owner, "spender": spender, "asset": asset,
		"remaining":  st.Allowance(owner, spender, asset),
		"expires_at": st.AllowanceExpiry(owner, spender, asset),
	})
}

func (s *Server) listBlocks(c *echo.Context) error {
	if err := checkQuery(c, "limit", "cursor", "order", "sort", "proposer"); err != nil {
		return err
	}
	limit, err := pageLimit(c, 50, 1000)
	if err != nil {
		return err
	}
	cursor, err := blockCursor(c)
	if err != nil {
		return err
	}
	descending, err := pageOrder(c)
	if err != nil {
		return err
	}
	if sort := c.QueryParamOr("sort", "height"); sort != "height" {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "sort must be one of: height")
	}
	blocks, next := s.node.ListBlocks(limit, cursor, !descending, c.QueryParam("proposer"))
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockView(b))
	}
	body := map[string]any{"items": out}
	if next > 0 {
		body["next_cursor"] = next
	}
	return c.JSON(http.StatusOK, body)
}

func blockView(b ledger.Block) map[string]any {
	h, _ := b.Hash()
	var next uint64
	if b.Height > 1 {
		next = b.Height - 1
	}
	return map[string]any{
		"height": b.Height, "hash": h, "time": b.Time, "proposer": b.Proposer,
		"prev_root": b.PrevRoot, "state_root": b.StateRoot, "tx_root": b.TxRoot,
		"tx_hashes": b.TxHashes, "cursor": next,
	}
}

func (s *Server) getBlock(c *echo.Context) error {
	height, err := strconv.ParseUint(c.Param("height"), 10, 64)
	if err != nil {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "height must be a number")
	}
	b, ok := s.node.Block(height)
	if !ok {
		if oldest := s.node.OldestBlock(); oldest > 0 && height < oldest {
			return apiErr(http.StatusGone, "PRUNED",
				fmt.Sprintf("block %d pruned; available from %d", height, oldest))
		}
		return apiErr(http.StatusNotFound, "BLOCK_NOT_FOUND", "block not found")
	}
	return c.JSON(http.StatusOK, blockView(b))
}

func (s *Server) getTx(c *echo.Context) error {
	r, ok := s.node.Receipt(c.Param("hash"))
	if !ok {
		return apiErr(http.StatusNotFound, "TX_NOT_FOUND", "transaction not found")
	}
	return c.JSON(http.StatusOK, r)
}

// --- write handlers ---

const maxBody = 2 << 20

func readBody(c *echo.Context) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, apiErr(http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body above 2 MiB")
	}
	return body, nil
}

// txBytes accepts raw canonical CBOR or {"tx": "<base64 or hex>"}.
func txBytes(body []byte) ([]byte, error) {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		return nil, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "empty body")
	}
	if body[0] == '{' {
		var wrapper struct {
			Tx string `json:"tx"`
		}
		if err := json.Unmarshal(body, &wrapper); err != nil {
			return nil, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body")
		}
		if wrapper.Tx == "" {
			return nil, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "tx field required")
		}
		if b, err := base64.StdEncoding.DecodeString(wrapper.Tx); err == nil {
			return b, nil
		}
		return hex.DecodeString(wrapper.Tx)
	}
	return body, nil
}

func (s *Server) submitTx(c *echo.Context) error {
	body, err := readBody(c)
	if err != nil {
		return err
	}
	raw, err := txBytes(body)
	if err != nil {
		return err
	}
	keys := c.Request().Header.Values("Idempotency-Key")
	if len(keys) > 1 || (len(keys) == 1 && keys[0] == "") {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "one non-empty Idempotency-Key required")
	}
	hash, replay, err := s.submitWithKey(raw, c.Request().Header.Get("Idempotency-Key"), c.Request().Header.Get("Authorization"))
	if err != nil {
		return mapLedgerError(err)
	}
	if replay {
		if receipt, ok := s.node.Receipt(hash); ok {
			return c.JSON(http.StatusOK, map[string]any{"hash": hash, "status": "finalized", "receipt": receipt})
		}
	} else if s.cfg.Workspace != nil {
		if actor, ok := c.Get("actor").(string); ok {
			_ = s.cfg.Workspace.Audit(actor, workspace.ActionPaymentWrite, hash, "accepted", "")
		}
	}
	return c.JSON(http.StatusAccepted, map[string]any{"hash": hash, "status": "accepted"})
}

func (s *Server) simulateTx(c *echo.Context) error {
	body, err := readBody(c)
	if err != nil {
		return err
	}
	raw, err := txBytes(body)
	if err != nil {
		return err
	}
	r, err := s.node.Simulate(raw)
	if err != nil {
		return mapLedgerError(err)
	}
	return c.JSON(http.StatusOK, r)
}

func (s *Server) produceBlock(c *echo.Context) error {
	b, err := s.node.ProduceBlock(uint64(time.Now().Unix()))
	if err != nil {
		return mapLedgerError(err)
	}
	return c.JSON(http.StatusOK, blockView(b))
}

func (s *Server) faucet(c *echo.Context) error {
	if !s.cfg.Devnet {
		return apiErr(http.StatusNotFound, "NOT_FOUND", "faucet disabled")
	}
	var req struct {
		Address string `json:"address"`
	}
	body, err := readBody(c)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Address == "" {
		return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "address required")
	}
	hash, err := s.node.Faucet(req.Address)
	if err != nil {
		return mapLedgerError(err)
	}
	return c.JSON(http.StatusAccepted, map[string]any{"hash": hash})
}

func (s *Server) webhookKey(c *echo.Context) error {
	key := s.node.WebhookPublicKey()
	if key == "" {
		return apiErr(http.StatusNotFound, "NOT_FOUND", "webhooks disabled")
	}
	return c.JSON(http.StatusOK, map[string]string{"algorithm": "ed25519", "public_key": key})
}

// --- SSE ---

func blockEvent(b ledger.Block) node.Event {
	h, _ := b.Hash()
	return node.Event{Type: "block", Height: b.Height, Time: b.Time, Hash: h, TxHashes: b.TxHashes}
}

// --- errors ---

type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func apiErr(status int, code, msg string) error {
	return &apiError{status: status, code: code, msg: msg}
}

func writeError(c *echo.Context, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		_ = c.JSON(ae.status, map[string]any{"error": map[string]string{"code": ae.code, "message": ae.msg}})
		return
	}
	var le *ledger.Error
	if errors.As(err, &le) {
		_ = c.JSON(statusFor(le.Code), map[string]any{"error": map[string]string{"code": string(le.Code), "message": le.Message}})
		return
	}
	_ = c.JSON(http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "INTERNAL", "message": err.Error()}})
}

func mapLedgerError(err error) error {
	var le *ledger.Error
	if errors.As(err, &le) {
		return &apiError{status: statusFor(le.Code), code: string(le.Code), msg: le.Message}
	}
	if code, ok := protocolErrorCode(err); ok {
		return &apiError{status: http.StatusBadRequest, code: code, msg: err.Error()}
	}
	return err
}

// protocolErrorCode names wire-format failures that never reach the ledger.
func protocolErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, protocol.ErrSignature):
		return "BAD_SIGNATURE", true
	case errors.Is(err, protocol.ErrNetwork):
		return "WRONG_NETWORK", true
	case errors.Is(err, protocol.ErrVersion), errors.Is(err, protocol.ErrValidation),
		errors.Is(err, protocol.ErrCBOR), errors.Is(err, protocol.ErrNonCanon),
		errors.Is(err, protocol.ErrCBORType), errors.Is(err, protocol.ErrCBORLimits):
		return "INVALID_SCHEMA", true
	}
	return "", false
}

func statusFor(code ledger.Code) int {
	switch code {
	case ledger.CodeUnauthorized:
		return http.StatusForbidden
	case ledger.CodeEscrowNotFound, ledger.CodeUnknownAsset, ledger.CodeRefundNotFound, ledger.CodeSubNotFound:
		return http.StatusNotFound
	case ledger.CodeLimitExceeded:
		return http.StatusTooManyRequests
	case ledger.CodeMempoolConflict:
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// --- rate limiting ---

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
}

func newLimiter(rate, burst int) *limiter {
	return &limiter{rate: float64(rate), burst: float64(burst), buckets: map[string]*bucket{}}
}

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
