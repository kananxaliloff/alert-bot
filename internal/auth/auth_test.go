package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kananxaliloff/alert-bot/internal/access"
	"github.com/kananxaliloff/alert-bot/internal/directory"
	"github.com/kananxaliloff/alert-bot/internal/store"
)

var ctx = context.Background()

// --- fakes -----------------------------------------------------------------

type fakeDir struct {
	people map[string]directory.Person
	err    error
}

func (d *fakeDir) LookupByEmail(_ context.Context, email string) (directory.Person, error) {
	if d.err != nil {
		return directory.Person{}, d.err
	}
	if p, ok := d.people[email]; ok {
		return p, nil
	}
	return directory.Person{}, directory.ErrNotFound
}

type sentMail struct{ to, subject, body string }

type fakeMailer struct {
	sent []sentMail
	err  error
}

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, sentMail{to, subject, body})
	return nil
}

var sixDigits = regexp.MustCompile(`\b\d{6}\b`)

func (m *fakeMailer) lastCode(t *testing.T) string {
	t.Helper()
	if len(m.sent) == 0 {
		t.Fatal("no email was sent")
	}
	code := sixDigits.FindString(m.sent[len(m.sent)-1].body)
	if code == "" {
		t.Fatalf("no code in email body: %q", m.sent[len(m.sent)-1].body)
	}
	return code
}

type env struct {
	svc  *Service
	st   *store.Memory
	dir  *fakeDir
	mail *fakeMailer
	now  time.Time
}

func newEnv(t *testing.T, mut func(*Config)) *env {
	t.Helper()
	st := store.NewMemory()
	if err := st.Seed(ctx, access.DefaultSeed()); err != nil {
		t.Fatal(err)
	}
	e := &env{
		st:   st,
		dir:  &fakeDir{people: map[string]directory.Person{"ann@corp.example": {Email: "ann@corp.example", FullName: "Ann Example", JobTitle: "Backend Engineer"}}},
		mail: &fakeMailer{},
		now:  time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
	}
	cfg := Config{Secret: []byte("0123456789abcdef-test-secret")}
	if mut != nil {
		mut(&cfg)
	}
	svc, err := New(cfg, st, e.dir, e.mail, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return e.now }
	svc.newCode = func() (string, error) { return "123456", nil }
	e.svc = svc
	return e
}

func (e *env) user(t *testing.T, telegramID int64) store.User {
	t.Helper()
	u, err := e.st.GetOrCreateUser(ctx, telegramID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) advance(d time.Duration) { e.now = e.now.Add(d) }

// --- tests -----------------------------------------------------------------

func TestNew_RejectsShortSecret(t *testing.T) {
	if _, err := New(Config{Secret: []byte("short")}, store.NewMemory(), &fakeDir{}, &fakeMailer{}, nil); err == nil {
		t.Fatal("short secret must be rejected")
	}
}

func TestHappyPath(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)

	res, err := e.svc.RequestCode(ctx, u, "  Ann@Corp.Example ")
	if err != nil {
		t.Fatal(err)
	}
	if res.Email != "ann@corp.example" || res.ExpiresIn != 10*time.Minute {
		t.Errorf("result = %+v", res)
	}
	if len(e.mail.sent) != 1 || e.mail.sent[0].to != "ann@corp.example" {
		t.Fatalf("mail = %+v", e.mail.sent)
	}
	if e.mail.lastCode(t) != "123456" {
		t.Errorf("unexpected code in mail")
	}

	got, err := e.svc.VerifyCode(ctx, u, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoApproved {
		t.Error("normal users must not be auto approved")
	}
	v := got.User
	if v.Status != store.StatusPending || v.Email != "ann@corp.example" || v.FullName != "Ann Example" || v.JobTitle != "Backend Engineer" || v.RoleID != 0 {
		t.Errorf("verified user = %+v", v)
	}
	audit, _ := e.st.ListAudit(ctx, 5)
	if len(audit) != 1 || audit[0].Action != "user.verified" || audit[0].TargetID != v.ID {
		t.Errorf("audit = %+v", audit)
	}
}

func TestRequestCode_InvalidEmails(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	bad := []string{"", "   ", "ann", "ann@", "@corp.example", "ann@corp", "ann corp@corp.example",
		"Ann <ann@corp.example>", "ann@corp.example, evil@x.test", "a@b@corp.example", "ann@corp.example\nBcc: x@y.z"}
	for _, in := range bad {
		if _, err := e.svc.RequestCode(ctx, u, in); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("%q: want ErrInvalidEmail, got %v", in, err)
		}
	}
	if len(e.mail.sent) != 0 {
		t.Error("no mail may be sent for invalid addresses")
	}
}

