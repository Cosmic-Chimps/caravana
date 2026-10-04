package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// fakeClock is a clock tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// lockedBuffer is a bytes.Buffer safe to write from the code under test and
// read from the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// authEnv is an Auth on a real store, with a fake clock and fast hashing.
type authEnv struct {
	auth   *Auth
	store  *store.Store
	clock  *fakeClock
	logs   *lockedBuffer
	hashes int // password hashes and checks run, via the test hook
	start  time.Time
}

// clockStart is when the environment's clock began.
func (e *authEnv) clockStart() time.Time { return e.start }

const (
	testEmail    = "you@example.com"
	testPassword = "correct horse battery"
)

// newAuthEnv builds an Auth with no admin yet.
func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	e := &authEnv{store: st, clock: newFakeClock(), logs: &lockedBuffer{}}
	e.start = e.clock.Now()
	e.auth = NewAuth(st, slog.New(slog.NewJSONHandler(e.logs, nil)),
		AuthOptions{Now: e.clock.Now, PasswordIterations: 1000})
	e.auth.onHash = func() { e.hashes++ }
	return e
}

// newAuthEnvWithAdmin builds an Auth whose admin is testEmail / testPassword.
func newAuthEnvWithAdmin(t *testing.T) *authEnv {
	t.Helper()
	e := newAuthEnv(t)
	if err := e.auth.InitAdmin(context.Background(), testEmail, testPassword); err != nil {
		t.Fatalf("InitAdmin: %v", err)
	}
	e.hashes = 0
	return e
}

func TestHashPassword(t *testing.T) {
	hash, err := hashPassword("correct horse battery", 1000)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "1000" {
		t.Fatalf("hash = %q, want pbkdf2-sha256$1000$<salt>$<key>", hash)
	}
	if strings.Contains(hash, "correct horse") || strings.ContainsAny(parts[2]+parts[3], "=") {
		t.Errorf("hash %q contains the password or base64 padding", hash)
	}

	if !checkPassword("correct horse battery", hash) {
		t.Error("the right password was refused")
	}
	if checkPassword("correct horse batterz", hash) {
		t.Error("a wrong password was accepted")
	}

	// Another hash of the same password has another salt.
	again, _ := hashPassword("correct horse battery", 1000)
	if again == hash {
		t.Error("two hashes of one password are identical: the salt is not random")
	}
}

func TestCheckPasswordUsesStoredIterations(t *testing.T) {
	// A hash made with a different count still verifies: the count is read
	// from the stored string, so it can be raised later.
	old, _ := hashPassword("correct horse battery", 500)
	if !checkPassword("correct horse battery", old) {
		t.Error("a hash with 500 iterations did not verify")
	}
}

func TestCheckPasswordMalformedHash(t *testing.T) {
	good, _ := hashPassword("correct horse battery", 1000)
	for _, stored := range []string{
		"",
		"plaintext",
		"pbkdf2-sha256$1000$salt",
		strings.Replace(good, "pbkdf2-sha256", "md5", 1),
		strings.Replace(good, "$1000$", "$zero$", 1),
		strings.Replace(good, "$1000$", "$0$", 1),
		strings.Replace(good, "$1000$", "$999999999999$", 1),
		"pbkdf2-sha256$1000$!!!$!!!",
		"pbkdf2-sha256$1000$AAAA$",
	} {
		if checkPassword("correct horse battery", stored) {
			t.Errorf("checkPassword accepted malformed hash %q", stored)
		}
	}
}

