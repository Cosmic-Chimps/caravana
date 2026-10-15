package nomadjob

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func translated(t *testing.T, tc goldenCase) *api.Job {
	t.Helper()
	res, err := Translate(tc.app, tc.deployment, tc.settings)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	return res.Job
}

// TestVolumeTask checks the caravana-volumes task of every golden case: it is
// in exactly the groups that have a volume, and it mounts them the way the
// service's group asks for them.
func TestVolumeTask(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			job := translated(t, tc)
			for i, svc := range tc.app.Services {
				tg := job.TaskGroups[i]
				var vt *api.Task
				for _, task := range tg.Tasks {
					if task.Name == VolumeTaskName {
						vt = task
					}
				}
				if len(svc.Volumes) == 0 {
					if vt != nil {
						t.Errorf("service %s has no volume but gets the %s task", svc.Name, VolumeTaskName)
					}
					continue
				}
				if vt == nil {
					t.Fatalf("service %s has volumes but no %s task", svc.Name, VolumeTaskName)
				}
				if tg.Tasks[0].Name != svc.Name {
					t.Errorf("service %s: the first task is %s, want the service's own", svc.Name, tg.Tasks[0].Name)
				}
				checkVolumeTask(t, tc.app.ID, svc, tg, vt)
			}
		})
	}
}

func checkVolumeTask(t *testing.T, appID string, svc app.Service, tg *api.TaskGroup, vt *api.Task) {
	t.Helper()
	if vt.Lifecycle == nil || vt.Lifecycle.Hook != "prestart" || vt.Lifecycle.Sidecar {
		t.Errorf("lifecycle = %+v, want prestart and not a sidecar", vt.Lifecycle)
	}
	if vt.Config["image"] != DefaultBackupImage {
		t.Errorf("image = %v, want the backup image", vt.Config["image"])
	}
	if !reflect.DeepEqual(vt.Config["entrypoint"], []string{"/bin/sh", "-c"}) || !reflect.DeepEqual(vt.Config["args"], []string{VolumeScript}) {
		t.Errorf("command = %v %v, want /bin/sh -c VolumeScript", vt.Config["entrypoint"], vt.Config["args"])
	}
	// Small reservation, bigger burst: Nomad holds a prestart task's
	// reservation for the copy's whole life.
	if r := vt.Resources; *r.CPU != 50 || *r.MemoryMB != 32 || *r.MemoryMaxMB != 512 {
		t.Errorf("resources = cpu %d, memory %d, max %d; want 50, 32, 512", *r.CPU, *r.MemoryMB, *r.MemoryMaxMB)
	}

	// Two templates of the one variable: the request under local/, and the
	// destination (with its secret key) under secrets/.
	if len(vt.Templates) != 2 {
		t.Fatalf("templates = %d, want 2", len(vt.Templates))
	}
	for i, wantDest := range []string{"local/request.env", "secrets/destination.env"} {
		tmpl := vt.Templates[i]
		if *tmpl.DestPath != wantDest || *tmpl.ChangeMode != "noop" || tmpl.Envvars == nil || *tmpl.Envvars {
			t.Errorf("template %d = %s %s env=%v, want %s, noop, env false", i, *tmpl.DestPath, *tmpl.ChangeMode, tmpl.Envvars, wantDest)
		}
		if want := `nomadVar "nomad/jobs/` + appID + "/" + svc.Name + `/caravana-volumes"`; !strings.Contains(*tmpl.EmbeddedTmpl, want) {
			t.Errorf("template %d doesn't read %s:\n%s", i, want, *tmpl.EmbeddedTmpl)
		}
	}

	// One mount for each volume, under /caravana-volumes. The task's mount is
	// writable exactly when the group's request is, and the service's own
	// mount keeps the read_only the user asked for.
	if len(vt.VolumeMounts) != len(svc.Volumes) {
		t.Fatalf("mounts = %d, want %d", len(vt.VolumeMounts), len(svc.Volumes))
	}
	for i, v := range svc.Volumes {
		m := vt.VolumeMounts[i]
		if *m.Volume != v.Name || *m.Destination != "/caravana-volumes/"+v.Name {
			t.Errorf("mount %d = %s at %s, want %s at /caravana-volumes/%s", i, *m.Volume, *m.Destination, v.Name, v.Name)
		}
		wantRO := v.ReadOnly && !hasVolumeBackup(svc)
		if tg.Volumes[v.Name].ReadOnly != wantRO || *m.ReadOnly != wantRO {
			t.Errorf("volume %s: group request read-only %v, task mount read-only %v; want both %v",
				v.Name, tg.Volumes[v.Name].ReadOnly, *m.ReadOnly, wantRO)
		}
		if got := *tg.Tasks[0].VolumeMounts[i].ReadOnly; got != v.ReadOnly {
			t.Errorf("the service's mount of %s has read_only %v, want %v", v.Name, got, v.ReadOnly)
		}
	}
}

