package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const apiActivitySentinel = "s3cret-sentinel-027api"

// seedActivity stores an app with a restart, a failing command whose arguments
// carry a sentinel (the store never kept them) and a server boot.
func seedActivity(t *testing.T, e *testEnv) {
	t.Helper()
	ctx := context.Background()
	a := apptest.Whoami().App
	if _, err := e.Store.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	three := 3
	for _, act := range []store.ActionRecord{
		{ID: "act_restart", AppID: a.ID, Action: store.ActionRestart, Trigger: "console", Status: store.ActionSucceeded, StartedAt: now.Add(-3 * time.Minute)},
		{ID: "act_run", AppID: a.ID, Action: store.ActionRun, Trigger: "manual", Status: store.ActionFailed, CopyID: "c1", Service: "web",
			Program: "cat", ExitCode: &three, StartedAt: now.Add(-2 * time.Minute)},
	} {
		if err := e.Store.InsertAction(ctx, act); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Store.AppendHostEvent(ctx, store.HostEvent{Kind: store.HostEventBoot, At: now.Add(-time.Minute), Detail: "{}", Key: "boot|1"}); err != nil {
		t.Fatal(err)
	}
}

func TestListActivity(t *testing.T) {
	e := monEnv(t)
	seedActivity(t, e)

	status, header, raw := call(t, http.MethodGet, e.URL+"/v1/activity", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/activity = %d %s", status, raw)
	}
	if cc := header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	checkSchema(t, loadContract(t), raw, "ActivityList")
	var out struct {
		Entries []struct {
			ID, Kind, Status, Message, App string
			Time                           time.Time
			Place                          *struct{ Project, Environment string }
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 3 {
		t.Fatalf("entries = %s", raw)
	}
	got := out.Entries
	if got[0].Kind != "server" || got[0].Message != "The server started." || got[0].App != "" || got[0].Place != nil {
		t.Errorf("newest = %+v", got[0])
	}
	if got[1].Kind != "command" || got[1].Status != "warning" || got[1].Message != "cat ran in whoami through the API, exit 3." ||
		got[1].App != "whoami" || got[1].Place == nil || got[1].Place.Project != "default" || got[1].Place.Environment != "production" {
		t.Errorf("command = %+v", got[1])
	}
	if got[2].Kind != "restart" || got[2].Message != "whoami was restarted from the console." {
		t.Errorf("restart = %+v", got[2])
	}
	if strings.Contains(string(raw), apiActivitySentinel) {
		t.Error("the answer holds command text")
	}

	// limit, before and server.
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/activity?limit=1", "", "")
	var one struct{ Entries []struct{ ID string } }
	if err := json.Unmarshal(raw, &one); err != nil || len(one.Entries) != 1 || one.Entries[0].ID != got[0].ID {
		t.Errorf("limit=1 = %s, %v", raw, err)
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/activity?before="+url.QueryEscape(got[1].Time.Format(time.RFC3339Nano)), "", "")
	var older struct{ Entries []struct{ ID string } }
	if err := json.Unmarshal(raw, &older); err != nil || len(older.Entries) != 1 || older.Entries[0].ID != got[2].ID {
		t.Errorf("before = %s, %v", raw, err)
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/activity?server=true", "", "")
	var srv struct{ Entries []struct{ Kind string } }
	if err := json.Unmarshal(raw, &srv); err != nil || len(srv.Entries) != 2 || srv.Entries[0].Kind != "server" || srv.Entries[1].Kind != "restart" {
		t.Errorf("server=true = %s, %v", raw, err)
	}
}

func TestListActivityRefusesBadQueries(t *testing.T) {
	e := monEnv(t)
	for q, field := range map[string]string{
		"limit=0": "limit", "limit=51": "limit", "limit=x": "limit", "before=yesterday": "before", "server=maybe": "server",
	} {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/activity?"+q, "", "")
		if status != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422", q, status, raw)
			continue
		}
		var body struct{ Error struct{ Code, Field string } }
		if err := json.Unmarshal(raw, &body); err != nil || body.Error.Code != "invalid_field" || body.Error.Field != field {
			t.Errorf("%s = %s", q, raw)
		}
	}
}

func TestListActivityIsEmptyOnANewInstall(t *testing.T) {
	e := monEnv(t)
	_, _, raw := call(t, http.MethodGet, e.URL+"/v1/activity", "", "")
	checkSchema(t, loadContract(t), raw, "ActivityList")
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Errorf("empty feed = %s", raw)
	}
}
