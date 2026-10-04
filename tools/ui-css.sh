#!/usr/bin/env bash
# Builds internal/ui/static/app.css from internal/ui/style/ with the Tailwind
# CSS v4 standalone CLI and the daisyUI 5 plugin, both pinned by version and
# SHA-256 in tools/ui-css.versions. No Node.
#
#   tools/ui-css.sh build   write internal/ui/static/app.css
#   tools/ui-css.sh check   build to a temp file and fail if it differs from the
#                           committed app.css (what CI runs)
#
# `go build` never runs this: app.css is committed.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "$root/tools/ui-css.versions"

style="$root/internal/ui/style"
templates="$root/internal/ui/templates"
committed="$root/internal/ui/static/app.css"
cache="$root/.cache/ui-css/tailwind-$TAILWIND_VERSION-daisyui-$DAISYUI_VERSION"

usage() {
  echo "usage: tools/ui-css.sh build|check" >&2
  exit 2
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# fetch URL DEST SHA256: download unless DEST is already there, then refuse
# anything whose checksum differs from the pinned one.
fetch() {
  local url="$1" dest="$2" want="$3" got
  mkdir -p "$(dirname "$dest")"
  if [[ ! -f "$dest" ]]; then
    echo "downloading $url" >&2
    curl --fail --silent --show-error --location --retry 3 --output "$dest.part" "$url"
    mv "$dest.part" "$dest"
  fi
  got="$(sha256 "$dest")"
  if [[ "$got" != "$want" ]]; then
    rm -f "$dest"
    echo "checksum mismatch for $url" >&2
    echo "  want $want" >&2
    echo "  got  $got" >&2
    exit 1
  fi
}

# The Tailwind binary for this machine.
tailwind_binary() {
  local asset want
  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64) asset=macos-arm64 want="$TAILWIND_SHA256_DARWIN_ARM64" ;;
    Linux-x86_64) asset=linux-x64 want="$TAILWIND_SHA256_LINUX_X64" ;;
    Linux-aarch64 | Linux-arm64) asset=linux-arm64 want="$TAILWIND_SHA256_LINUX_ARM64" ;;
    *)
      echo "no pinned Tailwind binary for $(uname -s) $(uname -m); add one to tools/ui-css.versions" >&2
      exit 1
      ;;
  esac
  local bin="$cache/tailwindcss-$asset"
  fetch "https://github.com/tailwindlabs/tailwindcss/releases/download/$TAILWIND_VERSION/tailwindcss-$asset" "$bin" "$want"
  chmod +x "$bin"
  echo "$bin"
}

# render OUT: build the stylesheet into OUT.
render() {
  local out="$1" bin work
  bin="$(tailwind_binary)"
  fetch "https://github.com/saadeghi/daisyui/releases/download/$DAISYUI_VERSION/daisyui.mjs" \
    "$cache/daisyui.mjs" "$DAISYUI_SHA256_PLUGIN"
  fetch "https://github.com/saadeghi/daisyui/releases/download/$DAISYUI_VERSION/daisyui-theme.mjs" \
    "$cache/daisyui-theme.mjs" "$DAISYUI_SHA256_THEME"

  # The CLI wants the plugin files next to the stylesheet, and the sources it
  # scans relative to it. Build in a scratch copy with that layout, so nothing
  # downloaded ever lands in the repository.
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' RETURN
  mkdir -p "$work/style" "$work/templates"
  cp "$style"/*.css "$work/style/"
  cp "$cache/daisyui.mjs" "$cache/daisyui-theme.mjs" "$work/style/"
  if compgen -G "$templates/*.html" >/dev/null; then
    cp "$templates"/*.html "$work/templates/"
  fi

  (cd "$work/style" && "$bin" --input input.css --output "$out" --minify >/dev/null)
}

case "${1:-}" in
  build)
    mkdir -p "$(dirname "$committed")"
    render "$committed"
    echo "wrote ${committed#"$root"/}"
    ;;
  check)
    tmp="$(mktemp)"
    trap 'rm -f "$tmp"' EXIT
    render "$tmp"
    if ! diff -q "$tmp" "$committed" >/dev/null 2>&1; then
      echo "internal/ui/static/app.css is out of date: run tools/ui-css.sh build and commit the result" >&2
      exit 1
    fi
    echo "internal/ui/static/app.css is up to date"
    ;;
  *) usage ;;
esac