func TestVolumeTemplatesAndScriptAvoidNomadInterpolation(t *testing.T) {
	for name, text := range map[string]string{
		"VolumeScript":             VolumeScript,
		"the request template":     *volumeTemplate("kuma", "web", volumeRequestDest, false).EmbeddedTmpl,
		"the destination template": *volumeTemplate("kuma", "web", volumeDestinationDest, true).EmbeddedTmpl,
	} {
		if strings.Contains(text, "${") {
			t.Errorf("%s contains \"${\", which Nomad interpolates", name)
		}
		if name != "VolumeScript" && !strings.Contains(text, `='{{ .V | replaceAll "'" "'\\''" }}'`) {
			// Values are single-quoted for the shell that sources the file.
			t.Errorf("%s doesn't single-quote values:\n%s", name, text)
		}
	}
}

// templateVar plays what Nomad's nomadVar gives a template: the variable's
// items as .Tuples.
type templateVar struct{ Tuples []templateTuple }
type templateTuple struct{ K, V string }

// render runs a template's text in Go's own template engine, with the two
// consul-template functions it uses written as they are documented there:
// regexMatch regex input, and replaceAll old new input. It is not Nomad's
// engine (the lab and the gate check that), but it is the same template
// language, so it proves which keys end up in which file and how a value is
// quoted.
func render(t *testing.T, tmpl *api.Template, items map[string]string) string {
	t.Helper()
	var tuples []templateTuple
	for _, k := range slices.Sorted(maps.Keys(items)) {
		tuples = append(tuples, templateTuple{K: k, V: items[k]})
	}
	funcs := template.FuncMap{
		"nomadVar":   func(string) *templateVar { return &templateVar{Tuples: tuples} },
		"regexMatch": func(re, s string) (bool, error) { return regexp.MatchString(re, s) },
		"replaceAll": func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
	}
	tp, err := template.New("t").Funcs(funcs).Parse(*tmpl.EmbeddedTmpl)
	if err != nil {
		t.Fatalf("parse %s: %v", *tmpl.DestPath, err)
	}
	var out strings.Builder
	if err := tp.Execute(&out, nil); err != nil {
		t.Fatalf("render %s: %v", *tmpl.DestPath, err)
	}
	return out.String()
}

