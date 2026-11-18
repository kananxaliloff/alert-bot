// Package store defines the persistence interface and its implementations:
// Memory (used in tests and quick experiments) and Postgres (production).
// Both implementations are checked by the same contract suite in storetest.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound is returned when a requested row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrEmailTaken is returned when an email is already bound to another user.
	ErrEmailTaken = errors.New("store: email already registered to another user")
)

// UserStatus is the lifecycle state of a user.
type UserStatus string

const (
	// StatusUnverified: started the bot, has not proven a work mailbox yet.
	StatusUnverified UserStatus = "unverified"
	// StatusPending: mailbox verified, waiting for an admin to approve.
	StatusPending UserStatus = "pending"
	// StatusActive: approved, receives alerts and can use the bot.
	StatusActive UserStatus = "active"
	// StatusDisabled: switched off by an admin (reversible).
	StatusDisabled UserStatus = "disabled"
)

// User is a person using the bot.
type User struct {
	ID         int64
	TelegramID int64
	Email      string // verified work email, lower case; empty until verified
	FullName   string
	JobTitle   string
	Status     UserStatus
	RoleID     int64 // 0 means no role yet
	CreatedAt  time.Time
	VerifiedAt time.Time // zero if never verified
	ApprovedAt time.Time // zero if never approved
	ApprovedBy int64     // user id of the approver, 0 if none
}

// Role is a node of the title hierarchy. ParentID is the role above this one
// (0 for the top). Permissions are the role's own grants; inheritance is
// computed by package access.
type Role struct {
	ID          int64
	Name        string // stable machine name, e.g. "team_lead"
	Title       string // display name, e.g. "Team Lead"
	ParentID    int64
	Permissions []string
}

// Tag classifies alerts and subscriptions, e.g. {team, be} or {module, booking}.
type Tag struct {
	ID   int64
	Kind string
	Name string
}

// OTP is one verification attempt: a request for a code, sent or not.
type OTP struct {
	ID         int64
	TelegramID int64
	Email      string
	CodeHash   []byte // HMAC of the code; never the code itself
	Sent       bool   // false for requests that were rejected before a code was sent
	ExpiresAt  time.Time
	CreatedAt  time.Time
	Attempts   int
	Consumed   bool
}

// Verification is the data written when a user proves their mailbox.
type Verification struct {
	UserID   int64
	Email    string
	FullName string
	JobTitle string
	Status   UserStatus // pending, or active for bootstrap admins
	RoleID   int64      // 0 for none
}

// AuditEntry records a security-relevant action.
type AuditEntry struct {
	ID       int64
	At       time.Time
	ActorID  int64 // 0 for the system
	Action   string
	TargetID int64 // 0 if none; deliberately not a foreign key, it survives user deletion
	Details  map[string]string
}

// Seed describes the roles and tags that must exist. Applying it is idempotent
// and never overwrites roles or tags that already exist.
type Seed struct {
	Roles []RoleSeed // parents must come before their children
	Tags  []TagSeed
}

// RoleSeed is one role of a Seed. Parent is the parent's Name, "" for the top.
type RoleSeed struct {
	Name        string
	Title       string
	Parent      string
	Permissions []string
}

// TagSeed is one tag of a Seed.
type TagSeed struct {
	Kind string
	Name string
}

// Store is everything the application needs to persist.
type Store interface {
	// Seed makes sure the given roles and tags exist.
	Seed(ctx context.Context, seed Seed) error

	// GetOrCreateUser returns the user for a Telegram id, creating an
	// unverified one on first contact.
	GetOrCreateUser(ctx context.Context, telegramID int64) (User, error)
	GetUser(ctx context.Context, id int64) (User, error)
	GetUserByTelegramID(ctx context.Context, telegramID int64) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	// ListUsers returns users ordered by id. An empty statuses slice means all.
	ListUsers(ctx context.Context, statuses []UserStatus, limit, offset int) ([]User, error)
	CountUsers(ctx context.Context, status UserStatus) (int, error)
	// MarkVerified stores the verified identity. Returns ErrEmailTaken if the
	// email already belongs to a different user.
	MarkVerified(ctx context.Context, v Verification) (User, error)
	// SetStatus changes the status. Moving to active records the approver the
	// first time (approved_at / approved_by).
	SetStatus(ctx context.Context, userID int64, status UserStatus, actorID int64) (User, error)
	SetRole(ctx context.Context, userID, roleID int64) (User, error)
	// DeleteUser permanently removes the user, their tags and their OTP history.
	DeleteUser(ctx context.Context, userID int64) error

	ListRoles(ctx context.Context) ([]Role, error)
	ListTags(ctx context.Context) ([]Tag, error)
	UserTagIDs(ctx context.Context, userID int64) ([]int64, error)
	// ToggleUserTag subscribes or unsubscribes; added reports the new state.
	ToggleUserTag(ctx context.Context, userID, tagID int64) (added bool, err error)

	CreateOTP(ctx context.Context, otp OTP) (OTP, error)
	// LatestOTP returns the most recent OTP that was actually sent.
	LatestOTP(ctx context.Context, telegramID int64) (OTP, error)
	IncrementOTPAttempts(ctx context.Context, id int64) (int, error)
	ConsumeOTP(ctx context.Context, id int64) error
	// CountOTPRequests counts requests (sent or not) since the given time,
	// per Telegram account and per email.
	CountOTPRequests(ctx context.Context, telegramID int64, email string, since time.Time) (byChat, byEmail int, err error)

	AppendAudit(ctx context.Context, e AuditEntry) error
	// ListAudit returns the newest entries first.
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)
}
