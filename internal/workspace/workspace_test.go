package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) *Workspace {
	t.Helper()
	dir := t.TempDir()
	w, err := New(
		map[string]Role{"alice": RoleOwner, "bob": RoleAdmin, "carol": RoleMember, "dave": RoleAuditor},
		map[string]string{"key-alice": "alice", "key-bob": "bob", "key-carol": "carol", "key-dave": "dave"},
		filepath.Join(dir, "audit.jsonl"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestRolePermissions(t *testing.T) {
	cases := []struct {
		role   Role
		action Action
		want   bool
	}{
		{RoleOwner, ActionSettings, true},
		{RoleAdmin, ActionSettings, false},
		{RoleAdmin, ActionMemberManage, true},
		{RoleMember, ActionMemberManage, false},
		{RoleMember, ActionPaymentWrite, true},
		{RoleAuditor, ActionPaymentRead, true},
		{RoleAuditor, ActionPaymentWrite, false},
		{RoleAuditor, ActionChannelWrite, false},
		{RoleAuditor, ActionAuditRead, true},
		{RoleMember, ActionAuditRead, false},
	}
	for _, c := range cases {
		if got := permissions[c.role][c.action]; got != c.want {
			t.Errorf("%s %s = %v, want %v", c.role, c.action, got, c.want)
		}
	}
	if !ValidRole(RoleAdmin) || ValidRole("root") {
		t.Fatal("role validation")
	}
}

func TestMemberCannotRemoveAdmin(t *testing.T) {
	w := fixture(t)
	if err := w.Remove("carol", "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member removed admin: %v", err)
	}
	if _, ok := w.RoleOf("bob"); !ok {
		t.Fatal("admin was removed")
	}
	entries := w.Entries(0, 10)
	if len(entries) != 1 || entries[0].Actor != "carol" || entries[0].Target != "bob" || entries[0].Result != "denied" {
		t.Fatalf("denial not audited: %+v", entries)
	}
}

func TestAdminManagesMembersButNotOwners(t *testing.T) {
	w := fixture(t)
	if err := w.SetRole("bob", "erin", RoleMember); err != nil {
		t.Fatal(err)
	}
	if role, ok := w.RoleOf("erin"); !ok || role != RoleMember {
		t.Fatalf("new member = %q %v", role, ok)
	}
	if err := w.SetRole("bob", "alice", RoleMember); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin demoted owner: %v", err)
	}
	if err := w.Remove("bob", "alice"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin removed owner: %v", err)
	}
	if err := w.SetRole("alice", "bob", RoleMember); err != nil {
		t.Fatal(err)
	}
	if role, _ := w.RoleOf("bob"); role != RoleMember {
		t.Fatalf("owner demote failed: %q", role)
	}
}

func TestLastOwnerCannotLeave(t *testing.T) {
	w := fixture(t)
	if err := w.SetRole("alice", "alice", RoleAdmin); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demoted last owner: %v", err)
	}
	if err := w.Remove("alice", "alice"); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removed last owner: %v", err)
	}
}

func TestAuditorIsReadOnly(t *testing.T) {
	w := fixture(t)
	if !w.Authorize("dave", ActionPaymentRead) || !w.Authorize("dave", ActionAuditRead) {
		t.Fatal("auditor lost read access")
	}
	if err := w.Require("dave", ActionPaymentWrite, "tx1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("auditor paid: %v", err)
	}
	if err := w.Require("dave", ActionMemberManage, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("auditor changed roles: %v", err)
	}
	entries := w.Entries(0, 10)
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d", len(entries))
	}
	for _, e := range entries {
		if e.Actor != "dave" || e.Result != "denied" || e.Time == 0 {
			t.Fatalf("bad audit entry: %+v", e)
		}
	}
}

func TestAuditPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.json")
	if err := os.WriteFile(path, []byte(`{"members":{"alice":"owner"},"principals":{"k":"alice"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Audit("alice", ActionPaymentWrite, "abc", "accepted", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.SetRole("alice", "bob", RoleMember); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := reloaded.RoleOf("bob"); !ok || role != RoleMember {
		t.Fatalf("membership not saved: %q %v", role, ok)
	}
	entries := reloaded.Entries(0, 10)
	if len(entries) != 2 || entries[0].Action != ActionPaymentWrite || entries[1].Seq != 2 {
		t.Fatalf("audit not reloaded: %+v", entries)
	}
	if got := reloaded.Entries(1, 10); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("pagination: %+v", got)
	}
}

func TestInvalidWorkspace(t *testing.T) {
	if _, err := New(map[string]Role{"alice": "root"}, nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown role: %v", err)
	}
	if _, err := New(map[string]Role{"alice": RoleMember}, nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ownerless workspace: %v", err)
	}
	if _, err := New(map[string]Role{"alice": RoleOwner}, map[string]string{"k": "bob"}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown principal: %v", err)
	}
}
