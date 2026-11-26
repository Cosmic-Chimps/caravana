package nomadjob

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// openSearchBackupCases is a service with an opensearch backup (feature 061): the
// cluster's data on one volume and a snapshot folder on another, path.repo set
// in the environment.
func openSearchBackupCases() []goldenCase {
	logs := app.App{
		ID:     "searchlogs",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{
			Name:   "indexer",
			Image:  "opensearchproject/opensearch:2.19.1",
			Ports:  []app.Port{{Name: "http", ContainerPort: 9200}},
			Health: &app.HealthCheck{},
			Env:    []app.EnvVar{{Name: "path.repo", Value: "/usr/share/opensearch/snapshots"}, {Name: "discovery.type", Value: "single-node"}},
			Secrets: []app.Secret{
				{Name: "OPENSEARCH_INITIAL_ADMIN_PASSWORD"},
			},
			Volumes: []app.Volume{
				{Name: "data", Path: "/usr/share/opensearch/data"},
				{Name: "snapshots", Path: "/usr/share/opensearch/snapshots"},
			},
			Backup: &app.Backup{Engine: app.BackupOpenSearch, Repository: "/usr/share/opensearch/snapshots", Schedule: "0 2 * * *", Keep: 5},
		}},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"indexer": "opensearchproject/opensearch@sha256:" + strings.Repeat("a", 64),
	}}
	return []goldenCase{{name: "opensearch-backup", app: logs, deployment: dep, settings: labSettings()}}
}

// TestOpenSearchTasks: a service with an opensearch backup gets caravana-upload
// with the snapshot folder's volume, writable, and no caravana-sqlite.
func TestOpenSearchTasks(t *testing.T) {
	tc := openSearchBackupCases()[0]
	tg := translated(t, tc).TaskGroups[0]
	var names []string
	for _, task := range tg.Tasks {
		names = append(names, task.Name)
	}
	if want := []string{"indexer", VolumeTaskName, app.UploadTaskName}; !slices.Equal(names, want) {
		t.Fatalf("tasks = %v, want %v", names, want)
	}
	up := tg.Tasks[2]
	if up.Lifecycle == nil || up.Lifecycle.Hook != "poststart" || !up.Lifecycle.Sidecar {
		t.Errorf("lifecycle = %+v, want a poststart sidecar", up.Lifecycle)
	}
	if len(up.VolumeMounts) != 1 {
		t.Fatalf("mounts = %d, want only the snapshot folder's volume", len(up.VolumeMounts))
	}
	if m := up.VolumeMounts[0]; *m.Volume != "snapshots" || *m.Destination != "/caravana-volumes/snapshots" || *m.ReadOnly {
		t.Errorf("mount = %s at %s read-only %v, want snapshots at /caravana-volumes/snapshots, writable", *m.Volume, *m.Destination, *m.ReadOnly)
	}
	if len(up.Templates) != 0 || len(up.Env) != 0 {
		t.Error("caravana-upload has templates or env: the destination never reaches the job")
	}
	if tg.Volumes["snapshots"].ReadOnly {
		t.Error("the group asks for the snapshot volume read-only")
	}
}

func TestOpenSearchPaths(t *testing.T) {
	tests := map[string]struct{ got, want string }{
		"object":   {OpenSearchObject("b/logs/indexer", "1791"), "b/logs/indexer/1791.opensearch.tar.gz"},
		"folder":   {OpenSearchFolderName("indexer-3"), "caravana-indexer-3"},
		"repo":     {OpenSearchRepositoryDir("snapshots", ""), "/caravana-volumes/snapshots"},
		"repo in":  {OpenSearchRepositoryDir("data", "snaps/os"), "/caravana-volumes/data/snaps/os"},
		"result":   {OpenSearchResultPath("indexer-3", "snapshot"), "/alloc/data/caravana/opensearch/indexer-3.snapshot.result"},
		"fs":       {OpenSearchResultFile("indexer-3", "upload"), "alloc/data/caravana/opensearch/indexer-3.upload.result"},
		"login":    {OpenSearchLoginRefused, "OpenSearch refused the login: set OPENSEARCH_INITIAL_ADMIN_PASSWORD, INDEXER_PASSWORD or ELASTIC_PASSWORD"},
		"command":  {strings.Join(OpenSearchCommand("echo $1", "a"), "|"), "/bin/sh|-c|echo $1|sh|a"},
		"ext only": {OpenSearchObjectExt, ".opensearch.tar.gz"},
	}
	for name, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", name, tt.got, tt.want)
		}
	}
	// The hint names the variables the login is found with, and no others.
	for _, name := range app.BackupPasswordNames(app.BackupOpenSearch) {
		if !strings.Contains(OpenSearchLoginRefused, name) {
			t.Errorf("the login hint doesn't name %s", name)
		}
	}
}

// The scripts use $VAR and $(...) only: Nomad interpolates "${...}" in task config.
func TestOpenSearchScriptsAvoidNomadInterpolation(t *testing.T) {
	for name, text := range map[string]string{
		"OpenSearchCleanScript":    OpenSearchCleanScript,
		"OpenSearchSnapshotScript": OpenSearchSnapshotScript,
		"OpenSearchRestoreScript":  OpenSearchRestoreScript,
		"OpenSearchUploadScript":   OpenSearchUploadScript,
		"OpenSearchDownloadScript": OpenSearchDownloadScript,
	} {
		if strings.Contains(text, "${") {
			t.Errorf("%s contains \"${\", which Nomad interpolates", name)
		}
		if strings.Contains(text, "pipefail") {
			t.Errorf("%s uses pipefail, which BusyBox ash may not have", name)
		}
	}
}

