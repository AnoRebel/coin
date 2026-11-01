// Package access implements scoped, time-bounded auditor access grants.
//
// A grant names one auditor, the assets they may read, and a half-open time
// window [From, Until). Checks are logged whether they succeed or fail, so an
// auditor cannot read outside scope without leaving a record. The package does
// not decide who may create grants; the API requires an owner or admin.
package access

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalid    = errors.New("access: invalid grant")
	ErrExists     = errors.New("access: grant already exists")
	ErrUnknown    = errors.New("access: unknown grant")
	ErrNoGrant    = errors.New("access: no grant")
	ErrExpired    = errors.New("access: grant expired")
	ErrOutOfScope = errors.New("access: asset outside grant scope")
)

// Grant authorizes one auditor to read one or more assets for a time window.
type Grant struct {
	ID      string   `json:"id"`
	Auditor string   `json:"auditor"`
	Assets  []string `json:"assets"`
	From    int64    `json:"from"`
	Until   int64    `json:"until"`
	Purpose string   `json:"purpose"`
}

// Event is one access or lifecycle log record.
type Event struct {
	Seq     uint64 `json:"seq"`
	Time    int64  `json:"time"`
	Auditor string `json:"auditor"`
	Action  string `json:"action"` // access, grant, revoke
	Grant   string `json:"grant,omitempty"`
	Asset   string `json:"asset,omitempty"`
	Result  string `json:"result"` // allowed, no-grant, expired, not-yet-active, out-of-scope, created, revoked
	Purpose string `json:"purpose,omitempty"`
}

// Registry holds grants and the access log.
//
// shortcut: the log is held in memory as well as appended to disk; stream it
// when audit volume makes that costly.
type Registry struct {
	mu      sync.Mutex
	path    string
	logPath string
	grants  map[string]Grant
	events  []Event
	seq     uint64
}

type file struct {
	Grants []Grant `json:"grants"`
}

// New builds an in-memory registry. A non-empty path persists grants there and
// appends the access log to "<path>.log.jsonl".
func New(path string) *Registry {
	r := &Registry{grants: map[string]Grant{}}
	if path != "" {
		r.path = path
		r.logPath = path + ".log.jsonl"
	}
	return r
}

// Load reads a grants file and its sibling access log.
func Load(path string) (*Registry, error) {
	r := New(path)
	raw, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for _, g := range f.Grants {
		if err := g.validate(); err != nil {
			return nil, err
		}
		if g.ID == "" {
			g.ID = g.id()
		}
		if _, dup := r.grants[g.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate grant %s", ErrInvalid, g.ID)
		}
		r.grants[g.ID] = g
	}
	if err := r.loadLog(); err != nil {
		return nil, err
	}
	return r, nil
}

// Grant validates and stores a grant, assigning its deterministic ID.
func (r *Registry) Grant(g Grant) (Grant, error) {
	g.Auditor = strings.TrimSpace(g.Auditor)
	g.Purpose = strings.TrimSpace(g.Purpose)
	g.Assets = normalizeAssets(g.Assets)
	if err := g.validate(); err != nil {
		return Grant{}, err
	}
	g.ID = g.id()

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.grants[g.ID]; exists {
		return Grant{}, fmt.Errorf("%w: %s", ErrExists, g.ID)
	}
	r.grants[g.ID] = g
	if err := r.saveLocked(); err != nil {
		delete(r.grants, g.ID)
		return Grant{}, err
	}
	r.appendLocked(Event{
		Time: now(), Auditor: g.Auditor, Action: "grant", Grant: g.ID,
		Result: "created", Purpose: g.Purpose,
	})
	return g, nil
}

// Revoke removes a grant. Revoking an unknown grant is an error.
func (r *Registry) Revoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.grants[id]
	if !ok {
		return ErrUnknown
	}
	delete(r.grants, id)
	if err := r.saveLocked(); err != nil {
		r.grants[id] = g
		return err
	}
	r.appendLocked(Event{
		Time: now(), Auditor: g.Auditor, Action: "revoke", Grant: g.ID,
		Result: "revoked", Purpose: g.Purpose,
	})
	return nil
}

