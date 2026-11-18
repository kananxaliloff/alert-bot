package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Postgres is the PostgreSQL implementation of Store. It uses database/sql
// only; the driver is registered by the caller (cmd/alertbot).
type Postgres struct {
	db *sql.DB
}

// NewPostgres wraps an open database handle.
func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

var _ Store = (*Postgres)(nil)

const userCols = `id, telegram_id, COALESCE(email, ''), full_name, job_title, status,
	COALESCE(role_id, 0), created_at, verified_at, approved_at, COALESCE(approved_by, 0)`

type rowScanner interface{ Scan(dest ...any) error }

func scanUser(r rowScanner) (User, error) {
	var (
		u                  User
		status             string
		verified, approved sql.NullTime
	)
	err := r.Scan(&u.ID, &u.TelegramID, &u.Email, &u.FullName, &u.JobTitle, &status,
		&u.RoleID, &u.CreatedAt, &verified, &approved, &u.ApprovedBy)
	if err != nil {
		return User{}, err
	}
	u.Status = UserStatus(status)
	if verified.Valid {
		u.VerifiedAt = verified.Time
	}
	if approved.Valid {
		u.ApprovedAt = approved.Time
	}
	return u, nil
}

// sqlState extracts the SQLSTATE code from a driver error without importing a
// driver. pgx errors expose SQLState().
func sqlState(err error) string {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState()
	}
	return ""
}

const (
	stateUniqueViolation     = "23505"
	stateForeignKeyViolation = "23503"
)

func notFoundIfNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) Seed(ctx context.Context, seed Seed) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	for _, rs := range seed.Roles {
		var id int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO roles (name, title) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING RETURNING id`,
			rs.Name, rs.Title).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue // exists already: leave it exactly as it is
		}
		if err != nil {
			return fmt.Errorf("seed role %s: %w", rs.Name, err)
		}
		if rs.Parent != "" {
			var parentID int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM roles WHERE name = $1`, rs.Parent).Scan(&parentID)
			if errors.Is(err, sql.ErrNoRows) {
				return errSeedParent(rs.Name, rs.Parent)
			}
			if err != nil {
				return fmt.Errorf("seed role %s: %w", rs.Name, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE roles SET parent_id = $2 WHERE id = $1`, id, parentID); err != nil {
				return fmt.Errorf("seed role %s: %w", rs.Name, err)
			}
		}
		for _, perm := range rs.Permissions {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				id, perm); err != nil {
				return fmt.Errorf("seed role %s permission %s: %w", rs.Name, perm, err)
			}
		}
	}
	for _, ts := range seed.Tags {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tags (kind, name) VALUES ($1, $2) ON CONFLICT (kind, name) DO NOTHING`,
			ts.Kind, ts.Name); err != nil {
			return fmt.Errorf("seed tag %s/%s: %w", ts.Kind, ts.Name, err)
		}
	}
	return tx.Commit()
}

func (p *Postgres) GetOrCreateUser(ctx context.Context, telegramID int64) (User, error) {
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO users (telegram_id) VALUES ($1) ON CONFLICT (telegram_id) DO NOTHING`, telegramID); err != nil {
		return User{}, err
	}
	return p.GetUserByTelegramID(ctx, telegramID)
}

func (p *Postgres) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(p.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
	return u, notFoundIfNoRows(err)
}

func (p *Postgres) GetUserByTelegramID(ctx context.Context, telegramID int64) (User, error) {
	u, err := scanUser(p.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE telegram_id = $1`, telegramID))
	return u, notFoundIfNoRows(err)
}

func (p *Postgres) GetUserByEmail(ctx context.Context, email string) (User, error) {
	if email == "" {
		return User{}, ErrNotFound
	}
	u, err := scanUser(p.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email))
	return u, notFoundIfNoRows(err)
}

