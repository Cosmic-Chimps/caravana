# Quickstart: validate feature 001

How to prove the feature works, from unit tests to a real deploy in the lab. Types and rules are
in [data-model.md](data-model.md); error paths in [contracts/translate.md](contracts/translate.md).

## 1. Tests (no lab needed)

```bash
go test ./internal/app ./internal/nomadjob -v
```

Expected:
- `TestValidate/*`: one subtest per row of the error catalogue, each passes by asserting
  `FieldError.Field`.
- `TestTranslate/*`: one subtest per golden file in `internal/nomadjob/testdata/`, byte for byte.
- `TestTranslateMatchesLabJob`: the whoami case equals `deploy/dev/whoami.json` field for field.
- `TestTranslateDeterministic` and `TestTranslateNewDeploymentOnlyChangesMeta` pass.

Run a single case: `go test ./internal/nomadjob -run 'TestTranslate/whoami' -v`.

After an intentional output change, regenerate goldens and **review the diff** before
committing:

```bash
go test ./internal/nomadjob -update
git diff internal/nomadjob/testdata/
```

## 2. Every CI check

```bash
gofmt -l .                       # prints nothing
go mod tidy -diff                # prints nothing
go vet ./...
go tool staticcheck ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
test ! -e scratchpad && echo "scratchpad removed"
```

## 3. The translator's output runs in the lab

```bash
deploy/dev/lab.sh up
deploy/dev/lab.sh check internal/nomadjob/testdata/whoami.golden.json
```

Expected: `OK: whoami answers over HTTPS through Traefik.` The Nomad UI
(`http://127.0.0.1:4646/ui`) shows job `whoami` with a successful deployment, 2 healthy
allocations, and meta `caravana.deployment = lab-manual`.

## 4. Auto-revert still works (optional, by hand)

After step 3, copy the golden file and change the port's `"To": 80` to `"To": 81`, which nothing
in the container listens on. Change `caravana.deployment` too, then submit the copy with
`lab.sh check <copy>` (its curl may still pass, because the old allocations keep serving). Watch
the deployment in the Nomad UI: the new allocation's check fails, Nomad marks the deployment
failed when the healthy deadline passes, and it reverts to the previous good version. The scripted proof is feature 005.
