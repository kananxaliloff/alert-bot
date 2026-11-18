// Package storetest is a contract test suite for store.Store implementations.
// The in-memory store runs it in every `go test`; the Postgres store runs it
// when TEST_DATABASE_URL is set. Passing both means they behave the same.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kananxaliloff/alert-bot/internal/store"
)

// Factory returns a brand new, empty store for one subtest.
type Factory func(t *testing.T) store.Store

var testSeed = store.Seed{
	Roles: []store.RoleSeed{
		{Name: "admin", Title: "Administrator", Permissions: []string{"p:a", "p:b"}},
		{Name: "lead", Title: "Lead", Parent: "admin", Permissions: []string{"p:b"}},
		{Name: "dev", Title: "Developer", Parent: "lead"},
	},
	Tags: []store.TagSeed{
		{Kind: "team", Name: "be"},
		{Kind: "team", Name: "fe"},
		{Kind: "module", Name: "booking"},
	},
}

// Run executes every contract test against stores made by newStore.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	cases := map[string]func(*testing.T, store.Store){
		"SeedIsIdempotentAndBuildsHierarchy": testSeedIdempotent,
		"UserLifecycle":                      testUserLifecycle,
		"EmailIsUnique":                      testEmailUnique,
		"StatusApprovalFields":               testStatusApproval,
		"SetRole":                            testSetRole,
		"ListUsersFilterOrderPaging":         testListUsers,
		"TagsToggle":                         testTags,
		"OTPLifecycle":                       testOTP,
		"OTPCountsWindow":                    testOTPCounts,
		"DeleteUserRemovesEverything":        testDelete,
		"AuditNewestFirst":                   testAudit,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					if f, ok := r.(failure); ok {
						t.Fatalf("unexpected error: %v", f.err)
					}
					panic(r)
				}
			}()
			s := newStore(t)
			if err := s.Seed(ctx, testSeed); err != nil {
				t.Fatalf("seed: %v", err)
			}
			fn(t, s)
		})
	}
}

var ctx = context.Background()

// failure carries an unexpected error out of a test body; Run turns it into t.Fatalf.
type failure struct{ err error }

// must unwraps (value, error) pairs so test bodies stay short.
func must[T any](v T, err error) T {
	if err != nil {
		panic(failure{err})
	}
	return v
}

