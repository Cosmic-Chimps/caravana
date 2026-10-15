package nomadjob

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
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

	if len(vt.Templates) != 1 {
		t.Fatalf("templates = %d, want 1", len(vt.Templates))
	}
	tmpl := vt.Templates[0]
	if *tmpl.DestPath != "local/request.env" || *tmpl.ChangeMode != "noop" || tmpl.Envvars == nil || *tmpl.Envvars {
		t.Errorf("template = %s %s env=%v, want local/request.env, noop, env false", *tmpl.DestPath, *tmpl.ChangeMode, tmpl.Envvars)
	}
	if want := `nomadVar "nomad/jobs/` + appID + "/" + svc.Name + `/caravana-volumes"`; !strings.Contains(*tmpl.EmbeddedTmpl, want) {
		t.Errorf("template doesn't read %s:\n%s", want, *tmpl.EmbeddedTmpl)
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

func TestVolumeTemplateAndScriptAvoidNomadInterpolation(t *testing.T) {
	tmpl := *volumeTemplate("kuma", "web").EmbeddedTmpl
	for name, text := range map[string]string{"VolumeScript": VolumeScript, "the template": tmpl} {
		if strings.Contains(text, "${") {
			t.Errorf("%s contains \"${\", which Nomad interpolates", name)
		}
	}
	// Values are single-quoted for the shell that sources the file.
	if !strings.Contains(tmpl, `='{{ .V | replaceAll "'" "'\\''" }}'`) {
		t.Errorf("the template doesn't single-quote values:\n%s", tmpl)
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

// scriptRun is one run of VolumeScript under a real sh, on temp directories.
type scriptRun struct {
	t    *testing.T
	root string // ROOT, with volumes "data" and "logs"
	data string // DATA
	dir  string // the working directory
}

// runScript runs VolumeScript with request as the contents of the request
// file (no file when request is nil) and reports the run. The script must
// exit 0 whatever it is given.
func runScript(t *testing.T, request *string) *scriptRun {
	t.Helper()
	for _, tool := range []string{"sh", "du", "cut", "tr", "date", "basename", "mv", "mkdir", "cat", "rm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	r := &scriptRun{t: t, dir: t.TempDir()}
	r.root = filepath.Join(r.dir, "vols")
	r.data = filepath.Join(r.dir, "alloc", "data", "caravana") // not created: the script makes it
	for name, size := range map[string]int{"data": 40 * 1024, "logs": 10} {
		if err := os.MkdirAll(filepath.Join(r.root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.root, name, "file"), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reqFile := filepath.Join(r.dir, "request.env") // absent unless written
	if request != nil {
		if err := os.WriteFile(reqFile, []byte(*request), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("sh", "-c", VolumeScript)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "ROOT="+r.root, "DATA="+r.data, "REQUEST_FILE="+reqFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the script must always exit 0, got %v\n%s", err, out)
	}
	return r
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
	// The stubs for the backup and restore runs fail with "not implemented
	// yet", which is what makes the shape testable end to end here.
	tests := []struct {
		name     string
		request  func() *string
		resultOf string // the request ID whose result file should exist; empty means none
		wantMsg  string
	}{
		{"a backup", func() *string { return request("REQUEST", "web-1", "ACTION", "backup", "NOT_AFTER", inAnHour()) }, "web-1", "not implemented yet"},
		{"a restore", func() *string {
			return request("REQUEST", "web-2", "ACTION", "restore", "OBJECT", "b/kuma/web/1/", "NOT_AFTER", inAnHour())
		}, "web-2", "not implemented yet"},
		{"an unknown action", func() *string { return request("REQUEST", "web-3", "ACTION", "format", "NOT_AFTER", inAnHour()) }, "web-3", "unknown action"},
		{"no action", func() *string { return request("REQUEST", "web-4", "NOT_AFTER", inAnHour()) }, "web-4", "unknown action"},
		{"values with quotes and shell syntax", func() *string {
			return request("REQUEST", "web-5", "ACTION", "backup", "NOT_AFTER", inAnHour(),
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
	cmd := exec.Command("sh", "-c", VolumeScript)
	cmd.Env = append(os.Environ(), "ROOT="+r.root, "DATA="+r.data, "REQUEST_FILE="+filepath.Join(r.dir, "request.env"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
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
