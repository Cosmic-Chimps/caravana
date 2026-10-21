package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// richApp is an app as getApp answers it, with fields this binary knows
// nothing about, numbers that must not turn into floats, markup characters and
// two services: deploy_image must keep all of it.
const richApp = `{
  "id": "shop--staging--web",
  "name": "Web <shop> & co",
  "source": {"kind": "image"},
  "x-future": {"list": [1, 2.50, 12345678901234567890, null, true], "s": "a&b<c>"},
  "services": [
    {"name": "web", "image": "ghcr.io/o/web:1", "ports": [{"name": "http", "containerPort": 8080}],
     "env": {"A": "1"}, "secrets": [{"name": "DB_URL"}], "memoryMB": 512,
     "health": {"path": "/up", "interval": "10s", "timeout": "2s"}, "x-new": {"k": "v"}},
    {"name": "worker", "image": "ghcr.io/o/worker:1", "command": ["run"], "volumes": [{"name": "data", "mount": "/data"}]}
  ]
}`

func richRecord(revision string) string {
	return `{"app":` + richApp + `,"project":"shop","environment":"staging","appName":"web",` +
		`"createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-02T00:00:00Z","revision":"` + revision +
		`","composeKept":false,"defined":true}`
}

// appWithImage is richApp with the image of one service replaced, built the
// slow way (through generic maps) so the test doesn't share code with the
// implementation.
func appWithImage(t *testing.T, service, image string) string {
	t.Helper()
	var app map[string]any
	dec := json.NewDecoder(strings.NewReader(richApp))
	dec.UseNumber() // keep 12345678901234567890 exact
	if err := dec.Decode(&app); err != nil {
		t.Fatal(err)
	}
	for _, s := range app["services"].([]any) {
		if m := s.(map[string]any); m["name"] == service {
			m["image"] = image
		}
	}
	out, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestDeployImageChangesOnlyOneImage: the body sent to deployApp is the stored
// app with that one service's image replaced, and nothing else of the record.
func TestDeployImageChangesOnlyOneImage(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("GET /v1/apps/shop--staging--web", 200, richRecord("0123456789abcdef"))
	f.reply("POST /v1/apps/shop--staging--web/deployments", 202, deploymentJSON)
	cs := f.session(t, true, nil, fastTuning, nil)

	got := mustCall(t, cs, "deploy_image", map[string]any{"app": "shop--staging--web", "service": "worker", "image": "ghcr.io/o/worker:2"})
	if got != deploymentJSON {
		t.Errorf("answer = %s, want the deployment as the API gave it", got)
	}

	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want getApp then deployApp: %+v", len(reqs), reqs)
	}
	deploy := reqs[1]
	if deploy.Method != "POST" || deploy.Path != "/v1/apps/shop--staging--web/deployments" {
		t.Errorf("second request = %s %s", deploy.Method, deploy.Path)
	}
	if h := deploy.Header.Get("If-Match"); h != `"0123456789abcdef"` {
		t.Errorf("If-Match = %q, want the revision it read, quoted", h)
	}
	if !jsonEqual(t, deploy.Body, appWithImage(t, "worker", "ghcr.io/o/worker:2")) {
		t.Errorf("body = %s\nwant the stored app with only the worker's image changed", deploy.Body)
	}
	// Exact numbers survive (a float64 round trip would write 12345678901234567000).
	if !strings.Contains(deploy.Body, "12345678901234567890") {
		t.Errorf("a number in the stored app was altered: %s", deploy.Body)
	}
	for _, forbidden := range []string{`"revision"`, `"createdAt"`, `"defined"`, `"appName"`} {
		if strings.Contains(deploy.Body, forbidden) {
			t.Errorf("body holds %s, which belongs to the record, not the app", forbidden)
		}
	}
	checkCommonHeaders(t, 1, deploy)
}

