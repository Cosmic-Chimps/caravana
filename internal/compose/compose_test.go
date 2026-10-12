package compose

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Run `go test ./internal/compose -update` to rewrite the golden files, then
// read the diff: it is the change in what Caravana makes of each file.
var update = flag.Bool("update", false, "rewrite golden files")

// golden is what a fixture becomes.
type golden struct {
	App      app.App       `json:"app"`
	Warnings []app.Warning `json:"warnings"`
}

// TestTranslateFixtures translates every testdata/<name>.compose.yaml, with
// <name>.overlay.json as the rest of the request when it exists, and
// compares the result with <name>.golden.json.
func TestTranslateFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.compose.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".compose.yaml")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var req Request
			if raw, err := os.ReadFile(filepath.Join("testdata", name+".overlay.json")); err == nil {
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatalf("overlay: %v", err)
				}
			}
			req.Compose = string(src)

			a, warnings, err := Translate(name, req)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			got, err := json.MarshalIndent(golden{App: a, Warnings: warnings}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from what Translate gives; run with -update and read the diff.\ngot:\n%s", path, got)
			}

			// The same request gives the same app (pure, deterministic).
			again, againWarnings, _ := Translate(name, req)
			if j1, _ := json.Marshal(golden{a, warnings}); !bytes.Equal(j1, mustJSON(t, golden{again, againWarnings})) {
				t.Error("a second Translate gave something else")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestRefused checks each attribute Caravana can't honor fails on its path,
// before anything else happens.
func TestRefused(t *testing.T) {
	big := "services:\n  web:\n    image: x\n" + strings.Repeat("# padding\n", MaxComposeBytes/10+1)
	tests := []struct {
		name      string
		file      string
		services  map[string]Overlay
		wantField string
		wantMsg   string
	}{
		{"build", "services:\n  web:\n    build: .\n", nil, "services.web.build", "prebuilt images"},
		{"a CPU reservation", "services:\n  web:\n    image: x\n    deploy:\n      resources:\n        reservations:\n          cpus: \"0.5\"\n", nil, "services.web.deploy.resources.reservations.cpus", ""},
		{"a reservation above the limit", "services:\n  web:\n    image: x\n    mem_limit: 256m\n    mem_reservation: 512m\n", nil, "services.web.resources.memoryMB", "ceiling"},
		{"build next to image", "services:\n  web:\n    image: x\n    build: .\n", nil, "services.web.build", "prebuilt images"},
		{"entrypoint", "services:\n  web:\n    image: x\n    entrypoint: [sh]\n", nil, "services.web.entrypoint", ""},
		{"env_file", "services:\n  web:\n    image: x\n    env_file: web.env\n", nil, "services.web.env_file", "files from your disk"},
		{"service secrets", "services:\n  web:\n    image: x\n    secrets: [k]\nsecrets:\n  k:\n    file: k.txt\n", nil, "secrets", ""},
		{"relative bind mount", "services:\n  web:\n    image: x\n    volumes: [\"./data:/data\"]\n", nil, "services.web.volumes[0]", "named volumes"},
		{"absolute bind mount", "services:\n  web:\n    image: x\n    volumes: [\"/srv:/data\"]\n", nil, "services.web.volumes[0]", "named volumes"},
		{"tmpfs", "services:\n  web:\n    image: x\n    volumes: [{type: tmpfs, target: /tmp}]\n", nil, "services.web.volumes[0]", "named volumes"},
		{"anonymous volume", "services:\n  web:\n    image: x\n    volumes: [\"/data\"]\n", nil, "services.web.volumes[0]", "name the volume"},
		{"volume driver", "services:\n  web:\n    image: x\nvolumes:\n  d:\n    driver: local\n", nil, "volumes.d.driver", ""},
		{"external volume", "services:\n  web:\n    image: x\nvolumes:\n  d:\n    external: true\n", nil, "volumes.d.external", ""},
		{"privileged", "services:\n  web:\n    image: x\n    privileged: true\n", nil, "services.web.privileged", "privileges"},
		{"profiles", "services:\n  web:\n    image: x\n    profiles: [dev]\n", nil, "services.web.profiles", ""},
		{"include", "include: [other.yaml]\nservices:\n  web:\n    image: x\n", nil, "include", ""},
		{"user", "services:\n  web:\n    image: x\n    user: \"1000\"\n", nil, "services.web.user", ""},
		{"udp port", "services:\n  web:\n    image: x\n    ports: [\"53:53/udp\"]\n", nil, "services.web.ports[0].protocol", "tcp"},
		{"env without a value", "services:\n  web:\n    image: x\n    environment: [DEBUG]\n", nil, "services.web.environment.DEBUG", "no value"},
		{"unset variable", "services:\n  web:\n    image: x\n    environment:\n      HOST: ${API_HOST}\n", nil, "compose", `"API_HOST"`},
		{"unknown key", "services:\n  web:\n    image: x\n    imagee: y\n", nil, "compose", "imagee"},
		{"invalid YAML", "services: [web\n", nil, "compose", "yaml"},
		{"empty", "", nil, "compose", "required"},
		{"too large", big, nil, "compose", "at most"},
		{"service name not a DNS label", "services:\n  Web_1:\n    image: x\n", nil, "services.Web_1.name", ""},
		{"volume in two services", "services:\n  a:\n    image: x\n    volumes: [\"data:/d\"]\n  b:\n    image: x\n    volumes: [\"data:/d\"]\nvolumes:\n  data: {}\n", nil, "services.b.volumes[0].name", "one service"},
		{"volume with two copies", "services:\n  db:\n    image: x\n    deploy: {replicas: 2}\n    volumes: [\"data:/d\"]\nvolumes:\n  data: {}\n", nil, "services.db.replicas", "one copy"},
		{"overlay for an unknown service", "services:\n  web:\n    image: x\n", map[string]Overlay{"api": {}}, "services.api", "no service"},
		{"overlay domain port not on the service", "services:\n  web:\n    image: x\n    ports: [\"80\"]\n",
			map[string]Overlay{"web": {Domains: []Domain{{Host: "a.example.com", Port: 81}}}}, "services.web.domains[0].port", "no port 81"},
		{"overlay domain port missing with two ports", "services:\n  web:\n    image: x\n    ports: [\"80\", \"81\"]\n",
			map[string]Overlay{"web": {Domains: []Domain{{Host: "a.example.com"}}}}, "services.web.domains[0].port", "2 ports"},
		{"overlay domain on a service without ports", "services:\n  web:\n    image: x\n",
			map[string]Overlay{"web": {Domains: []Domain{{Host: "a.example.com"}}}}, "services.web.domains[0]", "no ports"},
		{"overlay secret with a bad name", "services:\n  web:\n    image: x\n",
			map[string]Overlay{"web": {Secrets: []app.Secret{{Name: "1A"}}}}, "services.web.secrets[0].name", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Translate("blog", Request{Compose: tc.file, Services: tc.services})
			var fe *app.FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("Translate = %v, want a field error on %s", err, tc.wantField)
			}
			if fe.Field != tc.wantField || !strings.Contains(fe.Message, tc.wantMsg) {
				t.Errorf("error = %s: %s; want field %s, message with %q", fe.Field, fe.Message, tc.wantField, tc.wantMsg)
			}
		})
	}
}