// TestVolumeTemplatesSplitTheDestination: the secret key must never reach
// local/request.env, which Nomad's file system API shows to whoever holds
// read-fs. Only secrets/destination.env gets the destination's keys.
func TestVolumeTemplatesSplitTheDestination(t *testing.T) {
	items := map[string]string{
		"REQUEST": "web-17", "ACTION": "backup", "TS": "17", "NOT_AFTER": "99", "LIMIT": "1800", "KEEP": "4",
		"BACKUP_PATH":                          "backups/caravana/kuma/web",
		"OBJECT":                               "it's $(x)",
		"RCLONE_CONFIG_DEST_ENDPOINT":          "http://10.0.0.5:9000",
		"RCLONE_CONFIG_DEST_ACCESS_KEY_ID":     "AK",
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY": "s3cr3t-key-marker",
	}
	request := volumeTemplate("kuma", "web", volumeRequestDest, false)
	destination := volumeTemplate("kuma", "web", volumeDestinationDest, true)

	// The filter is in the template text, which Nomad's engine will run.
	if !strings.Contains(*request.EmbeddedTmpl, `not (.K | regexMatch "^RCLONE_CONFIG_DEST_")`) {
		t.Errorf("the request template doesn't filter out the destination keys:\n%s", *request.EmbeddedTmpl)
	}
	if !strings.Contains(*destination.EmbeddedTmpl, `if .K | regexMatch "^RCLONE_CONFIG_DEST_"`) {
		t.Errorf("the destination template doesn't keep only the destination keys:\n%s", *destination.EmbeddedTmpl)
	}

	gotRequest := render(t, request, items)
	if strings.Contains(gotRequest, "RCLONE_CONFIG_DEST_") || strings.Contains(gotRequest, "s3cr3t-key-marker") {
		t.Errorf("the request file holds the destination:\n%s", gotRequest)
	}
	wantRequest := "ACTION='backup'\nBACKUP_PATH='backups/caravana/kuma/web'\nKEEP='4'\nLIMIT='1800'\nNOT_AFTER='99'\n" +
		"OBJECT='it'\\''s $(x)'\nREQUEST='web-17'\nTS='17'\n"
	if gotRequest != wantRequest {
		t.Errorf("request file:\n%s\nwant:\n%s", gotRequest, wantRequest)
	}

	wantDestination := "RCLONE_CONFIG_DEST_ACCESS_KEY_ID='AK'\nRCLONE_CONFIG_DEST_ENDPOINT='http://10.0.0.5:9000'\n" +
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY='s3cr3t-key-marker'\n"
	if got := render(t, destination, items); got != wantDestination {
		t.Errorf("destination file:\n%s\nwant:\n%s", got, wantDestination)
	}

	// A variable with no destination (a service not backed up) renders an
	// empty destination file, and the request as it is.
	if got := render(t, destination, map[string]string{"REQUEST": ""}); got != "" {
		t.Errorf("destination file without a destination = %q, want empty", got)
	}
	if got := render(t, request, map[string]string{"REQUEST": ""}); got != "REQUEST=''\n" {
		t.Errorf("request file without a request = %q", got)
	}
}

// TestVolumeJobsHoldNoSecret: a service's secret and a destination key live in
// Nomad Variables; the job names a variable path and never a value.
func TestVolumeJobsHoldNoSecret(t *testing.T) {
	c := volumeBackupCases()[0]
	c.app.Services[0].Secrets = []app.Secret{{Name: "API_KEY", Value: new("s3cr3t-marker")}}
	c.app.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "prod"}}
	res, err := Translate(c.app.WithoutSecretValues(), c.deployment, c.settings)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res.Job)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "s3cr3t-marker") {
		t.Error("the job holds a secret value")
	}
}

func TestVolumeVariablePath(t *testing.T) {
	if got, want := VolumeVariablePath("kuma", "web"), "nomad/jobs/kuma/web/caravana-volumes"; got != want {
		t.Errorf("VolumeVariablePath = %q, want %q", got, want)
	}
}

// TestVolumeFilesAreWhereTheScriptWritesThem ties the paths Caravana reads
// (relative to the allocation) to the ones the script writes (absolute).
func TestVolumeFilesAreWhereTheScriptWritesThem(t *testing.T) {
	if VolumeDataDir != "/"+filepath.Dir(VolumeSizesFile) {
		t.Errorf("sizes file %q is not in %q", VolumeSizesFile, VolumeDataDir)
	}
	if got, want := VolumeResultFile("web-17"), "alloc/data/caravana/web-17.result"; got != want {
		t.Errorf("VolumeResultFile = %q, want %q", got, want)
	}
	if got, want := VolumeRequestFile, "caravana-volumes/local/request.env"; got != want {
		t.Errorf("VolumeRequestFile = %q, want %q", got, want)
	}
}

