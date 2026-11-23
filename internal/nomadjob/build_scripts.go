package nomadjob

// The two scripts of a build job (feature 050, plan D4). Each is run as
// `sh -c <script>`, in BusyBox ash (the source task's alpine/git and the
// rootless BuildKit image are both Alpine), so they are POSIX sh: printf, not
// echo, for every value, and `set -eu` so an unset variable stops them.
//
// They use $VAR and $(...) only: Nomad interpolates ${...} in task config
// before the shell sees it. Every value comes from the task's plain env
// (BuildJob), never from the script's text, so no value needs quoting in it.
// NOMAD_ALLOC_DIR, NOMAD_SECRETS_DIR and NOMAD_TASK_DIR are Nomad's own
// (/alloc, /secrets, /local); BUILD_CACHE (default /cache) is for tests.
//
// No secret is ever on a command line: a deploy key and a token are files
// the job's templates wrote (secrets/deploy_key, secrets/git-credentials),
// which git reads itself, and the push credentials are DOCKER_CONFIG's file.

// BuildSourceScript clones the branch, checks the Dockerfile and says what was
// cloned. In order:
//
//  1. When the cache is on (BUILD_CACHE_MB) and `du` says it is over that many
//     megabytes, it is emptied: it grows without bound otherwise.
//  2. Git access: a deploy key is used through GIT_SSH_COMMAND with only the
//     key and only the host keys of the job's known_hosts, and strict
//     checking; a token through git's credential store file; public needs
//     nothing. Git never asks for a password.
//  3. A shallow clone of one branch into /alloc/data/src. The reason a clone
//     fails is Git's own output above the marker line.
//  4. The context folder and the Dockerfile must exist: a sentence naming the
//     path if not.
//  5. /alloc/data/caravana/source.env gets COMMIT, AUTHOR and SUBJECT, each
//     base64 on one line, and the clone's .git is deleted so the build
//     context holds the files only.
//
// It exits 1 after a marker line that says why.
const BuildSourceScript = `set -eu
alloc=$NOMAD_ALLOC_DIR
secrets=$NOMAD_SECRETS_DIR
tdir=$NOMAD_TASK_DIR
cache=$(printenv BUILD_CACHE || true)
[ -n "$cache" ] || cache=/cache
src=$alloc/data/src
out=$alloc/data/caravana

say() { printf '%s %s\n' '` + BuildMarker + `' "$1"; }
fail() { printf '%s %s\n' '` + BuildMarker + `' "$1" >&2; exit 1; }

limit=$(printenv BUILD_CACHE_MB || true)
if [ -n "$limit" ] && [ -d "$cache" ]; then
  used=$(du -sm "$cache" 2>/dev/null | cut -f1)
  [ -n "$used" ] || used=0
  if [ "$used" -gt "$limit" ]; then
    say "the build cache is $used MB, over its $limit MB: emptying it"
    rm -rf "$cache"/* "$cache"/.[!.]* 2>/dev/null || true
  fi
fi

rm -rf "$src"
mkdir -p "$out"
chmod 0777 "$out"

export GIT_TERMINAL_PROMPT=0
helper=
case "$BUILD_ACCESS" in
  public) ;;
  deploy-key)
    export GIT_SSH_COMMAND="ssh -i $secrets/deploy_key -o IdentitiesOnly=yes -o BatchMode=yes -o UserKnownHostsFile=$tdir/known_hosts -o GlobalKnownHostsFile=/dev/null -o StrictHostKeyChecking=yes"
    ;;
  token) helper="store --file=$secrets/git-credentials" ;;
  *) fail "unknown Git access $BUILD_ACCESS" ;;
esac
set -- -c credential.helper= -c protocol.file.allow=never -c protocol.ext.allow=never
[ -z "$helper" ] || set -- "$@" -c "credential.helper=$helper"

say "cloning branch $BUILD_BRANCH of $BUILD_REPOSITORY"
if ! git "$@" clone --depth 1 --branch "$BUILD_BRANCH" --single-branch -- "$BUILD_REPOSITORY" "$src"; then
  fail "` + BuildCloneFailed + `"
fi

ctx=$src
[ "$BUILD_PATH" = . ] || ctx=$src/$BUILD_PATH
rel=$BUILD_PATH/$BUILD_DOCKERFILE
[ "$BUILD_PATH" != . ] || rel=$BUILD_DOCKERFILE
[ -d "$ctx" ] || fail "` + BuildFolderMissing + `$BUILD_PATH"
[ -f "$ctx/$BUILD_DOCKERFILE" ] || fail "` + BuildDockerfileMissing + `$rel"

commit=$(git -C "$src" rev-parse HEAD)
author=$(git -C "$src" log -1 --format=%an)
subject=$(git -C "$src" log -1 --format=%s)
enc() { printf '%s' "$1" | base64 | tr -d '\n'; }
{
  printf 'COMMIT=%s\n' "$(enc "$commit")"
  printf 'AUTHOR=%s\n' "$(enc "$author")"
  printf 'SUBJECT=%s\n' "$(enc "$subject")"
} > "$out/source.env.tmp"
mv "$out/source.env.tmp" "$out/source.env"
rm -rf "$src/.git"
say "cloned commit $commit"
`

