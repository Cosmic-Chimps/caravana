# Contract: API changes for feature 007

`api/openapi.yaml` goes from **0.4.0 to 0.5.0**. Everything below is added; no existing operation
changes shape, apart from the `Deployment.trigger` enum gaining `console`. All new routes need an
API token or a session (global `security`), answer the standard `Error` body, and are added to
the route table in `internal/api/api.go` in the same commit as the YAML (the contract test keeps
them equal). Reasons: research R7–R18.

| Method | Path | operationId | Console twin (SC-006) |
| --- | --- | --- | --- |
| `GET` | `/v1/apps` | `listApps` | Apps page table and headline |
| `POST` | `/v1/apps` | `createApp` | New app |
| `DELETE` | `/v1/apps/{id}` | `deleteApp` | Configure › Delete app |
| `GET` | `/v1/deployments` | `listRecentDeployments` | Apps page activity |
| `GET` | `/v1/apps/{id}/copies` | `listCopies` | Running tab |
| `GET` | `/v1/apps/{id}/logs` | `getLogs` | Logs tab |

Existing twins: deploy and redeploy (`deployApp`), history (`listDeployments`), definition
(`getApp`), webhook (`createWebhookToken`, `getWebhook`, `deleteWebhookToken`), settings
(`changePassword`, `createToken`, `listTokens`, `revokeToken`). Rollout progress appears on
`Deployment` (below), so the Deployments tab has a twin too.

## Paths

```yaml
  /v1/apps:
    get:
      operationId: listApps
      summary: List every app with its current status, by ID.
      tags: [apps]
      responses:
        "200":
          description: |
            All apps. When Nomad can't be read, each status is `unknown` and the stored
            data is still returned.
          content:
            application/json:
              schema: { $ref: "#/components/schemas/AppSummaryList" }
        "401": { $ref: "#/components/responses/Unauthorized" }
        "500": { $ref: "#/components/responses/Internal" }
    post:
      operationId: createApp
      summary: Create a new app and start its first deployment.
      description: |
        Like `deployApp` with a body, but never replaces a stored app: an existing ID is
        `409 app_exists` and nothing changes. Follow the rollout at `Location`.
      tags: [apps]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/App" }
      responses:
        "202":
          description: Saved; Nomad accepted the job.
          headers:
            Location: { $ref: "#/components/headers/DeploymentLocation" }
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Deployment" }
        "400": { $ref: "#/components/responses/BadRequest" }
        "401": { $ref: "#/components/responses/Unauthorized" }
        "403": { $ref: "#/components/responses/CrossOrigin" }
        "409":
          description: |
            `app_exists` (`field: "id"`): an app with this ID is stored. Or `job_conflict`,
            as for `deployApp`.
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Error" }
        "413": { $ref: "#/components/responses/BodyTooLarge" }
        "415": { $ref: "#/components/responses/UnsupportedMediaType" }
        "422": { $ref: "#/components/responses/ImageNotFound" }
        "500": { $ref: "#/components/responses/Internal" }
        "502": { $ref: "#/components/responses/BadGateway" }

  /v1/apps/{id}:            # existing path; add:
    delete:
      operationId: deleteApp
      summary: Delete an app for good.
      description: |
        Stops and purges the app's Nomad job, then erases the app, its deployments and its
        webhook token. If Nomad can't stop the job, nothing is erased. Not reversible.
      tags: [apps]
      responses:
        "204": { description: Deleted. }
        "401": { $ref: "#/components/responses/Unauthorized" }
        "403": { $ref: "#/components/responses/CrossOrigin" }
        "404": { $ref: "#/components/responses/NotFound" }
        "500": { $ref: "#/components/responses/Internal" }
        "502": { $ref: "#/components/responses/NomadUnavailable" }

  /v1/deployments:
    get:
      operationId: listRecentDeployments
      summary: List the newest deployments across all apps.
      tags: [deployments]
      parameters:
        - name: limit
          in: query
          schema: { type: integer, minimum: 1, maximum: 100, default: 20 }
      responses:
        "200":
          content:
            application/json:
              schema: { $ref: "#/components/schemas/DeploymentList" }
        "400": { $ref: "#/components/responses/BadRequest" }
        "401": { $ref: "#/components/responses/Unauthorized" }
        "500": { $ref: "#/components/responses/Internal" }

  /v1/apps/{id}/copies:
    parameters:
      - $ref: "#/components/parameters/AppID"
    get:
      operationId: listCopies
      summary: List the app's copies (Nomad allocations).
      description: |
        Every copy that hasn't ended, plus the ended copies of the app's latest job version
        that Nomad still keeps.
      tags: [apps]
      responses:
        "200":
          content:
            application/json:
              schema: { $ref: "#/components/schemas/CopyList" }
        "401": { $ref: "#/components/responses/Unauthorized" }
        "404": { $ref: "#/components/responses/NotFound" }
        "500": { $ref: "#/components/responses/Internal" }
        "502": { $ref: "#/components/responses/NomadUnavailable" }

  /v1/apps/{id}/logs:
    parameters:
      - $ref: "#/components/parameters/AppID"
    get:
      operationId: getLogs
      summary: Read the app's recent output, or follow it.
      description: |
        Without `follow`, returns up to `lines` recent lines as JSON. With `follow=true`, answers
        `text/event-stream`: the same recent lines as `log` events, then each new line as it is
        written, a comment every 20 s, until the client disconnects or its credentials end
        (then an `ended` event). Errors found before the stream starts are JSON as usual.
        Following doesn't renew a browser session.
      tags: [apps]
      parameters:
        - name: copy
          in: query
          description: One copy's ID (from `listCopies`). Absent means every listed copy.
          schema: { type: string }
        - name: stream
          in: query
          schema: { type: string, enum: [stdout, stderr, both], default: both }
        - name: lines
          in: query
          schema: { type: integer, minimum: 1, maximum: 500, default: 500 }
        - name: follow
          in: query
          schema: { type: boolean, default: false }
      responses:
        "200":
          content:
            application/json:
              schema: { $ref: "#/components/schemas/LogLines" }
            text/event-stream:
              schema:
                type: string
                description: |
                  `event: log` with `data:` one `LogLine` as JSON per line; `event: ended` with
                  no data when the stream ends because credentials ended.
        "400": { $ref: "#/components/responses/BadRequest" }
        "401": { $ref: "#/components/responses/Unauthorized" }
        "404":
          description: No such app, or `copy` isn't one of its copies.
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Error" }
        "500": { $ref: "#/components/responses/Internal" }
        "502": { $ref: "#/components/responses/NomadUnavailable" }
```