func TestParseVolumeResult(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    VolumeResult
		wantErr bool
	}{
		{
			name: "a backup of two volumes",
			text: "status=succeeded\nmessage=\nobject=b/kuma/web/1791/\n" +
				"volume.data.bytes=2000\nvolume.data.size=5000\nvolume.logs.bytes=10\nvolume.logs.size=12\n" +
				"expired=1700 1701\n",
			want: VolumeResult{
				Succeeded: true, Object: "b/kuma/web/1791/",
				Volumes: []VolumeOutcome{{Name: "data", Bytes: 2000, SizeKB: 5000}, {Name: "logs", Bytes: 10, SizeKB: 12}},
				Expired: []string{"1700", "1701"},
			},
		},
		{
			name: "a failure keeps its message, with an equals sign in it",
			text: "status=failed\nmessage=rclone: a=b failed\n",
			want: VolumeResult{Message: "rclone: a=b failed"},
		},
		{
			name: "volume names with dashes and underscores, size before bytes",
			text: "status=succeeded\nvolume.db_data-1.size=7\nvolume.db_data-1.bytes=3\n",
			want: VolumeResult{Succeeded: true, Volumes: []VolumeOutcome{{Name: "db_data-1", Bytes: 3, SizeKB: 7}}},
		},
		{
			name: "unknown keys, lines without a key and CRLF are tolerated",
			text: "status=succeeded\r\nfuture=thing\r\nnot a line\r\nvolume.data.owner=root\r\n",
			want: VolumeResult{Succeeded: true},
		},
		{name: "no status", text: "message=hello\n", wantErr: true},
		{name: "empty", text: "", wantErr: true},
		{name: "unknown status", text: "status=done\n", wantErr: true},
		{name: "a size that isn't a number", text: "status=succeeded\nvolume.data.bytes=lots\n", wantErr: true},
		{name: "a negative size", text: "status=succeeded\nvolume.data.size=-1\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVolumeResult(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseVolumeSizes(t *testing.T) {
	tests := []struct {
		name, text string
		want       map[string]int64
	}{
		{"two volumes", "data 5000\nlogs 12\n", map[string]int64{"data": 5000, "logs": 12}},
		{"empty", "", map[string]int64{}},
		{"bad lines are skipped, the rest kept", "data 5\nnonsense\nlogs lots\nneg -4\ntoo many fields 1\nok 0\n", map[string]int64{"data": 5, "ok": 0}},
		{"no trailing newline", "data 5", map[string]int64{"data": 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseVolumeSizes(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// scriptRun is one run of VolumeScript under a real sh, on temp directories,
// with a fake rclone and a fake timeout first on the PATH.
type scriptRun struct {
	t      *testing.T
	root   string // ROOT, with volumes "data" and "logs"
	data   string // DATA
	dir    string // the working directory
	bucket string // what "dest:" means to the fake rclone
	bin    string // the fake tools
	log    string // one line for each call the fake tools answered
}

// The fake tools. A stub runs only under the fake timeout (which sets
// UNDER_TIMEOUT), as the script must wrap every call to tar and rclone. The
// fake timeout drops its first argument, the seconds, and runs the rest; it
// logs the seconds, and can be told to run out instead. macOS has no timeout.
//
// The fake rclone knows what the script uses: rcat, lsf --dirs-only, purge,
// size --json and copyto. "dest:<path>" is the file or directory <path> under
// $FAKE_BUCKET, which stands for the bucket. FAKE_SECRET makes it refuse a
// run whose secret key isn't that; FAKE_FAIL_RCAT (a part of an object's name)
// and FAKE_FAIL_CMD (a subcommand) make the named calls fail.
const (
	fakeTimeout = `#!/bin/sh
echo "timeout $1 $2" >> "$FAKE_LOG"
if [ "$FAKE_TIMEOUT_EXPIRE" = "$2" ]; then
  echo "timed out" >&2
  exit 124
fi
shift
UNDER_TIMEOUT=1 exec "$@"
`
	fakeTar = `#!/bin/sh
[ -n "$UNDER_TIMEOUT" ] || { echo "tar ran outside timeout" >&2; exit 99; }
if [ -n "$FAKE_FAIL_TAR" ]; then
  echo "tar: $FAKE_FAIL_TAR" >&2
  echo "partial" 
  exit 2
fi
exec REALTAR "$@"
`
	fakeRclone = `#!/bin/sh
[ -n "$UNDER_TIMEOUT" ] || { echo "rclone ran outside timeout" >&2; exit 99; }
echo "rclone $*" >> "$FAKE_LOG"
cmd=$1
shift
if [ "$cmd" = "$FAKE_FAIL_CMD" ]; then
  [ "$cmd" != rcat ] || cat > /dev/null
  echo "Failed to $cmd: injected failure" >&2
  exit 1
fi
if [ -n "$FAKE_SECRET" ] && [ "$RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY" != "$FAKE_SECRET" ]; then
  [ "$cmd" != rcat ] || cat > /dev/null
  echo "AccessDenied: the secret key is wrong" >&2
  exit 1
fi
local_path() { echo "$FAKE_BUCKET/${1#dest:}"; }
case "$cmd" in
  rcat)
    p=$(local_path "$1")
    case "$p" in *"$FAKE_FAIL_RCAT"*) if [ -n "$FAKE_FAIL_RCAT" ]; then cat > /dev/null; echo "Failed to rcat: boom" >&2; exit 1; fi ;; esac
    mkdir -p "$(dirname "$p")" && cat > "$p" ;;
  lsf)
    shift # --dirs-only
    p=$(local_path "$1")
    [ -d "$p" ] || { echo "directory not found" >&2; exit 3; }
    for d in "$p"/*/; do [ -d "$d" ] && echo "$(basename "$d")/"; done
    exit 0 ;;
  purge)
    p=$(local_path "$1")
    [ -d "$p" ] || { echo "directory not found" >&2; exit 3; }
    rm -rf "$p" ;;
  size)
    shift # --json
    p=$(local_path "$1")
    [ -f "$p" ] || { echo "object not found" >&2; exit 3; }
    n=$(wc -c < "$p" | tr -d ' ')
    echo "{\"count\":1,\"bytes\":$n,\"sizeless\":0}" ;;
  copyto)
    case "$1" in
      dest:*) p=$(local_path "$1"); [ -f "$p" ] || { echo "object not found" >&2; exit 3; }; cp "$p" "$2" ;;
      *) p=$(local_path "$2"); mkdir -p "$(dirname "$p")" && cp "$1" "$p" ;;
    esac ;;
  *) echo "fake rclone: unknown command $cmd" >&2; exit 2 ;;
esac
`
)

// The fake tools are written once for the whole test run, not for each test:
// macOS scans every new executable the first time it runs, about half a
// second each, which made every script test take seconds.
var (
	fakeTools     sync.Once
	fakeToolsPath string
	fakeToolsErr  error
)

// fakeToolsDir returns the directory of the fake timeout, tar and rclone.
// realTar is the host's tar, which the fake tar runs unless told to fail.
func fakeToolsDir(t *testing.T, realTar string) string {
	t.Helper()
	fakeTools.Do(func() {
		fakeToolsPath, fakeToolsErr = os.MkdirTemp("", "caravana-fake-tools-")
		if fakeToolsErr != nil {
			return
		}
		for name, text := range map[string]string{
			"timeout": fakeTimeout,
			"tar":     strings.ReplaceAll(fakeTar, "REALTAR", realTar),
			"rclone":  fakeRclone,
		} {
			if fakeToolsErr = os.WriteFile(filepath.Join(fakeToolsPath, name), []byte(text), 0o755); fakeToolsErr != nil {
				return
			}
		}
	})
	if fakeToolsErr != nil {
		t.Fatalf("fake tools: %v", fakeToolsErr)
	}
	return fakeToolsPath
}

// TestMain removes the fake tools' directory when the tests are done.
func TestMain(m *testing.M) {
	code := m.Run()
	if fakeToolsPath != "" {
		os.RemoveAll(fakeToolsPath)
	}
	os.Exit(code)
}

// newScriptRun makes the directories and the fake tools. It skips the test
// when a tool the script needs is missing.
func newScriptRun(t *testing.T) *scriptRun {
	t.Helper()
	for _, tool := range []string{"sh", "du", "cut", "tr", "date", "basename", "dirname", "mv", "mkdir", "cat", "rm",
		"tar", "tail", "head", "sort", "grep", "sed", "wc", "cp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	realTar, _ := exec.LookPath("tar")
	r := &scriptRun{t: t, dir: t.TempDir()}
	r.root = filepath.Join(r.dir, "vols")
	r.data = filepath.Join(r.dir, "alloc", "data", "caravana") // not created: the script makes it
	r.bucket = filepath.Join(r.dir, "bucket")
	r.bin = fakeToolsDir(t, realTar)
	r.log = filepath.Join(r.dir, "calls.log")
	if err := os.MkdirAll(r.bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"data": 40 * 1024, "logs": 10} {
		if err := os.MkdirAll(filepath.Join(r.root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.root, name, "file"), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// run runs VolumeScript with request as the contents of the request file (no
// file when request is nil) and extra environment ("KEY=value"), and reports
// the run. The script must exit 0 whatever it is given.
func (r *scriptRun) run(request *string, env ...string) *scriptRun {
	r.t.Helper()
	reqFile := filepath.Join(r.dir, "request.env") // absent unless written
	if request != nil {
		if err := os.WriteFile(reqFile, []byte(*request), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", VolumeScript)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ROOT="+r.root, "DATA="+r.data, "REQUEST_FILE="+reqFile, "DEST_FILE="+filepath.Join(r.dir, "destination.env"),
		"FAKE_BUCKET="+r.bucket, "FAKE_LOG="+r.log)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("the script must always exit 0, got %v\n%s", err, out)
	}
	return r
}

// runScript is a run with the default directories.
func runScript(t *testing.T, request *string, env ...string) *scriptRun {
	t.Helper()
	return newScriptRun(t).run(request, env...)
}

// files lists the names in the data directory, hidden ones included.
func (r *scriptRun) files() []string {
	r.t.Helper()
	entries, err := os.ReadDir(r.data)
	if err != nil {
		r.t.Fatalf("data dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func (r *scriptRun) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.data, name))
	if err != nil {
		r.t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// request renders a request file the way the template would: single-quoted
// values, each ' written as '\”.
func request(kv ...string) *string {
	var b strings.Builder
	for i := 0; i < len(kv); i += 2 {
		fmt.Fprintf(&b, "%s='%s'\n", kv[i], strings.ReplaceAll(kv[i+1], "'", `'\''`))
	}
	s := b.String()
	return &s
}

func inAnHour() string  { return fmt.Sprint(time.Now().Add(time.Hour).Unix()) }
func anHourAgo() string { return fmt.Sprint(time.Now().Add(-time.Hour).Unix()) }

func TestVolumeScriptMeasuresSizes(t *testing.T) {
	for name, req := range map[string]*string{"no request file": nil, "an empty request": request("REQUEST", "")} {
		t.Run(name, func(t *testing.T) {
			r := runScript(t, req)
			// du counts in blocks, so only the order is certain: data holds
			// 40 KB, logs 10 bytes.
			sizes := ParseVolumeSizes(r.read("sizes"))
			if len(sizes) != 2 || sizes["data"] < 40 || sizes["logs"] >= sizes["data"] {
				t.Errorf("sizes = %v, want both volumes, data at least 40 KB and the larger", sizes)
			}
			if got := r.files(); !slices.Equal(got, []string{"sizes"}) {
				t.Errorf("data dir = %v, want only the sizes file (no result without a request)", got)
			}
		})
	}
}

func TestVolumeScriptRequests(t *testing.T) {
	// The restore run is a stub that fails with "not implemented yet" until
	// T016, and a backup request with no timestamp or destination fails before
	// it touches anything: both make the shape testable end to end here.
	tests := []struct {
		name     string
		request  func() *string
		resultOf string // the request ID whose result file should exist; empty means none
		wantMsg  string
	}{
		{"a backup without a timestamp", func() *string { return request("REQUEST", "web-1", "ACTION", "backup", "NOT_AFTER", inAnHour()) }, "web-1", "the request has no timestamp"},
		{"a backup without a destination", func() *string {
			return request("REQUEST", "web-10", "ACTION", "backup", "TS", "1791", "NOT_AFTER", inAnHour())
		}, "web-10", "the request has no backup destination"},
		{"a restore", func() *string {
			return request("REQUEST", "web-2", "ACTION", "restore", "OBJECT", "b/kuma/web/1/", "NOT_AFTER", inAnHour())
		}, "web-2", "not implemented yet"},
		{"an unknown action", func() *string { return request("REQUEST", "web-3", "ACTION", "format", "NOT_AFTER", inAnHour()) }, "web-3", "unknown action"},
		{"no action", func() *string { return request("REQUEST", "web-4", "NOT_AFTER", inAnHour()) }, "web-4", "unknown action"},
		{"values with quotes and shell syntax", func() *string {
			return request("REQUEST", "web-5", "ACTION", "restore", "NOT_AFTER", inAnHour(),
				"OBJECT", "it's $(touch PWNED) `touch PWNED2` \"x\" $HOME \\n")
		}, "web-5", "not implemented yet"},

		// Ignored: nothing but the sizes.
		{"an expired request", func() *string { return request("REQUEST", "web-6", "ACTION", "backup", "NOT_AFTER", anHourAgo()) }, "", ""},
		{"no NOT_AFTER", func() *string { return request("REQUEST", "web-7", "ACTION", "backup") }, "", ""},
		{"a NOT_AFTER that is no number", func() *string { return request("REQUEST", "web-8", "ACTION", "backup", "NOT_AFTER", "soon") }, "", ""},
		{"a request ID that is a path", func() *string {
			return request("REQUEST", "../../x", "ACTION", "backup", "NOT_AFTER", inAnHour())
		}, "", ""},
		{"a damaged file", func() *string { s := "REQUEST='web-9\nACTION=backup\n"; return &s }, "", ""},
		{"not shell at all", func() *string { s := "}{ ((\n"; return &s }, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runScript(t, tt.request())
			if _, err := os.Stat(filepath.Join(r.dir, "PWNED")); err == nil {
				t.Error("a value was run as a shell command")
			}
			if _, err := os.Stat(filepath.Join(r.dir, "PWNED2")); err == nil {
				t.Error("a value was run as a shell command (backticks)")
			}
			if tt.resultOf == "" {
				if got := r.files(); !slices.Equal(got, []string{"sizes"}) {
					t.Fatalf("data dir = %v, want only the sizes file", got)
				}
				return
			}
			if got := r.files(); !slices.Equal(got, []string{"sizes", tt.resultOf + ".result"}) {
				t.Fatalf("data dir = %v, want the sizes and %s.result and nothing else", got, tt.resultOf)
			}
			res, err := ParseVolumeResult(r.read(tt.resultOf + ".result"))
			if err != nil {
				t.Fatalf("result: %v", err)
			}
			if res.Succeeded || res.Message != tt.wantMsg {
				t.Errorf("result = %+v, want failed %q", res, tt.wantMsg)
			}
		})
	}
}

// TestVolumeScriptNeverRepeatsARun: a request whose result file is there
// (the run finished, and Caravana hasn't cleared the request yet) is left
// alone on the next start.
func TestVolumeScriptNeverRepeatsARun(t *testing.T) {
	r := runScript(t, request("REQUEST", "web-1", "ACTION", "backup", "NOT_AFTER", inAnHour()))
	first := r.read("web-1.result")

	// Same directories, same request: the copy starts again.
	if err := os.WriteFile(filepath.Join(r.data, "web-1.result"), []byte("status=succeeded\nmessage=kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run(nil) // the request file is still there
	if got := r.read("web-1.result"); got != "status=succeeded\nmessage=kept\n" {
		t.Errorf("the result was rewritten:\nfirst: %q\nnow:   %q", first, got)
	}
	if got := r.files(); !slices.Equal(got, []string{"sizes", "web-1.result"}) {
		t.Errorf("data dir = %v", got)
	}
}

// Every golden case with a volume carries the script, so the golden files
// show any change to it. A missing case would simply not be compared, so this
// keeps the three shapes of feature 016 on the list.
func TestVolumeGoldenCasesExist(t *testing.T) {
	var names []string
	for _, tc := range goldenCases() {
		names = append(names, tc.name)
	}
	for _, want := range []string{"volumes", "volume-backup", "volume-backup-readonly", "volume-readonly"} {
		if !slices.Contains(names, want) {
			t.Errorf("golden cases lack %q: %v", want, names)
		}
	}
}
