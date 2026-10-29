# Caravana for coding agents (MCP)

`caravana mcp` lets a coding agent (Claude Code, CodeAlta, Codex, or any client of the Model
Context Protocol) look at your installation and, if you allow it, act on it. Ask "is umbral
healthy?", "why did the last deploy of the blog fail?" or "what needs me?" and the agent finds out
itself.

It runs on your machine, next to the agent, and talks to Caravana's HTTP API with a token you
choose. Nothing is installed or opened on the host.

## Get the binary

Each release has the `caravana` binary for macOS (`darwin_arm64` for Apple silicon,
`darwin_amd64` for Intel) and Linux (the `linux_*` tarballs, which also hold the host's files):

```bash
v=0.10.0   # the release your host runs, or newer
curl -fsSLO "https://github.com/Cosmic-Chimps/caravana/releases/download/v$v/caravana_${v}_darwin_arm64.tar.gz"
tar -xzf "caravana_${v}_darwin_arm64.tar.gz"
sudo install "caravana_${v}_darwin_arm64/caravana" /usr/local/bin/caravana
caravana version
```

A binary downloaded with a browser is quarantined by macOS. Clear it with
`xattr -d com.apple.quarantine /usr/local/bin/caravana`.

## Pick a token

Make the token in the console under **Settings › API tokens**. It is shown once.

- **A deploy token** ("Only for one app") limits the agent to that app. It can read the app,
  its deployments and its Compose file, and deploy and promote it. Anything else is refused,
  and the agent is told why.
- **A full API token** reaches the whole installation: every app, copies, logs, backups and the
  needs-you list.

Keep the token in a file only you can read, rather than in the agent's config:

```bash
mkdir -p ~/.config/caravana && umask 077
printf '%s' 'cvat_...' > ~/.config/caravana/token
```

## Add it to your agent

Claude Code, for every project:

```bash
claude mcp add caravana --scope user \
  -e CARAVANA_URL=https://caravana.example.com \
  -e CARAVANA_TOKEN_FILE="$HOME/.config/caravana/token" \
  -- caravana mcp
```

Any other client takes the same three things: the command `caravana mcp`, and the two
environment variables. `CARAVANA_TOKEN` (the token itself) works instead of
`CARAVANA_TOKEN_FILE`, but not both. The token is never a command-line argument, since other
users of the machine can read those.

`CARAVANA_URL` must be `https://`. Plain `http://` is accepted only for `localhost` and other
loopback addresses, for a Caravana running on your machine.

## Reading, and acting

By default the agent can only read: this server offers no action tools unless you start it with
`--allow-actions`. That limit is enforced by the server, not by the token. An agent that also has
a shell runs as the same user, can read the token file and can call the API directly. What
bounds such an agent is the token itself, so give it a deploy token scoped to one app (it can
still deploy that app). It can see:
- projects and environments, and apps with their status and definition (never a secret's
  value);
- deployments, with their warnings, where they came from and why they failed, and it can wait
  for one to end;
- the copies, with their CPU and memory, and an app's recent output as it came;
- volumes, backups and recent actions;
- the needs-you list, a promotion preview, the exported jobspec and the kept Compose file.

To let it act, add `--allow-actions` after `caravana mcp`. It can then:
- redeploy, or deploy a new image to one service (the rest of the app is kept);
- roll back to a past version, or promote to another environment;
- restart, stop and start an app;
- back up a service now.

Your agent asks before each action unless you told it not to. Restart, stop and back up now are
marked as interrupting the service.

Never offered, with or without `--allow-actions`:
- deleting anything, or restoring a backup;
- Run a command and the terminal (their output can hold secrets, and whatever a tool returns
  goes to the agent's model);
- tokens, webhooks, registries, the backup destination and notification settings;
- writing a whole new definition or Compose file.

Do those in the console.

## Good to know

- An app's output is whatever the app prints. The agent reads it as data, but anyone who can
  make your app log a line can put words in front of the agent. The same goes for names and
  messages that come from deploys (branch names, commit messages and actors from CI origins) and
  for env values in app definitions: someone else may control that text. Keep actions off unless
  you need them.
- Each tool call is one API call, and the answer is the API's own JSON ([api/openapi.yaml](../api/openapi.yaml)).
- The server writes one line per tool call to standard error: the tool, ok or the error code,
  and how long it took. It never logs the token or an answer.
- A wrong address or token shows on the first tool call, as a plain message.
