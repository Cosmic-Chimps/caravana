package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

const (
	kumaDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	futureCatVer = "2027.01.01"
)

// templateLab is a service with a store whose directory is known, the log in
// a buffer, and a fake registry that knows Uptime Kuma's image.
type templateLab struct {
	*env
	dir  string
	logs *syncBuffer
}

func newTemplateLab(t *testing.T) *templateLab {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.registry.AddIndex("louislam/uptime-kuma", "2.5.11", kumaDigest)
	e.nomad.Succeed()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, log)
	return &templateLab{env: e, dir: dir, logs: logs}
}

// useCatalog makes ts the catalog in use, as a fetched one stored in the row.
func (l *templateLab) useCatalog(t *testing.T, version string, ts ...templates.Template) {
	t.Helper()
	bundle, err := json.Marshal(templates.Catalog{Format: templates.Format, Version: version, Templates: ts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templates.ParseCatalog(bundle); err != nil {
		t.Fatalf("the test catalog is invalid: %v", err)
	}
	row := store.TemplateCatalogRow{Bundle: bundle, BundleSum: templates.SumOf(bundle), Version: version,
		FetchedAt: time.Now(), CheckedAt: time.Now(), CheckedOKAt: time.Now()}
	if err := l.store.SaveTemplateCheck(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

// whoamiTemplate is a small template the fake registry can resolve: one web
// service (traefik/whoami) with a domain, a generated password and a secret
// input. Its image is :latest, which Check refuses; the service doesn't check.
func whoamiTemplate() templates.Template {
	secret := func(s string) *string { return &s }
	return templates.Template{
		Name: "demo", Title: "Demo", Version: "1.0-1", Description: "A demo.", Tags: []string{"demo", "web"},
		Upstream: "https://example.com", AppName: "demo", Notes: "Open https://{{domain}}.",
		Inputs: []templates.Input{
			{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "demo.example.com"},
			{Name: "api_key", Label: "API key", Type: templates.TypeSecret, Example: "example-key"},
		},
		Generated: []templates.Generated{{Name: "db_password", Length: 32, Alphabet: templates.AlphabetBase64URL}},
		Services: map[string]compose.Overlay{"web": {
			Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
			Health:  &compose.Health{Path: "/"},
			Secrets: []app.Secret{
				{Name: "DB_PASSWORD", Value: secret("{{db_password}}")},
				{Name: "API_KEY", Value: secret("{{api_key}}")},
			},
		}},
		Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n    environment:\n      PUBLIC_URL: \"https://{{domain}}\"\n",
	}
}

func installed(t *testing.T, l *templateLab, res DeployResult) store.DeploymentRecord {
	t.Helper()
	final := waitFinal(t, l.store, res.Deployment.ID)
	if final.Status != store.StatusHealthy {
		t.Fatalf("deployment = %s (%s), want healthy", final.Status, final.Error)
	}
	return final
}

func TestListTemplates(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)

	all, err := l.svc.ListTemplates(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Catalog.Source != CatalogEmbedded || all.Catalog.Version != all.Catalog.EmbeddedVersion || !all.Catalog.Checking {
		t.Errorf("catalog = %+v", all.Catalog)
	}
	if len(all.Templates) != 6 {
		t.Fatalf("%d templates, want the six built in", len(all.Templates))
	}
	for _, tpl := range all.Templates {
		if !tpl.Available || tpl.Unavailable != "" {
			t.Errorf("%s: available=%v %q", tpl.Name, tpl.Available, tpl.Unavailable)
		}
	}

	names := func(list TemplateList) string {
		var out []string
		for _, tpl := range list.Templates {
			out = append(out, tpl.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tt := range []struct{ q, tag, want string }{
		{"status", "", "uptime-kuma"},
		{"STATUS", "", "uptime-kuma"},
		{"", "monitoring", "uptime-kuma"},
		{"", "database", "adminer,postgres"},
		{"fediverse", "", "hollo"},
		{"postgres", "", "postgres"},
		{"", "nope", ""},
		{"zzzz", "", ""},
		{"status", "database", ""},
	} {
		got, err := l.svc.ListTemplates(ctx, tt.q, tt.tag)
		if err != nil {
			t.Fatal(err)
		}
		if names(got) != tt.want {
			t.Errorf("ListTemplates(%q, %q) = %q, want %q", tt.q, tt.tag, names(got), tt.want)
		}
	}
}

func TestTemplateAvailability(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	broken, new := whoamiTemplate(), whoamiTemplate()
	broken.Name, broken.Broken = "broken", "its image moved"
	new.Name, new.Requires = "newer", "99.0.0"
	fine := whoamiTemplate()
	fine.Name = "fine"
	l.useCatalog(t, futureCatVer, broken, fine, new)

	t.Run("a development build runs everything that isn't broken", func(t *testing.T) {
		list, _ := l.svc.ListTemplates(ctx, "", "")
		got := map[string]string{}
		for _, tpl := range list.Templates {
			got[tpl.Name] = fmt.Sprintf("%v/%s", tpl.Available, tpl.Unavailable)
		}
		want := map[string]string{"broken": "false/its image moved", "fine": "true/", "newer": "true/"}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
	})
	t.Run("a release that is too old", func(t *testing.T) {
		l.svc.SetVersion("0.20.0")
		info, err := l.svc.Template(ctx, "newer")
		if err != nil || info.Available || info.Unavailable != "needs caravana 99.0.0 or newer" {
			t.Errorf("Template = %+v, %v", info, err)
		}
		for _, name := range []string{"newer", "broken"} {
			_, err := l.svc.InstallTemplate(ctx, name, "x", map[string]string{"domain": "a.example.com", "api_key": "k"}, TemplateInstallOptions{}, app.TriggerManual)
			var unavailable *TemplateUnavailableError
			if !errors.Is(err, ErrTemplateUnavailable) || !errors.As(err, &unavailable) || unavailable.Reason == "" {
				t.Errorf("install of %s = %v, want ErrTemplateUnavailable with a reason", name, err)
			}
			if _, err := l.svc.PreviewTemplate(ctx, name, "x", map[string]string{"domain": "a.example.com"}, TemplateInstallOptions{}); !errors.Is(err, ErrTemplateUnavailable) {
				t.Errorf("preview of %s = %v", name, err)
			}
		}
		if _, err := l.store.GetApp(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an app was made: %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		if _, err := l.svc.Template(ctx, "nope"); !errors.Is(err, ErrTemplateNotFound) {
			t.Errorf("Template(nope) = %v", err)
		}
		if _, err := l.svc.InstallTemplate(ctx, "nope", "x", nil, TemplateInstallOptions{}, app.TriggerManual); !errors.Is(err, ErrTemplateNotFound) {
			t.Errorf("InstallTemplate(nope) = %v", err)
		}
	})
}

// A real embedded template, installed with only a domain typed (SC-001 in the
// service's terms).
func TestInstallUptimeKuma(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	setDestination(t, l.env) // a backup policy needs a place to send backups to

	res, err := l.svc.InstallTemplate(ctx, "uptime-kuma", "status", map[string]string{"domain": " Status.Example.com "}, TemplateInstallOptions{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("InstallTemplate: %v", err)
	}
	installed(t, l, res)

	rec, err := l.svc.GetApp(ctx, "status")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Template != (store.AppTemplate{Name: "uptime-kuma", Version: "2.5.11-1"}) {
		t.Errorf("Template = %+v", rec.Template)
	}
	svc := rec.App.Services[0]
	if svc.Domains[0].Host != "status.example.com" || svc.Health == nil || svc.Health.Path != "/robots.txt" ||
		svc.Backup == nil || svc.Backup.Engine != app.BackupVolume {
		t.Errorf("service = %+v", svc)
	}
	if rec.App.Source.Kind != app.SourceCompose || rec.Compose == nil || !strings.Contains(rec.Compose.Compose, "louislam/uptime-kuma:2.5.11") {
		t.Errorf("the kept file = %+v", rec.Compose)
	}
	if rec.Compose.Services["uptime-kuma"].Domains[0].Host != "status.example.com" {
		t.Errorf("the kept overlay's domain = %+v", rec.Compose.Services["uptime-kuma"].Domains)
	}
	// The summary lists carry it too.
	list, err := l.svc.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Template.Name != "uptime-kuma" {
		t.Errorf("ListApps = %+v", list)
	}

	t.Run("the same name again changes nothing", func(t *testing.T) {
		before, _ := l.svc.GetApp(ctx, "status")
		_, err := l.svc.InstallTemplate(ctx, "uptime-kuma", "status", map[string]string{"domain": "other.example.com"}, TemplateInstallOptions{}, app.TriggerManual)
		if !errors.Is(err, ErrAppExists) {
			t.Fatalf("error = %v, want ErrAppExists", err)
		}
		after, _ := l.svc.GetApp(ctx, "status")
		if after.Revision != before.Revision || after.Template != before.Template {
			t.Errorf("the app changed: %+v -> %+v", before, after)
		}
	})

	t.Run("a domain that is another app's is refused at the input", func(t *testing.T) {
		_, err := l.svc.InstallTemplate(ctx, "uptime-kuma", "status2", map[string]string{"domain": "status.example.com"}, TemplateInstallOptions{}, app.TriggerManual)
		var taken *DomainTakenError
		if !errors.Is(err, ErrDomainTaken) || !errors.As(err, &taken) || taken.Field != "inputs.domain" || taken.App != "status" {
			t.Errorf("error = %v (%+v), want a DomainTakenError at inputs.domain", err, taken)
		}
		if _, err := l.store.GetApp(ctx, "status2"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an app was made: %v", err)
		}
	})
}

func TestInstallTemplateRefusals(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	for _, tt := range []struct {
		name   string
		id     string
		inputs map[string]string
		field  string
	}{
		{"a bad domain", "status", map[string]string{"domain": "not a domain"}, "inputs.domain"},
		{"no domain", "status", nil, "inputs.domain"},
		{"an unknown input", "status", map[string]string{"domain": "a.example.com", "surprise": "x"}, "inputs.surprise"},
		{"a bad app ID", "Not A Name", map[string]string{"domain": "a.example.com"}, "id"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := l.svc.InstallTemplate(ctx, "uptime-kuma", tt.id, tt.inputs, TemplateInstallOptions{}, app.TriggerManual)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tt.field {
				t.Errorf("error = %v, want a field error at %s", err, tt.field)
			}
		})
	}
	t.Run("a project that doesn't exist", func(t *testing.T) {
		_, err := l.svc.InstallTemplate(ctx, "uptime-kuma", "nope--production--status", map[string]string{"domain": "a.example.com"}, TemplateInstallOptions{}, app.TriggerManual)
		var place *PlaceError
		if !errors.As(err, &place) {
			t.Errorf("error = %v, want a PlaceError", err)
		}
	})
	apps, _ := l.svc.ListApps(ctx)
	if len(apps) != 0 {
		t.Errorf("%d apps were made by refused installs", len(apps))
	}
}

// A deploy that fails after the app was saved still leaves the template on the
// app: the image can't be found.
func TestInstallTemplateFailedDeployKeepsTemplate(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	missing := whoamiTemplate()
	missing.Compose = strings.Replace(missing.Compose, "traefik/whoami:latest", "acme/missing:1.0", 1)
	l.useCatalog(t, futureCatVer, missing)

	_, err := l.svc.InstallTemplate(ctx, "demo", "demo", map[string]string{"domain": "a.example.com", "api_key": "k"}, TemplateInstallOptions{}, app.TriggerManual)
	if err == nil {
		t.Fatal("a missing image deployed")
	}
	rec, getErr := l.svc.GetApp(ctx, "demo")
	if getErr != nil {
		t.Fatalf("the app: %v", getErr)
	}
	if rec.Template.Name != "demo" || rec.Template.Version != "1.0-1" {
		t.Errorf("Template = %+v after a failed deploy", rec.Template)
	}
}

// sentinelReader yields the bytes that make the alphabet's characters spell
// the sentinel, so a generated secret has a value a test can look for.
type sentinelReader struct {
	mu   sync.Mutex
	data []byte
	read int
}

func newSentinelReader(sentinel string) *sentinelReader {
	const base64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	r := &sentinelReader{}
	for _, c := range sentinel {
		r.data = append(r.data, byte(strings.IndexRune(base64url, c)))
	}
	r.data = append(r.data, make([]byte, 64)...) // the sampler reads twice what it needs
	return r
}

func (r *sentinelReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := copy(p, r.data[r.read:])
	r.read += n
	if n == 0 {
		return 0, errors.New("the sentinel reader is empty")
	}
	return n, nil
}

func (r *sentinelReader) used() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read
}

// TestInstallTemplateKeepsSecretsOut: a generated secret and a secret input
// reach the secret store, and nothing else: no store row, kept file,
// deployment record, answer, Nomad job, error or log line (FR-003).
func TestInstallTemplateKeepsSecretsOut(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	const generated = "s3cret-sentinel-035-generated-xx" // 32 characters of the base64url alphabet
	const typed = "s3cret-sentinel-035-typed"
	if len(generated) != 32 {
		t.Fatalf("the generated sentinel has %d characters", len(generated))
	}
	l.useCatalog(t, futureCatVer, whoamiTemplate())
	l.svc.SetTemplateRandom(newSentinelReader(generated))

	inputs := map[string]string{"domain": "demo.example.com", "api_key": typed}
	preview, err := l.svc.PreviewTemplate(ctx, "demo", "demo", inputs, TemplateInstallOptions{})
	if err != nil {
		t.Fatalf("PreviewTemplate: %v", err)
	}
	if used := l.svc.templateRand.(*sentinelReader).used(); used != 0 {
		t.Errorf("a preview read %d random bytes: it must generate nothing", used)
	}
	res, err := l.svc.InstallTemplate(ctx, "demo", "demo", inputs, TemplateInstallOptions{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("InstallTemplate: %v", err)
	}
	final := installed(t, l, res)
	waitFor(t, "the secrets to be committed", func() bool { owner, _ := l.secrets.Kept("demo"); return owner == "" })

	// The secret store has the values.
	live := l.secrets.Live("demo")["web"]
	if live["API_KEY"] != typed || live["DB_PASSWORD"] != generated {
		t.Errorf("the secret store has %v", live)
	}

	// Nothing else does.
	rec, _ := l.svc.GetApp(ctx, "demo")
	answers := map[string]any{
		"the preview":          preview,
		"the install's answer": res,
		"the final record":     final,
		"the stored app":       rec,
		"the Nomad job":        l.nomad.Job("demo"),
		"the deployments":      mustList(t, l),
	}
	for what, v := range answers {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		for _, secret := range []string{generated, typed} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s holds %q", what, secret)
			}
		}
	}
	raw := rawStore(t, l.dir)
	logs := l.logs.String()
	for _, secret := range []string{generated, typed} {
		if strings.Contains(raw, secret) {
			t.Errorf("the SQLite file holds %q", secret)
		}
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs)
		}
	}
	// The kept Compose file and its overlay have the names only.
	if rec.Compose == nil || len(rec.Compose.Services["web"].Secrets) != 2 {
		t.Fatalf("kept file = %+v", rec.Compose)
	}
	for _, s := range rec.Compose.Services["web"].Secrets {
		if s.Value != nil {
			t.Errorf("the kept overlay has a value for %s", s.Name)
		}
	}

	t.Run("a refused install holds no secret in its error", func(t *testing.T) {
		bad := map[string]string{"domain": "not a domain", "api_key": typed}
		_, err := l.svc.InstallTemplate(ctx, "demo", "demo2", bad, TemplateInstallOptions{}, app.TriggerManual)
		if err == nil || strings.Contains(err.Error(), typed) {
			t.Errorf("error = %v", err)
		}
		_, err = l.svc.InstallTemplate(ctx, "demo", "demo2", map[string]string{"domain": "a.example.com", "api_key": "a\n" + typed}, TemplateInstallOptions{}, app.TriggerManual)
		if err == nil || strings.Contains(err.Error(), typed) {
			t.Errorf("error = %v", err)
		}
		if _, err := l.svc.PreviewTemplate(ctx, "demo", "demo2", bad, TemplateInstallOptions{}); err == nil || strings.Contains(err.Error(), typed) {
			t.Errorf("preview error = %v", err)
		}
		if strings.Contains(l.logs.String(), typed) {
			t.Error("a refusal logged the secret")
		}
	})
}

func mustList(t *testing.T, l *templateLab) []store.DeploymentRecord {
	t.Helper()
	recs, err := l.svc.ListDeployments(context.Background(), "demo", 50)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestPreviewTemplate(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	l.useCatalog(t, futureCatVer, whoamiTemplate())

	p, err := l.svc.PreviewTemplate(ctx, "demo", "demo", map[string]string{"domain": "demo.example.com"}, TemplateInstallOptions{}) // no secret input typed
	if err != nil {
		t.Fatalf("PreviewTemplate: %v", err)
	}
	if !strings.Contains(p.Compose, `PUBLIC_URL: "https://demo.example.com"`) || len(p.App.Services) != 1 {
		t.Errorf("preview = %+v", p)
	}
	for _, s := range p.App.Services[0].Secrets {
		if s.Value != nil {
			t.Errorf("secret %s has a value in a preview", s.Name)
		}
	}
	if names := p.App.Services[0].Secrets; len(names) != 2 || names[0].Name != "DB_PASSWORD" {
		t.Errorf("secrets = %+v", names)
	}
	if _, err := l.store.GetApp(ctx, "demo"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a preview made an app")
	}
	var fe *app.FieldError
	if _, err := l.svc.PreviewTemplate(ctx, "demo", "demo", map[string]string{"domain": "bad"}, TemplateInstallOptions{}); !errors.As(err, &fe) || fe.Field != "inputs.domain" {
		t.Errorf("a bad domain: %v", err)
	}
}

// ---- the catalog check ----

// catalogServer stands in for the catalog repository's release.
type catalogServer struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string]string // path -> body
	status   map[string]int    // path -> status, default 200
	requests []string          // "path|user-agent|rawquery|cookie|extra headers"
}

func newCatalogServer(t *testing.T) *catalogServer {
	t.Helper()
	cs := &catalogServer{files: map[string]string{}, status: map[string]int{}}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		cs.requests = append(cs.requests, fmt.Sprintf("%s|%s|%s|%s|%s", r.URL.Path, r.UserAgent(), r.URL.RawQuery, r.Header.Get("Cookie"), r.Header.Get("If-None-Match")))
		if code := cs.status[r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			return
		}
		body, ok := cs.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(cs.Close)
	return cs
}

// publish makes bundle the release's catalog.json with its true sums.
func (cs *catalogServer) publish(bundle []byte) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.files["/SHA256SUMS"] = string(templates.Sums(bundle))
	cs.files["/catalog.json"] = string(bundle)
}

func (cs *catalogServer) paths() []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var out []string
	for _, r := range cs.requests {
		out = append(out, strings.SplitN(r, "|", 2)[0])
	}
	return out
}

func catalogBundle(t *testing.T, version string, ts ...templates.Template) []byte {
	t.Helper()
	b, err := json.Marshal(templates.Catalog{Format: templates.Format, Version: version, Templates: ts})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// goodCatalogTemplate is a template that passes templates.Check, which a fetched
// catalog's templates must.
func goodCatalogTemplate(name string) templates.Template {
	return templates.Template{
		Name: name, Title: "Whoami", Version: "1-1", Description: "Shows who answered.", Tags: []string{"demo"},
		Upstream: "https://example.com", AppName: name,
		Inputs:   []templates.Input{{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "who.example.com"}},
		Services: map[string]compose.Overlay{"web": {Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}}, Health: &compose.Health{Path: "/"}}},
		Compose:  "services:\n  web:\n    image: traefik/whoami:1.10\n    expose: [\"80\"]\n",
	}
}

