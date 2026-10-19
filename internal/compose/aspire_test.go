package compose

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Feature 026: the Compose file Aspire 13.6.1 generates for the PublishToCaravana
// sample (an AppHost with Redis behind a password, Seq on a volume, two projects),
// and the form the publisher sends after resolving it. The fixtures are in
// testdata/aspire/.

const aspireSecret = "s3cret-fixture"

func readAspire(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "aspire", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// As generated, the file has ${...} variables: Caravana reads no environment, so it
// stops at the first one without a value, in alphabetical order.
func TestAspireAsGeneratedNamesTheFirstVariable(t *testing.T) {
	_, _, err := Translate("demo", Request{Compose: string(readAspire(t, "generated.compose.yaml"))})
	var fe *app.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("Translate = %v, want a field error", err)
	}
	if fe.Field != "compose" || !strings.Contains(fe.Message, `"APISERVICE_IMAGE"`) {
		t.Errorf("error = %s: %s; want field compose naming APISERVICE_IMAGE", fe.Field, fe.Message)
	}
}

// Resolved, with the secrets moved into the overlay, it becomes four services, and
// says only what is true: one warning about networks, one about depends_on.
func TestAspireResolved(t *testing.T) {
	src := string(readAspire(t, "resolved.compose.yaml"))
	if strings.Contains(src, aspireSecret) || strings.Contains(src, "${") {
		t.Fatal("the resolved file still has a secret value or a variable")
	}
	var req Request
	if err := json.Unmarshal(readAspire(t, "resolved.overlay.json"), &req); err != nil {
		t.Fatalf("overlay: %v", err)
	}
	req.Compose = src

	a, warnings, err := Translate("demo", req)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}

	// The same app and warnings as the golden, whose secrets hold names only.
	for i := range a.Services {
		for j := range a.Services[i].Secrets {
			a.Services[i].Secrets[j].Value = nil
		}
	}
	got, err := json.MarshalIndent(golden{App: a, Warnings: warnings}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if bytes.Contains(got, []byte(aspireSecret)) {
		t.Error("a secret value is in the translated app")
	}
	path := filepath.Join("testdata", "aspire", "resolved.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from what Translate gives; run with -update and read the diff.\ngot:\n%s", path, got)
	}

	// What the golden is meant to show, said in code.
	if len(a.Services) != 4 {
		t.Fatalf("%d services, want 4", len(a.Services))
	}
	cache := serviceNamed(t, a, "cache")
	if len(cache.Entrypoint) != 1 || cache.Entrypoint[0] != "/bin/sh" || cache.Command != "-c" {
		t.Errorf("cache entrypoint %q, command %q, args %q", cache.Entrypoint, cache.Command, cache.Args)
	}
	if len(cache.Secrets) != 1 || cache.Secrets[0].Name != "REDIS_PASSWORD" {
		t.Errorf("cache secrets = %+v", cache.Secrets)
	}
	web := serviceNamed(t, a, "webfrontend")
	var names []string
	for _, s := range web.Secrets {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "CACHE_PASSWORD,CACHE_URI,ConnectionStrings__cache" {
		t.Errorf("webfrontend secrets = %v", names)
	}
	for _, e := range web.Env {
		if strings.Contains(e.Value, aspireSecret) {
			t.Errorf("env %s holds the secret", e.Name)
		}
	}
	if v := serviceNamed(t, a, "seq").Volumes; len(v) != 1 || v[0].Name != "seq-data" || v[0].Path != "/data" {
		t.Errorf("seq volumes = %+v", v)
	}

	byField := map[string]int{}
	for _, w := range warnings {
		byField[w.Field]++
	}
	if len(warnings) != 2 || byField["networks"] != 1 || byField["services.webfrontend.depends_on"] != 1 {
		t.Errorf("warnings = %+v, want one on networks and one on services.webfrontend.depends_on", warnings)
	}
}

func serviceNamed(t *testing.T, a app.App, name string) app.Service {
	t.Helper()
	for _, svc := range a.Services {
		if svc.Name == name {
			return svc
		}
	}
	t.Fatalf("no service %q", name)
	return app.Service{}
}