func TestParseOpenSearchOutput(t *testing.T) {
	out := ParseSQLiteOutput("caravana-opensearch-snapshot indices=4\ncaravana-opensearch-restore indices=2\n" +
		"caravana-opensearch-download files=17\ncaravana-backup key=b/x/1791.opensearch.tar.gz bytes=900\n" +
		"caravana-expired 1700.opensearch.tar.gz\ncaravana-expired 1701.sqlite.gz\ncaravana-warning slow\n")
	if out.Indices != 2 || out.Files != 17 || out.Object != "b/x/1791.opensearch.tar.gz" || out.Bytes != 900 ||
		!slices.Equal(out.Expired, []string{"1700", "1701"}) || out.Warning != "slow" {
		t.Errorf("output = %+v", out)
	}
	if got := ParseSQLiteOutput("caravana-opensearch-snapshot indices=x\ncaravana-opensearch-download files=-1\n"); got.Indices != 0 || got.Files != 0 {
		t.Errorf("a marker without a number gave %+v", got)
	}
}

// --- The scripts, under a real sh with fake curl, rclone and tar.

// fakeCurl answers like a cluster. It records every call and the configuration
// it was handed on its standard input (the login), and it plays a small
// OpenSearch: registering the repository makes its folder, taking a snapshot
// writes files into it. Rules in $FAKE_RULES ("METHOD<TAB>path prefix<TAB>code<TAB>
// body", the first that matches wins) override an answer; FAKE_HTTP_ONLY makes
// the TLS handshake fail (curl 35); FAKE_REFUSE refuses the connection;
// FAKE_WANT_CONFIG is the one login configuration it accepts.
const fakeCurl = `#!/bin/sh
echo "curl $*" >> "$FAKE_LOG"
out=; method=GET; url=; data=
while [ $# -gt 0 ]; do
  case "$1" in
    -K) shift; [ "$1" != - ] || cat >> "$FAKE_CURL_STDIN" ;;
    -o) shift; out=$1 ;;
    -X) shift; method=$1 ;;
    --max-time) shift; echo "max-time $1" >> "$FAKE_LOG" ;;
    --data-binary) shift; data=$1 ;;
    -w|-H) shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
scheme=${url%%://*}
if [ "$scheme" = https ] && [ -n "$FAKE_HTTP_ONLY" ]; then
  echo "curl: (35) error:0A00010B:SSL routines::wrong version number" >&2
  exit 35
fi
if [ -n "$FAKE_REFUSE" ]; then
  echo "curl: (7) Failed to connect to 127.0.0.1 port 9200 after 0 ms: Couldn't connect to server" >&2
  exit 7
fi
path=${url#*://127.0.0.1:9200}
echo "call $method $path $scheme" >> "$FAKE_LOG"
[ -z "$data" ] || echo "data $data" >> "$FAKE_LOG"
if [ "$path" = / ]; then
  [ "$out" = /dev/null ] || echo '{"version":{}}' > "$out"
  exit 0
fi
reply() {
  printf '%s' "$2" > "$out"
  printf '%s' "$1"
  exit 0
}
if [ -n "$FAKE_WANT_CONFIG" ]; then
  if [ "$(cat "$FAKE_CURL_STDIN" 2>/dev/null | tail -n 1)" != "$FAKE_WANT_CONFIG" ]; then
    reply 401 'Unauthorized'
  fi
fi
if [ -f "$FAKE_RULES" ]; then
  while IFS='	' read -r rm rp rc rb; do
    case "$path" in
      "$rp"*) [ "$method" = "$rm" ] && reply "$rc" "$rb" ;;
    esac
  done < "$FAKE_RULES"
fi
case "$method $path" in
  "PUT /_snapshot/caravana")
    location=$(printf '%s' "$data" | sed -n 's/.*"location":"\([^"]*\)".*/\1/p')
    mkdir -p "$location" && echo "$location" > "$FAKE_STATE/location"
    reply 200 '{"acknowledged":true}' ;;
  "PUT /_snapshot/caravana/"*)
    location=$(cat "$FAKE_STATE/location")
    mkdir -p "$location/indices/abc/0" && echo latest > "$location/index.latest" && echo meta > "$location/meta-1.dat" && echo shard > "$location/indices/abc/0/__1"
    reply 200 '{"snapshot":{"snapshot":"1","uuid":"u","indices":["logs-1","orders"],"state":"SUCCESS","shards":{"total":2,"failed":0,"successful":2}}}' ;;
  "GET /_snapshot/caravana/_all")
    reply 200 '{"snapshots":[{"snapshot":"1700000000","uuid":"u","indices":["logs-1","orders"],"data_streams":[],"state":"SUCCESS","shards":{"total":2,"failed":0,"successful":2}}]}' ;;
  "POST /_snapshot/caravana/"*"/_restore"*)
    reply 200 '{"snapshot":{"snapshot":"1700000000","indices":["logs-1","orders"],"shards":{"total":2,"failed":0,"successful":2}}}' ;;
  *) reply 200 '{"acknowledged":true}' ;;
esac
`

// fakeOSTar can fail the archive or the extract, as a full disk would.
const fakeOSTar = `#!/bin/sh
[ -n "$UNDER_TIMEOUT" ] || { echo "tar ran outside timeout" >&2; exit 99; }
case "$*" in
  *-czf*) if [ -n "$FAKE_FAIL_TAR_C" ]; then echo "tar: $FAKE_FAIL_TAR_C" >&2; echo partial; exit 2; fi ;;
  *-xzf*) if [ -n "$FAKE_FAIL_TAR_X" ]; then echo "tar: $FAKE_FAIL_TAR_X" >&2; exit 2; fi ;;
esac
exec REALTAR "$@"
`

var (
	osTools     sync.Once
	osToolsPath string
	osToolsErr  error
)

