# Contract: Settings › Registries

- The add form keeps one Registry field (`name="host"`). It accepts `ghcr.io` or `ghcr.io/cosmic-chimps`. Its hint says that a path keeps the login to images under it, and the host alone covers the rest of that registry.
- A refused path shows its message on that field ("The path …").
- Each row shows the entry (`host` or `host/path`) and counts the apps that entry serves.
- Test posts `POST /settings/registries/test` and Remove posts `POST /settings/registries/remove`, each with a hidden `host` holding the entry. `?edit=` and `?tested=` carry the entry.
- After a Test of a path entry that works, the notice adds the note that the first deploy under the path shows whether it reads those images.
- Every form still works without script. No page holds a password.
