# Contract: API (`api/openapi.yaml` 0.9.1 → 0.10.0)

`Service.resources` gains `memoryMaxMB`:

```yaml
resources:
  type: object
  properties:
    cpuMHz: {type: integer, minimum: 0}
    memoryMB:
      type: integer
      minimum: 0
      description: Memory reserved for the service, in MB; the scheduler counts it. 0 means the installation's default (256 unless configured).
    memoryMaxMB:
      type: integer
      minimum: 0
      description: The most memory the service may use while the node has it spare, in MB. 0 means the larger of the reservation and the installation's default (1024 unless configured). Ignored, with a deployment warning, when the cluster doesn't allow memory oversubscription.
```

Error: 400 `invalid_field` on `services[i].resources.memoryMB` when it is larger than
`memoryMaxMB`.

# Contract: Compose

| Compose | Becomes |
| --- | --- |
| `mem_reservation`, `deploy.resources.reservations.memory` | `memoryMB` (reservation) |
| `mem_limit`, `deploy.resources.limits.memory` | `memoryMaxMB` (ceiling) |
| `deploy.resources.reservations.cpus`, `.devices`, … | refused, naming the path |

# Contract: console

Configure › Definition, each service's **Memory** row: `256 MB reserved, up to 1024 MB`, with
`default` in place of a number left at zero.

# Contract: installer

A run turns on the cluster's memory oversubscription when it's off, and lists `scheduler` in
`changed:`. A rerun with it on changes nothing.
