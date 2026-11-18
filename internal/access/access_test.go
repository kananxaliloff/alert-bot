package access

import (
	"context"
	"testing"

	"github.com/kananxaliloff/alert-bot/internal/store"
)

// defaultTree seeds an in-memory store with DefaultSeed and returns its tree.
func defaultTree(t *testing.T) *Tree {
	t.Helper()
	s := store.NewMemory()
	if err := s.Seed(context.Background(), DefaultSeed()); err != nil {
		t.Fatal(err)
	}
	roles, err := s.ListRoles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return NewTree(roles)
}

func role(t *testing.T, tr *Tree, name string) store.Role {
	t.Helper()
	r, ok := tr.RoleByName(name)
	if !ok {
		t.Fatalf("role %s missing", name)
	}
	return r
}

func TestDefaultSeedIsValid(t *testing.T) {
	seed := DefaultSeed()
	seen := map[string]bool{}
	valid := map[string]bool{}
	for _, p := range AllPermissions() {
		valid[p] = true
	}
	for _, r := range seed.Roles {
		if r.Parent != "" && !seen[r.Parent] {
			t.Errorf("role %s comes before its parent %s", r.Name, r.Parent)
		}
		seen[r.Name] = true
		for _, p := range r.Permissions {
			if !valid[p] {
				t.Errorf("role %s has unknown permission %s", r.Name, p)
			}
		}
	}
}

func TestHasInheritsFromRolesBelow(t *testing.T) {
	tr := defaultTree(t)
	admin, lead, eng := role(t, tr, RoleAdmin), role(t, tr, RoleTeamLead), role(t, tr, RoleEngineer)

	if !tr.Has(lead.ID, PermApproveUsers) {
		t.Error("team lead should approve users")
	}
	if tr.Has(lead.ID, PermDeleteUsers) {
		t.Error("team lead must not delete users")
	}
	if tr.Has(eng.ID, PermApproveUsers) {
		t.Error("engineer must not approve users")
	}
	for _, p := range AllPermissions() {
		if !tr.Has(admin.ID, p) {
			t.Errorf("admin lacks %s", p)
		}
	}
	if tr.Has(0, PermApproveUsers) || tr.Has(9999, PermApproveUsers) {
		t.Error("unknown or empty role has no permissions")
	}
}

func TestInheritanceFlowsUpward(t *testing.T) {
	// A permission granted only to a lower role is inherited by the roles above it.
	roles := []store.Role{
		{ID: 1, Name: "top"},
		{ID: 2, Name: "mid", ParentID: 1},
		{ID: 3, Name: "low", ParentID: 2, Permissions: []string{"x"}},
	}
	tr := NewTree(roles)
	if !tr.Has(1, "x") || !tr.Has(2, "x") || !tr.Has(3, "x") {
		t.Error("x should be effective for every ancestor of low")
	}
}

func TestIsAbove(t *testing.T) {
	tr := defaultTree(t)
	admin, lead, eng, obs := role(t, tr, RoleAdmin), role(t, tr, RoleTeamLead), role(t, tr, RoleEngineer), role(t, tr, RoleObserver)

	cases := []struct {
		a, b store.Role
		want bool
	}{
		{admin, obs, true}, {lead, eng, true}, {admin, lead, true},
		{eng, lead, false}, {lead, lead, false}, {obs, admin, false},
	}
	for _, c := range cases {
		if got := tr.IsAbove(c.a.ID, c.b.ID); got != c.want {
			t.Errorf("IsAbove(%s,%s) = %v, want %v", c.a.Name, c.b.Name, got, c.want)
		}
	}
	if tr.IsAbove(0, obs.ID) || tr.IsAbove(admin.ID, 0) {
		t.Error("role 0 is never above or below anything")
	}
}

func TestCycleDoesNotHang(t *testing.T) {
	tr := NewTree([]store.Role{{ID: 1, ParentID: 2}, {ID: 2, ParentID: 1}})
	if tr.Has(1, "x") {
		t.Error("no permission expected")
	}
	if tr.IsAbove(3, 1) {
		t.Error("unexpected")
	}
	_ = tr.IsAbove(1, 2) // must terminate
}

func TestCanAssign(t *testing.T) {
	tr := defaultTree(t)
	admin, lead, eng, obs := role(t, tr, RoleAdmin), role(t, tr, RoleTeamLead), role(t, tr, RoleEngineer), role(t, tr, RoleObserver)

	if !tr.CanAssign(admin.ID, admin.ID) || !tr.CanAssign(admin.ID, obs.ID) {
		t.Error("admin may assign any role")
	}
	if !tr.CanAssign(lead.ID, eng.ID) || !tr.CanAssign(lead.ID, obs.ID) {
		t.Error("lead may assign roles below lead")
	}
	if tr.CanAssign(lead.ID, lead.ID) || tr.CanAssign(lead.ID, admin.ID) {
		t.Error("lead must not assign own level or above")
	}
	// CanAssign is only about hierarchy. Whether someone may assign at all is a
	// separate check (users:approve), covered in TestCanManage.
	if !tr.CanAssign(eng.ID, obs.ID) {
		t.Error("observer is below engineer in the hierarchy")
	}
	if tr.CanAssign(eng.ID, 9999) {
		t.Error("unknown target role must be rejected")
	}
}

func TestAssignableRoles(t *testing.T) {
	tr := defaultTree(t)
	lead := role(t, tr, RoleTeamLead)
	got := tr.AssignableRoles(lead.ID)
	if len(got) != 2 || got[0].Name != RoleEngineer || got[1].Name != RoleObserver {
		t.Fatalf("AssignableRoles(lead) = %+v", got)
	}
	if n := len(tr.AssignableRoles(role(t, tr, RoleAdmin).ID)); n != 4 {
		t.Errorf("admin should assign all 4 roles, got %d", n)
	}
}

func TestCanManage(t *testing.T) {
	tr := defaultTree(t)
	admin, lead, eng := role(t, tr, RoleAdmin), role(t, tr, RoleTeamLead), role(t, tr, RoleEngineer)

	adminU := store.User{ID: 1, RoleID: admin.ID, Status: store.StatusActive}
	leadU := store.User{ID: 2, RoleID: lead.ID, Status: store.StatusActive}
	engU := store.User{ID: 3, RoleID: eng.ID, Status: store.StatusActive}
	pending := store.User{ID: 4, Status: store.StatusPending}

	check := func(name string, actor, target store.User, perm string, want bool) {
		t.Helper()
		if got := tr.CanManage(actor, target, perm); got != want {
			t.Errorf("%s: CanManage = %v, want %v", name, got, want)
		}
	}
	check("lead approves pending", leadU, pending, PermApproveUsers, true)
	check("admin deletes lead", adminU, leadU, PermDeleteUsers, true)
	check("lead cannot delete (no perm)", leadU, engU, PermDeleteUsers, false)
	check("engineer cannot approve", engU, pending, PermApproveUsers, false)
	check("nobody manages self", adminU, adminU, PermDisableUsers, false)
	check("lead cannot approve admin-level user", leadU, adminU, PermApproveUsers, false)
	check("lead approves lower role", leadU, engU, PermApproveUsers, true)

	disabledAdmin := adminU
	disabledAdmin.Status = store.StatusDisabled
	check("disabled actor loses power", disabledAdmin, pending, PermApproveUsers, false)
}