A new shared response, because `BadGateway` describes a deployment record and a `Location`
that these reads and the delete don't have:

```yaml
    NomadUnavailable:
      description: |
        Nomad couldn't be used: `nomad_unavailable` (unreachable, or the token was refused) or
        `nomad_rejected` (the message is what Nomad said). Nothing was changed.
      content:
        application/json:
          schema: { $ref: "#/components/schemas/Error" }
```

## Schemas

```yaml
    AppSummary:
      type: object
      required: [app, createdAt, updatedAt, status]
      properties:
        app: { $ref: "#/components/schemas/App" }
        createdAt: { type: string, format: date-time }
        updatedAt: { type: string, format: date-time }
        status: { $ref: "#/components/schemas/AppStatus" }
        lastDeployment: { $ref: "#/components/schemas/Deployment" }   # absent if never deployed

    AppSummaryList:
      type: object
      required: [apps]
      properties:
        apps: { type: array, items: { $ref: "#/components/schemas/AppSummary" } }

    AppStatus:
      type: object
      required: [state, wantedCopies, healthyCopies]
      properties:
        state: { type: string, enum: [rolling_out, healthy, degraded, not_running, unknown] }
        wantedCopies: { type: integer, minimum: 0 }
        healthyCopies: { type: integer, minimum: 0 }
        liveVersion: { type: integer, minimum: 0 }   # absent when no copy is healthy

    Copy:
      type: object
      required: [id, service, node, version, status, restarts]
      properties:
        id: { type: string }
        service: { type: string }
        node: { type: string }
        version: { type: integer, minimum: 0 }
        status: { type: string, enum: [starting, healthy, unhealthy, stopped, failed] }
        restarts: { type: integer, minimum: 0 }
        startedAt: { type: string, format: date-time }
        description: { type: string }

    CopyList:
      type: object
      required: [copies]
      properties:
        copies: { type: array, items: { $ref: "#/components/schemas/Copy" } }

    LogLine:
      type: object
      required: [copy, service, stream, text]
      properties:
        copy: { type: string }
        service: { type: string }
        stream: { type: string, enum: [stdout, stderr] }
        text: { type: string, description: One line without its newline; plain text. }
        time: { type: string, format: date-time, description: When Caravana received it (followed lines only). }

    LogLines:
      type: object
      required: [lines]
      properties:
        lines: { type: array, items: { $ref: "#/components/schemas/LogLine" } }

    Progress:
      type: object
      required: [wanted, placed, healthy, unhealthy, canariesWanted, promoted]
      properties:
        wanted: { type: integer }
        placed: { type: integer }
        healthy: { type: integer }
        unhealthy: { type: integer }
        canariesWanted: { type: integer }
        promoted: { type: boolean }
        description: { type: string }
```

`Deployment` gains:

```yaml
        trigger: { type: string, enum: [manual, webhook, console] }   # was [manual, webhook]
        progress: { $ref: "#/components/schemas/Progress" }           # only while status is running, when Nomad has it
```

`progress` is filled by `getDeployment` and `listDeployments` (newest running one only), not by
`listRecentDeployments`.

## Error codes

| Code | Status | When |
| --- | --- | --- |
| `app_exists` | 409 | `createApp` with a stored ID; `field: "id"` |

Existing codes cover the rest: `not_found`, `nomad_unavailable` and `nomad_rejected` (502),
`invalid_field` (400, for `stream`, `lines`, `limit`).
