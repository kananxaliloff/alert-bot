// Package auth implements sign-up: prove ownership of a work mailbox with a
// one-time code. The flow is stateless between messages; everything it needs
// lives in the store, so it survives restarts.
//
// Safeguards: the employee must exist (and be enabled) in the directory; codes
// are 6 digits from crypto/rand and stored only as an HMAC keyed with a server
// secret (a leaked database cannot be used to find codes); codes expire, allow
// a few guesses, are single use, and requests are rate limited per Telegram
// account and per email.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"github.com/kananxaliloff/alert-bot/internal/access"
	"github.com/kananxaliloff/alert-bot/internal/directory"
	mailer "github.com/kananxaliloff/alert-bot/internal/mail"
	"github.com/kananxaliloff/alert-bot/internal/store"
)

// Errors that the bot turns into messages for the user.
var (
	ErrNotUnverified        = errors.New("auth: user is not waiting for verification")
	ErrInvalidEmail         = errors.New("auth: not a valid email address")
	ErrDomainNotAllowed     = errors.New("auth: email domain is not allowed")
	ErrNotEmployee          = errors.New("auth: no active employee with that email")
	ErrEmailTaken           = errors.New("auth: email already registered to another Telegram account")
	ErrTooManyRequests      = errors.New("auth: too many code requests")
	ErrMailFailed           = errors.New("auth: could not send the email")
	ErrDirectoryUnavailable = errors.New("auth: employee directory unavailable")
	ErrNoActiveCode         = errors.New("auth: no active code")
	ErrCodeExpired          = errors.New("auth: code expired")
	ErrBadCodeFormat        = errors.New("auth: code must be 6 digits")
	ErrTooManyAttempts      = errors.New("auth: too many wrong attempts")
	ErrWrongCode            = errors.New("auth: wrong code")
	ErrCooldown             = errors.New("auth: wait before requesting another code")
)

// CooldownError carries how long the user has to wait.
type CooldownError struct{ RetryIn time.Duration }

func (e *CooldownError) Error() string {
	return fmt.Sprintf("%s (%s)", ErrCooldown, e.RetryIn.Round(time.Second))
}
func (e *CooldownError) Is(target error) bool { return target == ErrCooldown }

// WrongCodeError carries how many guesses are left.
type WrongCodeError struct{ AttemptsLeft int }

func (e *WrongCodeError) Error() string {
	return fmt.Sprintf("%s (%d left)", ErrWrongCode, e.AttemptsLeft)
}
func (e *WrongCodeError) Is(target error) bool { return target == ErrWrongCode }

// Config tunes the service. Zero values get sensible defaults.
type Config struct {
	// Secret keys the code hashes. At least 16 bytes. Required.
	Secret          []byte
	CodeTTL         time.Duration // default 10m
	MaxAttempts     int           // wrong guesses allowed per code, default 5
	ResendCooldown  time.Duration // minimum time between two codes, default 60s
	MaxPerChatHour  int           // requests per Telegram account per hour, default 10
	MaxPerEmailHour int           // requests per email per hour, default 5
	// AllowedDomains, if not empty, restricts emails to these domains.
	AllowedDomains []string
	// BootstrapAdmins are Telegram ids that become administrators right after
	// verifying their mailbox, so the very first admin needs nobody's approval.
	BootstrapAdmins map[int64]bool
}