func openSearchToolsDir(t *testing.T) string {
	t.Helper()
	realTar, err := exec.LookPath("tar")
	if err != nil {
		t.Skipf("tar not found: %v", err)
	}
	for _, tool := range []string{"sh", "grep", "sed", "cut", "tr", "head", "tail", "wc", "find", "mktemp", "basename", "dirname", "cat", "rm", "mkdir", "chmod", "date", "sort"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	osTools.Do(func() {
		osToolsPath, osToolsErr = os.MkdirTemp("", "caravana-opensearch-tools-")
		if osToolsErr != nil {
			return
		}
		for name, text := range map[string]string{
			"timeout": fakeTimeout,
			"rclone":  fakeSQLiteRclone,
			"curl":    fakeCurl,
			"tar":     strings.ReplaceAll(fakeOSTar, "REALTAR", realTar),
		} {
			if osToolsErr = os.WriteFile(filepath.Join(osToolsPath, name), []byte(text), 0o755); osToolsErr != nil {
				return
			}
		}
	})
	if osToolsErr != nil {
		t.Fatalf("fake tools: %v", osToolsErr)
	}
	return osToolsPath
}

// osRun is a directory to run the opensearch scripts in: a volume "snapshots"
// under vols (the sidecar's view), the work directory and the fake bucket.
type osRun struct {
	*sqliteRun
	state, rules, stdinLog string
}

func newOpenSearchRun(t *testing.T) *osRun {
	t.Helper()
	r := &osRun{sqliteRun: &sqliteRun{t: t, dir: t.TempDir(), bin: openSearchToolsDir(t)}}
	r.vols = filepath.Join(r.dir, "vols")
	r.work = filepath.Join(r.dir, "alloc", "data", "caravana", "opensearch")
	r.bucket = filepath.Join(r.dir, "bucket")
	r.log = filepath.Join(r.dir, "calls.log")
	r.state = filepath.Join(r.dir, "state")
	r.rules = filepath.Join(r.dir, "rules")
	r.stdinLog = filepath.Join(r.dir, "curl-stdin.log")
	for _, d := range []string{filepath.Join(r.vols, "snapshots"), r.bucket, r.state, r.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// run runs script with the fake cluster's environment added.
func (r *osRun) run(script, stdin string, env []string, args ...string) execResult {
	r.t.Helper()
	env = append(slices.Clone(env), "FAKE_STATE="+r.state, "FAKE_RULES="+r.rules, "FAKE_CURL_STDIN="+r.stdinLog, "TMPDIR="+r.dir)
	return r.exec(script, stdin, env, args...)
}

// rule makes the fake cluster answer method path... with code and body.
func (r *osRun) rule(method, prefix, code, body string) {
	r.t.Helper()
	f, err := os.OpenFile(r.rules, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(method + "\t" + prefix + "\t" + code + "\t" + body + "\n"); err != nil {
		r.t.Fatal(err)
	}
}

// calls lists "METHOD path" of every API call the fake cluster answered.
func (r *osRun) calls() []string {
	var out []string
	for line := range strings.Lines(r.callLog()) {
		if rest, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "call "); ok {
			out = append(out, rest)
		}
	}
	return out
}

// bodies lists the JSON bodies sent.
func (r *osRun) bodies() []string {
	var out []string
	for line := range strings.Lines(r.callLog()) {
		if rest, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "data "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func (r *osRun) repoDir() string { return filepath.Join(r.vols, "snapshots") }

// A folder as OpenSearch leaves it after a snapshot.
func (r *osRun) snapshotFolder(request string) string {
	folder := filepath.Join(r.repoDir(), "caravana-"+request)
	r.write(filepath.Join(folder, "index.latest"), "latest")
	r.write(filepath.Join(folder, "meta-x.dat"), "meta")
	r.write(filepath.Join(folder, "indices", "abc", "0", "__1"), "shard data")
	return folder
}

func TestOpenSearchCleanScript(t *testing.T) {
	t.Run("removes the folders of earlier runs, only those", func(t *testing.T) {
		r := newOpenSearchRun(t)
		repo := r.repoDir()
		r.write(filepath.Join(repo, "caravana-old-1", "index.latest"), "x")
		r.write(filepath.Join(repo, "caravana-old-2", "deep", "file"), "x")
		r.write(filepath.Join(repo, "mine", "keep"), "mine")
		r.write(filepath.Join(repo, "caravana-file"), "a file, not a folder")
		if err := os.Symlink(filepath.Join(repo, "mine"), filepath.Join(repo, "caravana-link")); err != nil {
			t.Fatal(err)
		}
		res := r.run(OpenSearchCleanScript, "", nil, repo, r.work, "fresh")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		for _, gone := range []string{"caravana-old-1", "caravana-old-2"} {
			if exists(filepath.Join(repo, gone)) {
				t.Errorf("%s is still there", gone)
			}
		}
		for _, kept := range []string{"mine/keep", "caravana-file", "caravana-link"} {
			if !exists(filepath.Join(repo, kept)) {
				t.Errorf("%s was removed", kept)
			}
		}
		if got := r.read(filepath.Join(repo, "mine", "keep")); got != "mine" {
			t.Errorf("a link's target was changed: %q", got)
		}
	})
	t.Run("the first clean of a run empties the work directory, the last one doesn't", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.write(filepath.Join(r.work, "old.snapshot.result"), "x")
		r.write(filepath.Join(r.work, "old.upload.result.work", "tar.err"), "x")
		r.run(OpenSearchCleanScript, "", nil, r.repoDir(), r.work, "keep")
		if got := r.workFiles(); len(got) != 2 {
			t.Fatalf("work after a keep clean = %v, want both kept", got)
		}
		res := r.run(OpenSearchCleanScript, "", nil, r.repoDir(), r.work, "fresh")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if got := r.workFiles(); len(got) != 0 {
			t.Errorf("work after a fresh clean = %v, want empty", got)
		}
	})
	t.Run("makes the work directory writable by the service's user, and a missing repository is fine", func(t *testing.T) {
		r := newOpenSearchRun(t)
		fresh := filepath.Join(r.dir, "new", "work")
		res := r.run(OpenSearchCleanScript, "", nil, filepath.Join(r.dir, "no-such-repo"), fresh, "fresh")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		info, err := os.Stat(fresh)
		if err != nil || info.Mode().Perm() != 0o777 {
			t.Errorf("work dir = %v, %v; want mode 0777", info, err)
		}
	})
	t.Run("a path that isn't absolute is refused", func(t *testing.T) {
		r := newOpenSearchRun(t)
		if res := r.run(OpenSearchCleanScript, "", nil, "relative", r.work, "fresh"); res.code == 0 || !strings.HasPrefix(res.lastLine(), "clean: ") {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})
}

const sentinelPassword = `pw-sentinel-061 "quoted" back\slash $HOME $(id) 'q'`

// snapshot runs the snapshot script for request "indexer-1", ts 1791, port 9200.
func (r *osRun) snapshot(stdin string, env ...string) execResult {
	return r.run(OpenSearchSnapshotScript, stdin, env, "/usr/share/opensearch/snapshots", "indexer-1", "1791", "9200", "600",
		filepath.Join(r.work, "indexer-1.snapshot.result"))
}

func (r *osRun) restore(stdin string, env ...string) execResult {
	return r.run(OpenSearchRestoreScript, stdin, env, "/usr/share/opensearch/snapshots", "indexer-1", "9200", "600",
		filepath.Join(r.work, "indexer-1.restore.result"))
}

func TestOpenSearchSnapshotScript(t *testing.T) {
	t.Run("registers the folder, snapshots every non-dot index, and unregisters", func(t *testing.T) {
		r := newOpenSearchRun(t)
		res := r.snapshot("")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		wantCalls := []string{
			"GET / https", // the probe: https first
			"DELETE /_snapshot/caravana https",
			"PUT /_snapshot/caravana https",
			"PUT /_snapshot/caravana/1791?wait_for_completion=true https",
			"DELETE /_snapshot/caravana https",
		}
		if got := r.calls(); !slices.Equal(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
		bodies := r.bodies()
		if len(bodies) != 2 ||
			bodies[0] != `{"type":"fs","settings":{"location":"/usr/share/opensearch/snapshots/caravana-indexer-1"}}` ||
			bodies[1] != `{"indices":"*,-.*","include_global_state":false}` {
			t.Errorf("bodies = %q", bodies)
		}
		if out := ParseSQLiteOutput(res.stdout); out.Indices != 2 {
			t.Errorf("output = %+v, want 2 indices", out)
		}
		if got := r.read(filepath.Join(r.work, "indexer-1.snapshot.result")); got != res.stdout {
			t.Errorf("result file = %q, want stdout %q", got, res.stdout)
		}
		if strings.Contains(r.callLog(), "timeout") {
			t.Error("the service's script used timeout, which its image may not have")
		}
	})

	t.Run("every call counts against what is left of the limit", func(t *testing.T) {
		r := newOpenSearchRun(t)
		if res := r.snapshot(""); res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		n := 0
		for line := range strings.Lines(r.callLog()) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "max-time "); ok {
				n++
				if v == "0" || len(v) > 3 {
					t.Errorf("max-time %s, want 1 to 600", v)
				}
			}
		}
		if n < 5 {
			t.Errorf("%d calls with --max-time, want every one", n)
		}
	})

	t.Run("the login is a curl configuration on stdin, escaped, and in no argument", func(t *testing.T) {
		r := newOpenSearchRun(t)
		stdin := "OPENSEARCH_USER=admin\nOPENSEARCH_PASSWORD=" + sentinelPassword + "\n"
		want := `user = "admin:pw-sentinel-061 \"quoted\" back\\slash $HOME $(id) 'q'"`
		res := r.snapshot(stdin, "FAKE_WANT_CONFIG="+want)
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if got := r.read(r.stdinLog); !strings.Contains(got, want+"\n") {
			t.Errorf("curl's configuration = %q, want the line %q", got, want)
		}
		for _, text := range []string{r.callLog(), res.stdout, res.stderr} {
			if strings.Contains(text, "pw-sentinel-061") || strings.Contains(text, "admin:") {
				t.Errorf("the login is in an argument, a log or an output: %q", text)
			}
		}
		if !strings.Contains(r.callLog(), "curl -K - ") {
			t.Error("the calls didn't take their login with -K -")
		}
	})

	t.Run("a wrong login is the hint, naming the variables", func(t *testing.T) {
		r := newOpenSearchRun(t)
		res := r.snapshot("OPENSEARCH_USER=admin\nOPENSEARCH_PASSWORD=nope\n", `FAKE_WANT_CONFIG=user = "admin:right"`)
		if res.code == 0 || res.lastLine() != "snapshot: "+OpenSearchLoginRefused {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})

	t.Run("no login at all calls without credentials", func(t *testing.T) {
		r := newOpenSearchRun(t)
		if res := r.snapshot(""); res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if strings.Contains(r.callLog(), "-K") {
			t.Error("a call carried a configuration with no login")
		}
	})

	t.Run("a line break in the login is refused", func(t *testing.T) {
		r := newOpenSearchRun(t)
		res := r.snapshot("OPENSEARCH_USER=admin\nOPENSEARCH_PASSWORD=a\rb\n")
		if res.code == 0 || res.lastLine() != "snapshot: the login has a line break" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if len(r.calls()) != 0 {
			t.Errorf("calls were made: %v", r.calls())
		}
	})

	t.Run("falls back to http only when the TLS handshake fails", func(t *testing.T) {
		r := newOpenSearchRun(t)
		res := r.snapshot("", "FAKE_HTTP_ONLY=1")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		for _, c := range r.calls() {
			if !strings.HasSuffix(c, " http") {
				t.Errorf("call %q went over https after the handshake failed", c)
			}
		}
		if len(r.calls()) != 5 {
			t.Errorf("calls = %v, want the probe and four calls", r.calls())
		}
	})

	t.Run("a refused connection is not retried over http", func(t *testing.T) {
		r := newOpenSearchRun(t)
		res := r.snapshot("", "FAKE_REFUSE=1")
		if res.code == 0 || !strings.HasPrefix(res.lastLine(), "snapshot: curl: (7) Failed to connect") {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if got := strings.Count(r.callLog(), "curl -sSk"); got != 1 {
			t.Errorf("curl ran %d times, want only the https probe", got)
		}
	})

	t.Run("a repository outside path.repo is OpenSearch's own sentence", func(t *testing.T) {
		r := newOpenSearchRun(t)
		reason := `[caravana] location [/usr/share/opensearch/snapshots/caravana-indexer-1] doesn't match any of the locations specified by path.repo because this setting is empty`
		r.rule("PUT", "/_snapshot/caravana", "500", `{"error":{"root_cause":[{"type":"repository_exception","reason":"`+reason+`"}],"type":"repository_exception","reason":"`+reason+`"},"status":500}`)
		res := r.snapshot("")
		if res.code == 0 || res.lastLine() != "snapshot: "+reason {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})

	t.Run("a reason with escaped quotes is unescaped", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("PUT", "/_snapshot/caravana", "400", `{"error":{"reason":"bad \"setting\" here \\ ok"},"status":400}`)
		res := r.snapshot("")
		if res.lastLine() != `snapshot: bad "setting" here \ ok` {
			t.Errorf("last line %q", res.lastLine())
		}
	})

	t.Run("an answer with no reason says the status", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("PUT", "/_snapshot/caravana/", "503", `Service Unavailable`)
		res := r.snapshot("")
		if res.code == 0 || res.lastLine() != "snapshot: OpenSearch answered HTTP 503" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})

	t.Run("a failed shard fails the run and the repository is unregistered", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("PUT", "/_snapshot/caravana/", "200",
			`{"snapshot":{"snapshot":"1791","indices":["a"],"state":"PARTIAL","failures":[{"index":"a","shard_id":0,"reason":"disk full"}],"shards":{"total":2,"failed":1,"successful":1}}}`)
		res := r.snapshot("")
		if res.code == 0 || res.lastLine() != "snapshot: the snapshot ended partial: disk full" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		calls := r.calls()
		if len(calls) < 4 || calls[len(calls)-1] != "DELETE /_snapshot/caravana https" {
			t.Errorf("calls = %v, want a last unregister", calls)
		}
	})

	t.Run("a success with a failed shard is not a success", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("PUT", "/_snapshot/caravana/", "200", `{"snapshot":{"state":"SUCCESS","shards":{"total":2,"failed":1,"successful":1}}}`)
		if res := r.snapshot(""); res.code == 0 {
			t.Error("the run succeeded")
		}
	})

	t.Run("the inputs are checked", func(t *testing.T) {
		r := newOpenSearchRun(t)
		for name, args := range map[string][]string{
			"timestamp": {"/r", "indexer-1", "17x91", "9200", "600", ""},
			"request":   {"/r", "../x", "1791", "9200", "600", ""},
			"port":      {"/r", "indexer-1", "1791", "92 00", "600", ""},
			"path":      {"r", "indexer-1", "1791", "9200", "600", ""},
		} {
			res := r.run(OpenSearchSnapshotScript, "", nil, args...)
			if res.code == 0 || !strings.HasPrefix(res.lastLine(), "snapshot: ") {
				t.Errorf("%s: exit %d, last line %q", name, res.code, res.lastLine())
			}
		}
		if len(r.calls()) != 0 {
			t.Errorf("calls were made: %v", r.calls())
		}
	})
}

func TestOpenSearchRestoreScript(t *testing.T) {
	t.Run("closes what exists, restores the snapshot's indices and unregisters", func(t *testing.T) {
		r := newOpenSearchRun(t)
		res := r.restore("")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		wantCalls := []string{
			"GET / https",
			"DELETE /_snapshot/caravana https",
			"PUT /_snapshot/caravana https",
			"GET /_snapshot/caravana/_all https",
			"POST /logs-1,orders/_close?ignore_unavailable=true https",
			"POST /_snapshot/caravana/1700000000/_restore?wait_for_completion=true https",
			"DELETE /_snapshot/caravana https",
		}
		if got := r.calls(); !slices.Equal(got, wantCalls) {
			t.Errorf("calls = %v, want %v", got, wantCalls)
		}
		bodies := r.bodies()
		if len(bodies) != 2 ||
			bodies[0] != `{"type":"fs","settings":{"location":"/usr/share/opensearch/snapshots/caravana-indexer-1","readonly":true}}` ||
			bodies[1] != `{"indices":"logs-1,orders","include_global_state":false}` {
			t.Errorf("bodies = %q", bodies)
		}
		if out := ParseSQLiteOutput(res.stdout); out.Indices != 2 {
			t.Errorf("output = %+v, want 2 indices", out)
		}
	})

	t.Run("a restore that fails opens the indices again and says so", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("POST", "/_snapshot/caravana/", "500", `{"error":{"reason":"shard failed to recover"},"status":500}`)
		res := r.restore("")
		if res.code == 0 || res.lastLine() != "restore: shard failed to recover; the indices it closed were opened again" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		calls := r.calls()
		if !slices.Contains(calls, "POST /logs-1,orders/_open?ignore_unavailable=true https") {
			t.Errorf("calls = %v, want the open", calls)
		}
		if calls[len(calls)-1] != "DELETE /_snapshot/caravana https" {
			t.Errorf("the last call = %q, want the unregister", calls[len(calls)-1])
		}
	})

	t.Run("when it can't open them again it says they may still be closed", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("POST", "/_snapshot/caravana/", "500", `{"error":{"reason":"boom"},"status":500}`)
		r.rule("POST", "/logs-1,orders/_open", "500", `{"error":{"reason":"nope"},"status":500}`)
		res := r.restore("")
		if res.code == 0 || res.lastLine() != "restore: boom; opening the indices it closed again failed, so they may still be closed" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})

	t.Run("failed shards in a good answer fail the restore", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("POST", "/_snapshot/caravana/", "200", `{"snapshot":{"shards":{"total":2,"failed":1,"successful":1}}}`)
		res := r.restore("")
		if res.code == 0 || !strings.HasPrefix(res.lastLine(), "restore: some shards could not be restored; the indices it closed were opened again") {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})

	t.Run("a close that fails opens them again too", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("POST", "/logs-1,orders/_close", "403", `{"error":{"reason":"no permissions for [indices:admin/close]"},"status":403}`)
		res := r.restore("")
		if res.code == 0 || res.lastLine() != "restore: no permissions for [indices:admin/close]; the indices it closed were opened again" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if slices.ContainsFunc(r.calls(), func(c string) bool { return strings.Contains(c, "_restore") }) {
			t.Error("it restored after the close failed")
		}
	})

	t.Run("a snapshot with no indices restores nothing", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("GET", "/_snapshot/caravana/_all", "200", `{"snapshots":[{"snapshot":"1700000000","indices":[],"state":"SUCCESS"}]}`)
		res := r.restore("")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		for _, c := range r.calls() {
			if strings.Contains(c, "_close") || strings.Contains(c, "_restore") {
				t.Errorf("call %q for a snapshot with no indices", c)
			}
		}
		if out := ParseSQLiteOutput(res.stdout); out.Indices != 0 || !strings.Contains(res.stdout, "indices=0") {
			t.Errorf("stdout = %q", res.stdout)
		}
	})

	t.Run("a snapshot that isn't complete is refused before anything is closed", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("GET", "/_snapshot/caravana/_all", "200", `{"snapshots":[{"snapshot":"1700000000","indices":["a"],"state":"PARTIAL"}]}`)
		res := r.restore("")
		if res.code == 0 || res.lastLine() != "restore: the snapshot in the backup is not complete (state partial)" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if slices.ContainsFunc(r.calls(), func(c string) bool { return strings.Contains(c, "_close") }) {
			t.Error("an index was closed")
		}
	})

	t.Run("an index name it can't put in a URL is refused before anything is closed", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("GET", "/_snapshot/caravana/_all", "200", `{"snapshots":[{"snapshot":"1700000000","indices":["ok","bad%2Fname"],"state":"SUCCESS"}]}`)
		res := r.restore("")
		if res.code == 0 || !strings.HasPrefix(res.lastLine(), "restore: the snapshot has an index whose name") {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if slices.ContainsFunc(r.calls(), func(c string) bool { return strings.Contains(c, "_close") }) {
			t.Error("an index was closed")
		}
	})

	t.Run("a folder with no snapshot in it is refused", func(t *testing.T) {
		r := newOpenSearchRun(t)
		r.rule("GET", "/_snapshot/caravana/_all", "200", `{"snapshots":[]}`)
		res := r.restore("")
		if res.code == 0 || res.lastLine() != "restore: the backup holds no snapshot Caravana can read" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})

	t.Run("the login is sent the same way and a refusal is the hint", func(t *testing.T) {
		r := newOpenSearchRun(t)
		stdin := "OPENSEARCH_USER=admin\nOPENSEARCH_PASSWORD=" + sentinelPassword + "\n"
		res := r.restore(stdin, `FAKE_WANT_CONFIG=user = "admin:other"`)
		if res.code == 0 || res.lastLine() != "restore: "+OpenSearchLoginRefused {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if strings.Contains(r.callLog(), "pw-sentinel-061") || strings.Contains(res.stderr, "pw-sentinel-061") {
			t.Error("the password is in a call or the output")
		}
	})
}

