# Quickstart: validate feature 002

Settings and defaults are in [research.md R9](research.md#r9-configuration); the HTTP contract
is [contracts/openapi.yaml](contracts/openapi.yaml); store behavior is in
[contracts/go-packages.md](contracts/go-packages.md).

## 1. Tests

```bash
go test ./internal/config ./internal/api ./internal/store ./cmd/caravana -v
```

Expected:
- `internal/config`: defaults, env, flag-over-env, and one failing case per invalid setting,
  each error naming the env var and flag.
- `internal/api`: the decode table (charset, wrong media type, unknown field with its path,
  oversized, empty, malformed, trailing data, type mismatch), 404/405/unclean paths as JSON,
  500 and panic recovery, health 200 and 503, log redaction, and every error body valid
  against the contract's `Error` schema.
- `internal/api` `TestContractMatchesRoutes`: 0 differences; the document validates as 3.1.
- `internal/store`: migrations on empty and up-to-date stores, a newer store refused, the
  second-process lock, app round-trips for all eight feature-001 shapes, idempotent save,
  deployments (create, update, transitions, list order, not found).
- `cmd/caravana`: start with defaults in a temp dir, call health, cancel, exit 0.

## 2. Run it by hand

```bash
go run ./cmd/caravana
# logs: migrations applied, listening on 127.0.0.1:7070
curl -s localhost:7070/v1/health                  # {"status":"ok"}
curl -si localhost:7070/v1/nope                   # 404, JSON error body
curl -si -X POST localhost:7070/v1/health         # 405, Allow: GET, HEAD, JSON error body
curl -si localhost:7070//v1/health                # 404 JSON, not a 301
```

Stop it with Ctrl-C: it logs the shutdown and exits 0. Start it again: it applies no
migrations. In another terminal, while it runs, start a second one with a different
`-listen`: it fails at once, saying the data directory is in use.

Bad config:

```bash
CARAVANA_DEFAULT_MEMORY_MB=-5 go run ./cmd/caravana   # exits 1, names CARAVANA_DEFAULT_MEMORY_MB / -default-memory-mb
go run ./cmd/caravana -listen :7070                   # starts, logs the "no authentication yet" warning
```

## 3. Every CI check

```bash
gofmt -l .                       # prints nothing
go mod tidy -diff                # prints nothing
go vet ./...
go tool staticcheck ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```
