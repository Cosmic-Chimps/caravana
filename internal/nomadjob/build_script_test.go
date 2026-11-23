package nomadjob

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The build job's two scripts (feature 050) under a real sh. The tools they
// call (git, du and buildctl-daemonless.sh) are fakes on a PATH that holds only
// them and the few real utilities the scripts use, so the host's own tools
// never answer, and every call is logged for the test to read.

// buildSentinel is in the fake deploy key, token and docker config. A fake tool
// fails when it finds it in its arguments, which is where `ps` would show it,
// and the tests search the whole call log for it.
const buildSentinel = "SENTINEL-s3cret"

const buildFakeGit = `#!/bin/sh
echo "git $*" >> "$FAKE_LOG"
echo "GIT_SSH_COMMAND=$GIT_SSH_COMMAND GIT_TERMINAL_PROMPT=$GIT_TERMINAL_PROMPT" >> "$FAKE_LOG"
case "$*" in *` + buildSentinel + `*) echo "git: a secret is in the arguments" >&2; exit 2 ;; esac
dir=
while [ $# -gt 0 ]; do
  case "$1" in
    -c) shift 2 ;;
    -C) dir=$2; shift 2 ;;
    *) break ;;
  esac
done
case "$1" in
  clone)
    [ -z "$FAKE_CLONE_FAIL" ] || { echo "fatal: could not read from remote repository." >&2; exit 128; }
    for last; do :; done
    mkdir -p "$last"
    cp -R "$FAKE_TREE/." "$last/"
    mkdir -p "$last/.git"
    echo "ref" > "$last/.git/HEAD" ;;
  rev-parse) printf '%s\n' "$FAKE_COMMIT" ;;
  log)
    case "$*" in
      *%an*) printf '%s\n' "$FAKE_AUTHOR" ;;
      *%s*) printf '%s\n' "$FAKE_SUBJECT" ;;
    esac ;;
  *) echo "fake git: unknown command $1" >&2; exit 2 ;;
esac
`

const buildFakeDU = `#!/bin/sh
echo "du $*" >> "$FAKE_LOG"
printf '%s\t%s\n' "$FAKE_DU" "$2"
`

const buildFakeBuildctl = `#!/bin/sh
echo "buildctl $*" >> "$FAKE_LOG"
echo "BUILDKITD_FLAGS=$BUILDKITD_FLAGS DOCKER_CONFIG=$DOCKER_CONFIG" >> "$FAKE_LOG"
case "$*" in *` + buildSentinel + `*) echo "buildctl: a secret is in the arguments" >&2; exit 2 ;; esac
[ -z "$FAKE_BUILD_FAIL" ] || { echo "error: failed to solve" >&2; exit 1; }
while [ $# -gt 0 ]; do
  [ "$1" = --metadata-file ] && meta=$2
  shift
done
[ -z "$FAKE_NO_DIGEST" ] || exit 0
printf '{"containerimage.digest":"sha256:%s"}\n' "$FAKE_DIGEST" > "$meta"
`

// buildScriptTools are the real utilities the scripts use.
var buildScriptTools = []string{"sh", "sed", "cat", "cp", "mkdir", "rm", "mv", "chmod", "dirname", "basename", "grep", "cut", "tr", "base64", "printenv"}

// buildRun is one run of a script: its own folders, a PATH of the chosen fakes
// and an environment like the task's.
type buildRun struct {
	t       *testing.T
	dir     string
	bin     string
	sh      string
	alloc   string // NOMAD_ALLOC_DIR
	secrets string // NOMAD_SECRETS_DIR
	task    string // NOMAD_TASK_DIR
	cache   string // BUILD_CACHE
	tree    string // what a fake clone leaves
	log     string
}

