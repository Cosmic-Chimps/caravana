# Release notes: one file per change

Every change that a person who runs Caravana would notice adds **one file here**, in the same branch
as the change. Nobody edits `CHANGELOG.md` for a change: it holds only released sections. When a
release is made, `tools/release.sh prepare X.Y.Z` gathers every file here into one
`## X.Y.Z - <date>` section of `CHANGELOG.md` and deletes the files. This README is never gathered.

Because two branches never edit the same file, merging or rebasing a branch after a release can't put
its notes inside the released section, which a shared "Unreleased" heading allowed without a conflict.

## The file

- Name: lowercase letters, digits, dots and dashes, then `.md`. Use the branch or feature name:
  `029-sqlite-backups.md`, `019.5-create-without-deploy.md`, `fix-dns-proxy.md`.
- Content: one or more of these subsections, and nothing else (no title, no text outside a
  subsection): `### Added`, `### Changed`, `### Fixed`, `### Security`, `### Upgrade`.
- Each subsection is `- ` bullets. A bullet may continue on following lines indented by two spaces.
  Write for the person who runs Caravana, in plain words, as the released sections do.
- `Upgrade` is only for what an upgrade needs beyond rerunning the installer.

```markdown
### Added
- **The thing people can now do**, in a sentence or two. Continuation lines
  are indented by two spaces.

### Upgrade
- Anything to do before or after rerunning the installer.
```

## The release

The release gathers one subsection per kind, in the order Added, Changed, Fixed, Security, Upgrade.
Inside a kind, bullets follow the file names in alphabetical order, then their order in the file. Both
ways of releasing (Run workflow, or a pushed `v*` tag) gather the files and commit their deletion
with the new section (`tools/release.sh commit X.Y.Z`), so the next release never gathers them again.

## Checks

`tools/release.sh check` (run by CI) fails on a bad file name, an empty file, a heading that isn't
one of the five kinds, text outside a subsection, or a `## Unreleased` section in `CHANGELOG.md`.
`tools/release.sh unreleased` prints what the next release would say. `tools/changelog-test.sh`
tests these commands.
