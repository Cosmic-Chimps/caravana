#!/usr/bin/env bash
# Runs deploy/dev/install-test.sh on every supported image, one after the other (two VMs at once would
# make the timing checks flaky), and prints one line per image at the end. A failed image doesn't stop
# the next one: one run gives the whole picture. About 20 minutes per image. Run it before a release.
#
#   deploy/dev/install-test-all.sh            all four images
#   deploy/dev/install-test-all.sh debian-12 debian-13    only these
#
# Each image's output goes to .cache/install-test-<image>.log (KEEP=1 is passed through, but only the
# last VM survives: every image uses the same VM name).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGES=("$@")
[ ${#IMAGES[@]} -gt 0 ] || IMAGES=(ubuntu-24.04 ubuntu-26.04 debian-12 debian-13)

mkdir -p "$ROOT/.cache"
results=() # one line per image, in order (the Mac's own bash has no associative arrays)
failed=0
for image in "${IMAGES[@]}"; do
  log="$ROOT/.cache/install-test-$image.log"
  echo "=== $image (log: $log)"
  start=$SECONDS
  if IMAGE="$image" "$ROOT/deploy/dev/install-test.sh" >"$log" 2>&1; then
    result="passed in $(((SECONDS - start) / 60)) min"
  else
    result="FAILED after $(((SECONDS - start) / 60)) min: $(grep -m1 '^FAIL:' "$log" || tail -1 "$log")"
    failed=1
  fi
  echo "    $result"
  results+=("$(printf '%-14s %s' "$image" "$result")")
done

echo
printf '%s\n' "${results[@]}"
if [ "$failed" = 0 ]; then
  echo "INSTALL TEST PASSED ON EVERY IMAGE"
else
  echo "INSTALL TEST FAILED ON AN IMAGE" >&2
  exit 1
fi
