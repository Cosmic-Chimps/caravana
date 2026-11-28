package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Task 2xg2: getApp and the environment read say where each service answers by
// name, and a name two apps claim answers to neither.
func TestGetAppAddresses(t *testing.T) {
	e := newTestEnv(t, "")
	one := func(id, svc string) string {
		return `{"id": "` + id + `", "source": {"kind": "image"}, "services": [{"name": "` + svc + `", "image": "traefik/whoami:latest", "ports": [{"name": "db", "containerPort": 5432}], "health": {"interval": "10s", "timeout": "2s"}}]}`
	}
	two := `{"id": "shop", "source": {"kind": "image"}, "services": [
	  {"name": "web", "image": "traefik/whoami:latest", "ports": [{"name": "http", "containerPort": 8080}], "health": {"interval": "10s", "timeout": "2s"}},
	  {"name": "worker", "image": "traefik/whoami:latest"}]}`
	for _, body := range []string{one("db", "postgres"), one("db-postgres", "x"), two} {
		var id struct{ ID string }
		_ = json.Unmarshal([]byte(body), &id)
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps?deploy=false", "application/json", body); status != http.StatusCreated {
			t.Fatalf("create %s = %d %s", id.ID, status, raw)
		}
	}

	type conflict struct{ Name, App, Service string }
	type address struct {
		Service   string
		Ports     []int
		Names     []string
		OwnName   string
		Conflicts []conflict
	}
	read := func(url string) (raw []byte, got []address) {
		t.Helper()
		status, _, raw := call(t, http.MethodGet, url, "", "")
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d %s", url, status, raw)
		}
		checkSchema(t, loadContract(t), raw, "AppRecord")
		var rec struct{ Addresses []address }
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		return raw, rec.Addresses
	}

	// db's postgres claims db-postgres (also claimed by the app db-postgres) and db.
	_, got := read(e.URL + "/v1/apps/db")
	want := []address{{Service: "postgres", Ports: []int{5432}, Names: []string{"db"},
		Conflicts: []conflict{{Name: "db-postgres", App: "db-postgres", Service: "x"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("db addresses = %+v, want %+v", got, want)
	}

	// The environment read answers the same.
	_, got = read(e.URL + "/v1/projects/default/environments/production/apps/db")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("environment read addresses = %+v, want %+v", got, want)
	}

	// An app with two services: only the one with ports has an address, with its own name.
	_, got = read(e.URL + "/v1/apps/shop")
	want = []address{{Service: "web", Ports: []int{8080}, Names: []string{"shop-web", "shop"}, OwnName: "web"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shop addresses = %+v, want %+v", got, want)
	}

	// names is an array even when every claim collides; the apps of a pair both say so.
	raw, got := read(e.URL + "/v1/apps/db-postgres")
	if len(got) != 1 || len(got[0].Conflicts) != 1 {
		t.Fatalf("db-postgres addresses = %+v", got)
	}
	if !strings.Contains(string(raw), `"names":[`) {
		t.Errorf("names missing from %s", raw)
	}

	// Other writers of a record leave addresses out.
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps?deploy=false", "application/json", one("fresh", "web"))
	if status != http.StatusCreated || strings.Contains(string(raw), `"addresses"`) {
		t.Errorf("createApp = %d %s, want no addresses", status, raw)
	}
}
