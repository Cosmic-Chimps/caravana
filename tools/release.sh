#!/usr/bin/env bash
# Builds a Caravana release (feature 012) and keeps its notes: CHANGELOG.md holds the released
# sections, changelog.d/ the notes of the next release, one file per change.
#
#   tools/release.sh build VERSION OUT_DIR   the files the installer downloads (below)
#   tools/release.sh notes VERSION           print VERSION's section of CHANGELOG.md, without its heading
#   tools/release.sh unreleased              print what the next release would say, gathered from
#                                            changelog.d/ (may print nothing)
#   tools/release.sh prepare VERSION         gather changelog.d/*.md into "## VERSION - <today, UTC>" in
#                                            CHANGELOG.md and delete the files
#   tools/release.sh commit VERSION          stage CHANGELOG.md and changelog.d/ (deletions included) and
#                                            commit them as "chore(release): VERSION", if prepare changed them
#   tools/release.sh check                   fail on a bad file in changelog.d/ or a "## Unreleased" section
#
#   CHANGELOG       path of the changelog (default: CHANGELOG.md at the repository root)
#   CHANGELOG_DIR   the notes directory (default: changelog.d at the repository root)
#
# Notes are not written in CHANGELOG.md. Each change adds one file in changelog.d/ (its README.md says
# how; the README is never gathered), so two branches never edit the same lines, and a branch merged or
# rebased after a release can't put its notes inside the released section, which a shared
# "## Unreleased" heading allowed without a conflict. A file holds "### Added", "### Changed",
# "### Fixed", "### Security" and "### Upgrade" subsections of "- " bullets (continuation lines
# indented).
#
# notes prints the lines under "## VERSION" or "## VERSION - date" up to the next "## ", blank lines
# trimmed at both ends. The release workflow publishes that as the GitHub release's notes. It fails
# when the section is missing or empty, so no release goes out with nothing to say.
#
# prepare writes one "## VERSION - <date>" section above the newest released one: one subsection per
# kind (Added, Changed, Fixed, Security, Upgrade, in that order), each kind's bullets in the files' name
# order and then their order in the file. It deletes the files it gathered. It refuses when changelog.d/
# has no notes, when check fails, and when VERSION already has a section, and then changes nothing.
#
# commit is what both release paths of .github/workflows/release.yml run after prepare (a dispatched run
# before it tags, a pushed tag when it records the release on main): it stages with "git add -A" so the
# deleted notes are committed too, or the next release would gather them again. It commits whatever is
# staged, so run it on a clean checkout.
#
# check names the file and line of a note with a bad name, an empty file, a heading that isn't one of
# the five kinds, an empty subsection, or text that isn't a bullet. It also fails when CHANGELOG.md has a
# "## Unreleased" section: a branch started before this rule brings it back when merged.
#
# build writes into OUT_DIR:
#   caravana_<VERSION>_linux_amd64.tar.gz   the binary (built with that version) and
#   caravana_<VERSION>_linux_arm64.tar.gz   the files deploy/install/install.sh needs on the host,
#                                           the root updater and the units of the server agent among them
#   caravana_<VERSION>_darwin_amd64.tar.gz  the binary alone, for a Mac where an agent runs
#   caravana_<VERSION>_darwin_arm64.tar.gz  `caravana mcp` (docs/mcp.md)
#   install.sh                              the installer, with CARAVANA_VERSION set
#   SHA256SUMS                              sha256sum lines for the five files above
#
# The release workflow publishes these on a v* tag; deploy/vm/host.sh and
# deploy/dev/install-test.sh build dev releases (0.0.0-dev.<time>) the same way.
# tools/changelog-test.sh tests notes, unreleased, prepare, commit and check.
set -euo pipefail

# Files come in name order, the same on every system.
export LC_COLLATE=C

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
changelog="${CHANGELOG:-$root/CHANGELOG.md}"
fragdir="${CHANGELOG_DIR:-$root/changelog.d}"

kinds="Added Changed Fixed Security Upgrade"

# The files every tarball carries next to the binary.
host_files=(nomad.service caravana-mesh.service caravana-node.service caravana-node.timer
  traefik.nomad.hcl caravana-policy.hcl traefik-policy.hcl caravana.service
  caravana-update.sh caravana-update.service caravana-update.timer)

usage() {
  echo "usage: tools/release.sh build VERSION OUT_DIR | notes VERSION | unreleased | prepare VERSION | commit VERSION | check" >&2
  exit 2
}

