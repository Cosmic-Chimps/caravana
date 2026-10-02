# Contract: Go packages for feature 004

The HTTP contract is [openapi.yaml](openapi.yaml) (it replaces `api/openapi.yaml`). Types and
the decision flow are in [data-model.md](../data-model.md); reasons in
[research.md](../research.md).

## `internal/store` (changed)

```go
type WebhookTokenRecord struct {
	AppID     string
	Hash      string // hex SHA-256 of the token
	CreatedAt time.Time
}

func (s *Store) SetWebhookToken(ctx context.Context, appID, hash string) (WebhookTokenRecord, error) // upsert; ErrNotFound: unknown app
func (s *Store) GetWebhookToken(ctx context.Context, appID string) (WebhookTokenRecord, error)       // ErrNotFound: none
func (s *Store) DeleteWebhookToken(ctx context.Context, appID string) error                           // nil when none; ErrNotFound: unknown app
func (s *Store) LatestHealthyDeployment(ctx context.Context, appID string) (DeploymentRecord, error)  // ErrNotFound: none
```

Migration `0002_webhook_tokens.sql` (data-model.md).

## `internal/service` (changed)

```go
// ErrUnauthorized is every webhook authorization failure, whatever the reason.
var ErrUnauthorized = errors.New("missing or invalid webhook token")

type WebhookToken struct {
	Token     string // shown once
	Path      string // /v1/webhooks/{id}?token=…
	CreatedAt time.Time
}

type WebhookStatus struct {
	Enabled   bool
	CreatedAt time.Time // zero when !Enabled
}

type WebhookResult struct {
	Changed    bool
	Deployment store.DeploymentRecord
}

func (s *Service) CreateWebhookToken(ctx context.Context, appID string) (WebhookToken, error)
func (s *Service) WebhookStatus(ctx context.Context, appID string) (WebhookStatus, error)
func (s *Service) DeleteWebhookToken(ctx context.Context, appID string) error

// Webhook checks token in constant time, skips unchanged pushes (research R6) and otherwise
// calls Redeploy(ctx, appID, app.TriggerWebhook). Its deploy errors are Redeploy's
// (*DeployError and friends); its only other error is ErrUnauthorized.
func (s *Service) Webhook(ctx context.Context, appID, token string) (WebhookResult, error)
```

## `internal/api` (changed)

```go
func New(svc *service.Service, st *store.Store, log *slog.Logger) http.Handler // + 4 routes
func NewWebhook(svc *service.Service, log *slog.Logger) http.Handler          // NEW: only POST /v1/webhooks/{id}
```

Both handlers share the JSON-only 404/405 handling, request logging and error mapping; the
webhook-only one has a one-route table. New code `unauthorized` (401, `WWW-Authenticate: Bearer`).

## `internal/config` (changed)

```go
type Config struct {
	// ...
	WebhookListen string // NEW: "" means off
}
```

## `cmd/caravana` (changed)

When `cfg.WebhookListen` is set, a second `http.Server` with `api.NewWebhook`; both servers
start before the wait and shut down together on SIGINT/SIGTERM.
