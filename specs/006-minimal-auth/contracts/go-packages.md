# Contract: Go packages for feature 006

The HTTP API contract is [openapi.yaml](openapi.yaml) (it replaces `api/openapi.yaml`); the
pages are in [pages.md](pages.md), the CLI in [cli.md](cli.md). Types and rules are in
[data-model.md](../data-model.md); reasons in [research.md](../research.md).

## `internal/store` (changed)

```go
// OpenShared opens a store another process holds the lock for (the running
// server), for the admin CLI: no lock, no migrations. It fails with
// ErrSchemaMismatch unless the database is at exactly this binary's latest
// migration (research R4).
func OpenShared(ctx context.Context, dataDir string) (*Store, error)

var ErrSchemaMismatch = errors.New("store schema differs from this caravana's")

type AdminRecord struct {
	Email             string
	PasswordHash      string
	CreatedAt         time.Time
	PasswordChangedAt time.Time
}

func (s *Store) CreateAdmin(ctx context.Context, a AdminRecord) error                   // ErrAlreadyExists
func (s *Store) GetAdmin(ctx context.Context) (AdminRecord, error)                       // ErrNotFound
func (s *Store) SetPassword(ctx context.Context, hash string, at time.Time) error        // ErrNotFound

type SessionRecord struct {
	IDHash                          string
	CreatedAt, LastSeenAt, ExpiresAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, r SessionRecord) error
func (s *Store) GetSession(ctx context.Context, idHash string) (SessionRecord, error)    // ErrNotFound
func (s *Store) TouchSession(ctx context.Context, idHash string, at time.Time) error
func (s *Store) DeleteSession(ctx context.Context, idHash string) error                  // nil when gone
func (s *Store) DeleteSessionsExcept(ctx context.Context, keepIDHash string) error       // "" deletes all
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time, idle time.Duration) error

type APITokenRecord struct {
	ID, Name, Hash string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	ExpiresAt      *time.Time
	RevokedAt      *time.Time
}

// CreateAPIToken inserts r unless an active token (not revoked, not expired
// at now) has the same name: then ErrAlreadyExists. Check and insert are one
// transaction.
func (s *Store) CreateAPIToken(ctx context.Context, r APITokenRecord, now time.Time) error
func (s *Store) GetAPITokenByHash(ctx context.Context, hash string) (APITokenRecord, error) // ErrNotFound
func (s *Store) ListAPITokens(ctx context.Context) ([]APITokenRecord, error)                // newest first
func (s *Store) TouchAPIToken(ctx context.Context, id string, at time.Time) error
func (s *Store) RevokeAPIToken(ctx context.Context, id string, at time.Time) error           // ErrNotFound; no-op when revoked
func (s *Store) ActiveAPITokenByName(ctx context.Context, name string, now time.Time) (APITokenRecord, error) // ErrNotFound

type SigninLimitRecord struct {
	Address     string
	Failures    int
	WindowStart time.Time
	LockedUntil *time.Time
}

func (s *Store) GetSigninLimit(ctx context.Context, address string) (SigninLimitRecord, error) // ErrNotFound
func (s *Store) PutSigninLimit(ctx context.Context, r SigninLimitRecord, pruneBefore time.Time) error
func (s *Store) DeleteSigninLimit(ctx context.Context, address string) error
```

Migration `0003_auth.sql` (data-model.md).

## `internal/service` (changed)

```go
// Auth is the one admin, its sessions, API tokens and the sign-in limit. It
// needs only the store, so the CLI can use it while Nomad is down.
type Auth struct { /* store, log, now, iterations */ }

type AuthOptions struct {
	Now                func() time.Time // nil: time.Now
	PasswordIterations int              // 0: 600,000 (tests use a small count)
}

func NewAuth(st *store.Store, log *slog.Logger, opts AuthOptions) *Auth

var (
	ErrNoAdmin          = errors.New("no admin yet: run caravana admin init on the host")
	ErrAdminExists      = errors.New("an admin already exists; use caravana admin reset-password")
	ErrBadCredentials   = errors.New("that email and password don't match")    // sign-in: same for both
	ErrSigninLimited    = errors.New("too many failed sign-ins from this address")
	ErrWrongPassword    = errors.New("the current password is wrong")
	// ErrNotAuthenticated is every API-credential failure (missing, malformed,
	// wrong, expired or revoked token or session, no admin yet). The API maps it
	// to the same 401 `unauthorized` as feature 004's webhook ErrUnauthorized.
	ErrNotAuthenticated = errors.New("missing or invalid API token or session")
)

// *app.FieldError (feature 001) reports rule failures with the field name:
// email, password/newPassword, name, expiresAt.

// SigninLimitError says how long to wait, and SigninFailure how many tries are left.
type SigninLimitError struct{ RetryAfter time.Duration }
type SigninFailure struct{ TriesLeft int }

// Admin.
func (a *Auth) InitAdmin(ctx context.Context, email, password string) error
func (a *Auth) AdminEmail(ctx context.Context) (string, error)                     // ErrNoAdmin
func (a *Auth) ResetPassword(ctx context.Context, password string) error           // ends all sessions
// ChangePassword ends every session except keepSession (the caller's, or "").
func (a *Auth) ChangePassword(ctx context.Context, current, next string, keepSession string) error

// Sign-in. address is the client address from ClientAddress. On a wrong email
// or password the error wraps ErrBadCredentials and is a *SigninFailure; when
// the address is locked it is a *SigninLimitError wrapping ErrSigninLimited.
func (a *Auth) SignIn(ctx context.Context, email, password, address string) (Session, error)
func (a *Auth) SignOut(ctx context.Context, sessionValue string) error

type Session struct {
	Value     string    // the cookie value; never logged
	ExpiresAt time.Time // absolute expiry, for the cookie
}

// CheckSession checks a cookie value: ErrNotAuthenticated when unknown or expired.
func (a *Auth) CheckSession(ctx context.Context, sessionValue string) error

// API tokens.
type CreatedAPIToken struct {
	Token string // shown once
	Info  APITokenInfo
}
type APITokenInfo struct {
	ID, Name   string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	State      string // active | expired | revoked
}

func (a *Auth) CreateAPIToken(ctx context.Context, name string, expiresAt *time.Time) (CreatedAPIToken, error) // ErrNoAdmin, *app.FieldError, ErrTokenNameTaken
func (a *Auth) ListAPITokens(ctx context.Context) ([]APITokenInfo, error)
func (a *Auth) RevokeAPIToken(ctx context.Context, id string) error          // store.ErrNotFound
func (a *Auth) RevokeAPITokenByName(ctx context.Context, name string) error  // store.ErrNotFound
// CheckAPIToken: ErrNotAuthenticated for anything but an active cvat_ token
// while an admin exists. Updates last_used_at at most once a minute.
func (a *Auth) CheckAPIToken(ctx context.Context, token string) error

var ErrTokenNameTaken = errors.New("an active token already has this name")
```