// TestWarnings checks each attribute honored differently warns on its path.
func TestWarnings(t *testing.T) {
	file := `services:
  web:
    image: x
    ports: ["8080:80"]
    depends_on: [db]
    restart: "no"
    container_name: web1
    hostname: web1
    labels: [a=b]
    networks: [front]
    pull_policy: always
    logging: {driver: json-file}
    stop_grace_period: 5s
    healthcheck: {test: ["CMD", "true"]}
    cpus: 1.5
  db:
    image: x
networks:
  front: {}
`
	_, warnings, err := Translate("blog", Request{Compose: file})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, w := range warnings {
		got[w.Field] = true
		if w.Message == "" {
			t.Errorf("warning on %s has no message", w.Field)
		}
	}
	for _, f := range []string{
		"services.web.ports[0]", "services.web.depends_on", "services.web.restart", "services.web.container_name",
		"services.web.hostname", "services.web.labels", "services.web.networks", "services.web.pull_policy",
		"services.web.logging", "services.web.stop_grace_period", "services.web.healthcheck",
		"services.web.deploy.resources.limits.cpus", "networks.front",
	} {
		if !got[f] {
			t.Errorf("no warning on %s; warnings: %+v", f, warnings)
		}
	}
}

// TestVariables checks variables take only the file's defaults: never the
// environment Caravana runs in.
func TestVariables(t *testing.T) {
	t.Setenv("LEAK", "from-the-host")
	file := "services:\n  web:\n    image: x\n    environment:\n      A: ${A:-default}\n      B: $${NOT_A_VARIABLE}\n      C: ${LEAK:-safe}\n"
	a, _, err := Translate("blog", Request{Compose: file})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "default", "B": "${NOT_A_VARIABLE}", "C": "safe"}
	for _, e := range a.Services[0].Env {
		if e.Value != want[e.Name] {
			t.Errorf("%s = %q, want %q", e.Name, e.Value, want[e.Name])
		}
	}
	if _, _, err := Translate("blog", Request{Compose: "services:\n  web:\n    image: ${LEAK}\n"}); err == nil {
		t.Error("a variable set only in Caravana's environment was used")
	}
}

// Feature 013: Compose's memory fields, as Docker means them.
func TestMemory(t *testing.T) {
	tests := []struct {
		name             string
		file             string
		reserve, ceiling int
	}{
		{"mem_limit is the ceiling", "services:\n  web:\n    image: x\n    mem_limit: 768m\n", 0, 768},
		{"mem_reservation is the reservation", "services:\n  web:\n    image: x\n    mem_reservation: 300m\n", 300, 0},
		{"both", "services:\n  web:\n    image: x\n    mem_limit: 1g\n    mem_reservation: 512m\n", 512, 1024},
		{"deploy.resources", "services:\n  web:\n    image: x\n    deploy:\n      resources:\n        limits: {memory: 2g}\n        reservations: {memory: 1g}\n", 1024, 2048},
		{"neither", "services:\n  web:\n    image: x\n", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, err := Translate("shop", Request{Compose: tt.file})
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			r := a.Services[0].Resources
			if r.MemoryMB != tt.reserve || r.MemoryMaxMB != tt.ceiling {
				t.Errorf("memoryMB %d, memoryMaxMB %d; want %d, %d", r.MemoryMB, r.MemoryMaxMB, tt.reserve, tt.ceiling)
			}
		})
	}
}
