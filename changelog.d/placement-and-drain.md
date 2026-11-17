### Added
- **Server labels and placement rules.** Give a server labels on its page or through the API (`ssd=true`, `region=eu`),
  and say on which servers a service runs: `placement.require` (labels a server must have), `placement.prefer`
  (labels the scheduler favours, with a weight) and `placement.avoid` (server names it never runs on), through the API,
  the Compose overlay or Configure. The rules become the job's constraints and affinities (the export writes them),
  and a rule no server satisfies is refused before anything is registered, naming the field (`409 placement_impossible`).
  A service with rules whose copies can't be placed is on the needs-you list. See `docs/placement-and-drain.md`.
- **Move work off a server.** On a server's page (or `POST /v1/servers/{name}/drain`), the server stops taking new
  work and its copies move to other servers one at a time, each waiting for its replacement to pass its health checks.
  Services with a volume or a published port, Traefik and batch runs stay and are listed with their reason. The page
  follows the move; **Accept work again** (`DELETE …/drain`) lets the server take work, and cancels a move in progress.
  A move that passes its deadline with copies left is on the needs-you list. API 0.46.0.

### Upgrade
- Rerun the installer on the first server: Caravana's Nomad policy gains `node:write` (node labels, eligibility).
  Until you do, saving a label or moving work off says Nomad refused it.
