// Package access holds the permission model and the role hierarchy logic.
// It is pure: no database, no Telegram.
package access

import (
	"sort"

	"github.com/kananxaliloff/alert-bot/internal/store"
)

// Permissions. A role is granted a set of these; users inherit them from
// their role and from all roles below it in the hierarchy.
const (
	PermApproveUsers    = "users:approve"
	PermDisableUsers    = "users:disable"
	PermDeleteUsers     = "users:delete"
	PermAssignAnyRole   = "roles:assign_any"
	PermBroadcastAll    = "broadcast:all"
	PermBroadcastTeam   = "broadcast:team"
	PermBroadcastModule = "broadcast:module"
)

// AllPermissions lists every permission, for the admin role and for validation.
func AllPermissions() []string {
	return []string{
		PermApproveUsers, PermDisableUsers, PermDeleteUsers, PermAssignAnyRole,
		PermBroadcastAll, PermBroadcastTeam, PermBroadcastModule,
	}
}

// Default role names created by DefaultSeed.
const (
	RoleAdmin    = "admin"
	RoleTeamLead = "team_lead"
	RoleEngineer = "engineer"
	RoleObserver = "observer"
)

// DefaultSeed is the starting hierarchy:
// Administrator > Team Lead > Engineer > Observer, plus the initial tags.
func DefaultSeed() store.Seed {
	return store.Seed{
		Roles: []store.RoleSeed{
			{Name: RoleAdmin, Title: "Administrator", Permissions: AllPermissions()},
			{Name: RoleTeamLead, Title: "Team Lead", Parent: RoleAdmin,
				Permissions: []string{PermApproveUsers, PermBroadcastTeam, PermBroadcastModule}},
			{Name: RoleEngineer, Title: "Engineer", Parent: RoleTeamLead},
			{Name: RoleObserver, Title: "Observer", Parent: RoleEngineer},
		},
		Tags: []store.TagSeed{
			{Kind: "team", Name: "be"},
			{Kind: "team", Name: "fe"},
			{Kind: "module", Name: "booking"},
			{Kind: "module", Name: "trade-service"},
			{Kind: "module", Name: "trade-guide"},
			{Kind: "module", Name: "e-cmr"},
			{Kind: "module", Name: "devops"},
		},
	}
}

// Tree answers hierarchy and permission questions for a set of roles.
type Tree struct {
	roles    map[int64]store.Role
	children map[int64][]int64
}

// NewTree builds a Tree from the stored roles.
func NewTree(roles []store.Role) *Tree {
	t := &Tree{roles: make(map[int64]store.Role, len(roles)), children: make(map[int64][]int64)}
	for _, r := range roles {
		t.roles[r.ID] = r
	}
	for _, r := range roles {
		if r.ParentID != 0 {
			t.children[r.ParentID] = append(t.children[r.ParentID], r.ID)
		}
	}
	return t
}

// Role returns a role by id.
func (t *Tree) Role(id int64) (store.Role, bool) {
	r, ok := t.roles[id]
	return r, ok
}

// RoleByName returns a role by its machine name.
func (t *Tree) RoleByName(name string) (store.Role, bool) {
	for _, r := range t.roles {
		if r.Name == name {
			return r, true
		}
	}
	return store.Role{}, false
}

// Roles returns all roles ordered by id.
func (t *Tree) Roles() []store.Role {
	out := make([]store.Role, 0, len(t.roles))
	for _, r := range t.roles {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Has reports whether a role grants a permission, directly or through any
// role below it in the hierarchy.
func (t *Tree) Has(roleID int64, perm string) bool {
	if _, ok := t.roles[roleID]; !ok {
		return false
	}
	seen := map[int64]bool{}
	var walk func(id int64) bool
	walk = func(id int64) bool {
		if seen[id] { // guards against accidental cycles
			return false
		}
		seen[id] = true
		for _, p := range t.roles[id].Permissions {
			if p == perm {
				return true
			}
		}
		for _, c := range t.children[id] {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(roleID)
}

// IsAbove reports whether ancestor is a strict ancestor of descendant.
func (t *Tree) IsAbove(ancestor, descendant int64) bool {
	if ancestor == 0 || descendant == 0 || ancestor == descendant {
		return false
	}
	seen := map[int64]bool{}
	cur := t.roles[descendant].ParentID
	for cur != 0 && !seen[cur] {
		if cur == ancestor {
			return true
		}
		seen[cur] = true
		cur = t.roles[cur].ParentID
	}
	return false
}

// CanAssign reports whether someone with actorRole may give targetRole to a
// user: either they may assign any role, or the target role is below theirs.
func (t *Tree) CanAssign(actorRole, targetRole int64) bool {
	if _, ok := t.roles[targetRole]; !ok {
		return false
	}
	return t.Has(actorRole, PermAssignAnyRole) || t.IsAbove(actorRole, targetRole)
}

// AssignableRoles lists the roles the actor may hand out, ordered by id.
func (t *Tree) AssignableRoles(actorRole int64) []store.Role {
	var out []store.Role
	for _, r := range t.Roles() {
		if t.CanAssign(actorRole, r.ID) {
			out = append(out, r)
		}
	}
	return out
}

// CanManage reports whether actor may perform a management action that needs
// perm (approve, disable, delete) on target. Nobody manages themselves, and
// without the assign-any permission an actor can only manage users whose
// role is below theirs or who have no role yet (pending requests).
func (t *Tree) CanManage(actor, target store.User, perm string) bool {
	if actor.ID == target.ID || actor.Status != store.StatusActive {
		return false
	}
	if !t.Has(actor.RoleID, perm) {
		return false
	}
	if t.Has(actor.RoleID, PermAssignAnyRole) {
		return true
	}
	return target.RoleID == 0 || t.IsAbove(actor.RoleID, target.RoleID)
}
