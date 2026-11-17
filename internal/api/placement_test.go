package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// withSecondServer adds a ready server "two" (index 2, node-2 at 10.99.0.2) to a cluster made by withCluster.
func withSecondServer(t *testing.T, e *testEnv) {
	t.Helper()
	if _, err := e.Store.InsertServer(context.Background(), store.Server{ID: "id-two", Name: "two", State: store.ServerReady,
		CreatedAt: time.Now(), JoinedAt: time.Now(), ReadyAt: time.Now()}); err != nil {
		t.Fatalf("InsertServer: %v", err)
	}
	e.Nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	e.Service.SetMoveTimingForTests(0, 2*time.Second, 5*time.Millisecond)
	e.Nomad.MoveCopiesTo("node-1", "hel-1")
}

type serverFull struct {
	Name          string            `json:"name"`
	AcceptsWork   bool              `json:"acceptsWork"`
	Labels        map[string]string `json:"labels"`
	LabelsApplied bool              `json:"labelsApplied"`
	Move          *struct {
		State string `json:"state"`
		Moved int    `json:"moved"`
		Stays []struct {
			App, Service, Reason string
		} `json:"stays"`
	} `json:"move"`
}

func TestServerLabelsAPI(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	withSecondServer(t, e)
	doc := loadContract(t)
	put := func(name, body string) (int, []byte) {
		status, hdr, raw := call(t, http.MethodPut, e.URL+"/v1/servers/"+name+"/labels", "application/json", body)
		if status == http.StatusOK && hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", hdr.Get("Cache-Control"))
		}
		return status, raw
	}

	t.Run("labels are saved, written to the node and shown", func(t *testing.T) {
		status, raw := put("two", `{"labels": {"ssd": "true", "region": "eu"}}`)
		if status != http.StatusOK {
			t.Fatalf("putServerLabels = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Server")
		got := decodeAs[serverFull](t, raw)
		if got.Labels["ssd"] != "true" || got.Labels["region"] != "eu" || !got.LabelsApplied {
			t.Errorf("server = %+v", got)
		}
		if e.Nomad.NodeMeta("node-2")["caravana.label.ssd"] != "true" {
			t.Error("the node doesn't have the label")
		}
		// getServer and listServers say the same; a server with none says {}.
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/two", "", "")
		checkSchema(t, doc, raw, "Server")
		if decodeAs[serverFull](t, raw).Labels["region"] != "eu" {
			t.Errorf("getServer = %s", raw)
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/hel-1", "", "")
		if !strings.Contains(string(raw), `"labels":{}`) || !strings.Contains(string(raw), `"move":null`) {
			t.Errorf("hel-1 = %s, want empty labels and no move", raw)
		}
	})

	t.Run("an empty object clears them", func(t *testing.T) {
		status, raw := put("two", `{"labels": {}}`)
		if status != http.StatusOK || len(decodeAs[serverFull](t, raw).Labels) != 0 {
			t.Errorf("clear = %d %s", status, raw)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		status, raw := put("nobody", `{"labels": {}}`)
		wantError(t, "an unknown server", status, raw, http.StatusNotFound, codeServerNotFound, "")
		e.Nomad.AddNode("node-lab", "lab", "ready", "")
		status, raw = put("lab", `{"labels": {"a": "1"}}`)
		wantError(t, "an unmanaged node", status, raw, http.StatusNotFound, codeServerNotFound, "")
		j := addServer(t, e, `{"name": "later"}`)
		status, raw = put(j.Server.Name, `{"labels": {"a": "1"}}`)
		wantError(t, "a server that hasn't joined", status, raw, http.StatusConflict, codeServerNotReady, "")
		status, raw = put("two", `{"labels": {"Bad Name": "x"}}`)
		wantError(t, "a bad name", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "labels")
		status, raw = put("two", `{"labels": {"ssd": "has space"}}`)
		wantError(t, "a bad value", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "labels.ssd")
		status, raw = put("two", `{}`)
		if status != http.StatusOK { // no labels sent clears them, as an empty object does
			t.Errorf("a body without labels = %d %s", status, raw)
		}
		status, raw = put("two", `{"labels": {"a": "1"}, "extra": 1}`)
		wantError(t, "an unknown field", status, raw, http.StatusBadRequest, "unknown_field", "extra")
	})

	t.Run("a token without node:write", func(t *testing.T) {
		e.Nomad.FailNodeWrites(403)
		defer e.Nomad.FailNodeWrites(0)
		status, raw := put("two", `{"labels": {"a": "1"}}`)
		got := wantError(t, "putServerLabels with a refused token", status, raw, http.StatusBadGateway, "nomad_unavailable", "")
		if !strings.Contains(got.Error.Message, "node:write") || !strings.Contains(got.Error.Message, "run the installer again") {
			t.Errorf("message = %q", got.Error.Message)
		}
	})
}

func TestDrainServerAPI(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	withSecondServer(t, e)
	doc := loadContract(t)

	// A copy of a two-copy app on two, and a service with a volume that stays.
	post := func(path, body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPost, e.URL+path, "application/json", body)
		return status, raw
	}
	e.Nomad.SetCopies("blog", api.AllocationListStub{ID: "blog-0", TaskGroup: "web", JobVersion: 1, ClientStatus: "running",
		DesiredStatus: "run", NodeID: "node-2", NodeName: "two", CreateIndex: 1})
	d := deployOK(t, e, "blog", placedBody(`{"prefer": [{"label": "ssd", "value": "true"}]}`))
	waitHealthy(t, e, "blog", d.ID)
	e.Nomad.SetCopies("blog", api.AllocationListStub{ID: "blog-0", TaskGroup: "web", JobVersion: 1, ClientStatus: "running",
		DesiredStatus: "run", NodeID: "node-2", NodeName: "two", CreateIndex: 1})

	t.Run("refusals", func(t *testing.T) {
		status, raw := post("/v1/servers/nobody/drain", `{}`)
		wantError(t, "an unknown server", status, raw, http.StatusNotFound, codeServerNotFound, "")
		status, raw = post("/v1/servers/two/drain", `{"deadlineSeconds": 5}`)
		wantError(t, "a deadline under a minute", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "deadlineSeconds")
		j := addServer(t, e, `{"name": "later"}`)
		status, raw = post("/v1/servers/"+j.Server.Name+"/drain", ``)
		wantError(t, "a server that hasn't joined", status, raw, http.StatusConflict, codeServerNotReady, "")
		status, raw = call2(t, http.MethodDelete, e.URL+"/v1/servers/nobody/drain")
		wantError(t, "undrain of an unknown server", status, raw, http.StatusNotFound, codeServerNotFound, "")
	})

	t.Run("nowhere to go", func(t *testing.T) {
		e.Nomad.SetNodeEligibilityOf("node-1", "ineligible", false)
		status, raw := post("/v1/servers/two/drain", ``)
		wantError(t, "no other server accepts work", status, raw, http.StatusConflict, codeNoOtherServer, "")
		e.Nomad.SetNodeEligibilityOf("node-1", "eligible", false)
	})

	t.Run("a move runs, ends done and is read back; accept work again", func(t *testing.T) {
		status, raw := post("/v1/servers/two/drain", `{"deadlineSeconds": 120}`)
		if status != http.StatusAccepted {
			t.Fatalf("drainServer = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Server")
		first := decodeAs[serverFull](t, raw)
		if first.AcceptsWork || first.Move == nil {
			t.Errorf("answer = %+v, want a server that takes no work and a move", first)
		}
		deadline := time.Now().Add(10 * time.Second)
		var got serverFull
		for {
			_, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/two", "", "")
			checkSchema(t, doc, raw, "Server")
			got = decodeAs[serverFull](t, raw)
			if got.Move != nil && got.Move.State == "done" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the move is %s", raw)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got.Move.Moved != 1 || got.AcceptsWork {
			t.Errorf("server after the move = %+v", got)
		}
		status, raw = post("/v1/servers/two/drain", `{"deadlineSeconds": 120}`)
		if status != http.StatusAccepted { // a finished move doesn't block a new one
			t.Errorf("a second drain = %d %s", status, raw)
		}
		status, raw = call2(t, http.MethodDelete, e.URL+"/v1/servers/two/drain")
		if status != http.StatusOK {
			t.Fatalf("undrainServer = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Server")
		if after := decodeAs[serverFull](t, raw); !after.AcceptsWork {
			t.Errorf("after undrain: %+v", after)
		}
	})
}

// call2 is call for a request with no body and no content type.
func call2(t *testing.T, method, url string) (int, []byte) {
	t.Helper()
	status, _, raw := call(t, method, url, "", "")
	return status, raw
}

// placedBody is a service with placement rules and no domain, so several apps can have it.
func placedBody(placement string) string {
	return `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest",
		"ports": [{"name": "http", "containerPort": 80}], "placement": ` + placement + `,
		"health": {"path": "/", "port": "http"}}]}`
}

// Placement rules are part of the app: they read back, a deploy whose rule no server can satisfy is 409
// placement_impossible on the rule's field, and the model's refusals are 400 on the field.
func TestPlacementAPI(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	post := func(id, body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/"+id+"/deployments", "application/json", body)
		return status, raw
	}

	t.Run("the rules deploy and read back", func(t *testing.T) {
		body := placedBody(`{"require": {"ssd": "true"}, "prefer": [{"label": "region", "value": "eu", "weight": 80}], "avoid": ["three"]}`)
		d := deployOK(t, e, "placed", body)
		waitHealthy(t, e, "placed", d.ID)
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/placed", "", "")
		if status != http.StatusOK {
			t.Fatalf("getApp = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "AppRecord")
		var rec struct {
			App struct {
				Services []struct {
					Placement json.RawMessage `json:"placement"`
				} `json:"services"`
			} `json:"app"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if got := string(rec.App.Services[0].Placement); !strings.Contains(got, `"ssd":"true"`) || !strings.Contains(got, `"weight":80`) || !strings.Contains(got, `"three"`) {
			t.Errorf("placement = %s", got)
		}
		if e.Nomad.PlanRequests() == 0 {
			t.Error("a deploy with rules didn't ask Nomad to plan it")
		}
	})

	t.Run("a label no server has", func(t *testing.T) {
		e.Nomad.SetPlanFailure("web", "${meta.caravana.label.gpu} = true", 1)
		defer e.Nomad.ClearPlanFailures()
		status, raw := post("gpu", placedBody(`{"require": {"gpu": "true"}}`))
		got := wantError(t, "deployApp with an impossible require", status, raw, http.StatusConflict, codePlacementImpossible, "services[0].placement.require.gpu")
		if !strings.Contains(got.Error.Message, "no server that accepts work has the label gpu=true") {
			t.Errorf("message = %q", got.Error.Message)
		}
		if e.Nomad.Job("gpu") != nil {
			t.Error("a job was registered")
		}
	})

	t.Run("every server avoided", func(t *testing.T) {
		e.Nomad.SetPlanFailure("web", "${meta.caravana.server} != hel-1", 1)
		defer e.Nomad.ClearPlanFailures()
		status, raw := post("avoids", placedBody(`{"avoid": ["hel-1"]}`))
		wantError(t, "deployApp that avoids every server", status, raw, http.StatusConflict, codePlacementImpossible, "services[0].placement.avoid")
	})

	t.Run("the model's refusals", func(t *testing.T) {
		for _, tc := range []struct{ name, placement, field string }{
			{"an empty object", `{}`, "services[0].placement"},
			{"a bad label name", `{"require": {"Bad": "x"}}`, "services[0].placement.require"},
			{"a bad value", `{"require": {"ssd": "a b"}}`, "services[0].placement.require.ssd"},
			{"a weight over 100", `{"prefer": [{"label": "ssd", "value": "true", "weight": 101}]}`, "services[0].placement.prefer[0].weight"},
			{"a reserved server name", `{"avoid": ["new"]}`, "services[0].placement.avoid[0]"},
		} {
			status, raw := post("bad", placedBody(tc.placement))
			wantError(t, tc.name, status, raw, http.StatusBadRequest, codeInvalidField, tc.field)
		}
	})
}

// The three calls are the console's: a deploy token is denied each (the default deny), and an unauthenticated
// request is a 401.
func TestPlacementCallsAreNotForDeployTokens(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	shopProject(t, e)
	deployToken := scopedToken(t, e, "shop", "web")
	for _, op := range []struct{ method, path, body string }{
		{http.MethodPut, "/v1/servers/hel-1/labels", `{"labels": {}}`},
		{http.MethodPost, "/v1/servers/hel-1/drain", `{}`},
		{http.MethodDelete, "/v1/servers/hel-1/drain", ``},
	} {
		key := op.method + " " + strings.Replace(op.path, "/hel-1/", "/{name}/", 1)
		if slicesContain(allowedForDeployTokens, key) {
			t.Errorf("%s is allowed for deploy tokens", key)
		}
		status, _, raw := callWith(t, deployToken, op.method, e.RawURL+op.path, "application/json", op.body)
		if status != http.StatusForbidden || !strings.Contains(string(raw), codeTokenScope) {
			t.Errorf("%s with a deploy token = %d %s, want 403 token_scope", key, status, raw)
		}
		if status, _, _ = callWith(t, "", op.method, e.RawURL+op.path, "application/json", op.body); status != http.StatusUnauthorized {
			t.Errorf("%s with no credentials = %d, want 401", key, status)
		}
	}
}

// The needs-you list's two kinds carry their numbers.
func TestAttentionItemsOfPlacement(t *testing.T) {
	doc := loadContract(t)
	for _, tc := range []struct {
		it   service.AttentionItem
		want []string
	}{
		{service.AttentionItem{Kind: service.AttentionCopiesUnplaced, AppID: "blog", Service: "web", Queued: 2, Level: "failed",
			When: time.Now(), Detail: "x"}, []string{`"kind":"copies_unplaced"`, `"queued":2`}},
		{service.AttentionItem{Kind: service.AttentionServerMoveIncomplete, Server: "two", Left: 3, Level: "warning",
			When: time.Now(), Detail: "x"}, []string{`"kind":"server_move_incomplete"`, `"left":3`, `"server":"two"`, `"level":"warning"`}},
	} {
		raw, err := json.Marshal(toAttentionItemJSON(tc.it))
		if err != nil {
			t.Fatal(err)
		}
		checkSchema(t, doc, raw, "AttentionItem")
		for _, w := range tc.want {
			if !strings.Contains(string(raw), w) {
				t.Errorf("item %s lacks %s", raw, w)
			}
		}
	}
}
