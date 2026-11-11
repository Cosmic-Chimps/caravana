### Changed
- **A backed-up Postgres service warns about a plain password.** When a service with a `postgres`
  backup gets `POSTGRES_PASSWORD` from its environment instead of a secret, the deploy still works
  and now warns on that field: a plain value shows in the app's definition, its Nomad job and
  exports. Make it a secret to keep it out of all three.
