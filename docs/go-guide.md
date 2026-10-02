# Go in Caravana (for C# developers)

The conventions this repo follows, with the C# idea each one replaces. For the full picture, read
the [Tour of Go](https://go.dev/tour), [Effective Go](https://go.dev/doc/effective_go),
[Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) and
[Organizing a Go module](https://go.dev/doc/modules/layout).

## Project layout

```
cmd/caravana/        main package: reads config, builds every dependency, starts the server
api/openapi.yaml     public API contract
internal/app/        App, Service, Deployment: the model everything else uses
internal/nomadjob/   app.App -> *nomad.Job (pure, no I/O)
internal/registry/   image tag -> digest
internal/deploy/     one deploy: resolve, translate, register, watch
internal/service/    business logic shared by UI and API
internal/api/        JSON HTTP handlers
internal/web/        HTMX handlers, templates/ and static/ (embedded)
internal/store/      SQLite + migrations (embedded)
deploy/dev/          local Nomad lab (Lima VM, Nomad and Traefik config)
```

- **A folder is a package.** All `.go` files in one folder must share one `package` name. (This is
  why the first proof-of-concept sketch didn't build: three packages in one folder.) Roughly: folder = namespace + project.
- **`internal/`** can only be imported by code in this module. It's like `internal` in C#, but
  enforced by the compiler for the whole folder tree.
- **`cmd/<name>/`** holds each binary's `main` package. Keep it thin: wiring only.
- **Package names**: short, lowercase, single word, no `utils`/`common`. The name is part of every
  call (`nomadjob.Translate`, not `nomadjob.TranslateNomadJob`).
- **`testdata/`** folders are ignored by the Go build. Put one next to the tests that use it
  (e.g. `internal/nomadjob/testdata/whoami.golden.json`).

## C# → Go

| C# | Go |
| --- | --- |
| `public` / `private` | Capitalized name = exported, lowercase = package-private |
| Exceptions, `try/catch` | Return `error` as the last value; check it right away: `if err != nil { return fmt.Errorf("register %q: %w", id, err) }` |
| `class : IFoo` | Interfaces are satisfied implicitly. Declare the interface in the package that *uses* it, keep it small (1–3 methods). |
| DI container | Constructor functions `NewDeployer(store, nomad)` called by hand in `main`. No container. |
| `CancellationToken` | `ctx context.Context`, always the first parameter of anything doing I/O |
| `async`/`await`, `Task` | Plain blocking calls; start a goroutine (`go f()`) only when you really need concurrency |
| `null` | Zero values (`""`, `0`, `nil`). Use a pointer only when "not set" must differ from zero. The Nomad API uses pointers everywhere; since Go 1.26, `new(expr)` builds one from a value: `new(2)`, `new("service")`. |
| Generic collections, LINQ | Slices, maps and `for` loops; the `slices` and `maps` packages for common helpers |
| xUnit `[Theory]` | Table-driven tests: a slice of cases and `t.Run(tc.name, ...)` |
| NuGet | Go modules: `go get pkg@version`, `go mod tidy` |
| `dotnet format` / analyzers | `gofmt` (not optional), `go vet`, `staticcheck` |

## Rules of thumb

- Accept interfaces, return concrete types (structs).
- Make the zero value useful when you can. Don't write constructors that only set defaults.
- No `panic` in library code. Errors are values, so wrap them with context and return them.
- Don't create an interface until there are two implementations or a test needs one.
- Keep dependencies few; the standard library covers HTTP, JSON, templates, SQL and embedding.
