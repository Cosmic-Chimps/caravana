#!/usr/bin/env bash
# Tests for the release notes commands of tools/release.sh (notes, unreleased, prepare, commit, check).
# Every scenario runs in a fresh temporary tree (mktemp -d) through the script's CHANGELOG and
# CHANGELOG_DIR overrides, so the real CHANGELOG.md and changelog.d/ are never touched. The release-path
# scenarios make a throwaway git repository there, with git's global and system settings left out.
# Exits 1 when any scenario fails. Ported from Frunchy's .github/scripts/changelog-test.sh.
#
#   bash tools/changelog-test.sh
set -uo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# A git that ignores the machine's settings (signing, hooks, default branch) and has an author.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

fails=0
total=0
out=""
code=0

pass() { total=$((total + 1)); echo "ok   $1"; }
fail() { total=$((total + 1)); fails=$((fails + 1)); echo "FAIL $1"; echo "     $2"; }

# A fresh tree: CHANGELOG.md with the intro and two released sections, and an empty changelog.d/
fresh() {
  rm -rf "$work/t"
  mkdir -p "$work/t/changelog.d"
  cat >"$work/t/CHANGELOG.md" <<'EOF'
# Changelog

Intro line.

## 0.2.0 - 2026-01-02

### Fixed
- Old fix.

## 0.1.0 - 2026-01-01

### Added
- First thing.
EOF
  export CHANGELOG="$work/t/CHANGELOG.md" CHANGELOG_DIR="$work/t/changelog.d"
}

# run ARGS...: the script's output (both streams) in $out, its exit code in $code
run() {
  out="$("$script" "$@" 2>&1)"
  code=$?
}

expect_code() { # NAME WANTED
  if [ "$code" -eq "$2" ]; then pass "$1"; else fail "$1" "exit $code, wanted $2; output: $out"; fi
}

expect_has() { # NAME TEXT: $out contains TEXT
  case "$out" in
  *"$2"*) pass "$1" ;;
  *) fail "$1" "output lacks \"$2\"; output: $out" ;;
  esac
}

# A note file: frag NAME, the content on stdin
frag() { cat >"$CHANGELOG_DIR/$1"; }

two_fragments() {
  frag a-first.md <<'EOF'
### Added

- A one.
  continues here.
- A two.

### Fixed

- Fix from a.

### Upgrade

- Upgrade from a.
EOF
  # Caravana's own style, bullets right under the heading, works the same.
  frag b-second.md <<'EOF'
### Changed
- Change from b.

### Added
- B one.

### Fixed
- Fix from b.
EOF
  echo "A README that is never gathered." >"$CHANGELOG_DIR/README.md"
}

expected="### Added
- A one.
  continues here.
- A two.
- B one.

### Changed
- Change from b.

### Fixed
- Fix from a.
- Fix from b.

### Upgrade
- Upgrade from a."

# ---- prepare with two notes that share kinds
fresh
two_fragments
run prepare 0.3.0
expect_code "prepare with two notes succeeds" 0
if [ ! -e "$CHANGELOG_DIR/a-first.md" ] && [ ! -e "$CHANGELOG_DIR/b-second.md" ] && [ -f "$CHANGELOG_DIR/README.md" ]; then
  pass "prepare deletes the notes and keeps the README"
else
  fail "prepare deletes the notes and keeps the README" "left: $(ls "$CHANGELOG_DIR")"
fi
run notes 0.3.0
expect_code "notes reads the prepared version" 0
if [ "$out" = "$expected" ]; then
  pass "kinds in order, bullets in file-name order"
else
  fail "kinds in order, bullets in file-name order" "got: $out"
fi
head_lines="$(sed -n 1,7p "$CHANGELOG")"
case "$head_lines" in
"# Changelog

Intro line.

## 0.3.0 - $(date -u +%Y-%m-%d)

### Added") pass "the new section sits right after the intro, dated today (UTC)" ;;
*) fail "the new section sits right after the intro, dated today (UTC)" "top: $head_lines" ;;
esac
if grep -q '^0.3.0 0.2.0 0.1.0 $' < <(grep '^## ' "$CHANGELOG" | awk '{ print $2 }' | tr '\n' ' '); then
  pass "the new section is above the released ones"
else
  fail "the new section is above the released ones" "$(grep '^## ' "$CHANGELOG")"
