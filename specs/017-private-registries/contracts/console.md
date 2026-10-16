# Contract: Console

Plain HTML forms; nothing needs JavaScript. The copy below is exact.

## Settings › Registries (new section, after Backup destination)

Lead: "Credentials Caravana uses to read private images. Use a token that can only read images: it
reaches each private app's own container as CARAVANA_REGISTRY_USERNAME and
CARAVANA_REGISTRY_PASSWORD."

A table, one row per registry:

| Column | Shows |
| --- | --- |
| Registry | host, mono |
| User | user name, mono |
| Last check | "works, 2 minutes ago" with the healthy mark; "refused, 2 minutes ago" with the failed mark and the message muted under it; "not checked yet" muted |
| Actions | "Test" (secondary, a form); "Edit" (a `<details>` with the edit form); "Remove" (a `<details>` with "Apps already running keep running. The next deploy of a private image from this registry fails until you add credentials again." and a danger "Remove" button) |

When there are none: "No registries yet. Public images from any registry deploy without one."

### Add form (always shown under the table)

- **Registry**: text, placeholder `ghcr.io`. Hint: "The host in the image name: ghcr.io,
  registry.gitlab.com, registry.example.com:5000. docker.io for Docker Hub."
- **User name**: text.
- **Password or token**: a password field.
- Button: "Save" (primary).

### Edit form (inside the row's `<details>`)

- **User name**: prefilled.
- **Password or token**: a password field, empty. Hint: "Leave empty to keep the current one."
- Button: "Save".

### Notices

- Saved: "Saved. The next deploy uses them; running apps pick them up the next time they start."
- Test ok: "ghcr.io accepted the credentials."
- Test refused: "ghcr.io refused the credentials: <message>."
- Field errors at their inputs.

## Deploy failures

The existing failed-deploy sentence shows the new message for `registry_auth_failed`. Its one fix
action links to `/settings#registries` with the text "Check the registry's credentials".