const osKey = "sk-sentinel-061"

func osDest() string {
	return strings.Join([]string{
		"RCLONE_CONFIG_DEST_TYPE=s3",
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY=" + osKey,
	}, "\n") + "\n"
}

func (r *osRun) upload(folder, ts, keep, pinned string, env ...string) execResult {
	env = append(slices.Clone(env), "FAKE_SECRET="+osKey)
	return r.run(OpenSearchUploadScript, osDest(), env, folder, "b/logs/indexer", ts, keep, "600", pinned, filepath.Join(r.work, "indexer-1.upload.result"))
}

func (r *osRun) download(object, folder string, env ...string) execResult {
	env = append(slices.Clone(env), "FAKE_SECRET="+osKey)
	return r.run(OpenSearchDownloadScript, osDest(), env, object, folder, "600", filepath.Join(r.work, "indexer-2.download.result"))
}

func TestOpenSearchUploadScript(t *testing.T) {
	t.Run("tars the folder into the destination and removes it", func(t *testing.T) {
		r := newOpenSearchRun(t)
		folder := r.snapshotFolder("indexer-1")
		res := r.upload(folder, "1791", "3", "")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		obj := filepath.Join(r.bucket, "b/logs/indexer/1791.opensearch.tar.gz")
		info, err := os.Stat(obj)
		if err != nil {
			t.Fatal(err)
		}
		listing, err := exec.Command("tar", "-tzf", obj).Output()
		if err != nil || !strings.Contains(string(listing), "index.latest") || !strings.Contains(string(listing), "__1") {
			t.Errorf("the object isn't the snapshot folder (err %v): %s", err, listing)
		}
		out := ParseSQLiteOutput(res.stdout)
		if out.Object != "b/logs/indexer/1791.opensearch.tar.gz" || out.Bytes != info.Size() {
			t.Errorf("output = %+v, want the object and its size %d", out, info.Size())
		}
		if exists(folder) {
			t.Error("the snapshot folder is still there")
		}
		if got := r.workFiles(); !slices.Equal(got, []string{"indexer-1.upload.result"}) {
			t.Errorf("work dir = %v, want only the result", got)
		}
		if got := r.read(filepath.Join(r.work, "indexer-1.upload.result")); got != res.stdout {
			t.Errorf("result file = %q, want stdout %q", got, res.stdout)
		}
		for _, prog := range []string{"tar", "rclone"} {
			if len(r.timeouts(prog)) == 0 {
				t.Errorf("no call to %s ran under timeout", prog)
			}
		}
		for _, text := range []string{res.stdout, res.stderr, r.callLog()} {
			if strings.Contains(text, osKey) {
				t.Error("the secret key is in an output or an argument")
			}
		}
	})

	t.Run("a failed upload leaves no object and removes the folder", func(t *testing.T) {
		r := newOpenSearchRun(t)
		folder := r.snapshotFolder("indexer-1")
		res := r.upload(folder, "1791", "3", "", "FAKE_PARTIAL_RCAT=1")
		if res.code == 0 || res.lastLine() != "upload: Failed to rcat: the connection broke" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if got := r.bucketFiles(); len(got) != 0 {
			t.Errorf("bucket = %v, want the partial object deleted", got)
		}
		if exists(folder) {
			t.Error("the snapshot folder is still there")
		}
	})

	t.Run("a tar that fails is a failed upload even though rclone was happy", func(t *testing.T) {
		r := newOpenSearchRun(t)
		folder := r.snapshotFolder("indexer-1")
		res := r.upload(folder, "1791", "3", "", "FAKE_FAIL_TAR_C=No space left on device")
		if res.code == 0 || res.lastLine() != "upload: tar: No space left on device" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if got := r.bucketFiles(); len(got) != 0 {
			t.Errorf("bucket = %v, want the truncated object deleted", got)
		}
	})

	t.Run("a folder with no snapshot in it is refused, and still removed", func(t *testing.T) {
		r := newOpenSearchRun(t)
		folder := filepath.Join(r.repoDir(), "caravana-indexer-1")
		r.write(filepath.Join(folder, "stray"), "x")
		res := r.upload(folder, "1791", "3", "")
		if res.code == 0 || res.lastLine() != "upload: the snapshot folder has no snapshot in it" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if len(r.bucketFiles()) != 0 {
			t.Error("something was uploaded")
		}
		if exists(folder) {
			t.Error("the folder is still there")
		}
	})

	t.Run("a folder that isn't one of Caravana's is never removed", func(t *testing.T) {
		r := newOpenSearchRun(t)
		folder := filepath.Join(r.repoDir(), "mine")
		r.write(filepath.Join(folder, "index.latest"), "x")
		res := r.upload(folder, "1791", "3", "")
		if res.code == 0 || res.lastLine() != "upload: the snapshot folder's path is not usable" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if !exists(filepath.Join(folder, "index.latest")) {
			t.Error("a folder of the owner's was removed")
		}
	})

	t.Run("a timestamp that is no number is refused", func(t *testing.T) {
		r := newOpenSearchRun(t)
		folder := r.snapshotFolder("indexer-1")
		if res := r.upload(folder, "17x", "3", ""); res.code == 0 || res.lastLine() != "upload: the request has no timestamp" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})
}