fi
if grep -q -B1 '^## 0.2.0' "$CHANGELOG" && [ "$(grep -B1 '^## 0.2.0' "$CHANGELOG" | head -1)" = "" ]; then
  pass "a blank line separates the new section from the next one"
else
  fail "a blank line separates the new section from the next one" "$(cat "$CHANGELOG")"
fi
if grep -q 'First thing.' < <(sed -n '/^## 0.2.0/,$p' "$CHANGELOG") && grep -q '^- Old fix.$' "$CHANGELOG"; then
  pass "released sections are untouched"
else
  fail "released sections are untouched" "released text is missing"
fi
run prepare 0.3.0
expect_code "prepare refuses a version that already has a section" 1
fresh
two_fragments
run prepare 0.2.0
expect_code "prepare refuses an existing released version too" 1
run check
expect_code "check passes after a refused prepare (nothing was deleted)" 0
if [ -f "$CHANGELOG_DIR/a-first.md" ]; then pass "a refused prepare keeps the notes"; else fail "a refused prepare keeps the notes" "a-first.md is gone"; fi
run prepare not-a-version
expect_code "prepare refuses a bad version" 2

# ---- prepare with none
fresh
before="$(cat "$CHANGELOG")"
run prepare 0.3.0
expect_code "prepare with no notes is refused" 1
expect_has "the refusal says there are no notes in changelog.d" "no notes in changelog.d"
if [ "$(cat "$CHANGELOG")" = "$before" ]; then pass "a refused prepare leaves CHANGELOG.md as it was"; else fail "a refused prepare leaves CHANGELOG.md as it was" "changed"; fi
echo "only a readme" >"$CHANGELOG_DIR/README.md"
run prepare 0.3.0
expect_code "a README alone is no notes" 1
rm -rf "$CHANGELOG_DIR"
run prepare 0.3.0
expect_code "a missing changelog.d is no notes" 1

# ---- prepare refuses a broken note
fresh
frag bad.md <<'EOF'
### Added
- Fine.

### Misc
- Not a kind.
EOF
before="$(cat "$CHANGELOG")"
run prepare 0.3.0
expect_code "prepare refuses a note that check would fail" 1
if [ "$(cat "$CHANGELOG")" = "$before" ] && [ -f "$CHANGELOG_DIR/bad.md" ]; then pass "and changes nothing"; else fail "and changes nothing" "files changed"; fi

# ---- unreleased
fresh
run unreleased
expect_code "unreleased with no notes succeeds" 0
if [ -z "$out" ]; then pass "unreleased prints nothing with no notes"; else fail "unreleased prints nothing with no notes" "output: $out"; fi
two_fragments
run unreleased
expect_code "unreleased with notes succeeds" 0
if [ "$out" = "$expected" ]; then pass "unreleased prints what prepare would write"; else fail "unreleased prints what prepare would write" "got: $out"; fi
if [ -f "$CHANGELOG_DIR/a-first.md" ]; then pass "unreleased deletes nothing"; else fail "unreleased deletes nothing" "notes gone"; fi