func TestRequestCode_DomainRestriction(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.AllowedDomains = []string{"corp.example"} })
	e.dir.people["x@other.test"] = directory.Person{Email: "x@other.test", FullName: "X"}
	u := e.user(t, 100)

	if _, err := e.svc.RequestCode(ctx, u, "x@other.test"); !errors.Is(err, ErrDomainNotAllowed) {
		t.Fatalf("want ErrDomainNotAllowed, got %v", err)
	}
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatalf("allowed domain failed: %v", err)
	}
}

func TestRequestCode_NotEmployeeCountsTowardLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxPerChatHour = 3 })
	u := e.user(t, 100)

	for i := 0; i < 3; i++ {
		if _, err := e.svc.RequestCode(ctx, u, "ghost@corp.example"); !errors.Is(err, ErrNotEmployee) {
			t.Fatalf("attempt %d: want ErrNotEmployee, got %v", i, err)
		}
	}
	// Probing is now blocked, even with a valid address.
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("want ErrTooManyRequests, got %v", err)
	}
	if len(e.mail.sent) != 0 {
		t.Error("no mail may be sent")
	}
	e.advance(61 * time.Minute) // the window moves on
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatalf("after an hour it should work again: %v", err)
	}
}

func TestRequestCode_PerEmailLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxPerEmailHour = 2; c.ResendCooldown = time.Second })
	// Three different Telegram accounts hammer the same mailbox.
	for i, id := range []int64{1, 2, 3} {
		_, err := e.svc.RequestCode(ctx, e.user(t, id), "ann@corp.example")
		if i < 2 && err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if i == 2 && !errors.Is(err, ErrTooManyRequests) {
			t.Fatalf("third request: want ErrTooManyRequests, got %v", err)
		}
		e.advance(2 * time.Second)
	}
}

func TestRequestCode_EmailTakenByAnotherUser(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.user(t, 1)
	if _, err := e.st.MarkVerified(ctx, store.Verification{UserID: owner.ID, Email: "ann@corp.example", Status: store.StatusActive}); err != nil {
		t.Fatal(err)
	}
	other := e.user(t, 2)
	if _, err := e.svc.RequestCode(ctx, other, "ann@corp.example"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("want ErrEmailTaken, got %v", err)
	}
	if len(e.mail.sent) != 0 {
		t.Error("no mail may be sent")
	}
}

func TestRequestCode_Cooldown(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	e.advance(20 * time.Second)
	_, err := e.svc.RequestCode(ctx, u, "ann@corp.example")
	var cd *CooldownError
	if !errors.Is(err, ErrCooldown) || !errors.As(err, &cd) || cd.RetryIn != 40*time.Second {
		t.Fatalf("want cooldown of 40s, got %v", err)
	}
	e.advance(41 * time.Second)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
	if len(e.mail.sent) != 2 {
		t.Errorf("sent %d mails, want 2", len(e.mail.sent))
	}
}

func TestResendInvalidatesPreviousCode(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	codes := []string{"111111", "222222"}
	e.svc.newCode = func() (string, error) { c := codes[0]; codes = codes[1:]; return c, nil }

	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Minute)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "111111"); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("old code must not work, got %v", err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "222222"); err != nil {
		t.Fatalf("new code must work: %v", err)
	}
}

