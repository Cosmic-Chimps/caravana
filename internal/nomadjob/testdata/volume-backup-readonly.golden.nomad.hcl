# Exported by Caravana.
#
# Needs: Traefik.

job "kuma" {
  type = "service"
  datacenters = ["*"]
  meta = {
    "caravana.app" = "kuma"
    "caravana.deployment" = "d1"
  }
  update {
    max_parallel = 1
    health_check = "checks"
    min_healthy_time = "10s"
    healthy_deadline = "3m0s"
    progress_deadline = "10m0s"
    auto_revert = true
    auto_promote = true
  }
  group "web" {
    count = 1
    shutdown_delay = "5s"
    update {
      canary = 0
      auto_promote = false
    }
    network {
      mode = "bridge"
      port "http" {
        to = 3001
      }
    }
    volume "data" {
      type = "host"
      source = "kuma_data"
      access_mode = "single-node-single-writer"
      attachment_mode = "file-system"
    }
    service {
      name = "kuma-web"
      port = "http"
      provider = "nomad"
      tags = ["traefik.enable=true", "traefik.http.routers.kuma-web.rule=Host(`kuma.example.com`)", "traefik.http.routers.kuma-web.entrypoints=websecure", "traefik.http.routers.kuma-web.tls=true"]
      canary_tags = ["caravana.canary=true"]
      check {
        name = "health"
        type = "http"
        path = "/"
        interval = "10s"
        timeout = "2s"
      }
    }
    task "web" {
      driver = "docker"
      config {
        image = "louislam/uptime-kuma@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }
      resources {
        cpu = 100
        memory = 64
        memory_max = 256
      }
      volume_mount {
        volume = "data"
        destination = "/app/data"
        read_only = true
      }
    }
    task "caravana-volumes" {
      driver = "docker"
      lifecycle {
        hook = "prestart"
        sidecar = false
      }
      config {
        args = ["[ -n \"$ROOT\" ] || ROOT=/caravana-volumes\n[ -n \"$DATA\" ] || DATA=/alloc/data/caravana\n[ -n \"$REQUEST_FILE\" ] || REQUEST_FILE=/local/request.env\n[ -n \"$DEST_FILE\" ] || DEST_FILE=/secrets/destination.env\n\n# One \"<volume> <kilobytes>\" line for each volume, replaced as a whole so\n# Caravana never reads half a file.\nmeasure_sizes() {\n  mkdir -p \"$DATA\" || return 0\n  : > \"$DATA/.sizes.tmp\" || return 0\n  for dir in \"$ROOT\"/*; do\n    [ -d \"$dir\" ] || continue\n    echo \"$(basename \"$dir\") $(kb_of \"$dir\")\" >> \"$DATA/.sizes.tmp\"\n  done\n  mv \"$DATA/.sizes.tmp\" \"$DATA/sizes\"\n}\n\n# kb_of <dir> prints the kilobytes the directory holds.\nkb_of() {\n  kb=$(du -sk \"$1\" 2>/dev/null | cut -f1)\n  [ -n \"$kb\" ] || kb=0\n  echo \"$kb\"\n}\n\n# finish <status> writes the result file from MESSAGE and the detail lines the\n# run left in $DETAIL, replacing nothing half-written, and removes the run's\n# scratch files.\nfinish() {\n  message=$(printf '%s' \"$MESSAGE\" | tr '\\n' ' ')\n  {\n    echo \"status=$1\"\n    echo \"message=$message\"\n    [ ! -f \"$DETAIL\" ] || cat \"$DETAIL\"\n  } > \"$DATA/.$REQUEST.tmp\" && mv \"$DATA/.$REQUEST.tmp\" \"$DATA/$REQUEST.result\"\n  rm -rf \"$DETAIL\" \"$WORK\"\n}\n\n# seconds_left prints the seconds of the run's time limit that remain, at\n# least 1: timeout 0 would mean no limit at all.\nseconds_left() {\n  now=$(date +%s)\n  left=$((DEADLINE - now))\n  [ \"$left\" -ge 1 ] || left=1\n  echo \"$left\"\n}\n\n# step_failed <what> <stderr file> <exit status> sets MESSAGE from the step's\n# last line of stderr, or says it ran out of time, and returns 1.\nstep_failed() {\n  line=$(tail -n 1 \"$2\" 2>/dev/null | tr -d '\\r')\n  case \"$3\" in\n    124|143) MESSAGE=\"$1 timed out (the limit is $LIMIT seconds)\" ;;\n    *) if [ -n \"$line\" ]; then MESSAGE=\"$1: $line\"; else MESSAGE=\"$1 failed (exit status $3)\"; fi ;;\n  esac\n  return 1\n}\n\n# --- The runs. Each returns 0 for success, or 1 with MESSAGE saying why. It\n# --- appends \"key=value\" lines to $DETAIL for the result file.\n\n# do_backup streams each volume as a gzipped tar into the destination, at\n# BACKUP_PATH/TS/<volume>.tar.gz. Nothing is staged on disk: the copy's own\n# disk is small. The first failure purges what the run uploaded, so no partial\n# backup is left behind.\ndo_backup() {\n  case \"$TS\" in ''|*[!0-9]*) MESSAGE=\"the request has no timestamp\"; return 1 ;; esac\n  [ -n \"$BACKUP_PATH\" ] || { MESSAGE=\"the request has no backup destination\"; return 1; }\n  found=\n  for dir in \"$ROOT\"/*; do\n    [ -d \"$dir\" ] || continue\n    found=1\n    if ! upload_volume \"$dir\" \"$(basename \"$dir\")\"; then\n      discard_backup\n      return 1\n    fi\n  done\n  [ -n \"$found\" ] || { MESSAGE=\"the service has no volume mounted\"; return 1; }\n  echo \"object=$BACKUP_PATH/$TS/\" >> \"$DETAIL\"\n  apply_retention\n  return 0\n}\n\n# upload_volume <dir> <name> archives one volume and records its sizes.\nupload_volume() {\n  rm -f \"$WORK/tar.status\"\n  {\n    timeout \"$(seconds_left)\" tar -czf - -C \"$1\" --exclude ./.caravana-restore . 2> \"$WORK/tar.err\"\n    echo $? > \"$WORK/tar.status\"\n  } | timeout \"$(seconds_left)\" rclone rcat \"dest:$BACKUP_PATH/$TS/$2.tar.gz\" 2> \"$WORK/rclone.err\"\n  uploaded=$?\n  archived=$(cat \"$WORK/tar.status\" 2>/dev/null)\n  # rclone's word comes first: when it dies, tar fails with a broken pipe.\n  [ \"$uploaded\" -eq 0 ] || { step_failed \"upload of $2\" \"$WORK/rclone.err\" \"$uploaded\"; return 1; }\n  [ -n \"$archived\" ] || archived=1\n  [ \"$archived\" = 0 ] || { step_failed \"archive of $2\" \"$WORK/tar.err\" \"$archived\"; return 1; }\n\n  out=$(timeout \"$(seconds_left)\" rclone size --json \"dest:$BACKUP_PATH/$TS/$2.tar.gz\" 2> \"$WORK/rclone.err\")\n  status=$?\n  [ \"$status\" -eq 0 ] || { step_failed \"size of the archive of $2\" \"$WORK/rclone.err\" \"$status\"; return 1; }\n  bytes=$(printf '%s' \"$out\" | sed -n 's/.*\"bytes\": *\\([0-9][0-9]*\\).*/\\1/p')\n  [ -n \"$bytes\" ] || { MESSAGE=\"the size of the archive of $2 is unreadable: $out\"; return 1; }\n  {\n    echo \"volume.$2.bytes=$bytes\"\n    echo \"volume.$2.size=$(kb_of \"$1\")\"\n  } >> \"$DETAIL\"\n}\n\n# discard_backup removes this run's directory from the destination, as well as\n# it can, and forgets the volumes the run had recorded. It gets its own minute:\n# the run's own time may be spent.\ndiscard_backup() {\n  : > \"$DETAIL\"\n  timeout 60 rclone purge \"dest:$BACKUP_PATH/$TS\" > /dev/null 2>&1\n  return 0\n}\n\n# apply_retention deletes the oldest backups beyond KEEP and lists their\n# timestamps in expired=. The new backup is already safe, so a failure here\n# doesn't fail the run: it is said in the message of a run that succeeded, and\n# what was deleted before the failure is still listed. Only directories named\n# with digits count, so nothing else under BACKUP_PATH is touched, and the new\n# backup is never deleted. No KEEP, or a bad one, deletes nothing.\napply_retention() {\n  case \"$KEEP\" in ''|*[!0-9]*|0) return 0 ;; esac\n  dirs=$(timeout \"$(seconds_left)\" rclone lsf --dirs-only \"dest:$BACKUP_PATH/\" 2> \"$WORK/rclone.err\")\n  status=$?\n  if [ \"$status\" -ne 0 ]; then\n    step_failed \"listing the old backups\" \"$WORK/rclone.err\" \"$status\"\n    MESSAGE=\"the backup is kept, but old backups were not removed: $MESSAGE\"\n    return 0\n  fi\n  old=$(echo \"$dirs\" | grep -E '^[0-9]+/$' | sort -n)\n  excess=$(($(echo \"$old\" | grep -c .) - KEEP))\n  [ \"$excess\" -gt 0 ] || return 0\n  expired=\n  for d in $(echo \"$old\" | head -n \"$excess\"); do\n    d=$(basename \"$d\")\n    [ \"$d\" != \"$TS\" ] || continue\n    timeout \"$(seconds_left)\" rclone purge \"dest:$BACKUP_PATH/$d\" > /dev/null 2> \"$WORK/rclone.err\"\n    status=$?\n    if [ \"$status\" -ne 0 ]; then\n      step_failed \"removing the backup $d\" \"$WORK/rclone.err\" \"$status\"\n      MESSAGE=\"the backup is kept, but old backups were not removed: $MESSAGE\"\n      break\n    fi\n    expired=\"$expired $d\"\n  done\n  [ -z \"$expired\" ] || echo \"expired=$(echo $expired)\" >> \"$DETAIL\"\n}\n\n# do_restore replaces every volume's files with the archives of the backup at\n# OBJECT. It must either restore every volume or leave every volume as it was,\n# so it works in three steps:\n#   1. stage: download each volume's archive into <volume>/.caravana-restore\n#      (on the volume's own disk, where the room for the data already is) and\n#      check that tar can read it. Nothing of the volume is touched yet, so a\n#      missing or damaged archive, even the last one's, changes nothing.\n#   2. swap: volume by volume, move the current files into\n#      .caravana-restore/old (a rename, no copying) and extract the archive.\n#   3. If any extract fails, every volume swapped so far, and that one, is\n#      emptied of what was extracted and gets its old files back.\n# At the end the staging directories are removed, old files with them. The\n# backup path leaves .caravana-restore out of its archives, so nothing of this\n# is ever backed up.\ndo_restore() {\n  [ -n \"$OBJECT\" ] || { MESSAGE=\"the request names no backup to restore\"; return 1; }\n  case \"$OBJECT\" in */) ;; *) OBJECT=\"$OBJECT/\" ;; esac\n  # Old files left by an earlier restore that was cut off or could not put\n  # them back may be the only copy of someone's data: staging again would\n  # delete them, so refuse until a person has looked.\n  for dir in \"$ROOT\"/*; do\n    if [ -d \"$dir/.caravana-restore/old\" ]; then\n      MESSAGE=\"$(basename \"$dir\") has files left in .caravana-restore/old by an earlier restore; check them, then delete that directory\"\n      return 1\n    fi\n  done\n  ROLLBACK_FAILED=\n  found=\n  for dir in \"$ROOT\"/*; do\n    [ -d \"$dir\" ] || continue\n    found=1\n    if ! stage_archive \"$dir\" \"$(basename \"$dir\")\"; then\n      discard_stages\n      return 1\n    fi\n  done\n  [ -n \"$found\" ] || { MESSAGE=\"the service has no volume mounted\"; return 1; }\n\n  SWAPPED=\n  for dir in \"$ROOT\"/*; do\n    [ -d \"$dir\" ] || continue\n    name=$(basename \"$dir\")\n    if ! swap_volume \"$dir\" \"$name\"; then\n      undo_swaps\n      if [ -n \"$ROLLBACK_FAILED\" ]; then\n        MESSAGE=\"$MESSAGE; the old files could not all be put back, they are in the .caravana-restore/old directory of each volume\"\n      else\n        discard_stages\n      fi\n      return 1\n    fi\n    SWAPPED=\"$SWAPPED $name\"\n  done\n  if ! discard_stages; then\n    MESSAGE=\"the volumes are restored, but the old files could not be removed from their .caravana-restore directories\"\n  fi\n  return 0\n}\n\n# stage_archive <dir> <name> downloads the volume's archive and checks it.\nstage_archive() {\n  rm -rf \"$1/.caravana-restore\"\n  mkdir -p \"$1/.caravana-restore\" 2> \"$WORK/mkdir.err\"\n  status=$?\n  [ \"$status\" -eq 0 ] || { step_failed \"preparing $2 for the restore\" \"$WORK/mkdir.err\" \"$status\"; return 1; }\n  timeout \"$(seconds_left)\" rclone copyto \"dest:$OBJECT$2.tar.gz\" \"$1/.caravana-restore/$2.tar.gz\" 2> \"$WORK/rclone.err\"\n  status=$?\n  [ \"$status\" -eq 0 ] || { step_failed \"download of $2\" \"$WORK/rclone.err\" \"$status\"; return 1; }\n  timeout \"$(seconds_left)\" tar -tzf \"$1/.caravana-restore/$2.tar.gz\" > /dev/null 2> \"$WORK/tar.err\"\n  status=$?\n  [ \"$status\" -eq 0 ] || { step_failed \"check of the archive of $2\" \"$WORK/tar.err\" \"$status\"; return 1; }\n}\n\n# swap_volume <dir> <name> moves the volume's files aside and extracts its\n# archive. When it fails it puts this volume back as it was, and says so with\n# ROLLBACK_FAILED if it couldn't.\nswap_volume() {\n  mkdir \"$1/.caravana-restore/old\" 2> \"$WORK/mkdir.err\"\n  status=$?\n  [ \"$status\" -eq 0 ] || { step_failed \"preparing $2 for the restore\" \"$WORK/mkdir.err\" \"$status\"; return 1; }\n  if ! move_entries \"$1\" \"$1/.caravana-restore/old\"; then\n    MESSAGE=\"the files of $2 could not be moved aside\"\n    put_back \"$1\" || ROLLBACK_FAILED=1\n    return 1\n  fi\n  timeout \"$(seconds_left)\" tar -xzf \"$1/.caravana-restore/$2.tar.gz\" -C \"$1\" 2> \"$WORK/tar.err\"\n  status=$?\n  if [ \"$status\" -ne 0 ]; then\n    step_failed \"extract of the archive of $2\" \"$WORK/tar.err\" \"$status\"\n    clear_entries \"$1\"\n    put_back \"$1\" || ROLLBACK_FAILED=1\n    return 1\n  fi\n}\n\n# undo_swaps puts back the volumes that were swapped before a later one failed.\nundo_swaps() {\n  for swapped in $SWAPPED; do\n    clear_entries \"$ROOT/$swapped\"\n    put_back \"$ROOT/$swapped\" || ROLLBACK_FAILED=1\n  done\n}\n\n# put_back <dir> moves a volume's old files back from .caravana-restore/old.\nput_back() {\n  move_entries \"$1/.caravana-restore/old\" \"$1\"\n}\n\n# discard_stages removes every volume's staging directory, the old files in it\n# included. It returns 1 when one could not be removed.\ndiscard_stages() {\n  discarded=0\n  for stage in \"$ROOT\"/*; do\n    [ -d \"$stage\" ] || continue\n    rm -rf \"$stage/.caravana-restore\" || discarded=1\n  done\n  return $discarded\n}\n\n# move_entries <from> <to> moves everything in <from>, hidden files too (a\n# plain * skips them), except a .caravana-restore. It tries every entry and\n# returns 1 if any failed.\nmove_entries() {\n  moved=0\n  for entry in \"$1\"/* \"$1\"/.[!.]* \"$1\"/..?*; do\n    [ -e \"$entry\" ] || [ -L \"$entry\" ] || continue\n    [ \"$(basename \"$entry\")\" != .caravana-restore ] || continue\n    mv \"$entry\" \"$2/\" || moved=1\n  done\n  return $moved\n}\n\n# clear_entries <dir> deletes everything in <dir>, hidden files too, except a\n# .caravana-restore: what a failed extract left behind.\nclear_entries() {\n  for entry in \"$1\"/* \"$1\"/.[!.]* \"$1\"/..?*; do\n    [ -e \"$entry\" ] || [ -L \"$entry\" ] || continue\n    [ \"$(basename \"$entry\")\" != .caravana-restore ] || continue\n    rm -rf \"$entry\"\n  done\n}\n\n# --- The request.\n\nhandle_request() {\n  [ -f \"$REQUEST_FILE\" ] || return 0\n  REQUEST= ACTION= OBJECT= NOT_AFTER= LIMIT= KEEP= TS= BACKUP_PATH=\n  set -a\n  . \"$REQUEST_FILE\"\n  [ ! -f \"$DEST_FILE\" ] || . \"$DEST_FILE\"\n  set +a\n  # The ID names a file: letters, digits, \"-\" and \"_\" only.\n  case \"$REQUEST\" in ''|*[!A-Za-z0-9_-]*) return 0 ;; esac\n  case \"$NOT_AFTER\" in ''|*[!0-9]*) return 0 ;; esac\n  [ \"$NOT_AFTER\" -ge \"$(date +%s)\" ] || return 0\n  [ ! -e \"$DATA/$REQUEST.result\" ] || return 0\n\n  # The run's time limit, counted from now. A LIMIT that is no number is\n  # the default of half an hour.\n  case \"$LIMIT\" in ''|*[!0-9]*|0) LIMIT=1800 ;; esac\n  DEADLINE=$(($(date +%s) + LIMIT))\n\n  WORK=\"$DATA/.$REQUEST.work\"\n  DETAIL=\"$DATA/.$REQUEST.detail\"\n  mkdir -p \"$WORK\" || return 0\n  rm -f \"$DETAIL\"\n  MESSAGE=\n  case \"$ACTION\" in\n    backup) if do_backup; then finish succeeded; else finish failed; fi ;;\n    restore) if do_restore; then finish succeeded; else finish failed; fi ;;\n    *) MESSAGE=\"unknown action\"; finish failed ;;\n  esac\n}\n\nmeasure_sizes\n( handle_request )\nexit 0\n"]
        entrypoint = ["/bin/sh", "-c"]
        image = "rclone/rclone@sha256:3103526c506266a9ecdf064efe99bf3677d92ef6407af124d8c56b4f49cbaa51"
      }
      resources {
        cpu = 50
        memory = 32
        memory_max = 512
      }
      volume_mount {
        volume = "data"
        destination = "/caravana-volumes/data"
        read_only = false
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/kuma/web/caravana-volumes\" }}{{ range .Tuples }}{{ if not (.K | regexMatch \"^RCLONE_CONFIG_DEST_\") }}{{ .K }}='{{ .V | replaceAll \"'\" \"'\\\\''\" }}'\n{{ end }}{{ end }}{{ end }}"
        destination = "local/request.env"
        env = false
        change_mode = "noop"
      }
      template {
        data = "{{ with nomadVar \"nomad/jobs/kuma/web/caravana-volumes\" }}{{ range .Tuples }}{{ if .K | regexMatch \"^RCLONE_CONFIG_DEST_\" }}{{ .K }}='{{ .V | replaceAll \"'\" \"'\\\\''\" }}'\n{{ end }}{{ end }}{{ end }}"
        destination = "secrets/destination.env"
        env = false
        change_mode = "noop"
      }
    }
  }
}
