# Data model: Memory per service

## app.Resources (existing, one field added)

| Field | JSON | Meaning |
| --- | --- | --- |
| `CPUMHz` | `cpuMHz` | unchanged |
| `MemoryMB` | `memoryMB` | **the reservation**: what the scheduler counts. 0 means the default. |
| `MemoryMaxMB` | `memoryMaxMB` | **new, the ceiling**: what the service may use while the node has memory spare. 0 means the default. |

Validation:

- `memoryMaxMB` is 0 or at least 10.
- When both are set, `memoryMB ≤ memoryMaxMB`, else the error is on
  `services[i].resources.memoryMB`.

## Effective values (the translator)

| Reservation set | Ceiling set | Reservation | Ceiling |
| --- | --- | --- | --- |
| no | no | `DefaultMemoryMB` (256) | max(reservation, `DefaultMemoryMaxMB` (1024)) |
| yes | no | `MemoryMB` | max(`MemoryMB`, 1024) |
| no | yes | min(256, `MemoryMaxMB`) | `MemoryMaxMB` |
| yes | yes | `MemoryMB` | `MemoryMaxMB` |

## Settings

| Setting | Default | Was |
| --- | --- | --- |
| `CARAVANA_DEFAULT_MEMORY_MB` / `-default-memory-mb` | 256 | 64 |
| `CARAVANA_DEFAULT_MEMORY_MAX_MB` / `-default-memory-max-mb` | 1024 | new |

The default ceiling can't be lower than the default reservation (a configuration error naming
`settings.defaultMemoryMaxMB`).
