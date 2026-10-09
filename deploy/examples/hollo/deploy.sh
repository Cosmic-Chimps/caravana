#!/usr/bin/env bash
# Sends compose.yaml and overlay.json to Caravana as one app, "hollo".
#
#   deploy/examples/hollo/deploy.sh preview   show the app it becomes, and the warnings; changes nothing
#   deploy/examples/hollo/deploy.sh deploy    deploy it (the first time, and after every change)
#
# Needs CARAVANA_URL (e.g. https://caravana.example.com) and CARAVANA_TOKEN (an API token, from
# the console's Settings), and python3. overlay.json holds secret values: it is gitignored.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
APP=hollo

: "${CARAVANA_URL:?set CARAVANA_URL, e.g. https://caravana.example.com}"
: "${CARAVANA_TOKEN:?set CARAVANA_TOKEN to an API token from Settings in the console}"
[ -f "$DIR/overlay.json" ] || { echo "missing $DIR/overlay.json: copy overlay.example.json and fill it in" >&2; exit 1; }
if grep -q REPLACE "$DIR/overlay.json"; then
  echo "overlay.json still has REPLACE placeholders" >&2
  exit 1
fi

case "${1:-}" in
preview) path="/v1/apps/$APP/compose/preview" ;;
deploy) path="/v1/apps/$APP/compose" ;;
*)
  sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
  ;;
esac

# The request body: the Compose file as a string, plus the overlay's services.
body="$(python3 - "$DIR/compose.yaml" "$DIR/overlay.json" <<'EOF'
import json, sys
compose = open(sys.argv[1]).read()
overlay = json.load(open(sys.argv[2]))
print(json.dumps({"compose": compose, "services": overlay["services"]}))
EOF
)"

# Answers never hold secret values, so they can be printed.
curl -sS -X POST "$CARAVANA_URL$path" \
  -H "Authorization: Bearer $CARAVANA_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @- <<<"$body" | python3 -m json.tool
