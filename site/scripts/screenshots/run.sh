#!/usr/bin/env bash
# Generates the screenshots of the documentation, never by hand:
#
#   site/scripts/screenshots/run.sh
#
# It builds the console's test binary from this checkout (so a UI change is always in the
# pictures), starts the demo (TestDocsDemo, internal/ui/docsdemo_test.go: a real console over a
# temporary SQLite store and a fake Nomad, filled with made-up data) on a loopback port, waits
# for its ready line, captures every screen in the light and the dark theme with Playwright
# (capture.mjs), optimises the captures (optimise.mjs) and copies them to docs/images/. It then
# stops the demo through a stop file and removes its temporary directory, pass or fail: nothing
# is left running or on disk.
#
# Why Playwright runs on the host and not in Docker: the console is a Go test on the host, and
# a browser in a container would reach it as host.docker.internal, a Host the console treats as
# a foreign address (its cookie, origin and passkey rules are written for the address the
# console is served at). With the browser on the host everything is http://localhost:<port>, the
# one address the console accepts without HTTPS, and the same command works on macOS and on a
# Linux CI runner. Playwright downloads its own Chromium (`npx playwright install chromium`).
#
# Needs: Go (the version of go.mod), Node 20 or newer with npm, and network access the first
# time (npm packages and Chromium). Nothing else; no Docker, no Nomad, no real installation.
#
# Settings (all optional): DEMO_PORT (default 7099), ONLY=name,name to take some screens only
# (the others are not copied and the full-run checks are off), KEEP=1 to leave the demo running
# after the capture until you press Enter (to look at it in a browser), LOCAL_ZONE=Europe/Madrid to
# see the local times beside the UTC ones (a check: the pictures stay in out/final, like ONLY's).
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
here=$(pwd -P)
root=$(cd "$here/../../.." && pwd -P)
images="$root/docs/images"
port="${DEMO_PORT:-7099}"

work=$(mktemp -d "${TMPDIR:-/tmp}/caravana-docs-demo.XXXXXX")
stop="$work/stop"
log="$work/demo.log"
pid=""

cleanup() {
  status=$?
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    touch "$stop"
    for _ in $(seq 1 50); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.2
    done
    kill "$pid" 2>/dev/null || true
  fi
  if [ "$status" -ne 0 ] && [ -f "$log" ]; then
    echo "run.sh: the screenshots failed; the end of the demo's log follows" >&2
    tail -n 30 "$log" >&2 || true
  fi
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
  echo "run.sh: port $port is already in use (set DEMO_PORT)" >&2
  exit 1
fi

echo "== Node packages and Chromium"
if [ ! -d node_modules ]; then
  npm ci --no-audit --no-fund
fi
npx --no-install playwright install chromium

echo "== The demo console (building)"
(cd "$root" && go test -c -o "$work/ui.test" ./internal/ui)
# The test binary runs in its package directory, as `go test` runs it.
(
  cd "$root/internal/ui"
  CARAVANA_DOCS_DEMO="127.0.0.1:$port" CARAVANA_DOCS_DEMO_STOP="$stop" \
    exec "$work/ui.test" -test.run '^TestDocsDemo$' -test.v -test.count=1 -test.timeout 30m
) >"$log" 2>&1 &
pid=$!

for _ in $(seq 1 300); do
  if grep -q '^docs demo ready at ' "$log" 2>/dev/null; then break; fi
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "run.sh: the demo stopped before it was ready" >&2
    exit 1
  fi
  sleep 0.5
done
grep -q '^docs demo ready at ' "$log" || { echo "run.sh: the demo was not ready in time" >&2; exit 1; }
echo "== Demo ready on http://localhost:$port"

export DEMO_URL="http://localhost:$port"
export DEMO_EMAIL="ana@example.com"
# Protects nothing: the demo's own owner password (docsDemoPassword in docsdemo_test.go).
export DEMO_PASSWORD="demo-password-for-screenshots"

echo "== Capture"
node capture.mjs
echo "== Optimise"
node optimise.mjs

if [ -n "${KEEP:-}" ]; then
  echo "== Demo left running at $DEMO_URL (sign in as $DEMO_EMAIL); press Enter to stop it"
  read -r _
fi

if [ -n "${ONLY:-}" ] || [ -n "${LOCAL_ZONE:-}" ]; then
  echo "== ONLY or LOCAL_ZONE is set: the pictures are in out/final, not copied to docs/images"
  exit 0
fi

echo "== docs/images"
mkdir -p "$images"
count=0
for file in out/final/*.png; do
  cp "$file" "$images/$(basename "$file")"
  count=$((count + 1))
done
echo "== $count images written to docs/images/"
