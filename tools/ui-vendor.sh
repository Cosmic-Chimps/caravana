#!/usr/bin/env bash
# Vendors the browser scripts the console needs (htmx and its SSE extension)
# into internal/ui/static/js/, pinned by version and SHA-256 in
# tools/ui-vendor.versions. No Node, no CDN: the files come from the npm
# registry's tarballs once, and are committed. A file of our own (version
# "ours", URL "-") is only checked: fetch never writes it.
#
#   tools/ui-vendor.sh fetch   download the pinned tarballs, verify the
#                              extracted files' checksums, write them
#   tools/ui-vendor.sh check   verify the committed files' checksums
#                              (offline; what CI runs)
#
# `go build` never runs this: the files are committed.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
versions="$root/tools/ui-vendor.versions"
dest="$root/internal/ui/static/js"
cache="$root/.cache/ui-vendor"

usage() {
  echo "usage: tools/ui-vendor.sh fetch|check" >&2
  exit 2
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# entries: print the versions file's lines without comments and blanks.
entries() {
  grep -Ev '^[[:space:]]*(#|$)' "$versions"
}

# fetch_one NAME VERSION URL PATH SHA256: download the tarball (once, into the
# cache), extract PATH from it, and write it to the vendor directory only when
# its checksum is the pinned one.
fetch_one() {
  local name="$1" version="$2" url="$3" inner="$4" want="$5"
  local tarball="$cache/${url##*/}" tmp got
  if [[ "$url" == "-" ]]; then
    check_one "$@"
    echo "kept ${dest#"$root"/}/$name (ours)"
    return
  fi
  mkdir -p "$cache" "$dest"
  if [[ ! -f "$tarball" ]]; then
    echo "downloading $url" >&2
    curl --fail --silent --show-error --location --retry 3 --output "$tarball.part" "$url"
    mv "$tarball.part" "$tarball"
  fi
  tmp="$(mktemp)"
  tar -xzOf "$tarball" "$inner" >"$tmp"
  got="$(sha256 "$tmp")"
  if [[ "$got" != "$want" ]]; then
    rm -f "$tmp"
    echo "checksum mismatch for $name $version ($inner in $url)" >&2
    echo "  want $want" >&2
    echo "  got  $got" >&2
    exit 1
  fi
  mv "$tmp" "$dest/$name"
  chmod 644 "$dest/$name"
  echo "wrote ${dest#"$root"/}/$name ($version)"
}

# check_one NAME VERSION URL PATH SHA256: the committed file must exist and
# match. Returns 1 on a problem, after saying which.
check_one() {
  local name="$1" want="$5" got
  if [[ ! -f "$dest/$name" ]]; then
    echo "${dest#"$root"/}/$name is missing: run tools/ui-vendor.sh fetch" >&2
    return 1
  fi
  got="$(sha256 "$dest/$name")"
  if [[ "$got" != "$want" ]]; then
    echo "${dest#"$root"/}/$name does not match tools/ui-vendor.versions" >&2
    echo "  want $want" >&2
    echo "  got  $got" >&2
    return 1
  fi
}

case "${1:-}" in
  fetch)
    while read -r name version url inner sum; do
      fetch_one "$name" "$version" "$url" "$inner" "$sum"
    done < <(entries)
    ;;
  check)
    failed=0
    while read -r name version url inner sum; do
      check_one "$name" "$version" "$url" "$inner" "$sum" || failed=1
    done < <(entries)
    if [[ "$failed" -ne 0 ]]; then
      exit 1
    fi
    echo "internal/ui/static/js vendored files match tools/ui-vendor.versions"
    ;;
  *) usage ;;
esac
