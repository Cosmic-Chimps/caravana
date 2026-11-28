### Fixed

- A service named `alloc` is refused when the app is saved, naming `services[i].name` (`services.alloc.name` in a
  Compose file). Nomad keeps the folder a copy's tasks share under that name and refused the job only at the deploy. API 0.69.1 (text only).
