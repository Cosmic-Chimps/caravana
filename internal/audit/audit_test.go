package audit

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testDomain struct {
	Host        string `json:"host"`
	Path        string `json:"path,omitempty"`
	WWWRedirect bool   `json:"wwwRedirect,omitempty"`
}

type testEnv struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type testSecret struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type testService struct {
	Name    string       `json:"name"`
	Image   string       `json:"image"`
	Domains []testDomain `json:"domains,omitempty"`
	Env     []testEnv    `json:"env,omitempty"`
	Secrets []testSecret `json:"secrets,omitempty"`
	Args    []string     `json:"args,omitempty"`
}

type testApp struct {
	ID       string        `json:"id"`
	Services []testService `json:"services"`
}

func TestDiff(t *testing.T) {
	web := func(mod func(*testService)) testApp {
		s := testService{Name: "web", Image: "ghcr.io/acme/web:1.4"}
		if mod != nil {
			mod(&s)
		}
		return testApp{ID: "shop--staging--web", Services: []testService{s}}
	}
	tests := []struct {
		name          string
		before, after any
		want          []Change
	}{
		{"equal", web(nil), web(nil), nil},
		{"image changed",
			web(nil), web(func(s *testService) { s.Image = "ghcr.io/acme/web:1.5" }),
			[]Change{{Path: "services[web].image", Op: OpChanged, From: "ghcr.io/acme/web:1.4", To: "ghcr.io/acme/web:1.5"}}},
		{"a domain's host changes: the old one is removed and the new one added",
			web(func(s *testService) { s.Domains = []testDomain{{Host: "old.example.com"}} }),
			web(func(s *testService) { s.Domains = []testDomain{{Host: "new.example.com"}} }),
			[]Change{
				{Path: "services[web].domains[old.example.com]", Op: OpRemoved, From: "{…}"},
				{Path: "services[web].domains[new.example.com]", Op: OpAdded, To: "{…}"},
			}},
		{"a domain option changes in place",
			web(func(s *testService) { s.Domains = []testDomain{{Host: "a.example.com"}, {Host: "b.example.com"}} }),
			web(func(s *testService) {
				s.Domains = []testDomain{{Host: "a.example.com", Path: "/app"}, {Host: "b.example.com"}}
			}),
			[]Change{{Path: "services[web].domains[a.example.com].path", Op: OpAdded, To: "/app"}}},
		{"a secret's value for an existing secret shows changed and no value",
			web(func(s *testService) { s.Secrets = []testSecret{{Name: "DB_PASSWORD", Value: "stored"}} }),
			web(func(s *testService) { s.Secrets = []testSecret{{Name: "DB_PASSWORD", Value: "s3cret-sentinel-057"}} }),
			[]Change{{Path: "services[web].secrets[DB_PASSWORD].value", Op: OpChanged}}},
		{"a service's first secret shows only its name",
			web(nil),
			web(func(s *testService) { s.Secrets = []testSecret{{Name: "API_KEY", Value: "s3cret-sentinel-057"}} }),
			[]Change{{Path: "services[web].secrets[API_KEY]", Op: OpAdded, To: "{…}"}}},
		{"a service's first domain shows its host",
			web(nil),
			web(func(s *testService) { s.Domains = []testDomain{{Host: "new.example.com"}} }),
			[]Change{{Path: "services[web].domains[new.example.com]", Op: OpAdded, To: "{…}"}}},
		{"a new secret beside another shows only its name",
			web(func(s *testService) { s.Secrets = []testSecret{{Name: "A", Value: "stored"}} }),
			web(func(s *testService) {
				s.Secrets = []testSecret{{Name: "A", Value: "stored"}, {Name: "B", Value: "s3cret-sentinel-057"}}
			}),
			[]Change{{Path: "services[web].secrets[B]", Op: OpAdded, To: "{…}"}}},
		{"an env value is redacted",
			web(func(s *testService) { s.Env = []testEnv{{Name: "MODE", Value: "old-sentinel"}} }),
			web(func(s *testService) { s.Env = []testEnv{{Name: "MODE", Value: "new-sentinel"}} }),
			[]Change{{Path: "services[web].env[MODE].value", Op: OpChanged}}},
		{"a list of strings compares by index",
			web(func(s *testService) { s.Args = []string{"serve", "--port=80"} }),
			web(func(s *testService) { s.Args = []string{"serve", "--port=8080", "--debug"} }),
			[]Change{
				{Path: "services[web].args[1]", Op: OpChanged, From: "--port=80", To: "--port=8080"},
				{Path: "services[web].args[2]", Op: OpAdded, To: "--debug"},
			}},
		{"a service added whole is one change named by the service",
			testApp{ID: "x"}, testApp{ID: "x", Services: []testService{{Name: "web", Image: "i"}}},
			[]Change{{Path: "services[web]", Op: OpAdded, To: "{…}"}}},
		{"a scalar becoming an object", map[string]any{"a": "x"}, map[string]any{"a": map[string]any{"b": 1}},
			[]Change{{Path: "a", Op: OpChanged, From: "x", To: "{…}"}}},
		{"numbers and bools", map[string]any{"n": 1, "ok": true}, map[string]any{"n": 2, "ok": false},
			[]Change{{Path: "n", Op: OpChanged, From: "1", To: "2"}, {Path: "ok", Op: OpChanged, From: "true", To: "false"}}},
		{"an empty string reads as two quotes, never as absent",
			map[string]any{"a": "x"}, map[string]any{"a": ""},
			[]Change{{Path: "a", Op: OpChanged, From: "x", To: `""`}}},
		{"empty lists and objects count as absent",
			map[string]any{"a": []string{}, "b": map[string]any{}}, map[string]any{}, nil},
		{"items with no key compare by index",
			map[string]any{"l": []map[string]any{{"x": 1}}}, map[string]any{"l": []map[string]any{{"x": 2}}},
			[]Change{{Path: "l[0].x", Op: OpChanged, From: "1", To: "2"}}},
		{"a duplicate key falls back to index",
			map[string]any{"l": []map[string]any{{"name": "a", "x": 1}, {"name": "a", "x": 2}}},
			map[string]any{"l": []map[string]any{{"name": "a", "x": 1}, {"name": "a", "x": 3}}},
			[]Change{{Path: "l[1].x", Op: OpChanged, From: "2", To: "3"}}},
		{"from nothing", nil, map[string]any{"a": "x"}, []Change{{Path: "a", Op: OpAdded, To: "x"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Diff(tc.before, tc.after)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Diff =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

// Every denylisted key, written in any case and as the ending of a longer key, with a sentinel in
// the before and the after, shows in no change, whether the value changed, appeared or went.
func TestDiffNeverShowsASecretKey(t *testing.T) {
	const sentinel = "s3cret-sentinel-057-diff"
	for _, word := range []string{"password", "token", "secret", "secretaccesskey", "privatekey", "apikey", "signingsecret", "hash", "value"} {
		for _, key := range []string{word, strings.ToUpper(word), "my_" + word, "my" + strings.ToUpper(word[:1]) + word[1:]} {
			t.Run(key, func(t *testing.T) {
				for name, pair := range map[string][2]any{
					"changed": {map[string]any{key: sentinel + "-old"}, map[string]any{key: sentinel + "-new"}},
					"added":   {map[string]any{}, map[string]any{key: sentinel}},
					"removed": {map[string]any{key: sentinel}, map[string]any{}},
					"nested":  {map[string]any{"a": map[string]any{key: sentinel + "-1"}}, map[string]any{"a": map[string]any{key: sentinel + "-2"}}},
					"listed": {
						map[string]any{"l": []map[string]any{{"name": "x", key: sentinel + "-1"}}},
						map[string]any{"l": []map[string]any{{"name": "x", key: sentinel + "-2"}}},
					},
					"an object under the key": {map[string]any{key: map[string]any{"k": sentinel}}, map[string]any{key: map[string]any{"k": sentinel + "2"}}},
				} {
					changes := Diff(pair[0], pair[1])
					if len(changes) != 1 {
						t.Errorf("%s: %d changes, want 1: %+v", name, len(changes), changes)
						continue
					}
					raw, _ := json.Marshal(changes)
					if strings.Contains(string(raw), sentinel) || changes[0].From != "" || changes[0].To != "" {
						t.Errorf("%s: the change shows a value: %s", name, raw)
					}
				}
			})
		}
	}
}

func TestDiffCutsValues(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := Diff(map[string]any{"image": "x"}, map[string]any{"image": long})
	if len(got) != 1 || len(got[0].To) > MaxValueBytes || !strings.HasSuffix(got[0].To, "…") || !strings.HasPrefix(got[0].To, "aaaa") {
		t.Errorf("long value: %+v", got)
	}
	// A multi-byte character is never cut in half.
	wide := strings.Repeat("é", 300)
	got = Diff(map[string]any{"v": "x"}, map[string]any{"v": wide})
	if len(got) != 1 || len(got[0].To) > MaxValueBytes || !isValidUTF8(got[0].To) {
		t.Errorf("multi-byte value: %q", got[0].To)
	}
	exact := strings.Repeat("b", MaxValueBytes)
	got = Diff(map[string]any{"v": "x"}, map[string]any{"v": exact})
	if got[0].To != exact {
		t.Errorf("a value of exactly %d bytes was cut", MaxValueBytes)
	}
}

func isValidUTF8(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestDiffCapsChanges(t *testing.T) {
	before, after := map[string]any{}, map[string]any{}
	for i := 0; i < MaxChanges+25; i++ {
		after[strings.Repeat("k", 1)+string(rune('a'+i%26))+strings.Repeat("z", i/26)] = "v"
	}
	got := Diff(before, after)
	if len(got) != MaxChanges+1 {
		t.Fatalf("got %d changes, want %d", len(got), MaxChanges+1)
	}
	if last := got[len(got)-1]; last.Path != "…" || last.To != "25 more changes" {
		t.Errorf("last change = %+v", last)
	}
}

func TestActionsAndKinds(t *testing.T) {
	seen := map[Action]bool{}
	for _, a := range Actions() {
		if seen[a] || a == "" || !ValidAction(a) {
			t.Errorf("action %q: duplicate, empty or invalid", a)
		}
		seen[a] = true
	}
	if ValidAction("made") || ValidAction("") {
		t.Error("ValidAction accepts an action outside the list")
	}
	kinds := map[Kind]bool{}
	for _, k := range Kinds() {
		if kinds[k] || k == "" || !ValidKind(k) {
			t.Errorf("kind %q: duplicate, empty or invalid", k)
		}
		kinds[k] = true
	}
	if ValidKind("thing") {
		t.Error("ValidKind accepts a kind outside the list")
	}
	// The ones the spec names.
	for _, a := range []Action{ActionDeployed, ActionSignInFailed, ActionAcceptedInvite, ActionRefreshedSecrets, ActionChanged} {
		if !seen[a] {
			t.Errorf("action %q is missing", a)
		}
	}
}

func TestEventJSON(t *testing.T) {
	e := Event{ID: 812, At: time.Date(2026, 11, 25, 10, 15, 2, 123456789, time.UTC), Entry: Entry{
		Actor:     Actor{Kind: ActorToken, Email: "ana@example.com", Token: "ci", PersonID: "01J"},
		Door:      DoorAPI,
		Address:   "203.0.113.4",
		Action:    ActionDeployed,
		Operation: "POST /v1/apps/{id}/deployments",
		Resource:  Resource{Kind: KindApp, ID: "shop--staging--web", Name: "web", Project: "shop", Environment: "staging"},
		Changes:   []Change{{Path: "services[web].image", Op: OpChanged, From: "a", To: "b"}, {Path: "x.value", Op: OpChanged}},
		Record:    &Record{Kind: RecordDeployment, ID: "01J"},
		Related:   []Related{{Kind: KindDomain, ID: "new.example.com", Action: ActionCreated}},
	}}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "at", "actor", "door", "address", "action", "operation", "resource", "changes", "record", "related", "fallback"} {
		if _, ok := got[key]; !ok {
			t.Errorf("event JSON has no %q: %s", key, raw)
		}
	}
	if got["at"] != "2026-11-25T10:15:02.123456789Z" {
		t.Errorf("at = %v", got["at"])
	}
	changes := got["changes"].([]any)
	if second := changes[1].(map[string]any); second["from"] != nil || second["to"] != nil {
		t.Errorf("a redacted change has from/to: %v", second)
	}
}

func TestCSV(t *testing.T) {
	var buf bytes.Buffer
	w := NewCSVWriter(&buf)
	at := time.Date(2026, 11, 25, 10, 15, 2, 0, time.UTC)
	rows := []Event{
		{ID: 1, At: at, Entry: Entry{
			Actor: Actor{Kind: ActorPerson, Email: "ana@example.com"}, Door: DoorConsole, Action: ActionUpdated,
			Operation: "POST /settings/registries",
			Resource:  Resource{Kind: KindRegistry, ID: "ghcr.io", Name: "ghcr.io"},
			Changes: []Change{
				{Path: "username", Op: OpChanged, From: "a", To: "b"},
				{Path: "password", Op: OpChanged},
			},
		}},
		{ID: 2, At: at, Entry: Entry{
			Actor: Actor{Kind: ActorAnonymous, Email: "=cmd|' /C calc'!A0"}, Door: DoorAPI, Address: "203.0.113.4",
			Action:   ActionSignInFailed,
			Resource: Resource{Kind: KindAccount, Name: "+1 attack"},
			Changes:  []Change{{Path: "note", Op: OpAdded, To: "-2+3"}},
			Record:   &Record{Kind: RecordDeployment, ID: "01J"},
		}},
	}
	for _, e := range rows {
		if err := w.Write(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("the output isn't CSV: %v", err)
	}
	if len(got) != 3 || !reflect.DeepEqual(got[0], CSVColumns) {
		t.Fatalf("rows = %v", got)
	}
	if got[1][0] != "1" || got[1][1] != "2026-11-25T10:15:02Z" || got[1][3] != "ana@example.com" || got[1][7] != "updated" {
		t.Errorf("row 1 = %v", got[1])
	}
	if want := "username changed a → b; password changed"; got[1][14] != want {
		t.Errorf("changes cell = %q, want %q", got[1][14], want)
	}
	// Formula cells are defused.
	if got[2][3] != "'=cmd|' /C calc'!A0" || got[2][11] != "'+1 attack" || got[2][14] != "note added → -2+3" {
		t.Errorf("row 2 = %v", got[2])
	}
	if got[2][16] != "01J" || got[2][15] != "deployment" {
		t.Errorf("record cells = %v", got[2][15:])
	}
}

func TestSafeCell(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "plain": "plain", "=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@sum": "'@sum", "\tx": "'\tx", "\rx": "'\rx",
		"a=b": "a=b", "é": "é",
	} {
		if got := SafeCell(in); got != want {
			t.Errorf("SafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSVEmptyHasTheHeader(t *testing.T) {
	var buf bytes.Buffer
	w := NewCSVWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != strings.Join(CSVColumns, ",") {
		t.Errorf("empty export = %q", got)
	}
}

// The package is pure: it may not reach for HTTP, the store or the service.
func TestPackageIsPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"net/http", "net", "os/exec",
		"github.com/cosmic-chimps/caravana/internal/store",
		"github.com/cosmic-chimps/caravana/internal/service",
		"github.com/cosmic-chimps/caravana/internal/api",
		"github.com/hashicorp/nomad",
	}
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
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %q, which the audit package must not", name, path)
				}
			}
		}
	}
	if checked < 3 {
		t.Errorf("checked %d files, want at least 3", checked)
	}
}

// A key is judged without its case and without "_", "-" and ".", so a snake_case or kebab-case name of
// a secret (a template input, a settings row's column) is redacted like a camelCase one.
func TestRedactedIgnoresSeparators(t *testing.T) {
	for key, want := range map[string]bool{
		"password": true, "DB_PASSWORD": true, "s3_secret_access_key": true, "secretAccessKey": true,
		"secret-key": true, "SecretKey": true, "oauth.client_secret": true, "private_key": true,
		"api-key": true, "signing_secret": true, "value": true, "id_token": true,
		"name": false, "access_key_id": false, "host": false, "path": false, "region": false, "bucket": false,
	} {
		if got := Redacted(key); got != want {
			t.Errorf("Redacted(%q) = %v, want %v", key, got, want)
		}
	}
}
