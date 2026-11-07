#!/usr/bin/env bash
# Caravana's updater (feature 030). The installer puts it at /usr/local/libexec/caravana-update;
# caravana-update.timer runs it as root every minute. When the console has asked for an update it
# checks the request, downloads that release's installer and tarball, checks both against the
# release's SHA256SUMS, and runs the installer, which upgrades with its own rollback (012).
#
# The request is a file that Caravana wrote. Caravana is not root and may be compromised, so the
# file is treated as hostile text:
#   - it must be a plain file of at most 256 bytes, never a symlink;
#   - it is read as text and never sourced; only two lines are taken from it, and each value must
#     match a strict pattern before anything uses it;
#   - its bytes are never printed or written anywhere else;
#   - root never writes inside /var/lib/caravana, which Caravana owns. The status and the logs go
#     to /var/lib/caravana-update, which only root can write.
# The contract with Caravana is specs/030-update-from-console/contracts/updater.md.
#
# The installer rewrites this file while it runs. Bash reads a script as it goes, so everything
# is defined before the last line, and that line is the only command left to read.
set -euo pipefail
export LC_ALL=C
umask 022
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

REQUEST=/var/lib/caravana/update-request
DIR=/var/lib/caravana-update
STATUS="$DIR/status"
OPT=/opt/caravana
LOCK=/run/caravana-update.lock
OVERRIDE=/etc/caravana/update.env # only the installer's own test writes it
OVERRIDE_UID=0                    # who must own it
RELEASES=https://github.com/Cosmic-Chimps/caravana/releases/download

version_re='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
id_re='^[0-9a-f]{32}$'
base_re='^(https?|file)://[A-Za-z0-9._:/-]+$'

# This run's facts. Only validated values are ever put in them.
id="" version="" from="" started="" work=""

