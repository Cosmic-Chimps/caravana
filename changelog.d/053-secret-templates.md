### Added
- **A secret can be built from text and references.** Instead of one reference replacing a whole value, a secret may be
  a `template` with `refs`: `Host=postgres;Username=banana;Password=${pw}` with `pw` read from your secret provider
  (Bella Baxter). A deploy reads the references, puts each value where its `${name}` is (`$$` is a literal `$`) and
  keeps the result like any other secret, so a connection string can use a password that never leaves the provider.
  Refresh secrets builds the values again and rolls the app out when one changed. A template that doesn't parse, or a
  reference that is missing or unused, is refused naming `services[i].secrets[j].template` or `…refs.<name>`. The console
  lists a template read-only ("template with 2 references: …") and keeps it through a save; set one through the API or
  the Compose overlay (the Aspire publisher sends them). API 0.59.0. See `docs/secret-providers.md`.