func TestTemplateCatalogCheck(t *testing.T) {
	ctx := context.Background()

	check := func(t *testing.T) (*templateLab, *catalogServer) {
		t.Helper()
		l := newTemplateLab(t)
		cs := newCatalogServer(t)
		l.svc.SetTemplatesURL(cs.URL)
		return l, cs
	}

	t.Run("a newer catalog is kept and offered", func(t *testing.T) {
		l, cs := check(t)
		cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami"), goodCatalogTemplate("whoami2")))
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatalf("check: %v", err)
		}
		view, _ := l.svc.TemplateCatalog(ctx)
		if view.Source != CatalogFetched || view.Version != futureCatVer || view.FetchedAt.IsZero() || view.CheckedOKAt.IsZero() || view.Error != "" {
			t.Errorf("catalog = %+v", view)
		}
		list, _ := l.svc.ListTemplates(ctx, "", "")
		if len(list.Templates) != 2 || list.Templates[0].Name != "whoami" {
			t.Errorf("templates = %d, want the fetched two", len(list.Templates))
		}
		// The request carries the User-Agent and nothing about the server.
		for _, r := range cs.requests {
			f := strings.Split(r, "|")
			if !strings.HasPrefix(f[1], "caravana/") || f[2] != "" || f[3] != "" || f[4] != "" {
				t.Errorf("request %q: a query, a cookie or another header went out", r)
			}
		}
		if got := cs.paths(); strings.Join(got, ",") != "/SHA256SUMS,/catalog.json" {
			t.Errorf("requests = %v", got)
		}

		t.Run("the same sum next day asks for the sums only", func(t *testing.T) {
			if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
				t.Fatal(err)
			}
			if got := cs.paths(); strings.Join(got, ",") != "/SHA256SUMS,/catalog.json,/SHA256SUMS" {
				t.Errorf("requests = %v", got)
			}
			if view, _ := l.svc.TemplateCatalog(ctx); view.Version != futureCatVer || view.Error != "" {
				t.Errorf("catalog = %+v", view)
			}
		})
		t.Run("an even newer one replaces it, an older one doesn't", func(t *testing.T) {
			cs.publish(catalogBundle(t, "2027.02.01", goodCatalogTemplate("whoami3")))
			if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
				t.Fatal(err)
			}
			if view, _ := l.svc.TemplateCatalog(ctx); view.Version != "2027.02.01" {
				t.Errorf("version = %s", view.Version)
			}
			cs.publish(catalogBundle(t, "2027.01.15", goodCatalogTemplate("whoami4")))
			if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
				t.Fatal(err)
			}
			if view, _ := l.svc.TemplateCatalog(ctx); view.Version != "2027.02.01" {
				t.Errorf("an older catalog replaced the kept one: %s", view.Version)
			}
		})
	})

	t.Run("a catalog older than the built-in one is not kept, and not fetched again", func(t *testing.T) {
		l, cs := check(t)
		cs.publish(catalogBundle(t, "2020.01.01", goodCatalogTemplate("whoami")))
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatal(err)
		}
		view, _ := l.svc.TemplateCatalog(ctx)
		if view.Source != CatalogEmbedded || !view.FetchedAt.IsZero() || view.Error != "" || view.CheckedOKAt.IsZero() {
			t.Errorf("catalog = %+v", view)
		}
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatal(err)
		}
		if got := cs.paths(); strings.Join(got, ",") != "/SHA256SUMS,/catalog.json,/SHA256SUMS" {
			t.Errorf("requests = %v: the old catalog was fetched twice", got)
		}
	})

	t.Run("the built-in catalog's own sum asks for no download", func(t *testing.T) {
		l, cs := check(t)
		_, bundle := templates.Embedded()
		cs.mu.Lock()
		cs.files["/SHA256SUMS"] = string(templates.Sums(bundle))
		cs.mu.Unlock()
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatal(err)
		}
		if got := cs.paths(); strings.Join(got, ",") != "/SHA256SUMS" {
			t.Errorf("requests = %v", got)
		}
	})

	failures := []struct {
		name   string
		setup  func(cs *catalogServer, t *testing.T)
		failed string
	}{
		{"a bad sum", func(cs *catalogServer, t *testing.T) {
			cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami")))
			cs.mu.Lock()
			cs.files["/catalog.json"] += " "
			cs.mu.Unlock()
		}, failCatalogSum},
		{"an unknown format", func(cs *catalogServer, t *testing.T) {
			cs.publish([]byte(`{"format": 2, "version": "2027.01.01", "templates": []}`))
		}, failCatalogFormat},
		{"an invalid template", func(cs *catalogServer, t *testing.T) {
			bad := goodCatalogTemplate("whoami")
			bad.Compose = "# {{unknown}}"
			cs.publish(catalogBundle(t, futureCatVer, bad))
		}, failCatalogBad},
		{"a template that doesn't pass Check", func(cs *catalogServer, t *testing.T) {
			bad := goodCatalogTemplate("whoami")
			bad.Compose = strings.Replace(bad.Compose, "traefik/whoami:1.10", "traefik/whoami:latest", 1)
			cs.publish(catalogBundle(t, futureCatVer, bad))
		}, failCatalogCheck},
		{"not JSON", func(cs *catalogServer, t *testing.T) { cs.publish([]byte("<html>")) }, failCatalogBad},
		{"sums without our line", func(cs *catalogServer, t *testing.T) {
			cs.mu.Lock()
			cs.files["/SHA256SUMS"] = strings.Repeat("0", 64) + "  other.json\n"
			cs.mu.Unlock()
		}, failCatalogBad},
		{"no release yet", func(cs *catalogServer, t *testing.T) {}, failCatalogNone},
		{"a server error", func(cs *catalogServer, t *testing.T) { cs.status["/SHA256SUMS"] = 500 }, failCatalogDown},
		{"a rate limit", func(cs *catalogServer, t *testing.T) { cs.status["/SHA256SUMS"] = 429 }, failGitHubLimit},
		{"a catalog that is too big", func(cs *catalogServer, t *testing.T) {
			big := []byte(`{"format": 1, "version": "2027.01.01", "pad": "` + strings.Repeat("x", 4<<20) + `"}`)
			cs.publish(big)
		}, failCatalogBad},
		{"sums that are too big", func(cs *catalogServer, t *testing.T) {
			cs.mu.Lock()
			cs.files["/SHA256SUMS"] = strings.Repeat("x", 5<<10)
			cs.mu.Unlock()
		}, failCatalogBad},
	}
	for _, tt := range failures {
		t.Run(tt.name+" changes nothing and says so in Caravana's words", func(t *testing.T) {
			l, cs := check(t)
			tt.setup(cs, t)
			if err := l.svc.fetchAndRecordTemplates(ctx); err == nil {
				t.Fatal("the check reported success")
			}
			view, _ := l.svc.TemplateCatalog(ctx)
			if view.Source != CatalogEmbedded || view.Error != tt.failed || !view.CheckedOKAt.IsZero() || view.CheckedAt.IsZero() {
				t.Errorf("catalog = %+v, want embedded with %q", view, tt.failed)
			}
			list, _ := l.svc.ListTemplates(ctx, "", "")
			if len(list.Templates) != 6 {
				t.Errorf("%d templates are offered", len(list.Templates))
			}
		})
	}

	t.Run("a failed check keeps the catalog that was fetched", func(t *testing.T) {
		l, cs := check(t)
		cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami")))
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatal(err)
		}
		cs.status["/SHA256SUMS"] = 500
		if err := l.svc.fetchAndRecordTemplates(ctx); err == nil {
			t.Fatal("no failure")
		}
		view, _ := l.svc.TemplateCatalog(ctx)
		if view.Source != CatalogFetched || view.Version != futureCatVer || view.Error != failCatalogDown || view.CheckedOKAt.IsZero() {
			t.Errorf("catalog = %+v", view)
		}
	})

	t.Run("a stored catalog that no longer matches its sum is not trusted", func(t *testing.T) {
		l, cs := check(t)
		cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami")))
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatal(err)
		}
		row, _ := l.store.TemplateCatalog(ctx)
		row.Bundle = append(row.Bundle, ' ')
		if err := l.store.SaveTemplateCheck(ctx, row); err != nil {
			t.Fatal(err)
		}
		l.svc.templates.cacheSum = "" // a process that restarted
		if view, _ := l.svc.TemplateCatalog(ctx); view.Source != CatalogEmbedded {
			t.Errorf("source = %s", view.Source)
		}
	})

	t.Run("with checking off no request is made, ever", func(t *testing.T) {
		l, cs := check(t)
		cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami")))
		if err := l.svc.SetTemplateChecking(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := l.svc.CheckTemplates(ctx); !errors.Is(err, ErrCheckDisabled) {
			t.Errorf("CheckTemplates = %v, want ErrCheckDisabled", err)
		}
		if err := l.svc.fetchAndRecordTemplates(ctx); !errors.Is(err, ErrCheckDisabled) {
			t.Errorf("a daily pass = %v, want ErrCheckDisabled", err)
		}
		l.svc.templateCheck(ctx)
		if got := cs.paths(); len(got) != 0 {
			t.Errorf("requests = %v with checking off", got)
		}
		view, _ := l.svc.TemplateCatalog(ctx)
		if view.Checking || view.Source != CatalogEmbedded {
			t.Errorf("catalog = %+v", view)
		}
		// The built-in catalog is still offered, and a fetched one that was already kept stays.
		if list, _ := l.svc.ListTemplates(ctx, "", ""); len(list.Templates) != 6 {
			t.Errorf("%d templates", len(list.Templates))
		}
	})

	t.Run("Check now runs once, then is too soon", func(t *testing.T) {
		l, cs := check(t)
		cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami")))
		if err := l.svc.CheckTemplates(ctx); err != nil {
			t.Fatalf("CheckTemplates: %v", err)
		}
		var soon *CheckTooSoonError
		if err := l.svc.CheckTemplates(ctx); !errors.Is(err, ErrCheckTooSoon) || !errors.As(err, &soon) || soon.Again.IsZero() {
			t.Errorf("the second call = %v, want ErrCheckTooSoon", err)
		}
		waitFor(t, "the check to finish", func() bool {
			v, _ := l.svc.TemplateCatalog(ctx)
			return !v.Running && !v.CheckedAt.IsZero()
		})
		if view, _ := l.svc.TemplateCatalog(ctx); view.Source != CatalogFetched {
			t.Errorf("catalog = %+v", view)
		}
		// Ten minutes later it may run again.
		l.svc.clock.(*fakeClock).Set(l.svc.clock.Now().Add(11 * time.Minute))
		if err := l.svc.CheckTemplates(ctx); err != nil {
			t.Errorf("after the gap: %v", err)
		}
		waitFor(t, "the second check to finish", func() bool { v, _ := l.svc.TemplateCatalog(ctx); return !v.Running })
	})
}