# ---- notes
fresh
two_fragments
run notes 0.3.0
expect_code "notes on a version with no section fails" 1
expect_has "and points to prepare while notes wait in changelog.d" "tools/release.sh prepare 0.3.0"
run notes 0.2.0
expect_code "notes on a released version works" 0
if [ "$out" = "### Fixed
- Old fix." ]; then pass "notes prints the section without its heading"; else fail "notes prints the section without its heading" "got: $out"; fi

# ---- check passes
fresh
run check
expect_code "check passes with no notes" 0
two_fragments
run check
expect_code "check passes with good notes and a README" 0
printf '### Added\n- Fine.\n' >"$CHANGELOG_DIR/019.5-create-without-deploy.md"
run check
expect_code "check takes a feature number with a dot in the name" 0

# ---- check fails
fresh
frag bad-kind.md <<'EOF'
### Added
- Fine.

### Misc
- Not a kind.
EOF
run check
expect_code "check fails on a heading that is not one of the five kinds" 1
expect_has "and names the file and the line" "changelog.d/bad-kind.md:4"

fresh
frag stray.md <<'EOF'
Some words first.

### Added
- Fine.
EOF
run check
expect_code "check fails on text outside a subsection" 1
expect_has "and names the file and the line (outside)" "changelog.d/stray.md:1"

fresh
frag stray2.md <<'EOF'
### Added
- Fine.
A line that is not a bullet.
EOF
run check
expect_code "check fails on text that is not a bullet" 1
expect_has "and names the file and the line (not a bullet)" "changelog.d/stray2.md:3"

fresh
frag title.md <<'EOF'
# A title

### Added
- Fine.
EOF
run check
expect_code "check fails on a title heading" 1
expect_has "and names the file and the line (title)" "changelog.d/title.md:1"

fresh
frag empty-section.md <<'EOF'
### Added

### Fixed
- Fine.
EOF
run check
expect_code "check fails on a subsection with no bullets" 1
expect_has "and names the line of the subsection" "changelog.d/empty-section.md:1"

fresh
: >"$CHANGELOG_DIR/empty.md"
run check
expect_code "check fails on an empty file" 1
expect_has "and names the file" "changelog.d/empty.md: empty file"

fresh
printf '\n\n' >"$CHANGELOG_DIR/blank.md"
run check
expect_code "check fails on a file of only blank lines" 1

fresh
printf '### Added\n- Fine.\n' >"$CHANGELOG_DIR/Bad_Name.md"
run check
expect_code "check fails on a bad name (capitals and an underscore)" 1
expect_has "and names the file (bad name)" "changelog.d/Bad_Name.md: bad name"

fresh
printf '### Added\n- Fine.\n' >"$CHANGELOG_DIR/notes.txt"
run check
expect_code "check fails on a file that is not .md" 1
expect_has "and names the file (not .md)" "changelog.d/notes.txt: bad name"

fresh
printf '### Added\n- Fine.\n' >"$CHANGELOG_DIR/.hidden.md"
run check
expect_code "check fails on a hidden file" 1

# ---- check fails on a stale "## Unreleased" (a branch started before the switch, merged after it)
unreleased_message='CHANGELOG.md has a "## Unreleased" section; notes now live in changelog.d/. Move its bullets into a new changelog.d/<name>.md (one "### Added/Changed/Fixed/Security/Upgrade" subsection per kind) and delete the section.'

fresh
cat >"$CHANGELOG" <<'EOF'
# Changelog

Intro line.

## Unreleased

### Added
- A bullet from an old branch.

## 0.2.0 - 2026-01-02

### Fixed
- Old fix.
EOF
run check
expect_code "check fails on a ## Unreleased section with bullets" 1
expect_has "and says what to do" "release: $unreleased_message"

fresh
cat >"$CHANGELOG" <<'EOF'
# Changelog

Intro line.

## Unreleased

## 0.2.0 - 2026-01-02

### Fixed
- Old fix.
EOF
run check
expect_code "check fails on a ## Unreleased heading alone" 1
expect_has "and says what to do (heading alone)" "release: $unreleased_message"

fresh
printf '\n## Unreleased\n' >>"$CHANGELOG"
run check
expect_code "check fails on a ## Unreleased heading that is the last line" 1

fresh
two_fragments
run prepare 0.3.0
expect_code "prepare works on a tree that has no Unreleased section" 0
run check
expect_code "check passes after prepare" 0

fresh
echo "- stray" >>"$CHANGELOG"
printf '### Added\n- Fine.\n' >"$CHANGELOG_DIR/ok.md"
run prepare 0.3.0
expect_code "prepare works when CHANGELOG.md has trailing text" 0

fresh
cat >"$CHANGELOG" <<'EOF'
# Changelog

Intro line.
EOF
printf '### Added\n- The very first.\n' >"$CHANGELOG_DIR/ok.md"
run prepare 0.1.0
expect_code "prepare works on a changelog with no released section yet" 0
run notes 0.1.0
if [ "$out" = "### Added
- The very first." ]; then pass "and its notes read back"; else fail "and its notes read back" "got: $out"; fi

# ---- the release paths, in a git repository (.github/workflows/release.yml)
# repo: a fresh tree committed on main, with the two notes and the README tracked.
repo() {
  fresh
  two_fragments
  git -C "$work/t" init -q -b main
  git -C "$work/t" add -A
  git -C "$work/t" commit -q -m "start"
}

# A dispatched run: prepare on main's tip, commit, tag. The commit holds the deleted notes.
repo
run prepare 0.3.0
expect_code "dispatch: prepare in the checkout" 0
run commit 0.3.0
expect_code "dispatch: commit succeeds" 0
expect_has "dispatch: commit says what it committed" "chore(release): 0.3.0"
if [ -z "$(git -C "$work/t" status --porcelain)" ]; then pass "dispatch: nothing is left uncommitted"; else fail "dispatch: nothing is left uncommitted" "$(git -C "$work/t" status --porcelain)"; fi
if [ "$(git -C "$work/t" ls-files changelog.d)" = "changelog.d/README.md" ]; then
  pass "dispatch: the release commit deletes the notes and keeps the README"
else
  fail "dispatch: the release commit deletes the notes and keeps the README" "tracked: $(git -C "$work/t" ls-files changelog.d)"
fi
if [ "$(git -C "$work/t" log -1 --format=%s)" = "chore(release): 0.3.0" ]; then pass "dispatch: the commit message names the version"; else fail "dispatch: the commit message names the version" "$(git -C "$work/t" log -1 --format=%s)"; fi
git -C "$work/t" tag v0.3.0
run prepare 0.4.0
expect_code "dispatch: the next release doesn't gather the same notes again" 1
expect_has "dispatch: and says there is nothing to release" "no notes in changelog.d"
run commit 0.3.0
expect_code "commit with nothing new succeeds" 0
expect_has "commit with nothing new says so" "nothing new to commit"
if [ "$(git -C "$work/t" rev-list --count HEAD)" = 2 ]; then pass "commit with nothing new makes no commit"; else fail "commit with nothing new makes no commit" "$(git -C "$work/t" log --oneline)"; fi

# A pushed tag (fix 4d3ce72): the tagged commit has notes but no section. The run prepares in its own
# checkout of the tag, publishes, then records the release on main while main is still the tag.
repo
git -C "$work/t" tag v0.3.0
run notes 0.3.0
expect_code "tag push: the tagged commit has no section yet" 1
run prepare 0.3.0
expect_code "tag push: prepare in the tag's checkout" 0
run notes 0.3.0
if [ "$out" = "$expected" ]; then pass "tag push: the release's notes are the tag's notes"; else fail "tag push: the release's notes are the tag's notes" "got: $out"; fi
if [ -n "$(git -C "$work/t" status --porcelain -- CHANGELOG.md changelog.d)" ]; then
  pass "tag push: the record step sees a prepared changelog to record"
else
  fail "tag push: the record step sees a prepared changelog to record" "status is clean"
fi
if [ "$(git -C "$work/t" rev-parse main)" = "$(git -C "$work/t" rev-parse HEAD)" ]; then pass "tag push: main is still the tagged commit"; else fail "tag push: main is still the tagged commit" "main moved"; fi
run commit 0.3.0
expect_code "tag push: commit on main succeeds" 0
if [ "$(git -C "$work/t" ls-files changelog.d)" = "changelog.d/README.md" ]; then
  pass "tag push: main's commit deletes the released notes"
else
  fail "tag push: main's commit deletes the released notes" "tracked: $(git -C "$work/t" ls-files changelog.d)"
fi
if grep -q '^## 0.3.0 - ' < <(git -C "$work/t" show HEAD:CHANGELOG.md); then pass "tag push: main's changelog has the section"; else fail "tag push: main's changelog has the section" "no section on main"; fi
if [ "$(git -C "$work/t" rev-parse v0.3.0)" = "$(git -C "$work/t" rev-parse HEAD~1)" ]; then pass "tag push: the tag never moves"; else fail "tag push: the tag never moves" "tag moved"; fi
printf '### Fixed\n- A later fix.\n' >"$CHANGELOG_DIR/later.md"
run unreleased
if [ "$out" = "### Fixed
- A later fix." ]; then pass "tag push: the next release has only newer notes"; else fail "tag push: the next release has only newer notes" "got: $out"; fi

# A pushed tag whose commit already holds its section (prepared and committed by hand): nothing to
# prepare, nothing to record.
repo
run prepare 0.3.0
run commit 0.3.0
git -C "$work/t" tag v0.3.0
run notes 0.3.0
expect_code "tag push, prepared by hand: notes work from the tag" 0
if [ -z "$(git -C "$work/t" status --porcelain -- CHANGELOG.md changelog.d)" ]; then
  pass "tag push, prepared by hand: nothing to record on main"
else
  fail "tag push, prepared by hand: nothing to record on main" "$(git -C "$work/t" status --porcelain)"
fi

echo
if [ "$fails" -eq 0 ]; then
  echo "changelog-test: all $total checks passed"
else
  echo "changelog-test: $fails of $total checks failed" >&2
  exit 1
fi