func roleByName(t *testing.T, s store.Store, name string) store.Role {
	t.Helper()
	for _, r := range must(s.ListRoles(ctx)) {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("role %q not found", name)
	return store.Role{}
}

func tagByName(t *testing.T, s store.Store, kind, name string) store.Tag {
	t.Helper()
	for _, tg := range must(s.ListTags(ctx)) {
		if tg.Kind == kind && tg.Name == name {
			return tg
		}
	}
	t.Fatalf("tag %s/%s not found", kind, name)
	return store.Tag{}
}

func testSeedIdempotent(t *testing.T, s store.Store) {
	if err := s.Seed(ctx, testSeed); err != nil { // second time
		t.Fatalf("second seed: %v", err)
	}
	roles := must(s.ListRoles(ctx))
	if len(roles) != 3 {
		t.Fatalf("want 3 roles after double seed, got %d", len(roles))
	}
	admin, lead, dev := roleByName(t, s, "admin"), roleByName(t, s, "lead"), roleByName(t, s, "dev")
	if admin.ParentID != 0 || lead.ParentID != admin.ID || dev.ParentID != lead.ID {
		t.Errorf("hierarchy wrong: admin.parent=%d lead.parent=%d dev.parent=%d", admin.ParentID, lead.ParentID, dev.ParentID)
	}
	if len(admin.Permissions) != 2 || len(lead.Permissions) != 1 || len(dev.Permissions) != 0 {
		t.Errorf("permissions wrong: %v %v %v", admin.Permissions, lead.Permissions, dev.Permissions)
	}
	if n := len(must(s.ListTags(ctx))); n != 3 {
		t.Errorf("want 3 tags, got %d", n)
	}
}

func testUserLifecycle(t *testing.T, s store.Store) {
	if _, err := s.GetUserByTelegramID(ctx, 111); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	u := must(s.GetOrCreateUser(ctx, 111))
	if u.ID == 0 || u.TelegramID != 111 || u.Status != store.StatusUnverified || u.Email != "" || u.RoleID != 0 {
		t.Fatalf("unexpected new user: %+v", u)
	}
	again := must(s.GetOrCreateUser(ctx, 111))
	if again.ID != u.ID {
		t.Fatalf("GetOrCreateUser must be idempotent: %d vs %d", again.ID, u.ID)
	}

	v := must(s.MarkVerified(ctx, store.Verification{
		UserID: u.ID, Email: "ann@corp.test", FullName: "Ann Example", JobTitle: "Engineer", Status: store.StatusPending,
	}))
	if v.Email != "ann@corp.test" || v.FullName != "Ann Example" || v.JobTitle != "Engineer" ||
		v.Status != store.StatusPending || v.VerifiedAt.IsZero() || !v.ApprovedAt.IsZero() {
		t.Fatalf("unexpected verified user: %+v", v)
	}

	byEmail := must(s.GetUserByEmail(ctx, "ann@corp.test"))
	if byEmail.ID != u.ID {
		t.Errorf("GetUserByEmail returned %d", byEmail.ID)
	}
	if _, err := s.GetUserByEmail(ctx, "nobody@corp.test"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := s.GetUser(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func testEmailUnique(t *testing.T, s store.Store) {
	a := must(s.GetOrCreateUser(ctx, 1))
	b := must(s.GetOrCreateUser(ctx, 2))
	must(s.MarkVerified(ctx, store.Verification{UserID: a.ID, Email: "x@corp.test", Status: store.StatusPending}))
	_, err := s.MarkVerified(ctx, store.Verification{UserID: b.ID, Email: "x@corp.test", Status: store.StatusPending})
	if !errors.Is(err, store.ErrEmailTaken) {
		t.Fatalf("want ErrEmailTaken, got %v", err)
	}
	// The same user may verify the same email again.
	must(s.MarkVerified(ctx, store.Verification{UserID: a.ID, Email: "x@corp.test", Status: store.StatusPending}))
}

func testStatusApproval(t *testing.T, s store.Store) {
	admin := must(s.GetOrCreateUser(ctx, 1))
	u := must(s.GetOrCreateUser(ctx, 2))
	must(s.MarkVerified(ctx, store.Verification{UserID: u.ID, Email: "u@corp.test", Status: store.StatusPending}))

	got := must(s.SetStatus(ctx, u.ID, store.StatusActive, admin.ID))
	if got.Status != store.StatusActive || got.ApprovedBy != admin.ID || got.ApprovedAt.IsZero() {
		t.Fatalf("approval not recorded: %+v", got)
	}
	disabled := must(s.SetStatus(ctx, u.ID, store.StatusDisabled, admin.ID))
	if disabled.Status != store.StatusDisabled {
		t.Fatalf("status = %s", disabled.Status)
	}
	other := must(s.GetOrCreateUser(ctx, 3))
	reenabled := must(s.SetStatus(ctx, u.ID, store.StatusActive, other.ID))
	if reenabled.ApprovedBy != admin.ID {
		t.Errorf("first approver must be kept, got %d", reenabled.ApprovedBy)
	}
	if _, err := s.SetStatus(ctx, 99999, store.StatusActive, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func testSetRole(t *testing.T, s store.Store) {
	u := must(s.GetOrCreateUser(ctx, 1))
	dev := roleByName(t, s, "dev")
	got := must(s.SetRole(ctx, u.ID, dev.ID))
	if got.RoleID != dev.ID {
		t.Fatalf("RoleID = %d, want %d", got.RoleID, dev.ID)
	}
	cleared := must(s.SetRole(ctx, u.ID, 0))
	if cleared.RoleID != 0 {
		t.Fatalf("role should be cleared, got %d", cleared.RoleID)
	}
	if _, err := s.SetRole(ctx, u.ID, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown role: want ErrNotFound, got %v", err)
	}
}

func testListUsers(t *testing.T, s store.Store) {
	var ids []int64
	for i := int64(1); i <= 5; i++ {
		u := must(s.GetOrCreateUser(ctx, 100+i))
		ids = append(ids, u.ID)
	}
	must(s.SetStatus(ctx, ids[1], store.StatusPending, 0))
	must(s.SetStatus(ctx, ids[3], store.StatusPending, 0))

	all := must(s.ListUsers(ctx, nil, 0, 0))
	if len(all) != 5 {
		t.Fatalf("all = %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Fatalf("not ordered by id: %v", all)
		}
	}
	pending := must(s.ListUsers(ctx, []store.UserStatus{store.StatusPending}, 0, 0))
	if len(pending) != 2 || pending[0].ID != ids[1] || pending[1].ID != ids[3] {
		t.Fatalf("pending = %+v", pending)
	}
	page := must(s.ListUsers(ctx, nil, 2, 1))
	if len(page) != 2 || page[0].ID != ids[1] || page[1].ID != ids[2] {
		t.Fatalf("page = %+v", page)
	}
	if n := must(s.CountUsers(ctx, store.StatusPending)); n != 2 {
		t.Errorf("CountUsers(pending) = %d", n)
	}
	if n := must(s.CountUsers(ctx, store.StatusActive)); n != 0 {
		t.Errorf("CountUsers(active) = %d", n)
	}
}

func testTags(t *testing.T, s store.Store) {
	u := must(s.GetOrCreateUser(ctx, 1))
	be, booking := tagByName(t, s, "team", "be"), tagByName(t, s, "module", "booking")

	if ids := must(s.UserTagIDs(ctx, u.ID)); len(ids) != 0 {
		t.Fatalf("new user has tags: %v", ids)
	}
	if added := must(s.ToggleUserTag(ctx, u.ID, be.ID)); !added {
		t.Error("first toggle should add")
	}
	must(s.ToggleUserTag(ctx, u.ID, booking.ID))
	ids := must(s.UserTagIDs(ctx, u.ID))
	if len(ids) != 2 {
		t.Fatalf("want 2 tags, got %v", ids)
	}
	if added := must(s.ToggleUserTag(ctx, u.ID, be.ID)); added {
		t.Error("second toggle should remove")
	}
	ids = must(s.UserTagIDs(ctx, u.ID))
	if len(ids) != 1 || ids[0] != booking.ID {
		t.Fatalf("want only booking, got %v", ids)
	}
	if _, err := s.ToggleUserTag(ctx, u.ID, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown tag: want ErrNotFound, got %v", err)
	}
	if _, err := s.ToggleUserTag(ctx, 99999, be.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown user: want ErrNotFound, got %v", err)
	}
}

func testOTP(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.LatestOTP(ctx, 7); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	// A rejected request (not sent) must never be returned as the latest OTP.
	must(s.CreateOTP(ctx, store.OTP{TelegramID: 7, Email: "a@corp.test", Sent: false, CreatedAt: now, ExpiresAt: now}))
	if _, err := s.LatestOTP(ctx, 7); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsent OTP must not be latest, got %v", err)
	}

	first := must(s.CreateOTP(ctx, store.OTP{
		TelegramID: 7, Email: "a@corp.test", CodeHash: []byte{1, 2, 3}, Sent: true,
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}))
	second := must(s.CreateOTP(ctx, store.OTP{
		TelegramID: 7, Email: "a@corp.test", CodeHash: []byte{9, 9}, Sent: true,
		CreatedAt: now.Add(time.Minute), ExpiresAt: now.Add(11 * time.Minute),
	}))
	if first.ID == 0 || second.ID <= first.ID {
		t.Fatalf("ids: %d, %d", first.ID, second.ID)
	}

	latest := must(s.LatestOTP(ctx, 7))
	if latest.ID != second.ID || string(latest.CodeHash) != string([]byte{9, 9}) || latest.Email != "a@corp.test" ||
		latest.Attempts != 0 || latest.Consumed || !latest.Sent {
		t.Fatalf("latest = %+v", latest)
	}
	if !latest.ExpiresAt.Equal(now.Add(11 * time.Minute)) {
		t.Errorf("ExpiresAt = %s", latest.ExpiresAt)
	}

	if n := must(s.IncrementOTPAttempts(ctx, second.ID)); n != 1 {
		t.Errorf("attempts = %d", n)
	}
	if n := must(s.IncrementOTPAttempts(ctx, second.ID)); n != 2 {
		t.Errorf("attempts = %d", n)
	}
	if err := s.ConsumeOTP(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	latest = must(s.LatestOTP(ctx, 7))
	if !latest.Consumed || latest.Attempts != 2 {
		t.Fatalf("latest after consume = %+v", latest)
	}
	if _, err := s.IncrementOTPAttempts(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if err := s.ConsumeOTP(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func testOTPCounts(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	add := func(tg int64, email string, age time.Duration, sent bool) {
		t.Helper()
		must(s.CreateOTP(ctx, store.OTP{TelegramID: tg, Email: email, Sent: sent, CreatedAt: now.Add(-age), ExpiresAt: now}))
	}
	add(1, "a@corp.test", 5*time.Minute, true)
	add(1, "b@corp.test", 10*time.Minute, false)
	add(1, "a@corp.test", 3*time.Hour, true) // outside the window
	add(2, "a@corp.test", 1*time.Minute, true)
	add(2, "c@corp.test", 1*time.Minute, true)

	byChat, byEmail := must2(t, s, 1, "a@corp.test", now.Add(-time.Hour))
	if byChat != 2 || byEmail != 2 {
		t.Errorf("byChat=%d byEmail=%d, want 2 and 2", byChat, byEmail)
	}
	byChat, byEmail = must2(t, s, 3, "nobody@corp.test", now.Add(-time.Hour))
	if byChat != 0 || byEmail != 0 {
		t.Errorf("byChat=%d byEmail=%d, want 0 and 0", byChat, byEmail)
	}
}

func must2(t *testing.T, s store.Store, tg int64, email string, since time.Time) (int, int) {
	t.Helper()
	a, b, err := s.CountOTPRequests(ctx, tg, email, since)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func testDelete(t *testing.T, s store.Store) {
	u := must(s.GetOrCreateUser(ctx, 55))
	be := tagByName(t, s, "team", "be")
	must(s.ToggleUserTag(ctx, u.ID, be.ID))
	now := time.Now().UTC()
	must(s.CreateOTP(ctx, store.OTP{TelegramID: 55, Email: "z@corp.test", Sent: true, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}))

	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUser(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("user still exists: %v", err)
	}
	if _, err := s.LatestOTP(ctx, 55); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("OTP history not removed: %v", err)
	}
	if err := s.DeleteUser(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
	// The same Telegram account can start over.
	again := must(s.GetOrCreateUser(ctx, 55))
	if again.Status != store.StatusUnverified {
		t.Errorf("re-created user = %+v", again)
	}
	if ids := must(s.UserTagIDs(ctx, again.ID)); len(ids) != 0 {
		t.Errorf("tags survived deletion: %v", ids)
	}
}

func testAudit(t *testing.T, s store.Store) {
	must0 := func(e store.AuditEntry) {
		t.Helper()
		if err := s.AppendAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	must0(store.AuditEntry{ActorID: 1, Action: "user.approve", TargetID: 2, Details: map[string]string{"role": "dev"}})
	must0(store.AuditEntry{ActorID: 0, Action: "system.start"})
	must0(store.AuditEntry{ActorID: 1, Action: "user.delete", TargetID: 3})

	got := must(s.ListAudit(ctx, 10))
	if len(got) != 3 || got[0].Action != "user.delete" || got[2].Action != "user.approve" {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[2].Details["role"] != "dev" || got[2].ActorID != 1 || got[2].TargetID != 2 || got[2].At.IsZero() {
		t.Errorf("entry fields wrong: %+v", got[2])
	}
	if limited := must(s.ListAudit(ctx, 2)); len(limited) != 2 {
		t.Errorf("limit not applied: %d", len(limited))
	}
}
