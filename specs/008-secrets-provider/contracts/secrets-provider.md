# Contract: `secrets.Provider`

Package `internal/secrets`. The service layer uses only this. Implementations: `deploy.Variables`
(Nomad Variables, production) and `secretstest.Store` (in memory, tests). Feature 014 adds Bella
Baxter.

```go
// Sets are an app's secrets: service name → secret name → value.
type Sets map[string]map[string]string

// ErrUnavailable is a store that can't be reached or refuses Caravana. The
// API answers it with 502 secret_store_unavailable.
var ErrUnavailable = errors.New("secret store unavailable")

type Provider interface {
	// Read returns the secrets every service of appID has now. No secrets is
	// an empty Sets, not an error.
	Read(ctx context.Context, appID string) (Sets, error)
	// Replace makes appID's secrets exactly sets, on behalf of deploymentID.
	// The sets it replaces are kept until Commit or Restore for that
	// deployment (or taken over by the next Replace).
	Replace(ctx context.Context, appID, deploymentID string, sets Sets) error
	// Commit forgets the kept sets, if deploymentID owns them.
	Commit(ctx context.Context, appID, deploymentID string) error
	// Restore makes the kept sets live again and forgets them, if
	// deploymentID owns them.
	Restore(ctx context.Context, appID, deploymentID string) error
	// Delete removes every secret of appID, kept ones included.
	Delete(ctx context.Context, appID string) error
}
```

Every error that means "the store can't be reached or refused us" matches `ErrUnavailable`
with `errors.Is`. No error message contains a value.

`secretstest.Store` also offers `SetUnavailable(bool)`, `Live(appID) Sets` and
`Kept(appID) (owner string, sets Sets)` for assertions.
