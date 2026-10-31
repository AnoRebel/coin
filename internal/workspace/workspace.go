// Package workspace implements server-side roles, permissions, and an
// append-only audit log for team deployments.
//
// A workspace file maps API keys ("principals") to member IDs and member IDs to
// roles. Membership changes are saved back atomically; every privileged attempt
// is appended to a sibling JSONL audit log. The file contains API keys, so keep
// it at mode 0600.
package workspace

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Role is a workspace role.
type Role string

const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleMember  Role = "member"
	RoleAuditor Role = "auditor"
)

// Action is a permission-controlled operation.
type Action string

const (
	ActionChannelRead   Action = "channel.read"
	ActionChannelWrite  Action = "channel.write"
	ActionChannelManage Action = "channel.manage"
	ActionPaymentRead   Action = "payment.read"
	ActionPaymentWrite  Action = "payment.write"
	ActionMemberRead    Action = "member.read"
	ActionMemberManage  Action = "member.manage"
	ActionSettings      Action = "settings.manage"
	ActionAuditRead     Action = "audit.read"
)

var (
	ErrForbidden     = errors.New("workspace: forbidden")
	ErrUnknownActor  = errors.New("workspace: unknown actor")
	ErrUnknownMember = errors.New("workspace: unknown member")
	ErrLastOwner     = errors.New("workspace: cannot remove the last owner")
	ErrInvalid       = errors.New("workspace: invalid workspace")
)

var permissions = map[Role]map[Action]bool{
	RoleOwner: {
		ActionChannelRead: true, ActionChannelWrite: true, ActionChannelManage: true,
		ActionPaymentRead: true, ActionPaymentWrite: true,
		ActionMemberRead: true, ActionMemberManage: true,
		ActionSettings: true, ActionAuditRead: true,
	},
	RoleAdmin: {
		ActionChannelRead: true, ActionChannelWrite: true, ActionChannelManage: true,
		ActionPaymentRead: true, ActionPaymentWrite: true,
		ActionMemberRead: true, ActionMemberManage: true,
		ActionAuditRead: true,
	},
	RoleMember: {
		ActionChannelRead: true, ActionChannelWrite: true,
		ActionPaymentRead: true, ActionPaymentWrite: true,
		ActionMemberRead: true,
	},
	RoleAuditor: {
		ActionChannelRead: true, ActionPaymentRead: true,
		ActionMemberRead: true, ActionAuditRead: true,
	},
}

var allActions = []Action{
	ActionChannelRead, ActionChannelWrite, ActionChannelManage,
	ActionPaymentRead, ActionPaymentWrite, ActionMemberRead,
	ActionMemberManage, ActionSettings, ActionAuditRead,
}

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool {
	_, ok := permissions[r]
	return ok
}

// Permissions lists the actions a role may perform.
func Permissions(r Role) []Action {
	out := []Action{}
	for _, a := range allActions {
		if permissions[r][a] {
			out = append(out, a)
		}
	}
	return out
}

// Member is a member view safe for API responses.
type Member struct {
	ID   string `json:"id"`
	Role Role   `json:"role"`
}