func (c Config) withDefaults() Config {
	if c.CodeTTL <= 0 {
		c.CodeTTL = 10 * time.Minute
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.ResendCooldown <= 0 {
		c.ResendCooldown = 60 * time.Second
	}
	if c.MaxPerChatHour <= 0 {
		c.MaxPerChatHour = 10
	}
	if c.MaxPerEmailHour <= 0 {
		c.MaxPerEmailHour = 5
	}
	return c
}

// Service runs the sign-up flow.
type Service struct {
	cfg  Config
	st   store.Store
	dir  directory.Directory
	mail mailer.Mailer
	log  *slog.Logger

	// Replaceable in tests.
	now     func() time.Time
	newCode func() (string, error)
}

// New creates the service.
func New(cfg Config, st store.Store, dir directory.Directory, m mailer.Mailer, log *slog.Logger) (*Service, error) {
	if len(cfg.Secret) < 16 {
		return nil, errors.New("auth: secret must be at least 16 bytes")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{cfg: cfg.withDefaults(), st: st, dir: dir, mail: m, log: log, now: time.Now, newCode: randomCode}, nil
}

// RequestResult describes a code that was sent.
type RequestResult struct {
	Email     string
	ExpiresIn time.Duration
}

// RequestCode checks the email and sends a code to it.
func (s *Service) RequestCode(ctx context.Context, u store.User, rawEmail string) (RequestResult, error) {
	if u.Status != store.StatusUnverified {
		return RequestResult{}, ErrNotUnverified
	}
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return RequestResult{}, err
	}
	now := s.now()

	if !s.domainAllowed(email) {
		s.recordRejected(ctx, u.TelegramID, email, now)
		return RequestResult{}, ErrDomainNotAllowed
	}

	byChat, byEmail, err := s.st.CountOTPRequests(ctx, u.TelegramID, email, now.Add(-time.Hour))
	if err != nil {
		return RequestResult{}, err
	}
	if byChat >= s.cfg.MaxPerChatHour || byEmail >= s.cfg.MaxPerEmailHour {
		return RequestResult{}, ErrTooManyRequests
	}
	if last, err := s.st.LatestOTP(ctx, u.TelegramID); err == nil {
		if wait := last.CreatedAt.Add(s.cfg.ResendCooldown).Sub(now); wait > 0 {
			return RequestResult{}, &CooldownError{RetryIn: wait}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return RequestResult{}, err
	}

	if _, err := s.dir.LookupByEmail(ctx, email); err != nil {
		if errors.Is(err, directory.ErrNotFound) {
			s.recordRejected(ctx, u.TelegramID, email, now)
			return RequestResult{}, ErrNotEmployee
		}
		s.log.Error("directory lookup failed", "err", err)
		return RequestResult{}, fmt.Errorf("%w: %v", ErrDirectoryUnavailable, err)
	}

	holder, err := s.st.GetUserByEmail(ctx, email)
	switch {
	case err == nil && holder.ID != u.ID:
		s.recordRejected(ctx, u.TelegramID, email, now)
		return RequestResult{}, ErrEmailTaken
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return RequestResult{}, err
	}

	code, err := s.newCode()
	if err != nil {
		return RequestResult{}, fmt.Errorf("auth: generate code: %w", err)
	}
	otp, err := s.st.CreateOTP(ctx, store.OTP{
		TelegramID: u.TelegramID,
		Email:      email,
		CodeHash:   s.hash(u.TelegramID, email, code),
		Sent:       true,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.cfg.CodeTTL),
	})
	if err != nil {
		return RequestResult{}, err
	}

	body := fmt.Sprintf("Your alert-bot verification code is %s\n\n"+
		"It expires in %d minutes. If you did not ask for it, ignore this email.\n",
		code, int(s.cfg.CodeTTL.Minutes()))
	if err := s.mail.Send(ctx, email, "alert-bot verification code", body); err != nil {
		_ = s.st.ConsumeOTP(ctx, otp.ID) // the user never got it, so it must not stay valid
		s.log.Error("sending verification email failed", "err", err)
		return RequestResult{}, fmt.Errorf("%w: %v", ErrMailFailed, err)
	}
	return RequestResult{Email: email, ExpiresIn: s.cfg.CodeTTL}, nil
}

// VerifyResult is the outcome of a successful verification.
type VerifyResult struct {
	User         store.User
	AutoApproved bool // true for bootstrap administrators
}

// VerifyCode checks a code and, if it is right, marks the user as verified.
// Normally the user then waits for an admin (status pending).
func (s *Service) VerifyCode(ctx context.Context, u store.User, rawCode string) (VerifyResult, error) {
	if u.Status != store.StatusUnverified {
		return VerifyResult{}, ErrNotUnverified
	}
	code, ok := normalizeCode(rawCode)
	if !ok {
		return VerifyResult{}, ErrBadCodeFormat // a typo in the format does not burn a guess
	}

	otp, err := s.st.LatestOTP(ctx, u.TelegramID)
	if errors.Is(err, store.ErrNotFound) {
		return VerifyResult{}, ErrNoActiveCode
	}
	if err != nil {
		return VerifyResult{}, err
	}
	if otp.Consumed {
		return VerifyResult{}, ErrNoActiveCode
	}
	if !s.now().Before(otp.ExpiresAt) {
		_ = s.st.ConsumeOTP(ctx, otp.ID)
		return VerifyResult{}, ErrCodeExpired
	}
	if otp.Attempts >= s.cfg.MaxAttempts {
		_ = s.st.ConsumeOTP(ctx, otp.ID)
		return VerifyResult{}, ErrTooManyAttempts
	}

	attempts, err := s.st.IncrementOTPAttempts(ctx, otp.ID)
	if err != nil {
		return VerifyResult{}, err
	}
	if !hmac.Equal(otp.CodeHash, s.hash(u.TelegramID, otp.Email, code)) {
		if attempts >= s.cfg.MaxAttempts {
			_ = s.st.ConsumeOTP(ctx, otp.ID)
			return VerifyResult{}, ErrTooManyAttempts
		}
		return VerifyResult{}, &WrongCodeError{AttemptsLeft: s.cfg.MaxAttempts - attempts}
	}
	if err := s.st.ConsumeOTP(ctx, otp.ID); err != nil {
		return VerifyResult{}, err
	}

	// Ask the directory again: the account may have been disabled since the code was sent.
	person, err := s.dir.LookupByEmail(ctx, otp.Email)
	if errors.Is(err, directory.ErrNotFound) {
		return VerifyResult{}, ErrNotEmployee
	}
	if err != nil {
		s.log.Error("directory lookup failed", "err", err)
		return VerifyResult{}, fmt.Errorf("%w: %v", ErrDirectoryUnavailable, err)
	}

	v := store.Verification{UserID: u.ID, Email: otp.Email, FullName: person.FullName, JobTitle: person.JobTitle, Status: store.StatusPending}
	auto := false
	if s.cfg.BootstrapAdmins[u.TelegramID] {
		if id, ok := s.adminRoleID(ctx); ok {
			v.Status, v.RoleID, auto = store.StatusActive, id, true
		} else {
			s.log.Error("bootstrap admin cannot be activated: admin role missing, user stays pending", "telegram_id", u.TelegramID)
		}
	}
	verified, err := s.st.MarkVerified(ctx, v)
	if err != nil {
		return VerifyResult{}, err // includes store.ErrEmailTaken
	}

	action := "user.verified"
	if auto {
		action = "user.bootstrap_admin"
	}
	if err := s.st.AppendAudit(ctx, store.AuditEntry{Action: action, TargetID: verified.ID, Details: map[string]string{"email": verified.Email}}); err != nil {
		s.log.Error("audit write failed", "err", err)
	}
	return VerifyResult{User: verified, AutoApproved: auto}, nil
}

func (s *Service) adminRoleID(ctx context.Context) (int64, bool) {
	roles, err := s.st.ListRoles(ctx)
	if err != nil {
		return 0, false
	}
	for _, r := range roles {
		if r.Name == access.RoleAdmin {
			return r.ID, true
		}
	}
	return 0, false
}

// recordRejected stores a request that never produced a code, so that
// probing many addresses still runs into the rate limit.
func (s *Service) recordRejected(ctx context.Context, telegramID int64, email string, now time.Time) {
	_, err := s.st.CreateOTP(ctx, store.OTP{TelegramID: telegramID, Email: email, Sent: false, Consumed: true, CreatedAt: now, ExpiresAt: now})
	if err != nil {
		s.log.Error("recording rejected request failed", "err", err)
	}
}

func (s *Service) domainAllowed(email string) bool {
	if len(s.cfg.AllowedDomains) == 0 {
		return true
	}
	_, domain, _ := strings.Cut(email, "@")
	for _, d := range s.cfg.AllowedDomains {
		if strings.EqualFold(strings.TrimSpace(d), domain) {
			return true
		}
	}
	return false
}

// hash binds a code to the Telegram account and email it was issued for.
func (s *Service) hash(telegramID int64, email, code string) []byte {
	mac := hmac.New(sha256.New, s.cfg.Secret)
	fmt.Fprintf(mac, "%d|%s|%s", telegramID, email, code)
	return mac.Sum(nil)
}

func normalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > 254 || strings.ContainsAny(e, " \t\r\n<>,;\"()") {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e {
		return "", ErrInvalidEmail
	}
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") || strings.Contains(domain, "@") {
		return "", ErrInvalidEmail
	}
	return e, nil
}

// normalizeCode accepts "123456", "123 456" and "123-456".
func normalizeCode(raw string) (string, bool) {
	code := strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(raw))
	if len(code) != 6 {
		return "", false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return code, true
}

// randomCode returns six random digits from the operating system's CSPRNG.
func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