func TestCheckPasswordRule(t *testing.T) {
	tests := []struct {
		password string
		ok       bool
	}{
		{"", false},
		{"short", false},
		{"elevenchars", false}, // 11
		{"twelve chars", true},
		{"twelvechars!", true},
		{"ünïcödé-pässwörd", true},
		{"ééééééééééé", false}, // 11 characters, 22 bytes
	}
	for _, tc := range tests {
		err := checkPasswordRule("newPassword", tc.password)
		if tc.ok != (err == nil) {
			t.Errorf("checkPasswordRule(%q) = %v, want ok=%v", tc.password, err, tc.ok)
		}
		if err != nil {
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "newPassword" {
				t.Errorf("checkPasswordRule(%q) = %v, want a field error naming newPassword", tc.password, err)
			}
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"you@example.com", "you@example.com", true},
		{"  You@Example.COM \n", "you@example.com", true},
		{"a@b", "a@b", true},
		{"", "", false},
		{"no-at-sign", "", false},
		{"@example.com", "", false},
		{"you@", "", false},
		{"a@b@c", "", false},
		{"   ", "", false},
	}
	for _, tc := range tests {
		got, err := normalizeEmail(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("normalizeEmail(%q) = %q, %v; want %q, ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
		if err != nil {
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "email" {
				t.Errorf("normalizeEmail(%q) error = %v, want a field error naming email", tc.in, err)
			}
		}
	}
}

func TestSigninErrors(t *testing.T) {
	var err error = &SigninFailure{TriesLeft: 3}
	if !errors.Is(err, ErrBadCredentials) {
		t.Error("SigninFailure does not wrap ErrBadCredentials")
	}
	err = &SigninLimitError{RetryAfter: time.Minute}
	if !errors.Is(err, ErrSigninLimited) {
		t.Error("SigninLimitError does not wrap ErrSigninLimited")
	}
}

func TestInitAdmin(t *testing.T) {
	ctx := context.Background()

	t.Run("stores the normalized email and a hash, never the password", func(t *testing.T) {
		e := newAuthEnv(t)
		if err := e.auth.InitAdmin(ctx, "  You@Example.com ", testPassword); err != nil {
			t.Fatalf("InitAdmin: %v", err)
		}
		admin, err := e.store.GetAdmin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if admin.Email != "you@example.com" {
			t.Errorf("stored email = %q, want you@example.com", admin.Email)
		}
		if strings.Contains(admin.PasswordHash, testPassword) || !checkPassword(testPassword, admin.PasswordHash) {
			t.Errorf("stored hash %q is not a hash of the password", admin.PasswordHash)
		}
		if strings.Contains(e.logs.String(), testPassword) || !strings.Contains(e.logs.String(), `"event":"admin_created"`) {
			t.Errorf("log should hold admin_created and no password:\n%s", e.logs.String())
		}
	})

	t.Run("a second init is refused and changes nothing", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		err := e.auth.InitAdmin(ctx, "other@example.com", "another long password")
		if !errors.Is(err, ErrAdminExists) {
			t.Fatalf("second InitAdmin = %v, want ErrAdminExists", err)
		}
		if email, _ := e.auth.AdminEmail(ctx); email != testEmail {
			t.Errorf("admin email = %q after a refused init, want %q", email, testEmail)
		}
	})

	t.Run("weak password and bad email are field errors", func(t *testing.T) {
		e := newAuthEnv(t)
		for _, tc := range []struct{ email, password, field string }{
			{testEmail, "short", "password"},
			{"not-an-email", testPassword, "email"},
		} {
			err := e.auth.InitAdmin(ctx, tc.email, tc.password)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Errorf("InitAdmin(%q, %q) = %v, want a field error naming %s", tc.email, tc.password, err, tc.field)
			}
		}
		if _, err := e.auth.AdminEmail(ctx); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("an admin exists after refused inits: %v", err)
		}
	})
}

func TestAdminEmail(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	if _, err := e.auth.AdminEmail(ctx); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("AdminEmail before init = %v, want ErrNoAdmin", err)
	}
	if err := e.auth.InitAdmin(ctx, testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	if email, err := e.auth.AdminEmail(ctx); err != nil || email != testEmail {
		t.Errorf("AdminEmail = %q, %v; want %q", email, err, testEmail)
	}
}

