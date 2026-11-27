package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// An app read with getApp and sent back unchanged with deployApp, as CI scripts, the CLI and the MCP
// tool do with one image changed, is the same app: the deploy is accepted and the stored app keeps
// its revision. The API writes JSON with encoding/json/v2, whose omitempty leaves out an empty value
// ({}, "", []), while the store's encoding/json keeps a non-nil pointer to one; a field where the two
// differ would come back changed or missing (task jpb5 found it for "health": {}). Each row sets one
// optional field of the model to its smallest valid value.
func TestGetAppDeployAppRoundTrip(t *testing.T) {
	e := buildEnv(t) // its builds succeed, for the build row
	putDest(t, e, "main", "backups", "SK-main")

	// service is a whoami service with one port and an HTTP health check, plus extra members.
	service := func(extra string) string {
		return `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest",
			"ports": [{"name": "http", "containerPort": 80}], "health": {"path": "/"}` + extra + `}]}`
	}
	// portless is a whoami service with no port, so no health check, plus extra members.
	portless := func(extra string) string {
		return `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest"` + extra + `}]}`
	}

	for _, tc := range []struct{ name, body string }{
		{"empty-health", `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest",
			"ports": [{"name": "http", "containerPort": 80}], "health": {}}]}`},
		{"disconnect", service(`, "disconnect": {"lostAfterSeconds": 60}`)},
		{"placement", service(`, "placement": {"avoid": ["nowhere"]}`)},
		{"scale", service(`, "scale": {"min": 1, "max": 2, "target": {"cpu": 70}}`)},
		{"dns", service(`, "dns": {"servers": ["1.1.1.1"]}`)},
		{"backup", portless(`, "volumes": [{"name": "data", "path": "/data"}], "backup": {"engine": "volume"}`)},
		{"restore", portless(`, "volumes": [{"name": "data", "path": "/data"}],
			"backup": {"engine": "volume", "restore": {"schedule": "0 4 * * *", "from": "golden"}}`)},
		{"build", builtBody(apiBuildImage, publicBuild)},
		{"secret-value", service(`, "secrets": [{"name": "API_KEY", "value": "k1"}]`)},
		{"empty-secret-value", service(`, "secrets": [{"name": "API_KEY", "value": ""}]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // each row is its own app; a rollout of the fake takes about a second
			id := "rt-" + tc.name
			waitHealthy(t, e, id, deployOK(t, e, id, tc.body).ID)
			before, revision := readApp(t, e, id)

			status, _, raw := callHeaders(t, http.MethodPost, e.URL+"/v1/apps/"+id+"/deployments", string(before),
				[][2]string{{"If-Match", `"` + revision + `"`}})
			if status != http.StatusAccepted {
				t.Fatalf("deployApp with the app getApp gave: %d %s\nbody: %s", status, raw, before)
			}
			waitHealthy(t, e, id, decodeAs[deployment](t, raw).ID)

			after, again := readApp(t, e, id)
			if again != revision {
				t.Errorf("revision %s after the round trip, want %s\nbefore: %s\nafter:  %s", again, revision, before, after)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("getApp changed:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

// readApp is getApp's app object as the API wrote it, and the app's revision from the ETag.
func readApp(t *testing.T, e *testEnv, id string) (json.RawMessage, string) {
	t.Helper()
	status, header, raw := call(t, http.MethodGet, e.URL+"/v1/apps/"+id, "", "")
	if status != http.StatusOK {
		t.Fatalf("getApp %s: %d %s", id, status, raw)
	}
	etag := header.Get("ETag")
	if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Fatalf("getApp %s: ETag %q, want a quoted revision", id, etag)
	}
	return decodeAs[struct {
		App json.RawMessage `json:"app"`
	}](t, raw).App, etag[1 : len(etag)-1]
}

// Every pointer field of what getApp and getCompose return, and their clients send back, is tagged
// omitzero, never omitempty. Under the API's encoding/json/v2, omitempty also leaves out a non-nil
// pointer to an empty value ({}, ""), which a client then sends back as nil; omitzero leaves out nil
// only, which is what the store's encoding/json does for both tags, so the two write the same.
func TestPointerFieldsAreOmitzero(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			tag := f.Tag.Get("json")
			if !f.IsExported() || tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if f.Type.Kind() == reflect.Pointer && strings.Contains(","+opts+",", ",omitempty,") {
				t.Errorf("%s.%s (%s): json tag %q has omitempty; use omitzero", path, f.Name, name, tag)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeFor[app.App](), "app.App")
	walk(reflect.TypeFor[compose.Request](), "compose.Request")
}