func newBuildRun(t *testing.T, fakes map[string]string) *buildRun {
	t.Helper()
	r := &buildRun{t: t, dir: t.TempDir()}
	r.bin = filepath.Join(r.dir, "bin")
	r.alloc, r.secrets, r.task = filepath.Join(r.dir, "alloc"), filepath.Join(r.dir, "secrets"), filepath.Join(r.dir, "local")
	r.cache, r.tree, r.log = filepath.Join(r.dir, "cache"), filepath.Join(r.dir, "tree"), filepath.Join(r.dir, "calls.log")
	for _, d := range []string{r.bin, filepath.Join(r.alloc, "data"), r.secrets, r.task, r.cache, r.tree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range buildScriptTools {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		if tool == "sh" {
			r.sh = path
		}
		if err := os.Symlink(path, filepath.Join(r.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range fakes {
		if err := os.WriteFile(filepath.Join(r.bin, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *buildRun) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// run runs script under sh as a task would: env is the task's plain env, and
// nothing of the test process's environment goes in.
func (r *buildRun) run(script string, env ...string) (stdout, stderr string, code int) {
	r.t.Helper()
	cmd := exec.Command(r.sh, "-c", script)
	cmd.Dir = r.dir
	cmd.Env = append([]string{"PATH=" + r.bin, "FAKE_LOG=" + r.log, "FAKE_TREE=" + r.tree,
		"NOMAD_ALLOC_DIR=" + r.alloc, "NOMAD_SECRETS_DIR=" + r.secrets, "NOMAD_TASK_DIR=" + r.task, "BUILD_CACHE=" + r.cache}, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code = exitCode(r.t, cmd.Run())
	return out.String(), errOut.String(), code
}

func (r *buildRun) calls() string {
	b, err := os.ReadFile(r.log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *buildRun) path(rel string) string { return filepath.Join(r.alloc, "data", "caravana", rel) }

// sourceFakes are the tools of the source task.
func sourceFakes() map[string]string {
	return map[string]string{"git": buildFakeGit, "du": buildFakeDU}
}

const (
	testCommit = "4e0a77c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7"
	testDigest = "sha256:deeea5700000000000000000000000000000000000000000000000000000abcd"
)

// sourceEnv is the source task's env for a public clone of the repository's root.
func sourceEnv(extra ...string) []string {
	return append([]string{"BUILD_REPOSITORY=https://github.com/acme/shop.git", "BUILD_BRANCH=main", "BUILD_PATH=.",
		"BUILD_DOCKERFILE=Dockerfile", "BUILD_ACCESS=public",
		"FAKE_COMMIT=" + testCommit, "FAKE_AUTHOR=Ana", "FAKE_SUBJECT=Retry supplier timeouts"}, extra...)
}

// sourceEnvFile reads source.env: each value base64.
func sourceEnvValues(t *testing.T, r *buildRun) map[string]string {
	t.Helper()
	b, err := os.ReadFile(r.path("source.env"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for line := range strings.Lines(string(b)) {
		k, v, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		if !ok || strings.ContainsAny(v, " \t\r") {
			t.Fatalf("source.env line %q must be KEY=base64 on one line", line)
		}
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			t.Fatalf("source.env %s is not base64: %v", k, err)
		}
		got[k] = string(raw)
	}
	return got
}

func TestBuildSourceScriptClones(t *testing.T) {
	r := newBuildRun(t, sourceFakes())
	r.write("tree/Dockerfile", "FROM scratch\n")
	r.write("tree/main.go", "package main\n")

	stdout, stderr, code := r.run(BuildSourceScript, sourceEnv()...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	got := sourceEnvValues(t, r)
	if got["COMMIT"] != testCommit || got["AUTHOR"] != "Ana" || got["SUBJECT"] != "Retry supplier timeouts" || len(got) != 3 {
		t.Errorf("source.env = %v", got)
	}
	src := filepath.Join(r.alloc, "data", "src")
	if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
		t.Error(".git was kept in the build context")
	}
	if _, err := os.Stat(filepath.Join(src, "Dockerfile")); err != nil {
		t.Errorf("the clone isn't in the build context: %v", err)
	}
	if !strings.Contains(stdout, BuildMarker+" cloned commit "+testCommit) {
		t.Errorf("stdout = %q, want the marker line with the commit", stdout)
	}
	calls := r.calls()
	if want := "clone --depth 1 --branch main --single-branch -- https://github.com/acme/shop.git " + src; !strings.Contains(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}
	if !strings.Contains(calls, "GIT_TERMINAL_PROMPT=0") || strings.Contains(calls, "GIT_SSH_COMMAND=ssh") {
		t.Errorf("calls = %q: git must never prompt, and a public clone has no ssh command", calls)
	}
	if strings.Contains(calls, "store --file") {
		t.Errorf("calls = %q: a public clone has no credential store", calls)
	}
	if strings.Contains(calls, "du ") {
		t.Errorf("calls = %q: with no cache, nothing measures it", calls)
	}
	info, err := os.Stat(filepath.Dir(r.path("source.env")))
	if err != nil || info.Mode().Perm() != 0o777 {
		t.Errorf("the results folder must be writable by the rootless builder: %v %v", info, err)
	}
}

// A value with quotes, a dollar and a command substitution can't break the file
// or run: each is base64 of the bytes.
func TestBuildSourceEnvKeepsAnyValue(t *testing.T) {
	r := newBuildRun(t, sourceFakes())
	r.write("tree/Dockerfile", "FROM scratch\n")
	subject := `it's "quoted" $(touch PWNED) ` + "`touch PWNED2` \\n é"
	author := `Ana "the $HOME" O'Neil`
	_, stderr, code := r.run(BuildSourceScript, sourceEnv("FAKE_SUBJECT="+subject, "FAKE_AUTHOR="+author)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := sourceEnvValues(t, r)
	if got["SUBJECT"] != subject || got["AUTHOR"] != author {
		t.Errorf("source.env = %q", got)
	}
	for _, p := range []string{"PWNED", "PWNED2"} {
		if _, err := os.Stat(filepath.Join(r.dir, p)); err == nil {
			t.Errorf("%s: a value was run as a shell command", p)
		}
	}
}

func TestBuildSourceScriptMissingDockerfile(t *testing.T) {
	for _, tc := range []struct {
		name, path, dockerfile, tree, want string
	}{
		{"root", ".", "Dockerfile", "other.txt", BuildMarker + " " + BuildDockerfileMissing + "Dockerfile"},
		{"folder", "web", "docker/Dockerfile.prod", "web/Dockerfile", BuildMarker + " " + BuildDockerfileMissing + "web/docker/Dockerfile.prod"},
		{"no folder", "web", "Dockerfile", "Dockerfile", BuildMarker + " " + BuildFolderMissing + "web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBuildRun(t, sourceFakes())
			r.write("tree/"+tc.tree, "x\n")
			stdout, stderr, code := r.run(BuildSourceScript, sourceEnv("BUILD_PATH="+tc.path, "BUILD_DOCKERFILE="+tc.dockerfile)...)
			if code != 1 {
				t.Fatalf("exit %d, want 1: %s%s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to say %q", stderr, tc.want)
			}
			if _, err := os.Stat(r.path("source.env")); err == nil {
				t.Error("source.env was written for a source that can't be built")
			}
		})
	}
}

func TestBuildSourceScriptCloneFails(t *testing.T) {
	r := newBuildRun(t, sourceFakes())
	stdout, stderr, code := r.run(BuildSourceScript, sourceEnv("FAKE_CLONE_FAIL=1")...)
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, BuildMarker+" "+BuildCloneFailed) || !strings.Contains(stderr, "could not read from remote repository") {
		t.Errorf("stderr = %q, want Git's reason and the marker's sentence", stderr)
	}
	if _, err := os.Stat(r.path("source.env")); err == nil {
		t.Error("source.env was written after a failed clone")
	}
}

// The cache is emptied when it is over its size, and only then.
func TestBuildSourceScriptEmptiesAnOversizedCache(t *testing.T) {
	for _, tc := range []struct {
		name        string
		limitEnv    string // "" for no cache
		du          string
		wantEmptied bool
		wantMeasure bool
	}{
		{"over", "BUILD_CACHE_MB=5120", "6000", true, true},
		{"under", "BUILD_CACHE_MB=5120", "100", false, true},
		{"exactly the size", "BUILD_CACHE_MB=5120", "5120", false, true},
		{"no cache", "", "999999", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBuildRun(t, sourceFakes())
			r.write("tree/Dockerfile", "FROM scratch\n")
			r.write("cache/index.json", "{}")
			r.write("cache/blobs/sha256/aa", "layer")
			r.write("cache/.hidden", "dotfile")
			env := sourceEnv("FAKE_DU=" + tc.du)
			if tc.limitEnv != "" {
				env = append(env, tc.limitEnv)
			}
			stdout, stderr, code := r.run(BuildSourceScript, env...)
			if code != 0 {
				t.Fatalf("exit %d: %s%s", code, stdout, stderr)
			}
			entries, err := os.ReadDir(r.cache)
			if err != nil {
				t.Fatalf("the cache folder itself must stay (it is a mount point): %v", err)
			}
			if emptied := len(entries) == 0; emptied != tc.wantEmptied {
				t.Errorf("cache emptied = %v (%d entries), want %v", emptied, len(entries), tc.wantEmptied)
			}
			if measured := strings.Contains(r.calls(), "du -sm "+r.cache); measured != tc.wantMeasure {
				t.Errorf("du called = %v, want %v", measured, tc.wantMeasure)
			}
			if said := strings.Contains(stdout, "emptying it"); said != tc.wantEmptied {
				t.Errorf("stdout = %q, want the line about emptying only when it did", stdout)
			}
		})
	}
}

func TestBuildSourceScriptDeployKey(t *testing.T) {
	r := newBuildRun(t, sourceFakes())
	r.write("tree/Dockerfile", "FROM scratch\n")
	r.write("secrets/deploy_key", "-----BEGIN OPENSSH PRIVATE KEY-----\n"+buildSentinel+"\n")
	_, stderr, code := r.run(BuildSourceScript, sourceEnv("BUILD_ACCESS=deploy-key", "BUILD_REPOSITORY=git@git.example.com:acme/shop.git")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	calls := r.calls()
	for _, want := range []string{
		"GIT_SSH_COMMAND=ssh -i " + r.secrets + "/deploy_key", "-o IdentitiesOnly=yes", "-o BatchMode=yes",
		"-o UserKnownHostsFile=" + r.task + "/known_hosts", "-o GlobalKnownHostsFile=/dev/null", "-o StrictHostKeyChecking=yes",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls = %q, want %q", calls, want)
		}
	}
	if strings.Contains(calls, "store --file") || strings.Contains(calls, buildSentinel) {
		t.Errorf("calls = %q: no credential store for a key, and the key never on a command line", calls)
	}
}

func TestBuildSourceScriptToken(t *testing.T) {
	r := newBuildRun(t, sourceFakes())
	r.write("tree/Dockerfile", "FROM scratch\n")
	r.write("secrets/git-credentials", "https://x-access-token:"+buildSentinel+"@github.com\n")
	_, stderr, code := r.run(BuildSourceScript, sourceEnv("BUILD_ACCESS=token")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	calls := r.calls()
	if want := "-c credential.helper= -c protocol.file.allow=never -c protocol.ext.allow=never -c credential.helper=store --file=" + r.secrets + "/git-credentials clone"; !strings.Contains(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}
	if strings.Contains(calls, buildSentinel) || strings.Contains(calls, "GIT_SSH_COMMAND=ssh") {
		t.Errorf("calls = %q: the token is only in the credential file, and a token clone has no ssh command", calls)
	}
}

func TestBuildSourceScriptUnknownAccess(t *testing.T) {
	r := newBuildRun(t, sourceFakes())
	_, stderr, code := r.run(BuildSourceScript, sourceEnv("BUILD_ACCESS=password")...)
	if code != 1 || !strings.Contains(stderr, "unknown Git access password") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(r.calls(), "clone") {
		t.Error("git cloned with an access it doesn't know")
	}
}

// buildEnv is the build task's env.
func buildEnv(extra ...string) []string {
	return append([]string{"BUILD_IMAGE=ghcr.io/acme/shop", "BUILD_PATH=.", "BUILD_DOCKERFILE=Dockerfile",
		"BUILDKITD_FLAGS=--oci-worker-no-process-sandbox", "FAKE_DIGEST=" + strings.TrimPrefix(testDigest, "sha256:")}, extra...)
}

func buildFakes() map[string]string {
	return map[string]string{"buildctl-daemonless.sh": buildFakeBuildctl}
}

// newBuiltRun is a run after a successful source task: source.env and a source.
func newBuiltRun(t *testing.T) *buildRun {
	t.Helper()
	r := newBuildRun(t, buildFakes())
	r.write("alloc/data/caravana/source.env", "COMMIT="+base64.StdEncoding.EncodeToString([]byte(testCommit))+"\nAUTHOR=QW5h\nSUBJECT=eA==\n")
	r.write("alloc/data/src/Dockerfile", "FROM scratch\n")
	return r
}

func buildctlCall(t *testing.T, r *buildRun) string {
	t.Helper()
	for line := range strings.Lines(r.calls()) {
		if strings.HasPrefix(line, "buildctl ") {
			return strings.TrimSuffix(line, "\n")
		}
	}
	t.Fatalf("buildctl wasn't called: %q", r.calls())
	return ""
}

func TestBuildScriptBuildsAndPushes(t *testing.T) {
	r := newBuiltRun(t)
	stdout, stderr, code := r.run(BuildScript, buildEnv()...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	call := buildctlCall(t, r)
	src := filepath.Join(r.alloc, "data", "src")
	for _, want := range []string{
		"build --progress=plain --frontend dockerfile.v0", "--local context=" + src, "--local dockerfile=" + src,
		"--opt filename=Dockerfile", "--output type=image,name=ghcr.io/acme/shop:git-4e0a77c1b2d3,push=true",
		"--metadata-file " + r.path("build.json"),
	} {
		if !strings.Contains(call, want) {
			t.Errorf("call = %q, want %q", call, want)
		}
	}
	for _, not := range []string{"registry.insecure", "cache"} {
		if strings.Contains(call, not) {
			t.Errorf("call = %q must not have %q: it wasn't asked for", call, not)
		}
	}
	if !strings.Contains(r.calls(), "BUILDKITD_FLAGS=--oci-worker-no-process-sandbox") {
		t.Errorf("calls = %q: the builder runs with the task's flags", r.calls())
	}
	if !strings.HasSuffix(strings.TrimSpace(stdout), BuildMarker+" pushed ghcr.io/acme/shop:git-4e0a77c1b2d3") {
		t.Errorf("stdout = %q, want it to end saying what was pushed", stdout)
	}
	if b, err := os.ReadFile(r.path("build.json")); err != nil || !strings.Contains(string(b), testDigest) {
		t.Errorf("build.json = %q, %v", b, err)
	}
}

func TestBuildScriptFolderAndDockerfile(t *testing.T) {
	r := newBuiltRun(t)
	_, stderr, code := r.run(BuildScript, buildEnv("BUILD_PATH=services/web", "BUILD_DOCKERFILE=docker/Dockerfile.prod")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	call := buildctlCall(t, r)
	ctx := filepath.Join(r.alloc, "data", "src", "services/web")
	for _, want := range []string{"--local context=" + ctx, "--local dockerfile=" + ctx + "/docker", "--opt filename=Dockerfile.prod"} {
		if !strings.Contains(call, want) {
			t.Errorf("call = %q, want %q", call, want)
		}
	}
}

func TestBuildScriptInsecureOnlyWhenAsked(t *testing.T) {
	r := newBuiltRun(t)
	if _, stderr, code := r.run(BuildScript, buildEnv("BUILD_INSECURE=1", "BUILD_IMAGE=127.0.0.1:5001/gate/app")...); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if call := buildctlCall(t, r); !strings.Contains(call, "name=127.0.0.1:5001/gate/app:git-4e0a77c1b2d3,push=true,registry.insecure=true") {
		t.Errorf("call = %q, want an insecure push to the loopback registry", call)
	}
}

// With a cache the layers are exported, and imported only when an earlier build
// left an index.
func TestBuildScriptCache(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cacheOn    bool
		index      bool
		wantExport bool
		wantImport bool
	}{
		{"no cache", false, false, false, false},
		{"first build", true, false, true, false},
		{"later build", true, true, true, true},
		{"index but cache off", false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBuiltRun(t)
			if tc.index {
				r.write("cache/index.json", "{}")
			}
			env := buildEnv()
			if tc.cacheOn {
				env = append(env, "BUILD_CACHE_MB=5120")
			}
			if _, stderr, code := r.run(BuildScript, env...); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			call := buildctlCall(t, r)
			export := "--export-cache type=local,dest=" + r.cache + ",mode=max"
			imp := "--import-cache type=local,src=" + r.cache
			if strings.Contains(call, export) != tc.wantExport || strings.Contains(call, imp) != tc.wantImport {
				t.Errorf("call = %q, want export %v and import %v", call, tc.wantExport, tc.wantImport)
			}
		})
	}
}

func TestBuildScriptFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"build fails", []string{"FAKE_BUILD_FAIL=1"}, BuildMarker + " " + BuildFailed},
		{"no digest", []string{"FAKE_NO_DIGEST=1"}, BuildMarker + " " + BuildNoDigest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBuiltRun(t)
			stdout, stderr, code := r.run(BuildScript, buildEnv(tc.env...)...)
			if code != 1 {
				t.Fatalf("exit %d, want 1: %s%s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
			if strings.Contains(stdout, "pushed") {
				t.Errorf("stdout = %q says it pushed", stdout)
			}
		})
	}
}

// A commit that isn't hex, or isn't there, stops the build before it starts:
// the value goes into an image name.
func TestBuildScriptRefusesABadCommit(t *testing.T) {
	for name, source := range map[string]string{
		"not hex": "COMMIT=" + base64.StdEncoding.EncodeToString([]byte("main;rm -rf /")) + "\n",
		"empty":   "COMMIT=\n",
		"missing": "AUTHOR=QW5h\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newBuiltRun(t)
			r.write("alloc/data/caravana/source.env", source)
			_, stderr, code := r.run(BuildScript, buildEnv()...)
			if code != 1 || !strings.Contains(stderr, "the source task left no commit") {
				t.Errorf("exit %d, stderr %q", code, stderr)
			}
			if strings.Contains(r.calls(), "buildctl") {
				t.Error("buildctl ran with a commit that isn't one")
			}
		})
	}
}

// No secret reaches a command line, whichever script runs: the key, the token
// and the push credentials are files the tools read themselves.
func TestBuildScriptsKeepSecretsOffCommandLines(t *testing.T) {
	r := newBuildRun(t, map[string]string{"git": buildFakeGit, "du": buildFakeDU, "buildctl-daemonless.sh": buildFakeBuildctl})
	r.write("tree/Dockerfile", "FROM scratch\n")
	r.write("secrets/deploy_key", buildSentinel)
	r.write("secrets/git-credentials", "https://x:"+buildSentinel+"@github.com\n")
	r.write("secrets/docker/config.json", `{"auths":{"ghcr.io":{"auth":"`+buildSentinel+`"}}}`)
	env := sourceEnv("BUILD_ACCESS=token", "BUILD_CACHE_MB=5120", "FAKE_DU=1")
	if _, stderr, code := r.run(BuildSourceScript, env...); code != 0 {
		t.Fatalf("source: exit %d: %s", code, stderr)
	}
	benv := buildEnv("BUILD_CACHE_MB=5120", "DOCKER_CONFIG="+r.secrets+"/docker")
	if _, stderr, code := r.run(BuildScript, benv...); code != 0 {
		t.Fatalf("build: exit %d: %s", code, stderr)
	}
	if strings.Contains(r.calls(), buildSentinel) {
		t.Errorf("a secret reached a command line or the tools' environment:\n%s", r.calls())
	}
}

// The scripts must not use ${...}: Nomad interpolates it in task config.
func TestBuildScriptsHaveNoNomadInterpolation(t *testing.T) {
	for name, script := range map[string]string{"source": BuildSourceScript, "build": BuildScript} {
		if strings.Contains(script, "${") {
			t.Errorf("the %s script has ${...}, which Nomad would interpolate", name)
		}
		if strings.Contains(script, "echo ") {
			t.Errorf("the %s script uses echo: dash's echo reads backslashes; use printf", name)
		}
	}
}