// BuildScript builds the cloned source with rootless BuildKit and pushes it.
// It reads the commit from source.env (and refuses anything but hex), builds
// $BUILD_PATH with the Dockerfile at $BUILD_DOCKERFILE, and pushes
// $BUILD_IMAGE:git-<first 12 of the commit>, over plain HTTP only when
// BUILD_INSECURE is set. With the cache on (BUILD_CACHE_MB) the layers are
// exported to /cache, and imported from it when an earlier build left one.
// BuildKit writes the digest the registry has to /alloc/data/caravana/build.json,
// which the script checks is there.
//
// It exits 1 after a marker line that says why, and 0 after "pushed".
const BuildScript = `set -eu
alloc=$NOMAD_ALLOC_DIR
cache=$(printenv BUILD_CACHE || true)
[ -n "$cache" ] || cache=/cache
src=$alloc/data/src
out=$alloc/data/caravana

say() { printf '%s %s\n' '` + BuildMarker + `' "$1"; }
fail() { printf '%s %s\n' '` + BuildMarker + `' "$1" >&2; exit 1; }

commit=$(sed -n 's/^COMMIT=//p' "$out/source.env" | base64 -d)
case "$commit" in
  ""|*[!0-9a-f]*) fail "the source task left no commit" ;;
esac
tag=git-$(printf '%s' "$commit" | cut -c1-12)

ctx=$src
[ "$BUILD_PATH" = . ] || ctx=$src/$BUILD_PATH
dockerfile=$ctx/$BUILD_DOCKERFILE
output="type=image,name=$BUILD_IMAGE:$tag,push=true"
[ -z "$(printenv BUILD_INSECURE || true)" ] || output="$output,registry.insecure=true"

rm -f "$out/build.json"
set -- build --progress=plain --frontend dockerfile.v0 --local "context=$ctx" --local "dockerfile=$(dirname "$dockerfile")" --opt "filename=$(basename "$dockerfile")" --output "$output" --metadata-file "$out/build.json"
if [ -n "$(printenv BUILD_CACHE_MB || true)" ]; then
  [ ! -f "$cache/index.json" ] || set -- "$@" --import-cache "type=local,src=$cache"
  set -- "$@" --export-cache "type=local,dest=$cache,mode=max"
fi

say "building $BUILD_IMAGE:$tag"
if ! buildctl-daemonless.sh "$@"; then
  fail "` + BuildFailed + `"
fi
grep -q containerimage.digest "$out/build.json" 2>/dev/null || fail "` + BuildNoDigest + `"
say "pushed $BUILD_IMAGE:$tag"
`