// List returns grants, optionally for one auditor, sorted by ID.
func (r *Registry) List(auditor string) []Grant {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Grant, 0, len(r.grants))
	for _, g := range r.grants {
		if auditor == "" || g.Auditor == auditor {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Check reports whether an auditor may read an asset at a unix time. Every call
// appends an access event.
func (r *Registry) Check(auditor, asset string, at int64) (Grant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	auditor = strings.TrimSpace(auditor)
	asset = strings.TrimSpace(asset)

	var candidates []Grant
	for _, g := range r.grants {
		if g.Auditor == auditor {
			candidates = append(candidates, g)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	result, grantID := "no-grant", ""
	var reason error = ErrNoGrant
	if len(candidates) > 0 {
		result, reason = "out-of-scope", ErrOutOfScope
	}
	for _, g := range candidates {
		if !contains(g.Assets, asset) {
			continue
		}
		grantID = g.ID
		switch {
		case at < g.From:
			result, reason = "not-yet-active", ErrExpired
		case at >= g.Until:
			result, reason = "expired", ErrExpired
		default:
			result, reason = "allowed", nil
			r.appendLocked(Event{
				Time: now(), Auditor: auditor, Action: "access", Grant: g.ID,
				Asset: asset, Result: result, Purpose: g.Purpose,
			})
			return g, nil
		}
	}
	r.appendLocked(Event{
		Time: now(), Auditor: auditor, Action: "access", Grant: grantID,
		Asset: asset, Result: result,
	})
	return Grant{}, reason
}

// Events returns log records with Seq > after, up to limit (default 100).
func (r *Registry) Events(after uint64, limit int) []Event {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Event{}
	for _, e := range r.events {
		if e.Seq <= after {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out
}

func (g Grant) validate() error {
	if g.Auditor == "" {
		return fmt.Errorf("%w: auditor is required", ErrInvalid)
	}
	if len(g.Assets) == 0 {
		return fmt.Errorf("%w: at least one asset is required", ErrInvalid)
	}
	if len(g.Assets) > 32 {
		return fmt.Errorf("%w: at most 32 assets per grant", ErrInvalid)
	}
	if g.From < 0 || g.Until <= 0 || g.Until <= g.From {
		return fmt.Errorf("%w: window must satisfy 0 <= from < until", ErrInvalid)
	}
	if g.Purpose == "" || len(g.Purpose) > 256 {
		return fmt.Errorf("%w: purpose must be 1-256 characters", ErrInvalid)
	}
	return nil
}

func (g Grant) id() string {
	assets := append([]string(nil), g.Assets...)
	sort.Strings(assets)
	h := sha256.New()
	_, _ = h.Write([]byte("arne-grant-v1"))
	for _, part := range []string{
		g.Auditor, g.Purpose,
		strconv.FormatInt(g.From, 10), strconv.FormatInt(g.Until, 10),
		strings.Join(assets, "\x00"),
	} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

func normalizeAssets(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func (r *Registry) saveLocked() error {
	if r.path == "" {
		return nil
	}
	grants := make([]Grant, 0, len(r.grants))
	for _, g := range r.grants {
		grants = append(grants, g)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	raw, err := json.MarshalIndent(file{Grants: grants}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (r *Registry) appendLocked(e Event) {
	r.seq++
	e.Seq = r.seq
	if e.Time == 0 {
		e.Time = now()
	}
	r.events = append(r.events, e)
	if r.logPath == "" {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Clean(r.logPath), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
	_ = f.Sync()
}

func (r *Registry) loadLog() error {
	if r.logPath == "" {
		return nil
	}
	f, err := os.Open(filepath.Clean(r.logPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return fmt.Errorf("%w: access log: %v", ErrInvalid, err)
		}
		r.events = append(r.events, e)
		if e.Seq > r.seq {
			r.seq = e.Seq
		}
	}
	return scanner.Err()
}

func now() int64 { return time.Now().Unix() }
