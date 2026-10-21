# Contributing to Caravana

Thanks for wanting to help. Caravana is a self-hosted PaaS on HashiCorp Nomad. Before you start:

- **The rules every change follows** are in [.specify/memory/constitution.md](.specify/memory/constitution.md).
  A change that breaks one won't be merged, however good it is otherwise.
- **The plan** is [docs/plan.md](docs/plan.md). Features outside the current phase need the
  maintainer's agreement first, so open an issue before writing a large change.
- **How to build and test** is in [CLAUDE.md](CLAUDE.md) under "Commands". CI runs the same list:
  `go build`, `go vet`, `staticcheck`, `gofmt`, the CGO-free cross-compile, `go test -race`,
  `go mod tidy -diff`, the UI asset checks and shellcheck.

## Pull requests

- One change per pull request, with tests (table-driven, standard `testing` package).
- Product features go through Spec Kit (`specs/<number>-<name>/`): spec, plan, tasks.
- Add a line to `CHANGELOG.md` under "Unreleased" for anything a user of Caravana would notice.
- Keep code, docs and names your own: don't copy from other PaaS projects.

## Contributor agreement

Caravana is licensed under the GNU AGPL v3. To keep the project able to change its license
terms later (for example, to offer a commercial license next to the AGPL), every contribution
is made under this agreement. Accept it once, in your first pull request's description, with
the line: "I have read and agree to the contributor agreement in CONTRIBUTING.md."

By submitting a contribution (code, documentation or anything else) to this repository, you
agree that:

1. **You keep your copyright.** You grant Cosmic Chimps S.L., which maintains Caravana, a
   perpetual, worldwide, non-exclusive, royalty-free, irrevocable license to use, copy, modify,
   distribute and sublicense your contribution, and to license it under any terms, including
   terms other than the AGPL.
2. **Patents.** If your contribution is covered by a patent you can license, you grant Cosmic
   Chimps S.L. and everyone who receives Caravana a perpetual, worldwide, royalty-free license
   under that patent to make, use and distribute the contribution as part of Caravana.
3. **It is yours to give.** You wrote the contribution, or you have the right to submit it
   under these terms, and your employer (if any) agrees or has no claim on it. Third-party
   code is marked as such, with its license.
4. **No obligation.** The maintainers may use your contribution or not. It is provided as is,
   without warranty.

The contribution stays available under the AGPL to everyone, as the rest of Caravana is.

## The name

"Caravana" and its logo identify the official project of Cosmic Chimps S.L. You're welcome to
fork the code under the AGPL, but a fork must use a different name and logo, so people can tell
them apart.
