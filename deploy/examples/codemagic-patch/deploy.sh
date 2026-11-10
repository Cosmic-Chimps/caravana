#!/usr/bin/env bash
# Sends compose.yaml and overlay.json to Caravana as one app, "codemagic-patch".
#
#   deploy/examples/codemagic-patch/deploy.sh preview   show the app it becomes, and the warnings; changes nothing
#   deploy/examples/codemagic-patch/deploy.sh deploy    deploy it (the first time, and after every change)
#
# Needs CARAVANA_URL (e.g. https://caravana.example.com) and CARAVANA_TOKEN (an API token, from
# the console's Settings), and python3. overlay.json holds secret values: it is gitignored, and
# OVERLAY points at it when it lives elsewhere (e.g. OVERLAY=~/.secrets/codemagic-patch/overlay.json).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
APP="${APP:-codemagic-patch}"
OVERLAY="${OVERLAY:-$DIR/overlay.json}"

: "${CARAVANA_URL:?set CARAVANA_URL, e.g. https://caravana.example.com}"
: "${CARAVANA_TOKEN:?set CARAVANA_TOKEN to an API token from Settings in the console}"
[ -f "$OVERLAY" ] || { echo "missing $OVERLAY: copy overlay.example.json and fill it in, or set OVERLAY" >&2; exit 1; }
if grep -q REPLACE "$OVERLAY"; then
  echo "overlay.json still has REPLACE placeholders" >&2
  exit 1
fi

case "${1:-}" in
preview) path="/v1/apps/$APP/compose/preview" ;;
deploy) path="/v1/apps/$APP/compose" ;;
*)
  sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
  ;;
esac

# The request body: the Compose file as a string, with each __NAME__ replaced by overlay.json's
# vars.NAME, plus the overlay's services. The vars are not sent on their own.
body="$(python3 - "$DIR/compose.yaml" "$OVERLAY" <<'EOF'
import json, re, sys
overlay = json.load(open(sys.argv[2]))
names = overlay.get("vars", {})
compose = open(sys.argv[1]).read()
compose = re.sub(r"__([A-Z0-9_]+)__", lambda m: names.get(m.group(1), m.group(0)), compose)
left = sorted(set(re.findall(r"__[A-Z0-9_]+__", compose)))
if left:
    sys.exit("overlay.json's vars has no value for " + ", ".join(left))
host = overlay["services"]["server"]["domains"][0]["host"]
if host != names.get("PATCH_DOMAIN"):
    sys.exit(f"services.server.domains[0].host ({host}) differs from vars.PATCH_DOMAIN")
print(json.dumps({"compose": compose, "services": overlay["services"]}))
EOF
)"

# Answers never hold secret values, so they can be printed.
curl -sS -X POST "$CARAVANA_URL$path" \
  -H "Authorization: Bearer $CARAVANA_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @- <<<"$body" | python3 -m json.tool
