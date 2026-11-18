package store

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is an in-process Store. It is used by tests, and it defines the
// behaviour that the Postgres implementation must match (see storetest).
type Memory struct {
	mu sync.Mutex

	nextUser, nextRole, nextTag, nextOTP, nextAudit int64

	users    map[int64]*User
	roles    map[int64]*Role
	tags     map[int64]Tag
	userTags map[int64]map[int64]bool // user id -> tag id set
	otps     []*OTP
	audit    []AuditEntry
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		users:    map[int64]*User{},
		roles:    map[int64]*Role{},
		tags:     map[int64]Tag{},
		userTags: map[int64]map[int64]bool{},
	}
}

var _ Store = (*Memory)(nil)

func (m *Memory) Seed(_ context.Context, seed Seed) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, rs := range seed.Roles {
		if m.roleByName(rs.Name) != nil {
			continue // never overwrite an existing role
		}
		var parentID int64
		if rs.Parent != "" {
			p := m.roleByName(rs.Parent)
			if p == nil {
				return errSeedParent(rs.Name, rs.Parent)
			}
			parentID = p.ID
		}
		m.nextRole++
		m.roles[m.nextRole] = &Role{
			ID: m.nextRole, Name: rs.Name, Title: rs.Title, ParentID: parentID,
			Permissions: append([]string(nil), rs.Permissions...),
		}
	}
	for _, ts := range seed.Tags {
		if m.tagExists(ts.Kind, ts.Name) {
			continue
		}
		m.nextTag++
		m.tags[m.nextTag] = Tag{ID: m.nextTag, Kind: ts.Kind, Name: ts.Name}
	}
	return nil
}

func (m *Memory) roleByName(name string) *Role {
	for _, r := range m.roles {
		if r.Name == name {
			return r
		}
	}
	return nil
}

func (m *Memory) tagExists(kind, name string) bool {
	for _, t := range m.tags {
		if t.Kind == kind && t.Name == name {
			return true
		}
	}
	return false
}

func (m *Memory) GetOrCreateUser(_ context.Context, telegramID int64) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.TelegramID == telegramID {
			return *u, nil
		}
	}
	m.nextUser++
	u := &User{ID: m.nextUser, TelegramID: telegramID, Status: StatusUnverified, CreatedAt: time.Now()}
	m.users[u.ID] = u
	return *u, nil
}

func (m *Memory) GetUser(_ context.Context, id int64) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[id]; ok {
		return *u, nil
	}
	return User{}, ErrNotFound
}

func (m *Memory) GetUserByTelegramID(_ context.Context, telegramID int64) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.TelegramID == telegramID {
			return *u, nil
		}
	}
	return User{}, ErrNotFound
}

func (m *Memory) GetUserByEmail(_ context.Context, email string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if email == "" {
		return User{}, ErrNotFound
	}
	for _, u := range m.users {
		if u.Email == email {
			return *u, nil
		}
	}
	return User{}, ErrNotFound
}

func (m *Memory) ListUsers(_ context.Context, statuses []UserStatus, limit, offset int) ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []User
	for _, u := range m.users {
		if len(statuses) > 0 && !containsStatus(statuses, u.Status) {
			continue
		}
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func containsStatus(list []UserStatus, s UserStatus) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (m *Memory) CountUsers(_ context.Context, status UserStatus) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, u := range m.users {
		if u.Status == status {
			n++
		}
	}
	return n, nil
}

func (m *Memory) MarkVerified(_ context.Context, v Verification) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[v.UserID]
	if !ok {
		return User{}, ErrNotFound
	}
	for _, other := range m.users {
		if other.ID != u.ID && v.Email != "" && other.Email == v.Email {
			return User{}, ErrEmailTaken
		}
	}
	now := time.Now()
	u.Email, u.FullName, u.JobTitle = v.Email, v.FullName, v.JobTitle
	u.Status, u.RoleID, u.VerifiedAt = v.Status, v.RoleID, now
	if v.Status == StatusActive && u.ApprovedAt.IsZero() {
		u.ApprovedAt = now
	}
	return *u, nil
}

