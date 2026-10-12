# Contract: console

New and changed routes. Every POST is session-only, cross-origin protected and a plain form
post; every page works without JavaScript. The parity table in `internal/api/parity_test.go`
gets one entry per new route.

| Route | Does | API twin |
| --- | --- | --- |
| `GET /apps` (changed) | the needs-you list under the headline | `listAttention`, `listApps` |
| `GET /parts/attention` (new) | the needs-you region, for the live refresh | `listAttention` |
| `GET /live` (changed) | also sends `attention` notices | `listAttention` |
| `GET /apps/new` (changed) | tabs "Image" and "Compose file" (`?from=compose`) | none (a blank form) |
| `POST /apps/new/compose/preview` (new) | the Compose preview and overlay fields; saves nothing | `previewCompose` |
| `POST /apps/new/compose` (new) | create and deploy (`If-None-Match: *` semantics); 303 to the deployment | `deployCompose` |
| `GET /apps/{id}/configure` (changed) | the definition as an edit form, with the revision | `getApp`, `getWebhook` |
| `POST /apps/{id}/configure` (new) | save and deploy the edit (`If-Match` semantics); 303 to the deployment | `getApp`, `deployApp` |
| `GET /apps/{id}/compose` (new) | the "Deploy from Compose" form, overlay prefilled | `getApp` |
| `POST /apps/{id}/compose/preview` (new) | preview against the stored app: added and removed services | `getApp`, `previewCompose` |
| `POST /apps/{id}/compose` (new) | deploy the file (`If-Match` semantics); 303 to the deployment | `deployCompose` |
| `GET /apps/{id}` (changed) | the Running tab with CPU and memory per copy | `getApp`, `listCopies` (`usage=true`) |
| `GET /parts/apps/{id}/copies` (changed) | the copies region with usage | `listCopies` (`usage=true`) |
| `GET /live/apps/{id}` (changed) | `?tab=running` adds a `usage` event every 5 s | `listCopies` (`usage=true`) |

## Answers

- A form with errors comes back as 422 with the page and every typed value, except secret
  values, and the message at its field (`aria-invalid`, `aria-describedby`, first error
  focused), as the existing New app form does.
- A stale edit (`app_changed`) comes back as 409 with the form as typed and a panel "This app
  changed since you opened the form" listing the current values of the fields that differ, and
  a fresh revision, so pressing save again deploys what was typed over the new definition.
- A successful save, create or deploy is 303 to `/apps/{id}/deployments#<deployment>`.
- htmx-boosted posts get the same answers; the region swap is the page's `main`.

## Live events

| Page | Event | Region refetched |
| --- | --- | --- |
| Apps | `attention` | `/parts/attention` |
| App, Running tab | `usage` (every 5 s while the tab is open), `copies` | `/parts/apps/{id}/copies` |
