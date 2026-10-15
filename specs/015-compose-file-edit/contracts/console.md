# Contract: Console

Plain HTML forms and links; nothing needs JavaScript (FR-012). Copy below is exact.

## Configure (`GET /apps/{id}/configure`)

### App with a kept file

1. **Compose file** section, first on the tab: the 014 `compose-form` (file, name, overlay per
   service, Preview, Deploy). It holds:
   - the kept file text in the textarea, as stored;
   - the overlay from the kept file: domains, health, backup, and secret names marked as set with
     empty value inputs ("Leave empty to keep the current value", as in 014);
   - a hidden `revision`.

   Hint under the textarea: "Put passwords and keys in secrets below, not in the file."
2. **Definition**: the 014 per-service panels, read-only (values shown, no inputs, no Save),
   headed by "This app is edited through its Compose file."
3. Secrets, Webhook, Export and Delete: unchanged.

### App made from Compose, no kept file

1. Notice above the Compose form: "This app's Compose file wasn't kept. Paste it once to edit it
   here from now on."
2. Compose form with an empty textarea, the overlay prefilled from the app (014's `draftFrom`),
   and the revision.
3. The per-field edit form, editable as in 014 (US2-3).

### Image app

Unchanged from 014.

## Preview and Deploy

`POST /apps/{id}/compose/preview` and `POST /apps/{id}/compose` (existing, 014). Every outcome
of 014 stays: preview with services, warnings, refusals naming the Compose path, services added
and removed; the stale refusal keeps the typed draft (US1-4). A successful deploy redirects to
the new deployment, as today.

`GET /apps/{id}/compose` (Deploy from Compose) opens prefilled from the kept file when there is
one, else as 014.

## Per-field save on a kept-file app

`POST /apps/{id}/configure` answers **409** with the Configure page and the notice "This app is
edited through its Compose file. Change the file above and deploy it." Nothing is deployed.

## Deployments history

Each entry whose deployment has a kept file shows a link **Download Compose file** next to
"Export jobspec":

`GET /apps/{id}/deployments/{deploymentId}/compose`

- 200, body = the file text exactly as deployed
- `Content-Type: application/yaml; charset=utf-8`
- `Content-Disposition: attachment; filename="<app>-<deploymentId>.compose.yaml"`
- `Cache-Control: no-store`
- 404 page when the deployment has no file or isn't this app's.

Entries without a file show no link (US3-3).

## Deployment warnings

A deployment that dropped the file shows the warning sentence in its history entry like any other
warning (R5).