func (m *Memory) SetStatus(_ context.Context, userID int64, status UserStatus, actorID int64) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return User{}, ErrNotFound
	}
	u.Status = status
	if status == StatusActive {
		if u.ApprovedAt.IsZero() {
			u.ApprovedAt = time.Now()
		}
		if u.ApprovedBy == 0 {
			u.ApprovedBy = actorID
		}
	}
	return *u, nil
}

func (m *Memory) SetRole(_ context.Context, userID, roleID int64) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return User{}, ErrNotFound
	}
	if roleID != 0 {
		if _, ok := m.roles[roleID]; !ok {
			return User{}, ErrNotFound
		}
	}
	u.RoleID = roleID
	return *u, nil
}

func (m *Memory) DeleteUser(_ context.Context, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return ErrNotFound
	}
	kept := m.otps[:0]
	for _, o := range m.otps {
		if o.TelegramID != u.TelegramID {
			kept = append(kept, o)
		}
	}
	m.otps = kept
	delete(m.userTags, userID)
	delete(m.users, userID)
	return nil
}

func (m *Memory) ListRoles(_ context.Context) ([]Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Role, 0, len(m.roles))
	for _, r := range m.roles {
		c := *r
		c.Permissions = append([]string(nil), r.Permissions...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) ListTags(_ context.Context) ([]Tag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Tag, 0, len(m.tags))
	for _, t := range m.tags {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) UserTagIDs(_ context.Context, userID int64) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[userID]; !ok {
		return nil, ErrNotFound
	}
	var out []int64
	for id := range m.userTags[userID] {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (m *Memory) ToggleUserTag(_ context.Context, userID, tagID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[userID]; !ok {
		return false, ErrNotFound
	}
	if _, ok := m.tags[tagID]; !ok {
		return false, ErrNotFound
	}
	set := m.userTags[userID]
	if set == nil {
		set = map[int64]bool{}
		m.userTags[userID] = set
	}
	if set[tagID] {
		delete(set, tagID)
		return false, nil
	}
	set[tagID] = true
	return true, nil
}

func (m *Memory) CreateOTP(_ context.Context, otp OTP) (OTP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextOTP++
	otp.ID = m.nextOTP
	if otp.CreatedAt.IsZero() {
		otp.CreatedAt = time.Now()
	}
	cp := otp
	cp.CodeHash = append([]byte(nil), otp.CodeHash...)
	m.otps = append(m.otps, &cp)
	return otp, nil
}

func (m *Memory) LatestOTP(_ context.Context, telegramID int64) (OTP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *OTP
	for _, o := range m.otps {
		if o.TelegramID == telegramID && o.Sent && (best == nil || o.ID > best.ID) {
			best = o
		}
	}
	if best == nil {
		return OTP{}, ErrNotFound
	}
	cp := *best
	cp.CodeHash = append([]byte(nil), best.CodeHash...)
	return cp, nil
}

func (m *Memory) IncrementOTPAttempts(_ context.Context, id int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.otps {
		if o.ID == id {
			o.Attempts++
			return o.Attempts, nil
		}
	}
	return 0, ErrNotFound
}

func (m *Memory) ConsumeOTP(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.otps {
		if o.ID == id {
			o.Consumed = true
			return nil
		}
	}
	return ErrNotFound
}

func (m *Memory) CountOTPRequests(_ context.Context, telegramID int64, email string, since time.Time) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var byChat, byEmail int
	for _, o := range m.otps {
		if o.CreatedAt.Before(since) {
			continue
		}
		if o.TelegramID == telegramID {
			byChat++
		}
		if email != "" && strings.EqualFold(o.Email, email) {
			byEmail++
		}
	}
	return byChat, byEmail, nil
}

func (m *Memory) AppendAudit(_ context.Context, e AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextAudit++
	e.ID = m.nextAudit
	if e.At.IsZero() {
		e.At = time.Now()
	}
	cp := make(map[string]string, len(e.Details))
	for k, v := range e.Details {
		cp[k] = v
	}
	e.Details = cp
	m.audit = append(m.audit, e)
	return nil
}

func (m *Memory) ListAudit(_ context.Context, limit int) ([]AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEntry, 0, len(m.audit))
	for i := len(m.audit) - 1; i >= 0; i-- {
		out = append(out, m.audit[i])
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}