func (p *Postgres) ListUsers(ctx context.Context, statuses []UserStatus, limit, offset int) ([]User, error) {
	var (
		args  []any
		where string
	)
	if len(statuses) > 0 {
		ph := make([]string, len(statuses))
		for i, s := range statuses {
			args = append(args, string(s))
			ph[i] = fmt.Sprintf("$%d", len(args))
		}
		where = " WHERE status IN (" + strings.Join(ph, ", ") + ")"
	}
	args = append(args, int64(limit), int64(offset))
	q := fmt.Sprintf(`SELECT %s FROM users%s ORDER BY id LIMIT NULLIF($%d::bigint, 0) OFFSET $%d::bigint`,
		userCols, where, len(args)-1, len(args))

	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (p *Postgres) CountUsers(ctx context.Context, status UserStatus) (int, error) {
	var n int
	err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE status = $1`, string(status)).Scan(&n)
	return n, err
}

func (p *Postgres) MarkVerified(ctx context.Context, v Verification) (User, error) {
	u, err := scanUser(p.db.QueryRowContext(ctx, `
		UPDATE users SET
			email       = NULLIF($2::text, ''),
			full_name   = $3::text,
			job_title   = $4::text,
			status      = $5::text,
			role_id     = NULLIF($6::bigint, 0),
			verified_at = now(),
			approved_at = CASE WHEN $5::text = 'active' AND approved_at IS NULL THEN now() ELSE approved_at END
		WHERE id = $1
		RETURNING `+userCols,
		v.UserID, v.Email, v.FullName, v.JobTitle, string(v.Status), v.RoleID))
	if sqlState(err) == stateUniqueViolation {
		return User{}, ErrEmailTaken
	}
	return u, notFoundIfNoRows(err)
}

func (p *Postgres) SetStatus(ctx context.Context, userID int64, status UserStatus, actorID int64) (User, error) {
	u, err := scanUser(p.db.QueryRowContext(ctx, `
		UPDATE users SET
			status      = $2::text,
			approved_at = CASE WHEN $2::text = 'active' AND approved_at IS NULL THEN now() ELSE approved_at END,
			approved_by = CASE WHEN $2::text = 'active' AND approved_by IS NULL THEN NULLIF($3::bigint, 0) ELSE approved_by END
		WHERE id = $1
		RETURNING `+userCols,
		userID, string(status), actorID))
	return u, notFoundIfNoRows(err)
}

func (p *Postgres) SetRole(ctx context.Context, userID, roleID int64) (User, error) {
	u, err := scanUser(p.db.QueryRowContext(ctx,
		`UPDATE users SET role_id = NULLIF($2::bigint, 0) WHERE id = $1 RETURNING `+userCols, userID, roleID))
	if sqlState(err) == stateForeignKeyViolation {
		return User{}, ErrNotFound // unknown role
	}
	return u, notFoundIfNoRows(err)
}

func (p *Postgres) DeleteUser(ctx context.Context, userID int64) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var telegramID int64
	if err := tx.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&telegramID); err != nil {
		return notFoundIfNoRows(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM otp_challenges WHERE telegram_id = $1`, telegramID); err != nil {
		return err
	}
	// user_tags rows go away through ON DELETE CASCADE.
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, name, title, COALESCE(parent_id, 0) FROM roles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var roles []Role
	byID := map[int64]int{}
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Title, &r.ParentID); err != nil {
			rows.Close()
			return nil, err
		}
		byID[r.ID] = len(roles)
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	prows, err := p.db.QueryContext(ctx, `SELECT role_id, permission FROM role_permissions ORDER BY role_id, permission`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var roleID int64
		var perm string
		if err := prows.Scan(&roleID, &perm); err != nil {
			return nil, err
		}
		if i, ok := byID[roleID]; ok {
			roles[i].Permissions = append(roles[i].Permissions, perm)
		}
	}
	return roles, prows.Err()
}