// TestDeployImageNeedsAService: which service to change must be clear.
func TestDeployImageNeedsAService(t *testing.T) {
	tests := []struct {
		name    string
		record  string
		args    map[string]any
		want    []string // pieces of the error text
		wantImg string   // the image that is deployed, "" when no deploy is expected
	}{
		{"two services, none named", richRecord("aaaa"), map[string]any{"app": "web", "image": "x:2"},
			[]string{"2 services", "web, worker", "`service`"}, ""},
		{"unknown service lists the names", richRecord("aaaa"), map[string]any{"app": "web", "image": "x:2", "service": "api"},
			[]string{`no service "api"`, "web, worker"}, ""},
		{"no services", `{"app":{"id":"web","source":{"kind":"image"},"services":[]},"revision":"aaaa","defined":false}`,
			map[string]any{"app": "web", "image": "x:2"}, []string{"no services yet", "define it first"}, ""},
		{"services missing", `{"app":{"id":"web","source":{"kind":"image"}},"revision":"aaaa","defined":false}`,
			map[string]any{"app": "web", "image": "x:2"}, []string{"no services yet"}, ""},
		{"one service, named", `{"app":{"id":"web","source":{"kind":"image"},"services":[{"name":"web","image":"a:1"}]},"revision":"aaaa"}`,
			map[string]any{"app": "web", "image": "x:2", "service": "web"}, nil, "x:2"},
		{"one service, not named", `{"app":{"id":"web","source":{"kind":"image"},"services":[{"name":"web","image":"a:1"}]},"revision":"aaaa"}`,
			map[string]any{"app": "web", "image": "x:2"}, nil, "x:2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.reply("GET /v1/apps/web", 200, tt.record)
			f.reply("POST /v1/apps/web/deployments", 202, deploymentJSON)
			cs := f.session(t, true, nil, fastTuning, nil)

			if tt.wantImg != "" {
				mustCall(t, cs, "deploy_image", tt.args)
				reqs := f.requests()
				if len(reqs) != 2 || !strings.Contains(reqs[1].Body, `"image":"`+tt.wantImg+`"`) {
					t.Errorf("requests = %+v", reqs)
				}
				return
			}
			got := mustFail(t, cs, "deploy_image", tt.args)
			for _, piece := range tt.want {
				if !strings.Contains(got, piece) {
					t.Errorf("text %q lacks %q", got, piece)
				}
			}
			for _, r := range f.requests() {
				if r.Method == "POST" {
					t.Errorf("deployed anyway: %+v", r)
				}
			}
		})
	}
}

// TestDeployImageRetriesOnceWhenTheAppChanged: a 412 app_changed makes it read
// the app again and send the new revision, once.
func TestDeployImageRetriesOnceWhenTheAppChanged(t *testing.T) {
	changed := `{"error":{"code":"app_changed","message":"the app changed since you read it"}}`

	t.Run("second try works", func(t *testing.T) {
		f := newFakeAPI(t)
		var reads, deploys atomic.Int32
		f.on("GET /v1/apps/shop--staging--web", func(w http.ResponseWriter, _ *http.Request) {
			n := reads.Add(1)
			fmt.Fprint(w, richRecord(fmt.Sprintf("%016x", n)))
		})
		f.on("POST /v1/apps/shop--staging--web/deployments", func(w http.ResponseWriter, r *http.Request) {
			if deploys.Add(1) == 1 {
				w.WriteHeader(http.StatusPreconditionFailed)
				fmt.Fprint(w, changed)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, deploymentJSON)
		})
		cs := f.session(t, true, nil, fastTuning, nil)

		mustCall(t, cs, "deploy_image", map[string]any{"app": "shop--staging--web", "service": "web", "image": "x:2"})
		reqs := f.requests()
		if len(reqs) != 4 {
			t.Fatalf("%d requests, want get, deploy, get, deploy: %+v", len(reqs), reqs)
		}
		if h := reqs[1].Header.Get("If-Match"); h != `"0000000000000001"` {
			t.Errorf("first deploy If-Match = %s", h)
		}
		if h := reqs[3].Header.Get("If-Match"); h != `"0000000000000002"` {
			t.Errorf("second deploy If-Match = %s, want the revision of the second read", h)
		}
	})

	t.Run("twice is an error", func(t *testing.T) {
		f := newFakeAPI(t)
		f.reply("GET /v1/apps/shop--staging--web", 200, richRecord("0123456789abcdef"))
		f.reply("POST /v1/apps/shop--staging--web/deployments", 412, changed)
		cs := f.session(t, true, nil, fastTuning, nil)

		got := mustFail(t, cs, "deploy_image", map[string]any{"app": "shop--staging--web", "service": "web", "image": "x:2"})
		if !strings.Contains(got, "app_changed") {
			t.Errorf("text = %q", got)
		}
		if n := len(f.requests()); n != 4 {
			t.Errorf("%d requests, want exactly one retry (4)", n)
		}
	})

	t.Run("another refusal is not retried", func(t *testing.T) {
		f := newFakeAPI(t)
		f.reply("GET /v1/apps/shop--staging--web", 200, richRecord("0123456789abcdef"))
		f.reply("POST /v1/apps/shop--staging--web/deployments", 422, `{"error":{"code":"image_not_found","message":"no such tag","field":"services[0].image"}}`)
		cs := f.session(t, true, nil, fastTuning, nil)

		got := mustFail(t, cs, "deploy_image", map[string]any{"app": "shop--staging--web", "service": "web", "image": "x:nope"})
		if !strings.Contains(got, "image_not_found") || !strings.Contains(got, "(field services[0].image)") {
			t.Errorf("text = %q", got)
		}
		if n := len(f.requests()); n != 2 {
			t.Errorf("%d requests, want 2", n)
		}
	})
}