func TestRequestCode_MailFailureInvalidatesCode(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	e.mail.err = errors.New("smtp down")

	_, err := e.svc.RequestCode(ctx, u, "ann@corp.example")
	if !errors.Is(err, ErrMailFailed) {
		t.Fatalf("want ErrMailFailed, got %v", err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrNoActiveCode) {
		t.Fatalf("a code that was never delivered must not verify, got %v", err)
	}
}

func TestRequestCode_DirectoryUnavailable(t *testing.T) {
	e := newEnv(t, nil)
	e.dir.err = errors.New("ldap: connection refused")
	_, err := e.svc.RequestCode(ctx, e.user(t, 100), "ann@corp.example")
	if !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("want ErrDirectoryUnavailable, got %v", err)
	}
	if errors.Is(err, ErrNotEmployee) {
		t.Error("an outage must not look like 'not an employee'")
	}
}

func TestRequestCode_OnlyForUnverifiedUsers(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	u.Status = store.StatusPending
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); !errors.Is(err, ErrNotUnverified) {
		t.Fatalf("got %v", err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrNotUnverified) {
		t.Fatalf("got %v", err)
	}
}

func TestVerify_WrongCodeThenLockout(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxAttempts = 3 })
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}

	for left := 2; left >= 1; left-- {
		_, err := e.svc.VerifyCode(ctx, u, "000000")
		var wc *WrongCodeError
		if !errors.As(err, &wc) || wc.AttemptsLeft != left {
			t.Fatalf("want %d attempts left, got %v", left, err)
		}
	}
	if _, err := e.svc.VerifyCode(ctx, u, "000000"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("want ErrTooManyAttempts, got %v", err)
	}
	// The right code no longer helps: the code is burnt.
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrNoActiveCode) {
		t.Fatalf("burnt code must not verify, got %v", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	e.advance(10*time.Minute + time.Second)
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("want ErrCodeExpired, got %v", err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrNoActiveCode) {
		t.Fatalf("expired code stays dead, got %v", err)
	}
}

func TestVerify_BadFormatDoesNotBurnAGuess(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxAttempts = 2 })
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"12345", "1234567", "abcdef", "12 34 5", "", "123456; DROP"} {
		if _, err := e.svc.VerifyCode(ctx, u, in); !errors.Is(err, ErrBadCodeFormat) {
			t.Errorf("%q: want ErrBadCodeFormat, got %v", in, err)
		}
	}
	otp, _ := e.st.LatestOTP(ctx, u.TelegramID)
	if otp.Attempts != 0 {
		t.Errorf("attempts = %d, format errors must not count", otp.Attempts)
	}
}

func TestVerify_AcceptsSpacesAndDashes(t *testing.T) {
	for _, in := range []string{"123 456", "123-456", " 123456 "} {
		e := newEnv(t, nil)
		u := e.user(t, 100)
		if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.VerifyCode(ctx, u, in); err != nil {
			t.Errorf("%q should be accepted: %v", in, err)
		}
	}
}

func TestVerify_CodeIsSingleUse(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrNotUnverified) && !errors.Is(err, ErrNoActiveCode) {
		t.Fatalf("second use must fail, got %v", err)
	}
	// Even with a stale user value the consumed code is dead.
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrNoActiveCode) {
		t.Fatalf("consumed code must be dead, got %v", err)
	}
}

func TestVerify_CodeFromAnotherAccountDoesNotWork(t *testing.T) {
	e := newEnv(t, nil)
	victim, attacker := e.user(t, 1), e.user(t, 2)
	if _, err := e.svc.RequestCode(ctx, victim, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyCode(ctx, attacker, "123456"); !errors.Is(err, ErrNoActiveCode) {
		t.Fatalf("attacker has no code of their own, got %v", err)
	}
}

func TestVerify_AccountDisabledMeanwhile(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	delete(e.dir.people, "ann@corp.example") // she left the company
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrNotEmployee) {
		t.Fatalf("want ErrNotEmployee, got %v", err)
	}
	got, _ := e.st.GetUser(ctx, u.ID)
	if got.Status != store.StatusUnverified || got.Email != "" {
		t.Errorf("user must stay unverified: %+v", got)
	}
}

