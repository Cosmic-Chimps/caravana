package accesslog

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"
)

// The lab's lines, observed 2026-11-20 on Traefik v3.7.13 (plan 047).
const labLine = `{"ClientHost":"::1","DownstreamContentSize":398,"DownstreamStatus":200,"Duration":3552007,` +
	`"RequestHost":"whoami.localhost","RequestMethod":"GET","RequestPath":"/","RetryAttempts":0,` +
	`"RouterName":"whoami-web@nomad","ServiceName":"whoami-web@nomad","StartUTC":"2026-11-20T19:36:54.533596236Z",` +
	`"TLSVersion":"1.3","entryPointName":"websecure","level":"info","msg":"","time":"2026-11-20T19:36:54Z"}`

func TestParse(t *testing.T) {
	start := time.Date(2026, 11, 20, 19, 36, 54, 533596236, time.UTC)
	tests := []struct {
		name string
		line string
		want Request
		ok   bool
	}{
		{"the lab's line", labLine, Request{
			Time: start, Router: "whoami-web", Service: "whoami-web", Method: "GET", Host: "whoami.localhost",
			Path: "/", Status: 200, Duration: 3552007 * time.Nanosecond, Bytes: 398, Client: "::1", TLS: "1.3",
			EntryPoint: "websecure",
		}, true},
		{"no router took it", `{"ClientHost":"1.2.3.4","DownstreamContentSize":19,"DownstreamStatus":404,"Duration":100000,` +
			`"RequestHost":"nope.example","RequestMethod":"GET","RequestPath":"/","StartUTC":"2026-11-20T19:36:54Z","entryPointName":"web","time":"2026-11-20T19:36:54Z"}`,
			Request{Time: time.Date(2026, 11, 20, 19, 36, 54, 0, time.UTC), Method: "GET", Host: "nope.example", Path: "/",
				Status: 404, Duration: 100 * time.Microsecond, Bytes: 19, Client: "1.2.3.4", EntryPoint: "web"}, true},
		{"another provider's name keeps its suffix", `{"DownstreamStatus":200,"StartUTC":"2026-11-20T19:36:54Z","ServiceName":"caravana@file","RouterName":"caravana@file"}`,
			Request{Time: time.Date(2026, 11, 20, 19, 36, 54, 0, time.UTC), Router: "caravana@file", Service: "caravana@file", Status: 200}, true},
		{"a client that went away", `{"DownstreamStatus":0,"StartUTC":"2026-11-20T19:36:54Z"}`,
			Request{Time: time.Date(2026, 11, 20, 19, 36, 54, 0, time.UTC)}, true},
		{"a retried request", `{"DownstreamStatus":200,"StartUTC":"2026-11-20T19:36:54Z","RetryAttempts":2}`,
			Request{Time: time.Date(2026, 11, 20, 19, 36, 54, 0, time.UTC), Status: 200, Retries: 2}, true},
		{"a time zone becomes UTC", `{"DownstreamStatus":200,"StartUTC":"2026-11-20T21:36:54+02:00"}`,
			Request{Time: time.Date(2026, 11, 20, 19, 36, 54, 0, time.UTC), Status: 200}, true},
		{"no StartUTC", `{"DownstreamStatus":200,"time":"2026-11-20T19:36:54Z"}`, Request{}, false},
		{"no status", `{"StartUTC":"2026-11-20T19:36:54Z"}`, Request{}, false},
		{"a bad time", `{"DownstreamStatus":200,"StartUTC":"yesterday"}`, Request{}, false},
		{"Traefik's own line", `{"level":"info","msg":"Starting provider *nomad.Provider","time":"2026-11-20T19:36:54Z"}`, Request{}, false},
		{"a cut line", `{"DownstreamStatus":200,"StartUTC":"2026-11-20T1`, Request{}, false},
		{"plain text", `2026/11/20 19:36:54 hello`, Request{}, false},
		{"empty", ``, Request{}, false},
		{"an array", `[1,2]`, Request{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.line)
			if ok != tt.ok || got != tt.want {
				t.Errorf("Parse = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseCutsALongPath(t *testing.T) {
	long := "/" + strings.Repeat("é", MaxPath) // 2 bytes a character
	line := `{"DownstreamStatus":200,"StartUTC":"2026-11-20T19:36:54Z","RequestPath":"` + long + `"}`
	got, ok := Parse(line)
	if !ok {
		t.Fatal("not a request")
	}
	if len(got.Path) > MaxPath || len(got.Path) < MaxPath-1 {
		t.Errorf("len(Path) = %d, want about %d", len(got.Path), MaxPath)
	}
	if strings.ToValidUTF8(got.Path, "") != got.Path {
		t.Error("the cut path is not valid UTF-8")
	}
}

func TestParseTooLong(t *testing.T) {
	line := `{"DownstreamStatus":200,"StartUTC":"2026-11-20T19:36:54Z","RequestHost":"` + strings.Repeat("a", maxLine) + `"}`
	if _, ok := Parse(line); ok {
		t.Error("a line over 64 KiB was read")
	}
}

func TestServiceOf(t *testing.T) {
	tests := []struct {
		line, want string
		ok         bool
	}{
		{labLine, "whoami-web", true},
		{`{"ServiceName":"caravana@file"}`, "caravana@file", true},
		{`{"DownstreamStatus":404}`, "", false},
		{`not json`, "", false},
		{``, "", false},
	}
	for _, tt := range tests {
		got, ok := ServiceOf(tt.line)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ServiceOf(%.30q) = %q, %v; want %q, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestClass(t *testing.T) {
	for status, want := range map[int]string{
		0: ClassOther, 99: ClassOther, 100: Class1xx, 101: Class1xx, 199: Class1xx, 200: Class2xx, 204: Class2xx, 299: Class2xx,
		301: Class3xx, 399: Class3xx, 404: Class4xx, 499: Class4xx, 500: Class5xx, 503: Class5xx, 599: Class5xx, 600: ClassOther, -1: ClassOther,
	} {
		if got := Class(status); got != want {
			t.Errorf("Class(%d) = %q, want %q", status, got, want)
		}
	}
	for _, c := range Classes {
		if !ValidClass(c) {
			t.Errorf("ValidClass(%q) = false", c)
		}
	}
	if ValidClass("6xx") || ValidClass("") {
		t.Error("ValidClass accepted a class that doesn't exist")
	}
}

func TestOwners(t *testing.T) {
	o := OwnersOf([]OwnerApp{
		{ID: "shop--staging--web", Project: "shop", Environment: "staging", Services: []string{"web", "worker-queue"}},
		{ID: "whoami", Project: "default", Environment: "production", Services: []string{"web"}},
		{ID: "a-b", Project: "default", Environment: "production", Services: []string{"c"}},
		{ID: "a", Project: "default", Environment: "production", Services: []string{"b-c"}}, // the same joined name as a-b's
	})
	tests := []struct {
		name  string
		want  Owner
		found bool
	}{
		{"whoami-web", Owner{"default", "production", "whoami", "web"}, true},
		{"shop--staging--web-worker-queue", Owner{"shop", "staging", "shop--staging--web", "worker-queue"}, true},
		{"a-b-c", Owner{"default", "production", "a", "b-c"}, true}, // "a" sorts before "a-b"
		{"caravana@file", Owner{}, false},
		{"gone-web", Owner{}, false},
		{"", Owner{}, false},
	}
	for _, tt := range tests {
		got, ok := o.Of(tt.name)
		if ok != tt.found || got != tt.want {
			t.Errorf("Of(%q) = %+v, %v; want %+v, %v", tt.name, got, ok, tt.want, tt.found)
		}
	}
}

// TestNoNetworkImports keeps the package pure: no network, Nomad or Caravana
// package.
func TestNoNetworkImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"net/http", "net", "os/exec", "github.com/hashicorp/nomad", "github.com/cosmic-chimps/caravana/"}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if path == bad || ((strings.HasSuffix(bad, "/") || strings.Contains(bad, ".")) && strings.HasPrefix(path, bad)) {
					t.Errorf("%s imports %q, which a pure package must not", name, path)
				}
			}
		}
	}
	if checked < 2 {
		t.Errorf("checked %d files, want at least 2", checked)
	}
}