// TestDeployImageRefusesAStrangeRecord: an answer that isn't an app is said so.
func TestDeployImageRefusesAStrangeRecord(t *testing.T) {
	for name, record := range map[string]string{
		"not an object":    `[1]`,
		"no revision":      `{"app":{"id":"web","services":[{"name":"web","image":"a"}]}}`,
		"no app":           `{"revision":"aaaa"}`,
		"service no name":  `{"app":{"services":[{"image":"a"}]},"revision":"aaaa"}`,
		"service not JSON": `{"app":{"services":[3]},"revision":"aaaa"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.reply("GET /v1/apps/web", 200, record)
			cs := f.session(t, true, nil, fastTuning, nil)
			got := mustFail(t, cs, "deploy_image", map[string]any{"app": "web", "image": "x:2"})
			if !strings.Contains(got, "Caravana's answer") {
				t.Errorf("text = %q", got)
			}
		})
	}
}

// TestPromoteUsesThePreviewsRevision: the promotion carries the target
// revision the preview gave, and a refused preview promotes nothing.
func TestPromoteUsesThePreviewsRevision(t *testing.T) {
	const src = "shop--staging--web"

	t.Run("a refused preview stops it", func(t *testing.T) {
		f := newFakeAPI(t)
		f.reply("POST /v1/apps/"+src+"/promote/preview", 409,
			`{"error":{"code":"promotion_removes_service","message":"target has a service the source lacks","field":"services.cache"}}`)
		cs := f.session(t, true, nil, fastTuning, nil)

		got := mustFail(t, cs, "promote_app", map[string]any{"app": src, "to": "production"})
		if !strings.Contains(got, "promotion_removes_service") || !strings.Contains(got, "(field services.cache)") {
			t.Errorf("text = %q", got)
		}
		if n := len(f.requests()); n != 1 {
			t.Errorf("%d requests, want only the preview", n)
		}
	})

	t.Run("the target changed after the preview", func(t *testing.T) {
		f := newFakeAPI(t)
		f.reply("POST /v1/apps/"+src+"/promote/preview", 200, previewJSON)
		f.reply("POST /v1/apps/"+src+"/promote", 412, `{"error":{"code":"app_changed","message":"the app changed since you read it"}}`)
		cs := f.session(t, true, nil, fastTuning, nil)

		got := mustFail(t, cs, "promote_app", map[string]any{"app": src, "to": "production"})
		if !strings.Contains(got, "app_changed") || !strings.Contains(got, "call preview_promotion and promote again") {
			t.Errorf("text = %q", got)
		}
		if n := len(f.requests()); n != 2 {
			t.Errorf("%d requests, want preview and one promote, no retry", n)
		}
	})

	t.Run("a preview without a revision", func(t *testing.T) {
		f := newFakeAPI(t)
		f.reply("POST /v1/apps/"+src+"/promote/preview", 200, `{"from":"x"}`)
		cs := f.session(t, true, nil, fastTuning, nil)
		got := mustFail(t, cs, "promote_app", map[string]any{"app": src, "to": "production"})
		if !strings.Contains(got, "no targetRevision") {
			t.Errorf("text = %q", got)
		}
	})
}

// TestActionErrorsReachTheAgent: the API's refusal of an action, such as
// stopping an app that is busy, comes through with its code.
func TestActionErrorsReachTheAgent(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("POST /v1/apps/web/restart", 409, `{"error":{"code":"app_busy","message":"a deploy is running"}}`)
	cs := f.session(t, true, nil, fastTuning, nil)

	got := mustFail(t, cs, "restart_app", map[string]any{"app": "web"})
	if got != "Caravana refused: app_busy: a deploy is running (HTTP 409)" {
		t.Errorf("text = %q", got)
	}
}

// sequence answers a route with the bodies in order, then the last one again.
func sequence(bodies ...string) http.HandlerFunc {
	var mu sync.Mutex
	n := 0
	return func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := bodies[min(n, len(bodies)-1)]
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}
}