# A version is what `sort -V` can order: digits and dots, an optional -suffix.
check_version() {
  [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] ||
    { echo "release: version $1 isn't X.Y.Z or X.Y.Z-suffix" >&2; exit 2; }
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
  check_version "$version"
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

  # The binary alone for a Mac, where an agent runs `caravana mcp` (feature 026.5). No host
  # files: a Mac is never a host.
  for arch in amd64 arm64; do
    local name="caravana_${version}_darwin_${arch}" dir="$tmp/caravana_${version}_darwin_${arch}"
    mkdir -p "$dir"
    (cd "$root" && CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$dir/caravana" ./cmd/caravana)
    if tar --version 2>/dev/null | grep -q GNU; then
      tar --owner=0 --group=0 -C "$tmp" -czf "$out/$name.tar.gz" "$name"
    else
      COPYFILE_DISABLE=1 tar --uid 0 --gid 0 --no-xattrs --no-mac-metadata -C "$tmp" -czf "$out/$name.tar.gz" "$name"
    fi
  done

  sed "s|^CARAVANA_VERSION=.*|CARAVANA_VERSION=$version|" "$root/deploy/install/install.sh" >"$out/install.sh"
  chmod 0755 "$out/install.sh"
  (cd "$out" && sha256 install.sh "caravana_${version}_linux_amd64.tar.gz" "caravana_${version}_linux_arm64.tar.gz" \
    "caravana_${version}_darwin_amd64.tar.gz" "caravana_${version}_darwin_arm64.tar.gz" >SHA256SUMS)
  echo "release $version in $out"
}

# The lines under the heading "## NAME" (exactly that, then the end of the line or " - date"),
# up to the next "## ", blank lines at either end trimmed.
section() {
  awk -v v="$1" '
    /^## / { on = ($2 == v && (NF == 2 || $3 == "-")); next }
    on' "$changelog" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}'
}

# The note files, one path per line, in name order; README.md is never one of them.
fragment_files() {
  local f
  for f in "$fragdir"/*.md; do
    [ -f "$f" ] || continue
    [ "${f##*/}" = "README.md" ] || printf '%s\n' "$f"
  done
}

# What the notes say, one "### Kind" subsection per kind that has bullets, the bullets right under the
# heading as in CHANGELOG.md's released sections. Blank lines are dropped, so the bullets of every file
# run together.
gather() {
  local kind chunk first=1
  for kind in $kinds; do
    chunk="$(fragment_files | while IFS= read -r f; do
      awk -v k="$kind" '
        /^###[[:space:]]/ { on = ($2 == k && NF == 2); next }
        /^#/ { on = 0; next }
        on && /[^[:space:]]/ { print }' "$f"
    done)"
    [ -n "$chunk" ] || continue
    if [ "$first" -eq 0 ]; then echo; fi
    first=0
    printf '### %s\n%s\n' "$kind" "$chunk"
  done
}

notes() {
  local version="$1" text
  check_version "$version"
  text="$(section "$version")"
  if [ -z "$text" ]; then
    echo "release: CHANGELOG.md has no section \"## $version - <date>\", or it is empty" >&2
    # The usual cause: the notes are still in changelog.d/. Say how to fix it.
    if [ -n "$(fragment_files)" ]; then
      echo "release: run \"tools/release.sh prepare $version\", commit the result, and tag that commit" >&2
    fi
    exit 1
  fi
  printf '%s\n' "$text"
}

unreleased() {
  gather
}