// Entry is one audit log record.
type Entry struct {
	Seq    uint64 `json:"seq"`
	Time   uint64 `json:"time"`
	Actor  string `json:"actor"`
	Action Action `json:"action"`
	Target string `json:"target,omitempty"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// File is the on-disk workspace document.
type File struct {
	Members    map[string]Role   `json:"members"`
	Principals map[string]string `json:"principals"`
}

// Workspace holds membership, permissions, and the audit log.
//
// shortcut: the audit log is held in memory as well as appended to disk; stream
// it when audit volume makes that costly.
type Workspace struct {
	mu         sync.Mutex
	path       string
	auditPath  string
	members    map[string]Role
	principals map[string]string
	entries    []Entry
	seq        uint64
}

// Load reads a workspace file and its sibling "<path>.audit.jsonl" audit log.
func Load(path string) (*Workspace, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	w, err := New(f.Members, f.Principals, path+".audit.jsonl")
	if err != nil {
		return nil, err
	}
	w.path = path
	if err := w.loadAudit(); err != nil {
		return nil, err
	}
	return w, nil
}

// New builds an in-memory workspace. A non-empty auditPath appends entries there.
func New(members map[string]Role, principals map[string]string, auditPath string) (*Workspace, error) {
	w := &Workspace{
		auditPath:  auditPath,
		members:    make(map[string]Role, len(members)),
		principals: make(map[string]string, len(principals)),
	}
	owners := 0
	for id, role := range members {
		if id == "" || !ValidRole(role) {
			return nil, fmt.Errorf("%w: member %q has role %q", ErrInvalid, id, role)
		}
		if role == RoleOwner {
			owners++
		}
		w.members[id] = role
	}
	if owners == 0 {
		return nil, fmt.Errorf("%w: at least one owner is required", ErrInvalid)
	}
	for key, id := range principals {
		if key == "" {
			return nil, fmt.Errorf("%w: empty principal", ErrInvalid)
		}
		if _, ok := w.members[id]; !ok {
			return nil, fmt.Errorf("%w: principal maps to unknown member %q", ErrInvalid, id)
		}
		w.principals[key] = id
	}
	if auditPath != "" {
		if err := os.MkdirAll(filepath.Dir(filepath.Clean(auditPath)), 0o750); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// Actor resolves an API key to a member ID.
func (w *Workspace) Actor(key string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id, ok := w.principals[key]
	return id, ok
}

// RoleOf returns a member's role.
func (w *Workspace) RoleOf(id string) (Role, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.members[id]
	return r, ok
}

// Members lists members sorted by ID.
func (w *Workspace) Members() []Member {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Member, 0, len(w.members))
	for id, role := range w.members {
		out = append(out, Member{ID: id, Role: role})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Authorize reports whether a member may perform an action.
func (w *Workspace) Authorize(id string, a Action) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return permissions[w.members[id]][a]
}

// Require authorizes an action and audits a denial.
func (w *Workspace) Require(id string, a Action, target string) error {
	if w.Authorize(id, a) {
		return nil
	}
	_ = w.append(Entry{Actor: id, Action: a, Target: target, Result: "denied"})
	return ErrForbidden
}

// Audit records a successful privileged action.
func (w *Workspace) Audit(actor string, a Action, target, result, detail string) error {
	return w.append(Entry{Actor: actor, Action: a, Target: target, Result: result, Detail: detail})
}

// SetRole adds a member or changes a role. RoleAdmin+ may manage non-owners; only an
// owner may manage owners, and the last owner cannot be demoted.
func (w *Workspace) SetRole(actor, id string, role Role) error {
	if id == "" || !ValidRole(role) {
		return fmt.Errorf("%w: role %q", ErrInvalid, role)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.manageLocked(actor, id, role); err != nil {
		return err
	}
	previous, existed := w.members[id]
	w.members[id] = role
	if err := w.saveLocked(); err != nil {
		if existed {
			w.members[id] = previous
		} else {
			delete(w.members, id)
		}
		return err
	}
	detail := "added"
	if existed {
		detail = string(previous) + " -> " + string(role)
	}
	w.entries = append(w.entries, Entry{
		Seq: w.nextSeqLocked(), Time: now(), Actor: actor, Action: ActionMemberManage,
		Target: id, Result: "ok", Detail: detail,
	})
	return w.writeLastLocked()
}

// Remove deletes a member. Only an owner may remove an owner, and the last
// owner cannot be removed.
func (w *Workspace) Remove(actor, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.manageLocked(actor, id, ""); err != nil {
		return err
	}
	if _, ok := w.members[id]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMember, id)
	}
	if w.members[id] == RoleOwner && w.ownersLocked() == 1 {
		return ErrLastOwner
	}
	previous := w.members[id]
	delete(w.members, id)
	removed := map[string]string{}
	for key, principal := range w.principals {
		if principal == id {
			removed[key] = principal
			delete(w.principals, key)
		}
	}
	if err := w.saveLocked(); err != nil {
		w.members[id] = previous
		for key, principal := range removed {
			w.principals[key] = principal
		}
		return err
	}
	w.entries = append(w.entries, Entry{
		Seq: w.nextSeqLocked(), Time: now(), Actor: actor, Action: ActionMemberManage,
		Target: id, Result: "ok", Detail: "removed",
	})
	return w.writeLastLocked()
}

func (w *Workspace) manageLocked(actor, id string, role Role) error {
	actorRole, ok := w.members[actor]
	if !ok {
		return ErrUnknownActor
	}
	if !permissions[actorRole][ActionMemberManage] {
		_ = w.appendLocked(Entry{Actor: actor, Action: ActionMemberManage, Target: id, Result: "denied"})
		return ErrForbidden
	}
	targetRole, exists := w.members[id]
	ownerTarget := (exists && targetRole == RoleOwner) || role == RoleOwner
	if ownerTarget && actorRole != RoleOwner {
		_ = w.appendLocked(Entry{Actor: actor, Action: ActionMemberManage, Target: id, Result: "denied", Detail: "owner change requires owner"})
		return ErrForbidden
	}
	if exists && targetRole == RoleOwner && role != RoleOwner && w.ownersLocked() == 1 {
		return ErrLastOwner
	}
	return nil
}

func (w *Workspace) ownersLocked() int {
	n := 0
	for _, role := range w.members {
		if role == RoleOwner {
			n++
		}
	}
	return n
}

// Entries returns audit records with Seq > after, up to limit (default 100).
func (w *Workspace) Entries(after uint64, limit int) []Entry {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []Entry{}
	for _, e := range w.entries {
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

func (w *Workspace) append(e Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendLocked(e)
}

func (w *Workspace) appendLocked(e Entry) error {
	e.Seq = w.nextSeqLocked()
	e.Time = now()
	w.entries = append(w.entries, e)
	return w.writeLastLocked()
}

func (w *Workspace) nextSeqLocked() uint64 {
	w.seq++
	return w.seq
}

func (w *Workspace) writeLastLocked() error {
	if w.auditPath == "" || len(w.entries) == 0 {
		return nil
	}
	raw, err := json.Marshal(w.entries[len(w.entries)-1])
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Clean(w.auditPath), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (w *Workspace) loadAudit() error {
	if w.auditPath == "" {
		return nil
	}
	f, err := os.Open(filepath.Clean(w.auditPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return fmt.Errorf("%w: audit log: %v", ErrInvalid, err)
		}
		w.entries = append(w.entries, e)
		if e.Seq > w.seq {
			w.seq = e.Seq
		}
	}
	return scanner.Err()
}

func (w *Workspace) saveLocked() error {
	if w.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(File{Members: w.members, Principals: w.principals}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func now() uint64 { return uint64(time.Now().Unix()) }
