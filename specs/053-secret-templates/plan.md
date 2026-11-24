# Implementation Plan: Secret templates

**Spec**: [spec.md](./spec.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

`app.Secret` gains `Template` and `Refs`. The deploy's reference resolution (037) treats each ref of a template as a
reference, reads each provider once, and renders the text before `secrets.Build`, which takes the rendered value from
the resolved sets as it takes a `from` value. Refresh needs no new path: it compares the resolved sets with the
stored ones. No migration, no new module, no policy change, no change to `nomadjob.Translate`. API **0.59.0**.

## Constitution Check

- **I Small**: no service, no module. ✓
- **II One model**: a template is a field of `app.Secret`, so Compose overlays and the API normalize into it;
  `Translate` never sees a template or a value. ✓
- **III Safe deploys**: a template is declared on every deploy like a value or a reference, so each deploy still
  replaces the whole set; Refresh is the normal health-checked rollout of the live version. ✓
- **IV Fail loudly**: every refusal names `services[i].secrets[j].template` or `….refs.<name>[.provider|.path]`, with the
  byte offset for the text; nothing is dropped (an unused ref is an error, not ignored). ✓
- **V API-first**: `api/openapi.yaml` 0.59.0 in the same change; the console calls the same service methods and has
  no feature of its own. ✓
- **VI Nomad-native**: values go into the same `nomad/jobs/<app>/<service>` Variable; no Nomad change. ✓
- **VII Secure**: the template and refs hold no secret and are stored as written; the rendered value lives only
  between the read and `secrets.Provider.Replace`, as 037's does; sentinel tests. ✓
- **VIII Plain Go**: a pure parser and renderer in `internal/app` with table tests. ✓

No Complexity Tracking entry.

## Decisions

**D1 Model.** `app.Secret{Name, Value, From, Template string "template,omitempty", Refs map[string]SecretRef
"refs,omitempty"}`. At most one of Value, From and Template; none keeps the current value, as before. A template needs
`Refs` (at least one) and `Refs` is refused without a template. Both fields are `omitempty`, so no stored app's JSON,
and so no `revision`, changes (a test marshals a secret without them). A template with no `${name}` at all is refused
on `…refs` ("send a plain value instead"): it would have nothing to read.

**D2 Syntax.** `${name}` inserts `Refs[name]`; `$$` is a literal `$`; any other `$` (a lone one, `${` without `}`,
an empty or invalid name, `$` before another character) is an error naming the byte offset. Names match
`^[A-Za-z_][A-Za-z0-9_]{0,63}$`. Every `${name}` needs an entry (error on `…template`), every entry must be used
(error on `…refs.<name>`). Limits: template 1–4096 bytes, UTF-8, no NUL; at most 16 refs. A ref is validated exactly as
a `from` (`validateSecretRefValue`, shared with `validateSecretRef`): `…refs.<name>.provider`, `….path`. `ParseTemplate`,
`RenderTemplate` and `TemplateRefNames` are pure (`internal/app/secrettemplate.go`).

**D3 No recursive expansion.** `RenderTemplate` writes each value into the output as is and never scans it again: a value
that holds `$` or `${x}` arrives as typed. One pass over the parsed parts.

**D4 Resolution.** `referencesOf` lists, for a template, one `referenceAt` per ref in the order the text first uses it,
with `field = services[i].secrets[j].refs.<name>`. So `checkReferences` (provider exists, allowed for the place, errors on
`…refs.<name>.provider`) and the one-read-per-provider grouping in `resolveReferences` cover templates with no new path. A
missing path is `*ReferenceError` `…refs.<name>.path`; a provider failure is `…refs.<name>` (the group's first ref), mapped
to 037's 422 `secret_not_found`, 422 `secret_provider_refused`, 502 `secret_provider_unavailable`. After the reads each
template is rendered; the rendered value gets the same UTF-8/NUL check as a read one, and goes into the resolved sets under
the secret's name. `secrets.Build` takes a template secret's value from the resolved sets (like `from`).
`placeholderSets` and `HasSecretReferences` follow `referencesOf`.

**D5 Refresh.** `RefreshSecrets` already compares the resolved sets with the stored ones and deploys the live version
with its digests pinned when one differs; a template's rendered value is in the resolved sets, so it needs no change.
`ErrNoSecretReferences` now also counts a template as a reference.

**D6 Never a value.** `Secret.WithoutValue` keeps `Template` and a clone of `Refs`, so `App.WithoutSecretValues`,
`compose.Request.WithoutSecretValues` and the kept Compose file keep them (they hold no secret). The rendered value and the
refs' values appear in no stored row, answer, log, error or job; `internal/service`, `internal/api` and `internal/ui`
sentinel tests search them, and the gate step searches the SQLite file.

**D7 Other service paths.** `SetSecretIf` and `SetSecretRefIf` clear `Template` and `Refs` when they replace a secret. A
copy to another environment keeps templates and `MissingSecret` skips them (`Secret.IsReference`); the provider's `usedBy` list
counts a template's providers; a promotion keeps the target's secrets (unchanged).

**D8 API.** `api/openapi.yaml` 0.58.0 → 0.59.0: `Secret.template` and `Secret.refs` (a map of `SecretRef`), with the rules
in their descriptions. No new route or code. A secret with `template` and `value` is 400 on `…template`.

**D9 Console.** Where 037 shows a reference read-only (Variables page, the Compose form's kept secrets), a template secret
reads "template with N references: provider/path, …" in the same row. It has no input, the Compose draft keeps it through
preview and deploy (`draftSecret.Template/Refs`), and a Configure save leaves it alone (`applyEdit` doesn't touch secrets).
The provider's row counts each ref. No editor.

**D10 Lab.** The gate step `secret-references` adds `GATE_CONN`, a template with `$$` and one ref: `printenv` in the copy
shows it rendered, Refresh after Bella's value changed shows it re-rendered, and a template ref to a missing path is 422
`secret_not_found` on `…refs.pw.path`.

## Deviations

(none yet)
