#!/usr/bin/env bash
# Builds a Caravana release: what the installer downloads (feature 012).
#
#   tools/release.sh build VERSION OUT_DIR
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
  echo "usage: tools/release.sh build VERSION OUT_DIR" >&2
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

case "${1:-}" in
build)
  [ $# -eq 3 ] || usage
  build "$2" "$3"
  ;;
*) usage ;;
esac