// A catalog the check fetched installs: the installed template's record names
// its version.
func TestInstallFromFetchedCatalog(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	l.registry.AddIndex("traefik/whoami", "1.10", whoamiDigest)
	cs := newCatalogServer(t)
	l.svc.SetTemplatesURL(cs.URL)
	cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami")))
	if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := l.svc.InstallTemplate(ctx, "whoami", "who", map[string]string{"domain": "who.example.com"}, TemplateInstallOptions{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("InstallTemplate: %v", err)
	}
	waitFinal(t, l.store, res.Deployment.ID)
	rec, _ := l.svc.GetApp(ctx, "who")
	if rec.Template != (store.AppTemplate{Name: "whoami", Version: "1-1"}) {
		t.Errorf("Template = %+v", rec.Template)
	}
}

// T011a: an install without backups, chosen by the owner, on an installation that has no
// backup destination yet.
func TestInstallTemplateWithoutBackups(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t) // no backup destination
	inputs := map[string]string{"domain": "status.example.com"}

	t.Run("without the option the refusal stays", func(t *testing.T) {
		_, err := l.svc.InstallTemplate(ctx, "uptime-kuma", "status", inputs, TemplateInstallOptions{}, app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services.uptime-kuma.backup" {
			t.Fatalf("error = %v, want the backup refusal", err)
		}
		if _, err := l.store.GetApp(ctx, "status"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an app was made: %v", err)
		}
	})

	want := app.Warning{Field: "services.uptime-kuma.backup", Message: "Backups are off for uptime-kuma: " +
		"set a backup destination in Settings › Backup storage, then turn its backup on in the Backups tab."}

	t.Run("a preview says what it would turn off", func(t *testing.T) {
		p, err := l.svc.PreviewTemplate(ctx, "uptime-kuma", "status", inputs, TemplateInstallOptions{WithoutBackups: true})
		if err != nil {
			t.Fatalf("PreviewTemplate: %v", err)
		}
		if p.App.Services[0].Backup != nil {
			t.Errorf("the preview has a backup: %+v", p.App.Services[0].Backup)
		}
		if len(p.Warnings) != 1 || p.Warnings[0] != want {
			t.Errorf("warnings = %+v, want %+v", p.Warnings, want)
		}
	})

	t.Run("the install has no backup, one warning per removed backup, and works", func(t *testing.T) {
		res, err := l.svc.InstallTemplate(ctx, "uptime-kuma", "status", inputs, TemplateInstallOptions{WithoutBackups: true}, app.TriggerManual)
		if err != nil {
			t.Fatalf("InstallTemplate: %v", err)
		}
		installed(t, l, res)
		if len(res.Warnings) != 1 || res.Warnings[0] != want {
			t.Errorf("warnings = %+v, want %+v", res.Warnings, want)
		}
		rec, _ := l.svc.GetApp(ctx, "status")
		if rec.App.Services[0].Backup != nil || rec.Template.Name != "uptime-kuma" {
			t.Errorf("app = %+v, template %+v", rec.App.Services[0], rec.Template)
		}
		if rec.Compose == nil || rec.Compose.Services["uptime-kuma"].Backup != nil {
			t.Errorf("the kept overlay has a backup: %+v", rec.Compose)
		}
		if got, _ := l.store.GetDeployment(ctx, res.Deployment.ID); len(got.Warnings) != 1 || got.Warnings[0] != want {
			t.Errorf("the deployment's warnings = %+v", got.Warnings)
		}
	})

	t.Run("a template with two backups warns twice, and one without any not at all", func(t *testing.T) {
		two := whoamiTemplate()
		two.Name = "two"
		two.Inputs = two.Inputs[:1]
		two.Generated = nil
		two.Services = map[string]compose.Overlay{
			"web": {Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}}, Backup: &app.Backup{Engine: app.BackupVolume}},
			"db":  {Backup: &app.Backup{Engine: app.BackupVolume}},
		}
		two.Compose = "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n    volumes:\n      - wdata:/w\n  db:\n    image: traefik/whoami:latest\n    expose: [\"81\"]\n    volumes:\n      - ddata:/d\nvolumes:\n  wdata: {}\n  ddata: {}\n"
		none := whoamiTemplate()
		none.Name = "none"
		l.useCatalog(t, futureCatVer, two, none)
		p, err := l.svc.PreviewTemplate(ctx, "two", "two", map[string]string{"domain": "two.example.com"}, TemplateInstallOptions{WithoutBackups: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Warnings) != 2 || p.Warnings[0].Field != "services.db.backup" || p.Warnings[1].Field != "services.web.backup" {
			t.Errorf("warnings = %+v", p.Warnings)
		}
		p, err = l.svc.PreviewTemplate(ctx, "none", "none", map[string]string{"domain": "n.example.com"}, TemplateInstallOptions{WithoutBackups: true})
		if err != nil || len(p.Warnings) != 0 {
			t.Errorf("a template with no backups: %+v, %v", p.Warnings, err)
		}
	})

	t.Run("the template itself is left alone", func(t *testing.T) {
		got, _ := newTemplateLab(t).svc.Template(ctx, "uptime-kuma")
		if got.Services["uptime-kuma"].Backup == nil {
			t.Error("the catalog's template lost its backup")
		}
	})
}