func TestVerify_EmailTakenInTheMeantime(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	thief := e.user(t, 200)
	if _, err := e.st.MarkVerified(ctx, store.Verification{UserID: thief.ID, Email: "ann@corp.example", Status: store.StatusPending}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, store.ErrEmailTaken) {
		t.Fatalf("want store.ErrEmailTaken, got %v", err)
	}
}

func TestVerify_BootstrapAdminIsActivatedAsAdmin(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.BootstrapAdmins = map[int64]bool{100: true} })
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.VerifyCode(ctx, u, "123456")
	if err != nil {
		t.Fatal(err)
	}
	roles, _ := e.st.ListRoles(ctx)
	var adminID int64
	for _, r := range roles {
		if r.Name == access.RoleAdmin {
			adminID = r.ID
		}
	}
	if !got.AutoApproved || got.User.Status != store.StatusActive || got.User.RoleID != adminID || got.User.ApprovedAt.IsZero() {
		t.Errorf("bootstrap admin = %+v (auto=%v)", got.User, got.AutoApproved)
	}
	audit, _ := e.st.ListAudit(ctx, 5)
	if len(audit) != 1 || audit[0].Action != "user.bootstrap_admin" {
		t.Errorf("audit = %+v", audit)
	}
}

func TestVerify_BootstrapWithoutAdminRoleFallsBackToPending(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.BootstrapAdmins = map[int64]bool{100: true} })
	unseeded := store.NewMemory() // no seed: no admin role exists
	e.svc.st = unseeded
	u, err := unseeded.GetOrCreateUser(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.VerifyCode(ctx, u, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoApproved || got.User.Status != store.StatusPending {
		t.Errorf("must fall back to pending, got %+v", got.User)
	}
}

func TestStoredHashIsNotTheCode(t *testing.T) {
	e := newEnv(t, nil)
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	otp, err := e.st.LatestOTP(ctx, u.TelegramID)
	if err != nil {
		t.Fatal(err)
	}
	if len(otp.CodeHash) != 32 || strings.Contains(string(otp.CodeHash), "123456") {
		t.Errorf("stored hash looks wrong: %x", otp.CodeHash)
	}
	// Keyed: the same code under another secret hashes differently.
	other := newEnv(t, func(c *Config) { c.Secret = []byte("a-completely-different-secret") })
	if string(other.svc.hash(100, "ann@corp.example", "123456")) == string(otp.CodeHash) {
		t.Error("hash must depend on the server secret")
	}
	// Bound to account and email.
	if string(e.svc.hash(101, "ann@corp.example", "123456")) == string(otp.CodeHash) ||
		string(e.svc.hash(100, "bob@corp.example", "123456")) == string(otp.CodeHash) {
		t.Error("hash must depend on the Telegram id and the email")
	}
}

func TestRandomCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 3000; i++ {
		c, err := randomCode()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^\d{6}$`).MatchString(c) {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 2900 {
		t.Errorf("only %d distinct codes in 3000 draws, generator looks broken", len(seen))
	}
}

func TestVerify_AlreadyExhaustedCodeIsRefusedEvenIfCorrect(t *testing.T) {
	// Defence in depth: if a code somehow still has all its attempts used
	// (a crash between increment and consume, or two requests racing),
	// even the right code must not be accepted.
	e := newEnv(t, func(c *Config) { c.MaxAttempts = 3 })
	u := e.user(t, 100)
	if _, err := e.svc.RequestCode(ctx, u, "ann@corp.example"); err != nil {
		t.Fatal(err)
	}
	otp, err := e.st.LatestOTP(ctx, u.TelegramID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.st.IncrementOTPAttempts(ctx, otp.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.VerifyCode(ctx, u, "123456"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("want ErrTooManyAttempts, got %v", err)
	}
}