## `internal/httpx` (new, small)

Request plumbing both the API and the pages need, kept out of `service` because it reads HTTP.

```go
// Proxies is the parsed CARAVANA_TRUSTED_PROXIES list.
type Proxies []netip.Prefix

func ParseProxies(s string) (Proxies, error)

// ClientAddress is the address the sign-in limit counts (IPv4, or the IPv6
// /64 as a prefix string), and HTTPS reports whether the client used HTTPS,
// both believing forwarded headers only from a trusted proxy (research R9).
func (p Proxies) ClientAddress(r *http.Request) string
func (p Proxies) HTTPS(r *http.Request) bool

// Session cookie helpers (research R6): the name depends on HTTPS.
func SessionCookie(r *http.Request, https bool) (string, bool)
func SetSessionCookie(w http.ResponseWriter, value string, expires time.Time, https bool)
func ClearSessionCookie(w http.ResponseWriter, https bool)

// LogRequests logs method, path, status and duration: never the query,
// headers or cookies (research R15). Moved here from internal/api.
func LogRequests(log *slog.Logger, next http.Handler) http.Handler
```

## `internal/api` (changed)

```go
// Deps groups what the API needs, now that it has two services.
type Deps struct {
	Service *service.Service
	Auth    *service.Auth
	Store   *store.Store // health check only
	Proxies httpx.Proxies
	Log     *slog.Logger
}

func New(d Deps) http.Handler                                         // + token and password routes, auth on every non-public route
func NewWebhook(svc *service.Service, log *slog.Logger) http.Handler  // unchanged
```

`route` gains `public bool`. New codes `cross_origin` (403) and `token_name_taken` (409);
`ErrNotAuthenticated` maps to 401 `unauthorized`, like the webhook's `ErrUnauthorized`.

## `internal/ui` (new)

```go
type Deps struct {
	Auth    *service.Auth
	Proxies httpx.Proxies
	Log     *slog.Logger
	Now     func() time.Time // nil: time.Now; the relative times on the pages (tests share the Auth's fake clock)
}

// New serves /login, /logout, /account (with its password and token forms),
// / (redirect) and /static/…. It returns an error, not just a handler,
// because its embedded templates are parsed here.
func New(d Deps) (http.Handler, error)
```

Embedded: `templates/*.html`, `static/app.css` (generated, committed), `static/fonts/*.woff2`,
`static/fonts/OFL.txt`. Sources for the CSS: `style/input.css`, `style/theme.css`.
`generate.go` holds `//go:generate ../../tools/ui-css.sh build`.

## `internal/config` (changed)

```go
type Config struct {
	// ...
	TrustedProxies httpx.Proxies // NEW: CARAVANA_TRUSTED_PROXIES / -trusted-proxies
}
```

## `cmd/caravana` (changed)

- `run` dispatches `admin …` to `runAdmin(ctx, args, getenv, stdin, stdout, stderr) int`
  (contract: cli.md); anything else starts the server.
- The main listener sends `/v1` to `api.New` and everything else to `ui.New`, wrapped once in
  `httpx.LogRequests`. It decides on the cleaned path with a small handler (`root` in `main.go`),
  not a `ServeMux`: a mux would redirect `//v1/health`, and the API never redirects. The
  webhook-only listener is unchanged (`api.NewWebhook` logs its own requests).
- At startup, if no admin exists, it logs a warning naming `caravana admin init`. The old
  "the API has no authentication yet" warning goes.
- `gate_test.go`: `gateTarget.APIToken`, one API-call helper that sets the Bearer header, and a
  first step `api-needs-token`. `lab_test.go`: `admin init` and `admin create-token`
  in-process while the server runs. `TestGate` reads `GATE_API_TOKEN`.

## Tools and scripts

- `tools/ui-css.sh build|check` and `tools/ui-css.versions` (research R13). `.cache/` is
  gitignored.
- `deploy/vm/host.sh`: new `admin` command; `prove` checks `admin status`, creates and finally
  revokes `gate-<run>`, and passes `GATE_API_TOKEN`.
- CI: `tools/ui-css.sh check`; shellcheck also covers `tools/*.sh`.