# Problems with the notes and with CHANGELOG.md, one per line as "release: file:line: what" (or
# "release: file: what"), on stdout. Prints nothing when all is well.
problems() {
  local f name dir
  dir="${fragdir##*/}"
  if [ -d "$fragdir" ]; then
    for f in "$fragdir"/* "$fragdir"/.[!.]*; do
      [ -e "$f" ] || continue
      name="${f##*/}"
      [ "$name" != "README.md" ] || continue
      if ! [[ "$name" =~ ^[a-z0-9.-]+\.md$ ]] || [[ "$name" == .* ]] || [ ! -f "$f" ]; then
        echo "release: $dir/$name: bad name; use lowercase letters, digits, dots and dashes, then .md (a file, not a folder)"
        continue
      fi
      awk -v p="release: $dir/$name" '
        function bad(msg) { printf "%s:%d: %s\n", p, NR, msg }
        function endsub() { if (cur != "" && !bullets) printf "%s:%d: subsection \"%s\" has no bullets\n", p, subline, cur }
        /[^[:space:]]/ { text = 1 }
        /^#/ {
          endsub()
          if ($0 ~ /^### (Added|Changed|Fixed|Security|Upgrade)[[:space:]]*$/) { cur = $2; subline = NR; bullets = 0 }
          else { bad("heading \"" $0 "\" is not one of ### Added, ### Changed, ### Fixed, ### Security, ### Upgrade"); cur = "x"; bullets = 1 }
          next
        }
        !/[^[:space:]]/ { next }
        cur == "" { bad("text outside a subsection (start with ### Added, ### Changed, ### Fixed, ### Security or ### Upgrade)"); next }
        /^- / { bullets = 1; next }
        /^[[:space:]]/ && bullets { next }
        { bad("text that is not a \"- \" bullet or an indented continuation line"); bullets = 1 }
        END {
          endsub()
          if (!text) printf "%s: empty file\n", p
        }' "$f"
    done
  fi
  if [ -f "$changelog" ]; then
    if awk '/^##[[:space:]]+Unreleased/ { found = 1 } END { exit !found }' "$changelog"; then
      echo "release: CHANGELOG.md has a \"## Unreleased\" section; notes now live in changelog.d/. Move its bullets into a new changelog.d/<name>.md (one \"### Added/Changed/Fixed/Security/Upgrade\" subsection per kind) and delete the section."
    fi
  fi
  return 0
}

check() {
  local out
  out="$(problems)"
  if [ -n "$out" ]; then
    printf '%s\n' "$out" >&2
    exit 1
  fi
  echo "release: changelog.d/ and CHANGELOG.md are in order"
}

prepare() {
  local version="$1" date tmp body out
  check_version "$version"
  if awk -v v="$version" '/^## / && $2 == v { found = 1 } END { exit !found }' "$changelog"; then
    echo "release: CHANGELOG.md already has a section for $version" >&2
    exit 1
  fi
  if [ -z "$(fragment_files)" ]; then
    echo "release: no notes in changelog.d ($fragdir): nothing to release" >&2
    exit 1
  fi
  out="$(problems)"
  if [ -n "$out" ]; then
    printf '%s\n' "$out" >&2
    echo "release: fix these before releasing" >&2
    exit 1
  fi
  date="$(date -u +%Y-%m-%d)"
  tmp="$(mktemp)"
  body="$(mktemp)"
  gather >"$body"
  if ! [ -s "$body" ]; then
    rm -f "$tmp" "$body"
    echo "release: no notes in changelog.d ($fragdir): nothing to release" >&2
    exit 1
  fi
  # Above the newest released section; with none yet, at the end.
  awk -v v="$version" -v d="$date" -v bodyfile="$body" '
    function put(   line) {
      print "## " v " - " d
      print ""
      while ((getline line < bodyfile) > 0) print line
      close(bodyfile)
    }
    !done && /^## / { put(); print ""; done = 1 }
    { print }
    END { if (!done) { print ""; put() } }' "$changelog" >"$tmp"
  cat "$tmp" >"$changelog"
  fragment_files | while IFS= read -r f; do rm -f "$f"; done
  rm -f "$tmp" "$body"
  echo "CHANGELOG.md: changelog.d/ is now \"## $version - $date\"; commit it (tools/release.sh commit $version), then tag v$version"
}

commit_release() {
  local version="$1"
  check_version "$version"
  # Each path from its own folder, so the repository is the one that holds it. -A stages the
  # deleted notes as well as the rewritten changelog.
  (cd "$(dirname "$changelog")" && git add -A -- "${changelog##*/}")
  (cd "$(dirname "$fragdir")" && git add -A -- "${fragdir##*/}")
  # Ask the index, so a deleted file counts as a change.
  if (cd "$(dirname "$changelog")" && git diff --cached --quiet); then
    echo "release: CHANGELOG.md and changelog.d/ have nothing new to commit"
    return 0
  fi
  (cd "$(dirname "$changelog")" && git commit -q -m "chore(release): $version")
  echo "release: committed chore(release): $version"
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
unreleased)
  [ $# -eq 1 ] || usage
  unreleased
  ;;
prepare)
  [ $# -eq 2 ] || usage
  prepare "$2"
  ;;
commit)
  [ $# -eq 2 ] || usage
  commit_release "$2"
  ;;
check)
  [ $# -eq 1 ] || usage
  check
  ;;
*) usage ;;
esac
