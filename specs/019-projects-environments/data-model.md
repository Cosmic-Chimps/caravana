# Data Model: Projects and Environments (019)

## `app.Place` (pure, `internal/app/place.go`)

```go
type Place struct{ Project, Environment, Name string }

const DefaultProject, DefaultEnvironment = "default", "production"

func PlaceOf(id string) (Place, error) // research R7
func (p Place) ID() string            // Name when default/production, else p--e--n
func ValidName(field, s string) error  // lowercase DNS label without "--", a *FieldError on field
```

Round trip: for every valid place, `PlaceOf(p.ID()) == p`. `PlaceOf("whoami")` is
`{default, production, whoami}`. `PlaceOf("a--b")`, `PlaceOf("a--b--c--d")`, `PlaceOf("a---b--c")`
are field errors on `id`. `app.App` is unchanged (research R3).

## Migration `0011_projects.sql`

```sql
CREATE TABLE projects (
  id           TEXT PRIMARY KEY,        -- a name (ValidName)
  display_name TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
) STRICT;

CREATE TABLE environments (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,             -- a name (ValidName)
  position   INTEGER NOT NULL,          -- order shown; production of a new project is 0
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, name)
) STRICT;

INSERT INTO projects VALUES ('default', 'Default', <now>, <now>);
INSERT INTO environments VALUES ('default', 'production', 0, <now>);

-- No REFERENCES here: SQLite refuses to add a referencing column with a non-NULL default
-- while foreign keys are on. The service checks the environment exists before any insert.
ALTER TABLE apps ADD COLUMN project     TEXT NOT NULL DEFAULT 'default';
ALTER TABLE apps ADD COLUMN environment TEXT NOT NULL DEFAULT 'production';
ALTER TABLE apps ADD COLUMN app_name    TEXT NOT NULL DEFAULT '';
UPDATE apps SET app_name = id;
CREATE UNIQUE INDEX apps_by_place ON apps(project, environment, app_name);
```

`<now>` is the stored time format (`storedTime`, nine fraction digits), written by SQL
(`strftime('%Y-%m-%dT%H:%M:%S', 'now') || '.000000000Z'`).

## Store

- `AppRecord` gains `Place app.Place`, read from the three columns.
- `InsertApp` and `SaveApp` (on insert) fill the columns from `app.PlaceOf(a.ID)`; an ID that
  `PlaceOf` refuses is stored as `default`/`production`/ID (the grandfathered `--` IDs; only
  the service's create check refuses new ones). Updates never change the columns.
- `ListApps(ctx)` keeps its order (by ID); `ListAppsIn(ctx, project, environment string)`
  filters (either may be empty for "any").
- Projects: `ListProjects` (by created_at, Default first), `GetProject`, `InsertProject(p)`
  (also inserts its `production` environment, one transaction; `ErrAlreadyExists`),
  `SetProjectDisplayName`, `DeleteProject` (refused with `ErrNotEmpty` naming the app IDs while
  any app has that project; environments go by cascade).
- Environments: `ListEnvironments(project)` (by position), `InsertEnvironment(project, name)`
  (position = max+1; `ErrAlreadyExists`; `ErrNotFound` for the project), `DeleteEnvironment`
  (`ErrNotEmpty` with app IDs), `EnvironmentExists(project, name)`.
- `ErrNotEmpty` is a new sentinel; its error type carries the app IDs.

## Deployments

No new column. `source_deployment` may now hold a deployment of another app (a promotion).
`DeploymentRecord` gains `SourceApp string`, read by a join on `source_deployment`
(empty when none or when it is the same app). A deployment is a **promotion** when
`SourceApp != ""` and differs from the record's app.

## Service types

- `Project{ID, DisplayName, Environments []Environment, CreatedAt, UpdatedAt}`;
  `Environment{Project, Name, Position, Apps int}`.
- `PromotionPreview{From, To string (app IDs); FromEnvironment, ToEnvironment string;
  FromDeployment string; Services []ServiceChange; Added []string; Warnings []app.Warning;
  TargetRevision string}`;
  `ServiceChange{Name; ImageFrom, ImageTo, DigestFrom, DigestTo string; Changed []string}`
  (`Changed` names fields: `command`, `args`, `ports`, `health`, `volumes`).
- `AttentionItem`, `notify.Event` and the activity row gain `Project`, `Environment`, `AppName`
  (from the stored place). `notify.Event` also gains `PromotedFrom` (environment name).