func (p *Postgres) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, kind, name FROM tags ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Postgres) exists(ctx context.Context, q string, id int64) (bool, error) {
	var one int
	err := p.db.QueryRowContext(ctx, q, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (p *Postgres) UserTagIDs(ctx context.Context, userID int64) ([]int64, error) {
	ok, err := p.exists(ctx, `SELECT 1 FROM users WHERE id = $1`, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := p.db.QueryContext(ctx, `SELECT tag_id FROM user_tags WHERE user_id = $1 ORDER BY tag_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (p *Postgres) ToggleUserTag(ctx context.Context, userID, tagID int64) (bool, error) {
	for _, c := range []struct {
		q  string
		id int64
	}{{`SELECT 1 FROM users WHERE id = $1`, userID}, {`SELECT 1 FROM tags WHERE id = $1`, tagID}} {
		ok, err := p.exists(ctx, c.q, c.id)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, ErrNotFound
		}
	}
	res, err := p.db.ExecContext(ctx, `DELETE FROM user_tags WHERE user_id = $1 AND tag_id = $2`, userID, tagID)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return false, nil // it was set, now removed
	}
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO user_tags (user_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, tagID); err != nil {
		return false, err
	}
	return true, nil
}

func (p *Postgres) CreateOTP(ctx context.Context, otp OTP) (OTP, error) {
	hash := otp.CodeHash
	if hash == nil {
		hash = []byte{}
	}
	if otp.CreatedAt.IsZero() {
		otp.CreatedAt = time.Now()
	}
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO otp_challenges (telegram_id, email, code_hash, sent, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		otp.TelegramID, otp.Email, hash, otp.Sent, otp.ExpiresAt, otp.CreatedAt).Scan(&otp.ID)
	return otp, err
}

func (p *Postgres) LatestOTP(ctx context.Context, telegramID int64) (OTP, error) {
	var o OTP
	err := p.db.QueryRowContext(ctx, `
		SELECT id, telegram_id, email, code_hash, sent, expires_at, created_at, attempts, consumed
		FROM otp_challenges
		WHERE telegram_id = $1 AND sent
		ORDER BY id DESC LIMIT 1`, telegramID).
		Scan(&o.ID, &o.TelegramID, &o.Email, &o.CodeHash, &o.Sent, &o.ExpiresAt, &o.CreatedAt, &o.Attempts, &o.Consumed)
	return o, notFoundIfNoRows(err)
}

func (p *Postgres) IncrementOTPAttempts(ctx context.Context, id int64) (int, error) {
	var n int
	err := p.db.QueryRowContext(ctx,
		`UPDATE otp_challenges SET attempts = attempts + 1 WHERE id = $1 RETURNING attempts`, id).Scan(&n)
	return n, notFoundIfNoRows(err)
}

func (p *Postgres) ConsumeOTP(ctx context.Context, id int64) error {
	res, err := p.db.ExecContext(ctx, `UPDATE otp_challenges SET consumed = TRUE WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) CountOTPRequests(ctx context.Context, telegramID int64, email string, since time.Time) (int, int, error) {
	var byChat, byEmail int
	err := p.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE telegram_id = $1::bigint),
			COUNT(*) FILTER (WHERE lower(email) = lower($2::text))
		FROM otp_challenges
		WHERE created_at >= $3::timestamptz
		  AND (telegram_id = $1::bigint OR lower(email) = lower($2::text))`,
		telegramID, email, since).Scan(&byChat, &byEmail)
	return byChat, byEmail, err
}

func (p *Postgres) AppendAudit(ctx context.Context, e AuditEntry) error {
	details := e.Details
	if details == nil {
		details = map[string]string{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	var at sql.NullTime
	if !e.At.IsZero() {
		at = sql.NullTime{Time: e.At, Valid: true}
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO audit_log (at, actor_id, action, target_id, details)
		VALUES (COALESCE($1::timestamptz, now()), NULLIF($2::bigint, 0), $3::text, NULLIF($4::bigint, 0), $5::jsonb)`,
		at, e.ActorID, e.Action, e.TargetID, string(raw))
	return err
}

func (p *Postgres) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, at, COALESCE(actor_id, 0), action, COALESCE(target_id, 0), details
		FROM audit_log ORDER BY id DESC LIMIT NULLIF($1::bigint, 0)`, int64(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var raw []byte
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.Action, &e.TargetID, &raw); err != nil {
			return nil, err
		}
		e.Details = map[string]string{}
		if err := json.Unmarshal(raw, &e.Details); err != nil {
			return nil, fmt.Errorf("audit %d details: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
