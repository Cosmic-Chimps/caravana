#!/usr/bin/env bash
# Builds a Caravana release: what the installer downloads (feature 012).
#
#   tools/release.sh build VERSION OUT_DIR
#   tools/release.sh notes VERSION
#
# notes prints VERSION's section of CHANGELOG.md (the lines under "## VERSION - date", without
# that heading), which the release workflow publishes as the release's notes. It fails when
# the section is missing or empty, so no release goes out with nothing to say.
#
# writes into OUT_DIR:
#   caravana_<VERSION>_linux_amd64.tar.gz   the binary (built with that version) and
#   caravana_<VERSION>_linux_arm64.tar.gz   the files deploy/install/install.sh needs on the host
#   install.sh                              the installer, with CARAVANA_VERSION set
#   SHA256SUMS                              sha256sum lines for the three files above
#
# The release workflow publishes these on a v* tag; deploy/vm/host.sh and
# deploy/dev/install-test.sh build dev releases (0.0.0-dev.<time>) the same way.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The files every tarball carries next to the binary.
host_files=(nomad.hcl.tpl traefik.nomad.hcl caravana-policy.hcl traefik-policy.hcl caravana.service)

usage() {
  echo "usage: tools/release.sh build VERSION OUT_DIR | notes VERSION" >&2
  exit 2
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    shasum -a 256 "$@"
  fi
}

build() {
  local version="$1" out="$2" arch tmp
  # A version is what `sort -V` can order: digits and dots, an optional -suffix.
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] ||
    { echo "release: version $version isn't X.Y.Z or X.Y.Z-suffix" >&2; exit 2; }
  mkdir -p "$out"
  out="$(cd "$out" && pwd)"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  for arch in amd64 arm64; do
    local name="caravana_${version}_linux_${arch}" dir="$tmp/caravana_${version}_linux_${arch}"
    mkdir -p "$dir"
    (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$dir/caravana" ./cmd/caravana)
    for f in "${host_files[@]}"; do cp "$root/deploy/install/$f" "$dir/"; done
    # Owned by root in the tarball: GNU tar (CI, Linux) says so with --owner, macOS's bsdtar
    # with --uid, and without the Mac's extended attributes, which GNU tar warns about.
    if tar --version 2>/dev/null | grep -q GNU; then
      tar --owner=0 --group=0 -C "$tmp" -czf "$out/$name.tar.gz" "$name"
    else
      COPYFILE_DISABLE=1 tar --uid 0 --gid 0 --no-xattrs --no-mac-metadata -C "$tmp" -czf "$out/$name.tar.gz" "$name"
    fi
  done

  sed "s|^CARAVANA_VERSION=.*|CARAVANA_VERSION=$version|" "$root/deploy/install/install.sh" >"$out/install.sh"
  chmod 0755 "$out/install.sh"
  (cd "$out" && sha256 install.sh "caravana_${version}_linux_amd64.tar.gz" "caravana_${version}_linux_arm64.tar.gz" >SHA256SUMS)
  echo "release $version in $out"
}

notes() {
  local version="$1" text
  # From the version's heading (exactly "## X.Y.Z" then " - date" or the end of the line) to
  # the next "## ", blank lines at either end trimmed.
  text="$(awk -v v="$version" '
    /^## / { on = ($2 == v && (NF == 2 || $3 == "-")); next }
    on' "$root/CHANGELOG.md" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}')"
  if [ -z "$text" ]; then
    echo "release: CHANGELOG.md has no section \"## $version - <date>\", or it is empty" >&2
    exit 1
  fi
  printf '%s\n' "$text"
}

case "${1:-}" in
build)
  [ $# -eq 3 ] || usage
  build "$2" "$3"
  ;;
notes)
  [ $# -eq 2 ] || usage
  notes "$2"
  ;;
*) usage ;;
esac
