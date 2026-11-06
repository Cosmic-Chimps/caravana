# Contract: API 0.33.0

Every answer below is JSON; times are RFC 3339 UTC; errors are the usual `{"error": {code, message,
field}}`. Deploy tokens are refused (403 `token_scope`) by the scope middleware's default deny: add the
two new routes to the scope walk's table as not allowed. A cookie request passes
`http.CrossOriginProtection` as every other post does.

## Schema `UpdateRun`

```json
{
  "id": "3f6c0d2a9b7e41c58d0f2e6a1b9c7d40",
  "version": "0.17.0",
  "fromVersion": "0.16.0",
  "trigger": "console",
  "tokenName": "",
  "state": "rolled_back",
  "reason": "Caravana 0.17.0 didn't answer its health check within 60 s; 0.16.0 runs again, with its data as it was",
  "requestedAt": "2026-11-06T14:02:41Z",
  "startedAt": "2026-11-06T14:03:00Z",
  "finishedAt": "2026-11-06T14:05:12Z",
  "log": "/var/lib/caravana-update/logs/3f6c0d2a9b7e41c58d0f2e6a1b9c7d40.log"
}
```

- `trigger`: `console` (a signed-in session), `api` (an API token; `tokenName` is its name), `automatic`.
- `state`: `requested`, `running`, `updated`, `rolled_back`, `failed`, `refused`, `lost`.
- `reason`: empty for `requested`, `running`, `updated`.
- `startedAt`, `finishedAt`: `null` until known.
- `log`: where the updater keeps the installer's output on the server (root only); `""` before the
  updater started the run, and for `lost` runs it never started.

## `POST /v1/updates/runs` (`requestUpdate`)

Body `{"version": "0.17.0"}` (a leading `v` is accepted and dropped). 202 with the `UpdateRun`
(`state: requested`). There is no single-run read, so no `Location`: the run appears in
`getUpdates.caravana.run` and `listUpdateRuns`.

| Status | Code | When |
| --- | --- | --- |
| 422 | `invalid_field` (field `version`) | missing, or not `X.Y.Z[-suffix]` |
| 409 | `update_unavailable` (field `version`) | a development build ("This is a development build, which has no release to compare with."), or the updater isn't installed ("This server's updater isn't installed. Run the installer of <latest> once on the server; it adds the updater, and later updates can start here.") |
| 409 | `update_running` | a run is `requested` or `running` ("An update to 0.17.0 is already under way.") |
| 409 | `update_not_offered` (field `version`) | not the newest release Caravana has seen, or not newer than the running version ("0.17.1 isn't the newest release caravana has seen (0.17.0). Check for releases first." / "caravana 0.17.0 already runs here.") |
| 500 | | the request file couldn't be written (the row is removed again) |

## `GET /v1/updates/runs` (`listUpdateRuns`)

200 `{"runs": [UpdateRun…]}`, newest first, at most 20.

## `GET /v1/updates` (`getUpdates`), additions to `caravana`

```json
"updater": {"installed": true, "unavailable": ""},
"run": UpdateRun | null,
"autoUpdate": {"enabled": false, "hour": 3, "next": null}
```

- `updater.installed`: the marker file is there. `unavailable`: why `requestUpdate` would answer
  `update_unavailable` right now (empty when it wouldn't).
- `run`: the open run, else the newest run that finished within the last 24 hours, else `null`.
- `autoUpdate.next`: the time of the next automatic request when one is due (on, a patch release is out
  that never had a run, the check on, the updater installed), else `null`.
- `history[]` gains `kind: "update"` rows (one per finished run; a `version` row explained by an
  `updated` run within an hour of it is left out).

## `PUT /v1/updates/settings` (`putUpdateSettings`), additions

`{"checkEnabled": true, "autoUpdate": true, "autoUpdateHour": 3}`; every field optional (a missing one
keeps its value). `autoUpdateHour` 0–23, else 422 `invalid_field` (field `autoUpdateHour`). The answer
is `getUpdates`'s, as today.

## Attention kind `caravana_update_failed`

Item on `listAttention` (and so `problem.opened` / `problem.resolved`): no app; `title` "caravana
update failed"; `text` from the one source of words (`service.ProblemParts`), by state:

- `rolled_back`: "caravana 0.17.0 didn't start, so 0.16.0 runs again." (warning)
- `failed`: "The update to caravana 0.17.0 failed: <reason>." (failed)
- `refused`: "The server's updater refused caravana 0.17.0: <reason>." (warning)
- `lost`: "The update to caravana 0.17.0 didn't finish: <reason>." (warning)

The versions are mono parts. Action "Open updates", `/ops/updates`. Key
`caravana_update_failed|||<run id>`. Open while the newest finished run is in one of these states and
the running version isn't at least the run's version.
