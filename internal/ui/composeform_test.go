package ui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// overlayPost makes the posted form for a draft: the marker of each service,
// then the given inputs (svc.<name>.<field> -> value).
func overlayPost(compose string, services []string, inputs map[string]string) url.Values {
	post := url.Values{"compose": {compose}}
	for _, name := range services {
		post.Set("svc."+name+".overlay", "1")
	}
	for k, v := range inputs {
		post.Set(k, v)
	}
	return post
}

// sortedSecrets orders each service's secrets by name, so two requests that
// differ only in the order of their secrets compare equal.
func sortedSecrets(req compose.Request) compose.Request {
	for name, ov := range req.Services {
		slices.SortFunc(ov.Secrets, func(a, b app.Secret) int { return strings.Compare(a.Name, b.Name) })
		req.Services[name] = ov
	}
	return req
}

// The Hollo example's overlay, typed into the form, is the request the API
// decodes from the same JSON.
func TestComposeDraftRoundTrip(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/examples/hollo/overlay.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var want compose.Request
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	want.Compose = "services: {}\n"

	post := overlayPost(want.Compose, []string{"hollo", "postgres"}, map[string]string{
		"svc.hollo.domains":                     "social.example.com:3000",
		"svc.hollo.secret.SECRET_KEY":           "REPLACE: openssl rand -hex 32",
		"svc.hollo.secret.DATABASE_URL":         "postgres://hollo:REPLACE-DB-PASSWORD@postgres:5432/hollo",
		"svc.postgres.secret.POSTGRES_PASSWORD": "REPLACE-DB-PASSWORD",
		"svc.postgres.backupEngine":             "postgres",
		"svc.postgres.backupKeep":               "14",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatalf("parse refused: %+v", d)
	}
	got := d.request()
	if !reflect.DeepEqual(sortedSecrets(got), sortedSecrets(want)) {
		t.Errorf("request = %+v\nwant     %+v", got, want)
	}
}

func TestComposeDraftEmptySecretKeeps(t *testing.T) {
	post := overlayPost("x", []string{"web"}, map[string]string{
		"svc.web.secret.API_KEY": "",
		"svc.web.secret.TOKEN":   "t1",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("parse refused")
	}
	secrets := d.request().Services["web"].Secrets
	if len(secrets) != 2 || secrets[0].Name != "API_KEY" || secrets[0].Value != nil ||
		secrets[1].Name != "TOKEN" || secrets[1].Value == nil || *secrets[1].Value != "t1" {
		t.Errorf("secrets = %+v; want API_KEY with no value (keep) and TOKEN with t1", secrets)
	}
}

func TestComposeDraftNewSecretRows(t *testing.T) {
	post := overlayPost("x", []string{"web"}, map[string]string{
		"svc.web.secret.OLD":       "",
		"svc.web.newSecret.0":      " NEW_ONE ",
		"svc.web.newSecretValue.0": "v1",
		"svc.web.newSecret.2":      "NEW_TWO",
		"svc.web.newSecretValue.2": "",
		"svc.web.newSecret.1":      "",
		"svc.web.newSecretValue.1": "",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("parse refused")
	}
	web, _ := d.service("web")
	var names []string
	for _, s := range web.Secrets {
		names = append(names, s.Name)
	}
	if want := []string{"OLD", "NEW_ONE", "NEW_TWO"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	secrets := d.request().Services["web"].Secrets
	if secrets[1].Value == nil || *secrets[1].Value != "v1" || secrets[2].Value != nil {
		t.Errorf("secrets = %+v; NEW_ONE sends v1, NEW_TWO keeps", secrets)
	}

	for _, tc := range []struct{ name, row, value, want string }{
		{"value without a name", "", "v", "Enter the secret's name"},
		{"name already listed", "OLD", "v", "already listed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			post := overlayPost("x", []string{"web"}, map[string]string{
				"svc.web.secret.OLD": "", "svc.web.newSecret.0": tc.row, "svc.web.newSecretValue.0": tc.value,
			})
			d, ok := parseComposeDraft(post)
			web, _ := d.service("web")
			if ok || !strings.Contains(web.NewSecrets[0].Error.String(), tc.want) {
				t.Errorf("ok = %v, error = %q; want a refusal saying %q", ok, web.NewSecrets[0].Error, tc.want)
			}
		})
	}
}

func TestComposeDraftOverlay(t *testing.T) {
	post := overlayPost("x", []string{"web", "db"}, map[string]string{
		"svc.web.domains":        "a.example.com\r\n\r\n  b.example.com:9100 \n",
		"svc.web.healthPath":     "/healthz",
		"svc.web.healthInterval": "15",
		"svc.web.healthTimeout":  "1.5",
		"svc.web.healthPort":     "3000",
		"svc.db.backupEngine":    "postgres",
		"svc.db.backupSchedule":  "0 4 * * *",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("parse refused")
	}
	req := d.request()
	web := req.Services["web"]
	if want := []compose.Domain{{Host: "a.example.com"}, {Host: "b.example.com", Port: 9100}}; !reflect.DeepEqual(web.Domains, want) {
		t.Errorf("domains = %+v, want %+v", web.Domains, want)
	}
	wantHealth := &compose.Health{Path: "/healthz", Port: 3000, Interval: 15 * time.Second, Timeout: 1500 * time.Millisecond}
	if !reflect.DeepEqual(web.Health, wantHealth) {
		t.Errorf("health = %+v, want %+v", web.Health, wantHealth)
	}
	if want := (&app.Backup{Engine: "postgres", Schedule: "0 4 * * *"}); !reflect.DeepEqual(req.Services["db"].Backup, want) {
		t.Errorf("backup = %+v, want %+v", req.Services["db"].Backup, want)
	}

	// A service with nothing set has no entry: no health check means the default TCP one.
	d, _ = parseComposeDraft(overlayPost("x", []string{"web"}, nil))
	if len(d.request().Services) != 0 {
		t.Errorf("empty overlay sent %+v", d.request().Services)
	}
}

func TestComposeDraftErrors(t *testing.T) {
	tests := []struct {
		name, field, value string
		wantOn             string // the input the error lands on
		wantText           string
	}{
		{"domain port text", "domains", "a.example.com:http", "domains", "host:port"},
		{"domain port zero", "domains", "a.example.com:0", "domains", "host:port"},
		{"domain without a host", "domains", ":80", "domains", "host:port"},
		{"health port text", "healthPort", "web", "healthPort", "1 to 65535"},
		{"interval text", "healthInterval", "soon", "healthInterval", "seconds"},
		{"timeout zero", "healthTimeout", "0", "healthTimeout", "seconds"},
		{"engine unknown", "backupEngine", "mysql", "backupEngine", "none or postgres"},
		{"schedule without an engine", "backupSchedule", "0 4 * * *", "backupEngine", "Choose postgres"},
		{"keep text", "backupKeep", "many", "backupKeep", "1 to 100"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inputs := map[string]string{"svc.db." + tc.field: tc.value}
			if strings.HasPrefix(tc.field, "backup") && tc.field != "backupEngine" {
				inputs["svc.db.backupEngine"] = "postgres"
				if tc.name == "schedule without an engine" {
					delete(inputs, "svc.db.backupEngine")
				}
			}
			d, ok := parseComposeDraft(overlayPost("x", []string{"db"}, inputs))
			if ok {
				t.Fatal("parse accepted it")
			}
			db, _ := d.service("db")
			for _, in := range db.inputs() {
				got := in.field.Error.String()
				switch {
				case in.name == tc.wantOn && !strings.Contains(got, tc.wantText):
					t.Errorf("%s error = %q, want it to say %q", in.name, got, tc.wantText)
				case in.name != tc.wantOn && got != "":
					t.Errorf("%s has an unexpected error %q", in.name, got)
				}
			}
		})
	}
}

func TestComposeDraftFrom(t *testing.T) {
	stored := app.App{ID: "shop", Services: []app.Service{
		{Name: "web", Image: "ghcr.io/acme/shop:2",
			Ports:   []app.Port{{Name: "http", ContainerPort: 3000}, {Name: "metrics", ContainerPort: 9100}},
			Domains: []app.Domain{{Host: "shop.example.com", Port: "http"}},
			Health:  &app.HealthCheck{Path: "/healthz", Port: "http", Interval: 15 * time.Second},
			Secrets: []app.Secret{{Name: "API_KEY"}}},
		{Name: "db", Image: "postgres:17", Backup: &app.Backup{Engine: "postgres", Keep: 3}},
		{Name: "docs", Image: "nginx", Ports: []app.Port{{Name: "http", ContainerPort: 80}},
			Domains: []app.Domain{{Host: "docs.example.com", Port: "http"}}},
	}}
	d := draftFrom(stored)

	web, _ := d.service("web")
	if web.Domains.Value != "shop.example.com:3000" || web.HealthPath.Value != "/healthz" ||
		web.HealthInterval.Value != "15" || web.HealthPort.Value != "3000" {
		t.Errorf("web = %q, %q, %q, %q; the several-ports service names its ports", web.Domains.Value, web.HealthPath.Value, web.HealthInterval.Value, web.HealthPort.Value)
	}
	if len(web.Secrets) != 1 || web.Secrets[0].Name != "API_KEY" || !web.Secrets[0].Exists {
		t.Errorf("web secrets = %+v, want API_KEY, existing", web.Secrets)
	}
	docs, _ := d.service("docs")
	if docs.Domains.Value != "docs.example.com" {
		t.Errorf("docs domains = %q; a one-port service names no port", docs.Domains.Value)
	}
	db, _ := d.service("db")
	if db.BackupEngine.Value != "postgres" || db.BackupKeep.Value != "3" || db.BackupSchedule.Value != "" {
		t.Errorf("db backup = %q, %q, %q", db.BackupEngine.Value, db.BackupSchedule.Value, db.BackupKeep.Value)
	}

	// The prefill is a request that, translated, would give back what is stored.
	req := d.request()
	want := compose.Overlay{
		Domains: []compose.Domain{{Host: "shop.example.com", Port: 3000}},
		Health:  &compose.Health{Path: "/healthz", Port: 3000, Interval: 15 * time.Second},
		Secrets: []app.Secret{{Name: "API_KEY"}},
	}
	if !reflect.DeepEqual(req.Services["web"], want) {
		t.Errorf("web overlay = %+v, want %+v", req.Services["web"], want)
	}
	if req.Services["db"].Backup == nil || req.Services["db"].Backup.Keep != 3 {
		t.Errorf("db overlay = %+v, want its backup", req.Services["db"])
	}
	if names := serviceNames(d); !slices.Equal(names, []string{"db", "docs", "web"}) {
		t.Errorf("services = %v, want them sorted", names)
	}
}

func serviceNames(d composeDraft) []string {
	var names []string
	for _, s := range d.Services {
		names = append(names, s.Name)
	}
	return names
}

func TestComposeDraftKeepStored(t *testing.T) {
	stored := app.App{Services: []app.Service{
		{Name: "web", Secrets: []app.Secret{{Name: "API_KEY"}, {Name: "TOKEN"}}},
		{Name: "gone", Secrets: []app.Secret{{Name: "X"}}},
	}}
	// The form shows TOKEN only: the stored API_KEY still goes along, as a keep.
	post := overlayPost("x", []string{"web", "worker"}, map[string]string{
		"svc.web.secret.TOKEN": "", "svc.web.newSecret.0": "NEW", "svc.web.newSecretValue.0": "v",
	})
	d, _ := parseComposeDraft(post)
	d.keepStored(stored)

	web, _ := d.service("web")
	var got []string
	for _, s := range web.Secrets {
		got = append(got, fmt.Sprintf("%s/%v", s.Name, s.Exists))
	}
	if want := []string{"API_KEY/true", "NEW/false", "TOKEN/true"}; !slices.Equal(got, want) {
		t.Errorf("secrets = %v, want %v", got, want)
	}
	secrets := d.request().Services["web"].Secrets
	if len(secrets) != 3 || secrets[0].Value != nil || secrets[1].Value == nil || secrets[2].Value != nil {
		t.Errorf("request secrets = %+v; API_KEY and TOKEN keep, NEW sends its value", secrets)
	}
	if _, ok := d.service("gone"); ok {
		t.Error("a stored service the post doesn't have was added")
	}
}

// A page gets the draft's inputs and never a secret value, wherever it looks.
func TestComposeDraftForPageHasNoValues(t *testing.T) {
	const secret = "s3cr3t-value-9f2"
	post := overlayPost("x", []string{"web"}, map[string]string{
		"svc.web.secret.API_KEY": secret, "svc.web.newSecret.0": "NEW", "svc.web.newSecretValue.0": secret + "2",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("parse refused")
	}
	if !strings.Contains(fmt.Sprintf("%+v", d.request()), "API_KEY") || len(d.values) == 0 {
		t.Fatal("the draft lost the values it must send")
	}
	page := d.forPage()
	if page.values != nil {
		t.Error("forPage kept the values")
	}
	if dump := fmt.Sprintf("%+v", page); strings.Contains(dump, secret) {
		t.Errorf("the page's draft holds a secret value: %s", dump)
	}
	if page.request().Services["web"].Secrets[0].Value != nil {
		t.Error("a page's draft can still send a value")
	}
}

func TestComposeDraftMarkFirstError(t *testing.T) {
	post := overlayPost("x", []string{"db", "web"}, map[string]string{
		"svc.db.backupKeep":     "many",
		"svc.db.backupEngine":   "postgres",
		"svc.web.healthTimeout": "0",
	})
	d, ok := parseComposeDraft(post)
	if ok {
		t.Fatal("parse accepted it")
	}
	d.markFirstError()
	var alerts []string
	for _, s := range d.Services {
		for _, in := range s.inputs() {
			if in.field.Alert {
				alerts = append(alerts, s.Name+"."+in.name)
			}
		}
	}
	if want := []string{"db.backupKeep"}; !slices.Equal(alerts, want) {
		t.Errorf("announced %v, want %v", alerts, want)
	}

	// A problem with the name or the file comes before any service's.
	d.Name.Error = sentence{plain("Enter a name for the app.")}
	d.markFirstError()
	if !d.Name.Alert {
		t.Error("the name's error wasn't announced")
	}
}
