### Added
- **Deploy a repository that has a Dockerfile, with no CI.** New app has a **Build from Git** tab: give a repository, a
  branch, an optional folder and Dockerfile, and the image to push to. A deploy clones the branch, builds the Dockerfile
  with rootless BuildKit as a Nomad batch job, pushes the image with the credentials of the registry entry that covers
  it (Settings › Registries) and deploys the pushed digest like any image: health checked, reverted on failure.
  Rollback and promotion deploy a past digest and never build. Through the API a service has `build` and its `image` is
  the repository builds push to. See `docs/building-from-git.md`.
- **Private repositories.** A deploy key that Caravana makes (its public half and fingerprint are shown, the private
  half stays in a Nomad Variable and in no page, answer or log) or a token you give. Configure has a Build panel for
  the source and the Git access, and Next steps on a new app's page walk through the key or token, the push webhook and
  the first build. The keys of github.com and gitlab.com are built in; any other SSH host gives its key.
- **A push builds and deploys.** The app's webhook is the repository's push webhook: GitHub, GitLab (also its
  `X-Gitlab-Token`), Gitea and Forgejo bodies are understood. A push to another branch, a branch deletion, a ping and a
  push of the commit already live build nothing.
- **Builds on the Deployments tab.** A deployment that built shows a Build stage (queued, building with the time it has
  taken, built in 41 s, failed), the commit, branch and subject, and a **Build log** page that follows a build that is
  going. A failed build fails its deployment with the last log lines, sends the usual `deploy.failed` notification and
  leaves the running version alone. A newer deploy cancels the build in progress.
- **Settings › Builds**: memory, CPU, timeout, builds at once and the size of each app's build cache. API 0.56.0:
  `getDeploymentBuild`, `getGitAccess`, `createDeployKey`, `putGitToken`, `deleteGitAccess`, `getBuildSettings`,
  `putBuildSettings`, `Service.build`, `Deployment.build`, `WebhookResult.skipped`.

### Upgrade
- Nothing to do on a host beyond rerunning the installer as usual: builds use the jobs, variables (`nomad/jobs/*`,
  `caravana/*`), host volumes and file reads Caravana's Nomad policy already allows. The first build pulls the Git and
  BuildKit images (about 100 MB), and an image to push to needs a registry entry whose credentials may push.
