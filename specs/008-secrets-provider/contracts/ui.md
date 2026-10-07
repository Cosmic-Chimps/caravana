# Contract: console changes

## Configure tab: Secrets panel

`GET /apps/{id}/configure` shows, after the definition, one **Secrets** panel. Per service it
lists the secret names (mono), each with a **Remove** button. Under the list there is one
**Set a secret** form:

- `service` (select, only shown when the app has more than one service)
- `name` (text, `autocomplete="off"`, `spellcheck="false"`)
- `value` (textarea, `autocomplete="off"`, `spellcheck="false"`, never pre-filled)
- submit: **Set and deploy**

A short line says that setting or removing a secret starts a deploy and that values can't be
read back.

## Routes

| Method | Path | Form fields | Does |
| --- | --- | --- | --- |
| POST | `/apps/{id}/secrets` | `service`, `name`, `value` | `service.SetSecret`, then redirect to `/apps/{id}/deployments` |
| POST | `/apps/{id}/secrets/remove` | `service`, `name` | `service.RemoveSecret`, then redirect to `/apps/{id}/deployments` |

Both are `protected` (cross-origin check) and need a session, like the other POST routes. On an
error the Configure page renders again with the error next to the form, and the `name` but never
the `value` filled back in.

## Service methods

```go
// SetSecret deploys appID with secret name of service set to value, keeping
// every other secret.
func (s *Service) SetSecret(ctx context.Context, appID, service, name, value string, trigger app.Trigger) (store.DeploymentRecord, error)
// RemoveSecret deploys appID without that secret, keeping every other one.
func (s *Service) RemoveSecret(ctx context.Context, appID, service, name string, trigger app.Trigger) (store.DeploymentRecord, error)
```

Both are a `Deploy` of the stored app with one change. The API reaches the same result through
`deployApp` (constitution V).