// Retention is sqlite's, with its own extension: only <digits>.opensearch.tar.gz
// count, pinned ones stay and don't count against keep, never this run's.
func TestOpenSearchUploadRetention(t *testing.T) {
	tests := []struct {
		name        string
		existing    []string
		keep, ts    string
		pinned      string
		wantLeft    []string
		wantExpired []string
		wantWarning string
	}{
		{name: "the oldest beyond KEEP go", existing: []string{"1700", "1701", "1702", "1703"}, keep: "3", ts: "1791",
			wantLeft: []string{"1702", "1703", "1791"}, wantExpired: []string{"1700", "1701"}},
		{name: "a pinned backup is kept and doesn't count against KEEP", existing: []string{"1700", "1701", "1702", "1703"}, keep: "2", ts: "1791",
			pinned: "1700", wantLeft: []string{"1700", "1703", "1791"}, wantExpired: []string{"1701", "1702"}},
		{name: "the new backup is never deleted, pins or not", existing: []string{"1800", "1801"}, keep: "1", ts: "1791",
			pinned: "1801", wantLeft: []string{"1791", "1800", "1801"}},
		{name: "a pin list that is not digits deletes nothing and says so", existing: []string{"1700", "1701"}, keep: "1", ts: "1791",
			pinned: "1700 $(touch x)", wantLeft: []string{"1700", "1701", "1791"},
			wantWarning: "the backup is kept, but old backups were not removed: the list of pinned backups is not valid"},
		{name: "no KEEP deletes nothing", existing: []string{"1700", "1701"}, keep: "", ts: "1791",
			wantLeft: []string{"1700", "1701", "1791"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newOpenSearchRun(t)
			for _, ts := range tt.existing {
				r.write(filepath.Join(r.bucket, "b/logs/indexer", ts+".opensearch.tar.gz"), "old "+ts)
			}
			// Another engine's objects, or a different name, are not ours to delete.
			others := []string{"b/logs/indexer/1500.sqlite.gz", "b/logs/indexer/1600.tar.gz", "b/logs/indexer/latest.opensearch.tar.gz", "b/logs/indexer/notes/readme"}
			for _, o := range others {
				r.write(filepath.Join(r.bucket, o), "keep me")
			}
			folder := r.snapshotFolder("indexer-1")
			res := r.upload(folder, tt.ts, tt.keep, tt.pinned)
			if res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			out := ParseSQLiteOutput(res.stdout)
			if !slices.Equal(out.Expired, tt.wantExpired) || out.Warning != tt.wantWarning {
				t.Errorf("expired = %v, warning = %q; want %v and %q", out.Expired, out.Warning, tt.wantExpired, tt.wantWarning)
			}
			var left []string
			for _, f := range r.bucketFiles() {
				if name, ok := strings.CutPrefix(f, "b/logs/indexer/"); ok {
					if ts, ok := strings.CutSuffix(name, ".opensearch.tar.gz"); ok && ts != "latest" {
						left = append(left, ts)
					}
				}
			}
			slices.Sort(left)
			slices.Sort(tt.wantLeft)
			if !slices.Equal(left, tt.wantLeft) {
				t.Errorf("backups left = %v, want %v", left, tt.wantLeft)
			}
			for _, o := range others {
				if !slices.Contains(r.bucketFiles(), o) {
					t.Errorf("retention deleted %s, which isn't an opensearch backup", o)
				}
			}
		})
	}
}

