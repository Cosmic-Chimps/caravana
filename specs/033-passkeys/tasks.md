# Tasks: Sign in with a passkey

**Input**: [plan.md](./plan.md), [spec.md](./spec.md), [data-model.md](./data-model.md), [contracts/](./contracts/)

Tests are required (constitution VIII): each task's tests are in the task. Two parts, built one after the other and
reviewed in between: **Part A** (store, service, software authenticator, API, CLI) and **Part B** (console, script,
lab step, docs).

## Part A — the server side

- [ ] **T001** `go get github.com/go-webauthn/webauthn@v0.18.2`, `go mod tidy` · go.mod, go.sum
- [ ] **T002** Migration and store: `passkeys` table, admin `password_signin` and `webauthn_handle`, session
  `verified_at`; store methods of data-model.md; table tests (insert, unique credential ID, list order, rename, delete,
  use update, count by RP, handle set once, verified_at round trip) · internal/store/migrations/0026_passkeys.sql,
  internal/store/passkeys.go, internal/store/passkeys_test.go, internal/store/sessions.go, internal/store/admin*.go
- [ ] **T003** Software authenticator: `webauthntest.New(rpID)` with an ES256 key; `Create(optionsJSON, origin)` and
  `Get(optionsJSON, origin)` returning the browser's JSON (base64url fields), with knobs for the counter, the UV flag,
  the origin and the RP ID it signs for; `PrivateKeySentinel()` (its key bytes as hex and base64) for leak tests;
  a test that go-webauthn accepts what it makes · internal/webauthntest/authenticator.go, authenticator_test.go
- [ ] **T004** Ceremonies in memory (R7): kinds, single use, 5-minute expiry, session binding, 8 per address, 512 in
  all; tests with a fake clock · internal/service/ceremonies.go, internal/service/ceremonies_test.go
- [ ] **T005** `Auth` passkeys (R1–R13): `BeginPasskeySignIn`, `FinishPasskeySignIn`, `BeginPasskeyConfirm`,
  `FinishPasskeyConfirm`, `VerifyPassword`, `SessionConfirmed`, `BeginPasskeyRegistration`,
  `FinishPasskeyRegistration`, `ListPasskeys`, `RenamePasskey`, `RemovePasskey`, `SignInSettings`,
  `SetPasswordSignIn`, `PasswordSignInAllowed`; `SignIn` takes the console domain and refuses with
  `ErrPasswordSignInOff`; `ResetPassword` turns password sign-in on; new sessions are confirmed; errors
  `ErrPasskeyRefused`/`*PasskeyFailure`, `ErrNotConfirmed`, `ErrLastPasskey`, `ErrNoPasskey`, `ErrPasskeyNotFound`,
  `ErrPasswordSignInOff`. Tests with the software authenticator: add then sign in; other domain, replay, backwards
  counter, no UV, wrong user handle, other session's ceremony, expired ceremony: each refused and counted, the sixth
  try limited before verification; password off refuses `SignIn` without a password check; fallback when no passkey for
  the domain; last passkey refused; reset turns password on; logs carry no sentinel · internal/service/passkeys.go,
  internal/service/passkeys_test.go, internal/service/auth.go, internal/service/sessions.go (+ callers of `SignIn`)
- [ ] **T006** API 0.36.0: the five operations and their codes; openapi; scope walk lists them as denied; contract
  tests; `consoleParity` doesn't change yet (no console routes) · internal/api/passkeys.go, internal/api/passkeys_test.go,
  internal/api/errors.go (or where codes live), internal/api/routes, api/openapi.yaml
- [ ] **T007** CLI: `admin passkeys list` (ID, name, domain, added, last used), `admin passkeys remove --id`,
  `reset-password` prints that password sign-in is on again when it was off, `status` prints the sign-in line; tests ·
  cmd/caravana/admin.go, cmd/caravana/admin_test.go

## Part B — the console, the lab and the docs

- [ ] **T008** Relying party from the request (R2/R3) in `internal/ui`: `passkeyPlace(r)` → `(rp, available, why)`
  and the console domain; tests for https host, localhost, `*.localhost`, 127.0.0.1, `::1`, public URL set and
  different host, port kept in the origin · internal/ui/passkeys.go, internal/ui/passkeys_test.go
- [ ] **T009** `/login`: the passkey block, `POST /login/passkey/options`, `POST /login/passkey`, password-off page and
  refusal · internal/ui/login.go, internal/ui/templates/login.html, internal/ui/passkeys.go, tests
- [ ] **T010** Settings › Sign-in page and its routes (contracts/console.md), the nav entry and `fingerprint` icon;
  parity table entries and the console-only set · internal/ui/passkeys.go, internal/ui/settings.go,
  internal/ui/settingsnav.go, internal/ui/icons.go, internal/ui/templates/settings.html, internal/ui/ui.go,
  internal/api/parity_test.go, tests
- [ ] **T011** `static/js/passkeys.js` and its `ours` line; `tools/ui-css.sh build` · internal/ui/static/js/passkeys.js,
  tools/ui-vendor.versions, internal/ui/static/app.css
- [ ] **T012** Lab step `console-passkeys` (before `console-appearance`): at `http://localhost:<port>` with the software
  authenticator, through the console's own routes: confirm window, add, sign out, sign in with it; rename; password off
  refuses the form; `runAdmin reset-password` turns it on; another domain's passkey, a replay and a backwards counter
  refused and counted (limit reached, then cleared by waiting is not needed: use a second address only if the lab can,
  else check the tries-left sentence); remove; the sentinel in no page, log or the SQLite file; leaves password sign-in
  on and no passkeys · cmd/caravana/console_passkeys_test.go, cmd/caravana/console_lab_test.go
- [ ] **T013** Docs: changelog note, CLAUDE.md paragraph, the installer's `--domain` help says passkeys belong to the
  domain · changelog.d/passkeys.md, CLAUDE.md, deploy/install/install.sh
