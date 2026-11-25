package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Phase B of the audit log (feature 057), through the real handlers: a write request that changed
// something leaves exactly one entry, noted by the service, not the fallback.

// auditSince is the entries written after the first n, oldest first.
func auditSince(t *testing.T, e *testEnv, n int) []audit.Event {
	t.Helper()
	got, _, err := e.Store.ListAuditEvents(context.Background(), store.AuditFilter{Limit: store.MaxAuditLimit})
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
		got[i], got[j] = got[j], got[i]
	}
	if n > len(got) {
		return nil
	}
	return got[n:]
}

// oneEntry runs f and returns the one entry it wrote, which must not be a fallback.
func oneEntry(t *testing.T, e *testEnv, f func()) audit.Event {
	t.Helper()
	n := auditRowCount(t, e.Store)
	f()
	got := auditSince(t, e, n)
	if len(got) != 1 {
		t.Fatalf("%d audit entries, want 1: %+v", len(got), got)
	}
	if got[0].Fallback {
		t.Fatalf("the entry is the fallback: %+v", got[0])
	}
	return got[0]
}

func findChange(changes []audit.Change, path string) *audit.Change {
	for i := range changes {
		if changes[i].Path == path {
			return &changes[i]
		}
	}
	return nil
}

func allEntries(t *testing.T, e *testEnv) string {
	t.Helper()
	return string(mustJSON(t, auditSince(t, e, 0)))
}

func TestAuditDeployThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	var first deployment
	got := oneEntry(t, e, func() { first = deployOK(t, e, "whoami", whoamiBody) })
	waitHealthy(t, e, "whoami", first.ID)
	if got.Action != audit.ActionCreated || got.Resource.Kind != audit.KindApp || got.Resource.ID != "whoami" ||
		got.Record == nil || got.Record.Kind != audit.RecordDeployment || got.Record.ID != first.ID {
		t.Errorf("first deploy = %+v", got)
	}
	if got.Actor.Kind != audit.ActorToken || got.Actor.Token != "tests" || got.Door != audit.DoorAPI ||
		got.Operation != "POST /v1/apps/{id}/deployments" {
		t.Errorf("who and how = %+v %s %s", got.Actor, got.Door, got.Operation)
	}

	// A domain moved: the old host removed, the new one added.
	var second deployment
	got = oneEntry(t, e, func() {
		second = deployOK(t, e, "whoami", strings.Replace(whoamiBody, "whoami.localhost", "other.localhost", 1))
	})
	waitHealthy(t, e, "whoami", second.ID)
	if got.Action != audit.ActionDeployed || got.Record == nil || got.Record.ID != second.ID {
		t.Errorf("second deploy = %+v", got)
	}
	if c := findChange(got.Changes, "services[web].domains[whoami.localhost]"); c == nil || c.Op != audit.OpRemoved {
		t.Errorf("the old host is not shown removed: %+v", got.Changes)
	}
	if c := findChange(got.Changes, "services[web].domains[other.localhost]"); c == nil || c.Op != audit.OpAdded {
		t.Errorf("the new host is not shown added: %+v", got.Changes)
	}
}

func TestAuditSecretSetThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", withSecrets(`[{"name": "DATABASE_URL", "value": "`+secretMarker+`-one"}]`))
	waitHealthy(t, e, "whoami", d.ID)

	got := oneEntry(t, e, func() {
		d = deployOK(t, e, "whoami", withSecrets(`[{"name": "DATABASE_URL", "value": "`+secretMarker+`-two"}]`))
	})
	waitHealthy(t, e, "whoami", d.ID)
	c := findChange(got.Changes, "services[web].secrets[DATABASE_URL].value")
	if c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("secret change = %+v in %+v, want changed with no from or to", c, got.Changes)
	}
	if strings.Contains(allEntries(t, e), secretMarker) {
		t.Error("a secret value is in an entry")
	}
}

func TestAuditWebhookRedeployThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)

	var token string
	got := oneEntry(t, e, func() {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/webhook", "", "")
		if status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("create webhook token = %d %s", status, raw)
		}
		token = decodeAs[struct {
			Token string `json:"token"`
		}](t, raw).Token
	})
	if got.Action != audit.ActionCreated || got.Resource.Kind != audit.KindWebhookToken || got.Resource.ID != "whoami" {
		t.Errorf("webhook token entry = %+v", got)
	}

	call1 := func() (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, e.URL+"/v1/webhooks/whoami?token="+token, nil)
		resp, err := noRedirects.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			raw.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, []byte(raw.String())
	}

	// Nothing moved: the call changes nothing, and so leaves no entry (not even the fallback).
	n := auditRowCount(t, e.Store)
	if status, raw := call1(); status != http.StatusOK {
		t.Fatalf("webhook with nothing changed = %d %s", status, raw)
	}
	if rows := auditSince(t, e, n); len(rows) != 0 {
		t.Errorf("a webhook that changed nothing left %+v", rows)
	}

	// The tag moved: the webhook redeploys, and the webhook is the actor.
	e.Registry.AddIndex("traefik/whoami", "latest", movedDigest)
	got = oneEntry(t, e, func() {
		if status, raw := call1(); status != http.StatusAccepted && status != http.StatusOK {
			t.Fatalf("webhook = %d %s", status, raw)
		}
	})
	if got.Action != audit.ActionRedeployed || got.Resource.Kind != audit.KindApp || got.Resource.ID != "whoami" {
		t.Errorf("webhook entry = %+v", got)
	}
	if got.Actor.Kind != audit.ActorWebhook || got.Actor.Token != "whoami" || got.Door != audit.DoorWebhook ||
		got.Record == nil || got.Record.Kind != audit.RecordDeployment {
		t.Errorf("who and how = %+v %s record %+v", got.Actor, got.Door, got.Record)
	}
	if strings.Contains(allEntries(t, e), token) {
		t.Error("the webhook token is in an entry")
	}
}

func TestAuditDeleteAppThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)
	got := oneEntry(t, e, func() {
		if status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/apps/whoami", "", ""); status != http.StatusNoContent {
			t.Fatalf("DELETE = %d %s", status, raw)
		}
	})
	if got.Action != audit.ActionDeleted || got.Resource.Kind != audit.KindApp || got.Resource.ID != "whoami" {
		t.Errorf("entry = %+v", got)
	}
	// An unknown app is a 404 and leaves nothing.
	n := auditRowCount(t, e.Store)
	if status, _, _ := call(t, http.MethodDelete, e.URL+"/v1/apps/ghost", "", ""); status != http.StatusNotFound {
		t.Errorf("DELETE of an unknown app = %d", status)
	}
	if rows := auditSince(t, e, n); len(rows) != 0 {
		t.Errorf("a refused delete left %+v", rows)
	}
}

func TestAuditBackUpNowThroughTheAPI(t *testing.T) {
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", dbCompose)
	if status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	var b struct {
		ID string `json:"id"`
	}
	got := oneEntry(t, e, func() {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "db"}`)
		if status != http.StatusAccepted {
			t.Fatalf("POST backups = %d %s", status, raw)
		}
		b = decodeAs[struct {
			ID string `json:"id"`
		}](t, raw)
	})
	if got.Action != audit.ActionBackedUp || got.Resource.Kind != audit.KindBackup || got.Resource.ID != b.ID ||
		got.Record == nil || got.Record.Kind != audit.RecordBackup || got.Record.ID != b.ID {
		t.Errorf("entry = %+v, want backed up %s", got, b.ID)
	}
	// Refused while it runs: nothing.
	n := auditRowCount(t, e.Store)
	if status, _, _ := call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "db"}`); status != http.StatusConflict {
		t.Errorf("second backup = %d", status)
	}
	if rows := auditSince(t, e, n); len(rows) != 0 {
		t.Errorf("a refused backup left %+v", rows)
	}
}

func TestAuditCreateScheduleThroughTheAPI(t *testing.T) {
	e := scheduleEnv(t)
	var sc struct {
		ID string `json:"id"`
	}
	got := oneEntry(t, e, func() {
		sc.ID = createScheduleOK(t, e, nightly).ID
	})
	if got.Action != audit.ActionCreated || got.Resource.Kind != audit.KindSchedule || got.Resource.ID != sc.ID ||
		got.Resource.Name != "rates-sync" || got.Operation != "POST /v1/schedules" {
		t.Errorf("entry = %+v", got)
	}
	got = oneEntry(t, e, func() { runScheduleOK(t, e, sc.ID) })
	if got.Action != audit.ActionRanCommand || got.Resource.Kind != audit.KindSchedule || got.Resource.ID != sc.ID ||
		got.Record == nil || got.Record.Kind != audit.RecordScheduleRun {
		t.Errorf("run now entry = %+v", got)
	}
}