func TestOpenSearchDownloadScript(t *testing.T) {
	// A backup made by the upload script, to download.
	made := func(t *testing.T) (*osRun, string) {
		r := newOpenSearchRun(t)
		if res := r.upload(r.snapshotFolder("indexer-1"), "1791", "3", ""); res.code != 0 {
			t.Fatalf("upload: exit %d: %s", res.code, res.stderr)
		}
		return r, "b/logs/indexer/1791.opensearch.tar.gz"
	}

	t.Run("extracts the snapshot into a fresh folder", func(t *testing.T) {
		r, object := made(t)
		folder := filepath.Join(r.repoDir(), "caravana-indexer-2")
		r.write(filepath.Join(folder, "left-from-before"), "x")
		res := r.download(object, folder)
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if got := r.read(filepath.Join(folder, "indices", "abc", "0", "__1")); got != "shard data" {
			t.Errorf("shard file = %q", got)
		}
		if exists(filepath.Join(folder, "left-from-before")) {
			t.Error("the folder wasn't emptied first")
		}
		if out := ParseSQLiteOutput(res.stdout); out.Files != 3 {
			t.Errorf("output = %+v, want 3 files", out)
		}
		if got := r.workFiles(); !slices.Equal(got, []string{"indexer-1.upload.result", "indexer-2.download.result"}) {
			t.Errorf("work dir = %v, want only the results", got)
		}
		if strings.Contains(res.stdout+res.stderr+r.callLog(), osKey) {
			t.Error("the secret key is in an output or an argument")
		}
	})

	t.Run("a failed fetch removes the folder", func(t *testing.T) {
		r, object := made(t)
		folder := filepath.Join(r.repoDir(), "caravana-indexer-2")
		res := r.download(object, folder, "FAKE_FAIL_CMD=cat")
		if res.code == 0 || res.lastLine() != "download: Failed to cat: injected failure" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if exists(folder) {
			t.Error("the folder is still there")
		}
	})

	t.Run("an extract that fails removes the folder", func(t *testing.T) {
		r, object := made(t)
		folder := filepath.Join(r.repoDir(), "caravana-indexer-2")
		res := r.download(object, folder, "FAKE_FAIL_TAR_X=unexpected end of file")
		if res.code == 0 || res.lastLine() != "download: tar: unexpected end of file" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if exists(folder) {
			t.Error("the folder is still there")
		}
	})

	t.Run("a backup of another kind is refused", func(t *testing.T) {
		r := newOpenSearchRun(t)
		other := filepath.Join(r.dir, "other")
		r.write(filepath.Join(other, "not-a-snapshot"), "x")
		obj := filepath.Join(r.bucket, "b/logs/indexer/1800.opensearch.tar.gz")
		if err := os.MkdirAll(filepath.Dir(obj), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("tar", "-C", other, "-czf", obj, ".").CombinedOutput(); err != nil {
			t.Fatalf("tar: %v\n%s", err, out)
		}
		folder := filepath.Join(r.repoDir(), "caravana-indexer-2")
		res := r.download("b/logs/indexer/1800.opensearch.tar.gz", folder)
		if res.code == 0 || res.lastLine() != "download: the backup holds no OpenSearch snapshot" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if exists(folder) {
			t.Error("the folder is still there")
		}
	})

	t.Run("a folder that isn't one of Caravana's is never emptied", func(t *testing.T) {
		r, object := made(t)
		mine := filepath.Join(r.repoDir(), "mine")
		r.write(filepath.Join(mine, "keep"), "x")
		res := r.download(object, mine)
		if res.code == 0 || !exists(filepath.Join(mine, "keep")) {
			t.Errorf("exit %d, kept = %v", res.code, exists(filepath.Join(mine, "keep")))
		}
	})
}

// The whole chain: the fake cluster snapshots into the folder, the upload script
// archives it, the download script extracts it somewhere else and the restore
// script is given the same folder name it would find.
func TestOpenSearchRoundTrip(t *testing.T) {
	r := newOpenSearchRun(t)
	if res := r.run(OpenSearchCleanScript, "", nil, r.repoDir(), r.work, "fresh"); res.code != 0 {
		t.Fatalf("clean: exit %d: %s", res.code, res.stderr)
	}
	// The fake cluster makes the folder at the path it is told, which in a copy is the
	// service's view; here that is the sidecar's.
	res := r.run(OpenSearchSnapshotScript, "", nil, r.repoDir(), "indexer-1", "1791", "9200", "600", filepath.Join(r.work, "indexer-1.snapshot.result"))
	if res.code != 0 {
		t.Fatalf("snapshot: exit %d: %s", res.code, res.stderr)
	}
	if res := r.upload(filepath.Join(r.repoDir(), "caravana-indexer-1"), "1791", "3", ""); res.code != 0 {
		t.Fatalf("upload: exit %d: %s", res.code, res.stderr)
	}
	if res := r.run(OpenSearchCleanScript, "", nil, r.repoDir(), r.work, "keep"); res.code != 0 {
		t.Fatalf("clean: exit %d: %s", res.code, res.stderr)
	}
	folder := filepath.Join(r.repoDir(), "caravana-indexer-1")
	if res := r.run(OpenSearchDownloadScript, osDest(), []string{"FAKE_SECRET=" + osKey}, "b/logs/indexer/1791.opensearch.tar.gz", folder, "600",
		filepath.Join(r.work, "indexer-1.download.result")); res.code != 0 {
		t.Fatalf("download: exit %d: %s", res.code, res.stderr)
	}
	if res := r.run(OpenSearchRestoreScript, "", nil, r.repoDir(), "indexer-1", "9200", "600", filepath.Join(r.work, "indexer-1.restore.result")); res.code != 0 {
		t.Fatalf("restore: exit %d: %s", res.code, res.stderr)
	}
	if got := r.read(filepath.Join(folder, "indices", "abc", "0", "__1")); got != "shard\n" {
		t.Errorf("the extracted shard file = %q", got)
	}
	if res := r.run(OpenSearchCleanScript, "", nil, r.repoDir(), r.work, "keep"); res.code != 0 || exists(folder) {
		t.Errorf("the last clean left the folder (exit %d)", res.code)
	}
}