log() { echo "caravana-update: $*" >&2; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
printable() { LC_ALL=C tr -cd '[:print:]'; }
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() { [ -z "$work" ] || rm -rf -- "$work"; }

# newer A B: true when version A sorts after version B (the installer's own check).
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$1" ]; }

# installed_version prints the version "current" points at, or nothing.
installed_version() {
  local target
  target="$(readlink "$OPT/current" 2>/dev/null)" || return 0
  printf '%s' "${target##*/}"
}

# status_value KEY: the first KEY=value line of the status file, or nothing.
status_value() { sed -n "/^$1=/{s/^$1=//p;q}" "$STATUS" 2>/dev/null || true; }

# write_status STATE [REASON]: replace the status file in one step with this run's facts. The
# reason is cut to one printable line of at most 300 bytes.
write_status() {
  local state="$1" reason="${2:-}" finished=""
  [ "$state" = running ] || finished="$(now)"
  reason="$(printf '%s' "$reason" | printable)"
  reason="${reason:0:300}"
  {
    echo "ID=$id"
    echo "VERSION=$version"
    echo "FROM=$from"
    echo "STATE=$state"
    echo "STARTED=$started"
    echo "FINISHED=$finished"
    echo "REASON=$reason"
  } >"$STATUS.tmp"
  mv -f -- "$STATUS.tmp" "$STATUS"
}

# refuse_run REASON: end the run as refused, with nothing installed.
refuse_run() {
  write_status refused "$1"
  log "refused: $1"
}

# reason_of LOG: the installer's last "caravana install: " line, without the prefix, as one
# printable line. Never more of the log.
reason_of() {
  local line
  line="$(grep -a '^caravana install: ' "$1" 2>/dev/null | tail -n 1)" || true
  printf '%s' "${line#caravana install: }" | printable
}

# release_base prints where the release files are downloaded from: GitHub, or the root-owned test
# file's address. It prints nothing and fails when that file's value can't be used.
release_base() {
  local info value
  info="$(stat -c '%u %a %F' -- "$OVERRIDE" 2>/dev/null)" || {
    printf '%s' "$RELEASES"
    return 0
  }
  # Owned by root, a regular file (stat doesn't follow a symlink), not writable by group or others.
  if [ "${info%% *}" = "$OVERRIDE_UID" ] && [[ "$info" == *"regular"* ]] && [ "$((8#$(cut -d' ' -f2 <<<"$info") & 022))" = 0 ]; then
    value="$(sed -n '/^CARAVANA_UPDATE_RELEASES=/{s/^CARAVANA_UPDATE_RELEASES=//p;q}' "$OVERRIDE" 2>/dev/null)" || true
    [[ "$value" =~ $base_re ]] || return 1
    printf '%s' "${value%/}"
    return 0
  fi
  # Not the root-owned file it must be: ignored.
  printf '%s' "$RELEASES"
}

main() {
  trap cleanup EXIT

  # 1. One run at a time. The installer inherits no descriptor of the lock (see step 10).
  exec 9>"$LOCK"
  flock -n 9 || return 0

  # 2. No request: nothing to do, and nothing logged, since this runs every minute.
  [ -e "$REQUEST" ] || [ -L "$REQUEST" ] || return 0

  # 3. Only a plain file of at most 256 bytes.
  local size content version_in
  if [ -L "$REQUEST" ] || [ ! -f "$REQUEST" ]; then
    log "the update request isn't a plain file; ignored"
    return 0
  fi
  size="$(stat -c %s -- "$REQUEST" 2>/dev/null)" || return 0
  if [ "$size" -gt 256 ]; then
    log "the update request isn't a plain file; ignored"
    return 0
  fi

  # 4. Read the two values as text. The file may have changed since the checks above, so the read
  # is bounded in bytes and in time; what it returns is only ever matched against patterns.
  content="$(timeout 5 head -c 256 -- "$REQUEST" 2>/dev/null)" || return 0
  version_in="$(sed -n '/^VERSION=/{s/^VERSION=//p;q}' <<<"$content")"
  id="$(sed -n '/^ID=/{s/^ID=//p;q}' <<<"$content")"
  content=""
  if ! [[ "$id" =~ $id_re ]]; then
    id=""
    log "the update request has no valid ID; ignored"
    return 0
  fi

  # 5. This request already has an outcome: Caravana just hasn't removed the file yet.
  if [ "$(status_value ID)" = "$id" ]; then
    case "$(status_value STATE)" in updated | rolled_back | failed | refused) return 0 ;; esac
  fi

  if [ ! -d "$DIR" ]; then
    log "$DIR is missing; run the installer again"
    return 0
  fi

  # 6. From here every refusal is written to the status file.
  local current
  current="$(installed_version)"
  started="$(now)"
  [[ "$current" =~ $version_re ]] && from="$current"
  if ! [[ "$version_in" =~ $version_re ]]; then
    refuse_run "The request's version isn't a release version."
    return 0
  fi
  version="$version_in"
  if [ -z "$from" ]; then
    refuse_run "The updater can't tell which caravana runs here (/opt/caravana/current)."
    return 0
  fi
  if ! newer "$version" "$from"; then
    refuse_run "caravana $from runs here, and $version isn't newer."
    return 0
  fi

  # 7. Say it started.
  write_status running ""
  log "updating caravana $from to $version"

  # 8. Download the release's files into a directory of their own.
  local base arch tarball proto_redir="=https,http,file" file
  if ! base="$(release_base)"; then
    refuse_run "The updater's $OVERRIDE has a CARAVANA_UPDATE_RELEASES value it can't use."
    return 0
  fi
  case "$base" in https://*) proto_redir="=https" ;; esac
  arch="$(dpkg --print-architecture)"
  tarball="caravana_${version}_linux_${arch}.tar.gz"
  work="$(mktemp -d)"
  for file in SHA256SUMS install.sh "$tarball"; do
    if ! curl -fsSL --max-time 300 --proto '=https,http,file' --proto-redir "$proto_redir" \
      -o "$work/$file" "$base/v$version/$file"; then
      refuse_run "Couldn't download caravana $version's release files: it isn't a published release, or the server can't reach GitHub."
      return 0
    fi
  done

  # 9. Both files must be listed in SHA256SUMS and match it, and the installer must be this version's.
  local sums="$work/SHA256SUMS" wanted="$work/wanted.sums" ok=1
  for file in install.sh "$tarball"; do
    awk -v want="$file" '{ n = $2; sub(/^\*/, "", n) } n == want { found = 1; print } END { exit !found }' "$sums" >>"$wanted" || ok=0
  done
  if [ "$ok" = 1 ]; then
    (cd "$work" && sha256sum --check --strict --quiet wanted.sums >/dev/null 2>&1) || ok=0
  fi
  if [ "$ok" = 1 ]; then
    grep -qxF "CARAVANA_VERSION=$version" "$work/install.sh" || ok=0
  fi
  if [ "$ok" = 0 ]; then
    refuse_run "caravana $version's release files don't match their checksums; nothing was installed."
    return 0
  fi

  # 10. Run the installer on those files. Its whole output goes to a root-only log. It gets no
  # descriptor of the lock (9>&-) and no terminal.
  local logfile="$DIR/logs/$id.log" rc=0 state reason cur
  install -d -m 0700 "$DIR/logs"
  install -m 0600 /dev/null "$logfile"
  bash "$work/install.sh" --from "$work" </dev/null >"$logfile" 2>&1 9>&- || rc=$?

  cur="$(installed_version)"
  reason=""
  if [ "$rc" = 0 ] && [ "$cur" = "$version" ]; then
    state=updated
  else
    reason="$(reason_of "$logfile")"
    case "$rc" in
    1)
      if [ "$cur" = "$from" ] && [[ "$reason" == *"runs again"* ]]; then
        state=rolled_back
      else
        state=failed
      fi
      ;;
    2) state=refused ;;
    *) state=failed ;;
    esac
    if [ -z "$reason" ]; then
      if [ "$rc" = 0 ]; then
        reason="The installer finished, but caravana $version isn't the version that runs here."
      else
        reason="The installer stopped with exit code $rc."
      fi
    fi
  fi
  write_status "$state" "$reason"
  log "the update to caravana $version ended: $state"

  rm -rf -- "$work"
  work=""
  # Keep the newest 10 logs.
  find "$DIR/logs" -maxdepth 1 -type f -name '*.log' -printf '%T@ %p\n' | sort -rn | tail -n +11 | cut -d' ' -f2- |
    xargs -r rm -f --
  return 0
}

main "$@"; exit $?