func TestResetPassword(t *testing.T) {
	ctx := context.Background()
	const newPassword = "a brand new password"

	t.Run("old password fails, new works, sessions end, tokens stay", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		now := e.clock.Now()
		for _, id := range []string{"s1", "s2"} {
			if err := e.store.CreateSession(ctx, store.SessionRecord{IDHash: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		created, err := e.auth.CreateAPIToken(ctx, "ci", nil)
		if err != nil {
			t.Fatal(err)
		}

		if err := e.auth.ResetPassword(ctx, newPassword); err != nil {
			t.Fatalf("ResetPassword: %v", err)
		}
		admin, _ := e.store.GetAdmin(ctx)
		if checkPassword(testPassword, admin.PasswordHash) {
			t.Error("the old password still works")
		}
		if !checkPassword(newPassword, admin.PasswordHash) {
			t.Error("the new password does not work")
		}
		for _, id := range []string{"s1", "s2"} {
			if _, err := e.store.GetSession(ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("session %s after reset = %v, want it deleted", id, err)
			}
		}
		if err := e.auth.CheckAPIToken(ctx, created.Token); err != nil {
			t.Errorf("the API token stopped working after a reset: %v", err)
		}
		out := e.logs.String()
		if !strings.Contains(out, `"event":"password_reset"`) || strings.Contains(out, newPassword) {
			t.Errorf("log should hold password_reset and no password:\n%s", out)
		}
	})

	t.Run("without an admin", func(t *testing.T) {
		e := newAuthEnv(t)
		if err := e.auth.ResetPassword(ctx, newPassword); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("ResetPassword without admin = %v, want ErrNoAdmin", err)
		}
	})

	t.Run("a weak password is a field error and changes nothing", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		err := e.auth.ResetPassword(ctx, "short")
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "password" {
			t.Errorf("ResetPassword(short) = %v, want a field error naming password", err)
		}
		admin, _ := e.store.GetAdmin(ctx)
		if !checkPassword(testPassword, admin.PasswordHash) {
			t.Error("the password changed on a refused reset")
		}
	})
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	const newPassword = "a brand new password"

	signedIn := func(t *testing.T, e *authEnv) (keep, other Session, token CreatedAPIToken) {
		t.Helper()
		var err error
		if keep, err = e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9"); err != nil {
			t.Fatal(err)
		}
		if other, err = e.auth.SignIn(ctx, testEmail, testPassword, "203.0.113.9"); err != nil {
			t.Fatal(err)
		}
		if token, err = e.auth.CreateAPIToken(ctx, "ci", nil); err != nil {
			t.Fatal(err)
		}
		return keep, other, token
	}

	t.Run("a wrong current password changes nothing", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		keep, other, _ := signedIn(t, e)
		err := e.auth.ChangePassword(ctx, "not the password", newPassword, keep.Value)
		if !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("ChangePassword = %v, want ErrWrongPassword", err)
		}
		admin, _ := e.store.GetAdmin(ctx)
		if !checkPassword(testPassword, admin.PasswordHash) {
			t.Error("the password changed on a refused change")
		}
		for name, s := range map[string]Session{"calling": keep, "other": other} {
			if err := e.auth.CheckSession(ctx, s.Value); err != nil {
				t.Errorf("the %s session ended on a refused change: %v", name, err)
			}
		}
	})

	t.Run("a short new password is a field error naming newPassword", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		err := e.auth.ChangePassword(ctx, testPassword, "short", "")
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "newPassword" {
			t.Fatalf("ChangePassword = %v, want a field error naming newPassword", err)
		}
		admin, _ := e.store.GetAdmin(ctx)
		if !checkPassword(testPassword, admin.PasswordHash) {
			t.Error("the password changed on a refused change")
		}
	})

	t.Run("success: old fails, new works, other sessions end, this one and tokens stay", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		keep, other, token := signedIn(t, e)

		if err := e.auth.ChangePassword(ctx, testPassword, newPassword, keep.Value); err != nil {
			t.Fatalf("ChangePassword: %v", err)
		}
		admin, _ := e.store.GetAdmin(ctx)
		if checkPassword(testPassword, admin.PasswordHash) || !checkPassword(newPassword, admin.PasswordHash) {
			t.Error("the stored password is not the new one")
		}
		if !admin.PasswordChangedAt.Equal(e.clock.Now()) {
			t.Errorf("PasswordChangedAt = %v, want %v", admin.PasswordChangedAt, e.clock.Now())
		}
		if err := e.auth.CheckSession(ctx, keep.Value); err != nil {
			t.Errorf("the calling session ended: %v", err)
		}
		if err := e.auth.CheckSession(ctx, other.Value); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("another session survived: %v", err)
		}
		if err := e.auth.CheckAPIToken(ctx, token.Token); err != nil {
			t.Errorf("the API token stopped working: %v", err)
		}
		if _, err := e.auth.SignIn(ctx, testEmail, newPassword, "203.0.113.9"); err != nil {
			t.Errorf("signing in with the new password: %v", err)
		}
	})

	t.Run("without a calling session every session ends", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		keep, other, _ := signedIn(t, e)
		if err := e.auth.ChangePassword(ctx, testPassword, newPassword, ""); err != nil {
			t.Fatalf("ChangePassword: %v", err)
		}
		for name, s := range map[string]Session{"first": keep, "second": other} {
			if err := e.auth.CheckSession(ctx, s.Value); !errors.Is(err, ErrNotAuthenticated) {
				t.Errorf("the %s session survived: %v", name, err)
			}
		}
	})

	t.Run("without an admin", func(t *testing.T) {
		e := newAuthEnv(t)
		if err := e.auth.ChangePassword(ctx, testPassword, newPassword, ""); !errors.Is(err, ErrNoAdmin) {
			t.Errorf("ChangePassword without admin = %v, want ErrNoAdmin", err)
		}
	})

	t.Run("the change is logged, without passwords", func(t *testing.T) {
		e := newAuthEnvWithAdmin(t)
		if err := e.auth.ChangePassword(ctx, testPassword, newPassword, ""); err != nil {
			t.Fatal(err)
		}
		out := e.logs.String()
		if !strings.Contains(out, `"event":"password_changed"`) || strings.Contains(out, newPassword) || strings.Contains(out, testPassword) {
			t.Errorf("log should hold password_changed and no password:\n%s", out)
		}
	})
}
