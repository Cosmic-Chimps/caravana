### Added
- **A preview for every pull request.** An environment named `pr-<number>` is a preview: caravana-action's new
  `preview: true` opens it with a deploy token, copies the app of `staging` into it with the pull request's image and
  its own address, and removes it when the pull request closes. A deploy token may now create preview environments
  of its own project and delete its own app and the empty environment there, and nothing else.
- **Limit, expiry and a password for previews.** Each project keeps at most 5 open previews by default (0 to 20) and
  closes one 7 days after its newest deploy (1 to 30), so a missed close still cleans up. An optional project
  password makes every preview address Team only. All three are on the project's settings page, which also shows
  each preview's expiry and a Close preview button; the API has `getPreviews`, `putPreviewSettings` and
  `closePreview` (0.54.0). See `docs/previews.md`, including the one wildcard DNS record to set by hand.

### Changed
- **The prefix `pr-` is kept for previews.** An environment name that starts with `pr-` must be `pr-<number>`.

### Upgrade
- Nothing to rerun: the new columns are added when Caravana starts, and the Nomad policy is unchanged. Previews need caravana-action with preview mode, and one wildcard DNS
  record for the preview addresses.
