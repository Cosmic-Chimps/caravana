package ui

// The rest of the demo installation (TestDocsDemo): logs and requests, backups, schedules, the
// installation's settings, a preview, the image scanner's results, a week of monitoring history and
// the servers. Every number,
// name and line here is made up. Nothing is random between runs except the clock: the generators
// are seeded, so two runs draw the same pictures a few minutes apart.

import (
	"context"
	"fmt"
	"hash/fnv"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// ---- Logs and requests ---------------------------------------------------------------------

func rngFor(seed string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(seed))
	return rand.New(rand.NewPCG(h.Sum64(), 7))
}

func pick[T any](r *rand.Rand, xs ...T) T { return xs[r.IntN(len(xs))] }

const demoStamp = "2006-01-02T15:04:05Z"

// webLogLines is what a small storefront prints, oldest first, one line about every 40 seconds for
// span, the last one about a minute old. One warning and one error, as a real hour has.
func (d *docsDemo) webLogLines(seed string, span time.Duration) ([]string, time.Time) {
	r := rngFor(seed)
	var lines []string
	paths := []string{"/", "/products", "/products/42", "/products/17", "/cart", "/search?q=mug", "/checkout", "/account", "/static/app.css"}
	last := d.now
	for at := d.now.Add(-span); at.Before(d.now.Add(-time.Minute)); at = at.Add(time.Duration(25+r.IntN(30)) * time.Second) {
		ts := at.Format(demoStamp)
		switch n := len(lines); {
		case n == 31:
			lines = append(lines, ts+" WARN slow query on orders (480 ms): consider an index on orders(customer_id)")
		case n == 58 && seed == "web0":
			lines = append(lines, ts+" ERROR payment provider timed out after 5000 ms for order 10438, retrying (1 of 3)")
		case r.IntN(100) < 9:
			lines = append(lines, fmt.Sprintf("%s session started for customer %d", ts, 1000+r.IntN(400)))
		default:
			lines = append(lines, fmt.Sprintf("%s GET %s 200 %d ms req=%08x", ts, pick(r, paths...), 6+r.IntN(40), r.Uint32()))
		}
		last = at
	}
	return lines, last
}

func (d *docsDemo) apiLogLines(seed string, span time.Duration) ([]string, time.Time) {
	r := rngFor(seed)
	var lines []string
	routes := []string{"GET /v1/products", "GET /v1/products/42", "POST /v1/cart/items", "GET /v1/orders", "POST /v1/orders", "GET /v1/me"}
	last := d.now
	for at := d.now.Add(-span); at.Before(d.now.Add(-time.Minute)); at = at.Add(time.Duration(30+r.IntN(40)) * time.Second) {
		ts := at.Format(demoStamp)
		route := pick(r, routes...)
		status := 200
		if strings.HasPrefix(route, "POST") {
			status = 201
		}
		if len(lines) == 22 {
			lines = append(lines, ts+" WARN rate limit reached for an API key (60 requests a minute), answering 429")
		} else {
			lines = append(lines, fmt.Sprintf("%s %s %d %d ms", ts, route, status, 4+r.IntN(30)))
		}
		last = at
	}
	return lines, last
}

// seedDocsLogs puts each running copy's output in the fake Nomad's file system.
func (d *docsDemo) seedDocsLogs(webIDs, apiIDs, stagingIDs []string) {
	for i, id := range webIDs {
		lines, last := d.webLogLines(fmt.Sprintf("web%d", i), 50*time.Minute)
		d.Nomad.AddLogFile(id, "stdout", 0, lines, last.Add(5*time.Second))
	}
	for i, id := range apiIDs {
		lines, last := d.apiLogLines(fmt.Sprintf("api%d", i), 50*time.Minute)
		d.Nomad.AddLogFile(id, "stdout", 0, lines, last.Add(5*time.Second))
	}
	for i, id := range stagingIDs {
		lines, last := d.webLogLines(fmt.Sprintf("stg%d", i), 50*time.Minute)
		d.Nomad.AddLogFile(id, "stdout", 0, lines, last.Add(5*time.Second))
	}
	for i, id := range d.searchIDs {
		lines, last := d.searchLogLines(fmt.Sprintf("search%d", i), 50*time.Minute)
		d.Nomad.AddLogFile(id, "stdout", 0, lines, last.Add(5*time.Second))
	}
}

// searchLogLines is what a quiet single-node OpenSearch prints: a flush or a refresh every few
// minutes and one slow query.
func (d *docsDemo) searchLogLines(seed string, span time.Duration) ([]string, time.Time) {
	r := rngFor(seed)
	var lines []string
	last := d.now
	for at := d.now.Add(-span); at.Before(d.now.Add(-time.Minute)); at = at.Add(time.Duration(90+r.IntN(90)) * time.Second) {
		ts := at.Format(demoStamp)
		if len(lines) == 11 {
			lines = append(lines, ts+" WARN [o.o.s.query] [products][0] took[1.2s] search_type[QUERY_THEN_FETCH] total_shards[1]")
		} else {
			lines = append(lines, fmt.Sprintf("%s INFO [o.o.i.s.IndexShard] [%s][0] %s in %d ms", ts,
				pick(r, "products", "orders", "customers"), pick(r, "flush completed", "refresh completed", "merge completed"), 9+r.IntN(60)))
		}
		last = at
	}
	return lines, last
}

// accessLine is one line of Traefik's access log, as the installer's Traefik writes it.
func accessLine(start time.Time, dur time.Duration, svc, host, method, path string, status int, client string, size int) string {
	return fmt.Sprintf(`{"ClientHost":%q,"DownstreamContentSize":%d,"DownstreamStatus":%d,"Duration":%d,`+
		`"RequestHost":%q,"RequestMethod":%q,"RequestPath":%q,"RetryAttempts":0,"RouterName":%q,"ServiceName":%q,"StartUTC":%q,`+
		`"TLSVersion":"1.3","entryPointName":"websecure","level":"info","msg":"","time":%q}`,
		client, size, status, dur.Nanoseconds(), host, method, path, svc+"@nomad", svc+"@nomad",
		start.UTC().Format(time.RFC3339Nano), start.Add(dur).UTC().Format(time.RFC3339))
}

// seedDocsRequests makes Traefik's allocation and its access log: about eight requests a minute for
// the shop's three addresses over the last 50 minutes.
func (d *docsDemo) seedDocsRequests() {
	d.Nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true", "--accesslog.format=json")
	id := demoUUID("traefik")
	d.Nomad.SetCopies("traefik", api.AllocationListStub{ID: id, TaskGroup: "traefik", JobVersion: 1, ClientStatus: "running", DesiredStatus: "run",
		NodeName: "hel-1", NodeID: "node-1", CreateTime: d.ago(40 * 24 * time.Hour).UnixNano(),
		TaskStates: map[string]*api.TaskState{"traefik": {State: "running", StartedAt: d.ago(40 * 24 * time.Hour)}}})
	r := rngFor("traefik")
	clients := []string{"203.0.113.7", "198.51.100.23", "203.0.113.58", "192.0.2.41", "198.51.100.9", "203.0.113.120"}
	type route struct {
		svc, host string
		paths     []string
		weight    int
	}
	routes := []route{
		{"shop--production--web-web", "shop.example.com", []string{"/", "/products", "/products/42", "/products/17", "/cart", "/search", "/checkout", "/static/app.css", "/static/app.js", "/favicon.ico"}, 6},
		{"shop--production--api-api", "api.shop.example.com", []string{"/v1/products", "/v1/products/42", "/v1/cart/items", "/v1/orders", "/v1/me"}, 3},
		{"shop--staging--web-web", "staging.shop.example.com", []string{"/", "/products", "/checkout"}, 1},
	}
	var lines []string
	for at := d.now.Add(-50 * time.Minute); at.Before(d.now.Add(-30 * time.Second)); at = at.Add(time.Duration(2+r.IntN(12)) * time.Second) {
		n := r.IntN(10)
		var rt route
		switch {
		case n < 6:
			rt = routes[0]
		case n < 9:
			rt = routes[1]
		default:
			rt = routes[2]
		}
		path := pick(r, rt.paths...)
		method, status := "GET", 200
		dur := time.Duration(4+r.IntN(40)) * time.Millisecond
		size := 900 + r.IntN(20000)
		switch roll := r.IntN(200); {
		case roll == 1:
			status, path = 404, "/wp-login.php"
		case roll == 2:
			status, path = 404, "/products/9999"
		case roll == 3:
			method, path, status, dur = "POST", "/v1/orders", 201, 180*time.Millisecond
		case roll == 4:
			method, path, dur = "POST", "/checkout", 1200*time.Millisecond
		case roll == 5:
			status, dur = 500, 310*time.Millisecond
		case roll < 12:
			dur = time.Duration(400+r.IntN(500)) * time.Millisecond
		}
		if strings.HasPrefix(path, "/v1/") && method == "GET" && rt.svc != routes[1].svc {
			path = "/products"
		}
		if method == "POST" && strings.HasPrefix(path, "/v1/") {
			rt = routes[1]
		}
		lines = append(lines, accessLine(at, dur, rt.svc, rt.host, method, path, status, pick(r, clients...), size))
	}
	d.Nomad.AddLogFile(id, "stdout", 0, lines, d.now)
}

// ---- Backups -------------------------------------------------------------------------------

// lastAt is the latest hh:00 UTC at or before t.
func lastAt(t time.Time, hour int) time.Time {
	t = t.UTC()
	at := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, time.UTC)
	if at.After(t) {
		at = at.AddDate(0, 0, -1)
	}
	return at
}

func (d *docsDemo) seedDocsBackups(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	secret := "demo-secret-key-not-real"
	if _, err := d.Svc.SetBackupDestination(ctx, service.DestinationInput{Endpoint: "https://s3.eu-central-1.example.com", Region: "eu-central-1",
		Bucket: "shop-backups", Prefix: "caravana", AccessKeyID: "AKIADEMO0000EXAMPLE", SecretAccessKey: &secret}); err != nil {
		t.Fatalf("SetBackupDestination: %v", err)
	}
	appID := shopID("production", "db")
	first := lastAt(d.now, 3)
	var ids []string
	for i := 6; i >= 0; i-- {
		started := first.AddDate(0, 0, -i)
		id := fmt.Sprintf("bk_%012x", 0xa1c3e5b70000+i*7919)
		finished := started.Add(time.Duration(38+i%4) * time.Second)
		rec := store.BackupRecord{AppID: appID, ID: id, Service: "db", JobID: appID + "_db_backup", Engine: "postgres", Status: store.BackupSucceeded,
			Object: fmt.Sprintf("caravana/%s/db/%s.dump", appID, started.Format("20060102T150405Z")), Bytes: int64(41_300_000 + (6-i)*1_150_000),
			StartedAt: started, FinishedAt: &finished, Trigger: store.BackupTriggerTimer, Destination: "main"}
		if i == 2 {
			rec.Trigger, rec.Actor = store.BackupTriggerOwner, docsDemoEmail
			rec.StartedAt = started.Add(14*time.Hour + 22*time.Minute)
			fin := rec.StartedAt.Add(41 * time.Second)
			rec.FinishedAt = &fin
		}
		d.at(started, func() {
			if err := d.Store.SaveBackup(ctx, rec); err != nil {
				t.Fatalf("SaveBackup: %v", err)
			}
		})
		ids = append(ids, id)
	}
	if _, err := d.Store.PinBackup(ctx, appID, ids[2], "before-1.8", d.ago(4*24*time.Hour)); err != nil {
		t.Fatalf("PinBackup: %v", err)
	}
}

// ---- Schedules -----------------------------------------------------------------------------

func (d *docsDemo) seedDocsSchedules(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	apiID := shopID("production", "api")
	var report, prune service.Schedule
	var err error
	d.at(d.ago(12*24*time.Hour), func() {
		report, err = d.Svc.CreateSchedule(ctx, service.NewSchedule{App: apiID, Service: "api", Name: "nightly-report",
			Command: []string{"node", "scripts/nightly-report.js"}, Cron: "0 2 * * *", StopAfterSeconds: 1800})
		if err != nil {
			t.Fatalf("CreateSchedule nightly-report: %v", err)
		}
		prune, err = d.Svc.CreateSchedule(ctx, service.NewSchedule{App: apiID, Service: "api", Name: "prune-sessions",
			Command: []string{"node", "scripts/prune-sessions.js", "--older-than", "30d"}, Cron: "0 * * * *", StopAfterSeconds: 300})
		if err != nil {
			t.Fatalf("CreateSchedule prune-sessions: %v", err)
		}
	})
	run := func(sc service.Schedule, id string, slot time.Time, took time.Duration, status string, exit int, out, errOut, msg string) {
		started := slot.Add(2 * time.Second)
		rec := store.ScheduleRunRecord{ID: id, ScheduleID: sc.ID, AppID: sc.App, Service: sc.Service, Trigger: "timer", SlotAt: &slot,
			Command: sc.Command, Status: store.RunStarting, StartedAt: started, JobVersion: new(3), DeploymentID: "api-p3"}
		if err := d.Store.InsertRun(ctx, rec); err != nil {
			t.Fatalf("InsertRun(%s): %v", id, err)
		}
		st := store.RunSettlement{Status: status, ExitCode: &exit, Error: msg, TaskStartedAt: &started, FinishedAt: started.Add(took),
			Stdout: []byte(out), Stderr: []byte(errOut), OutputKept: true}
		if err := d.Store.SettleRun(ctx, id, st); err != nil {
			t.Fatalf("SettleRun(%s): %v", id, err)
		}
	}
	slot := lastAt(d.now, 2)
	for i := 4; i >= 1; i-- {
		s := slot.AddDate(0, 0, -i)
		run(report, fmt.Sprintf("run_nr%02d", i), s, 21*time.Second+time.Duration(i)*time.Second, store.RunOK, 0,
			fmt.Sprintf("report: %d orders summarised for %s\nreport: sent to ops@example.com\n", 1180+i*23, s.AddDate(0, 0, -1).Format("2006-01-02")), "", "")
	}
	// The newest run failed: the one problem the Apps home lists.
	run(report, "run_nr00", slot, 5*time.Second, store.RunFailed, 1, "report: summarising orders for "+slot.AddDate(0, 0, -1).Format("2006-01-02")+"\n",
		"error: could not connect to the mail relay mail.example.com:587 (connection timed out)\n", "exit 1")
	hour := d.now.UTC().Truncate(time.Hour)
	for i := 5; i >= 0; i-- {
		s := hour.Add(-time.Duration(i) * time.Hour)
		if s.After(d.now) {
			continue
		}
		run(prune, fmt.Sprintf("run_ps%02d", i), s, 3*time.Second, store.RunOK, 0, fmt.Sprintf("pruned %d expired sessions\n", 40+i*7), "", "")
	}
}

// ---- Settings, people, tokens, a registry, a channel, the audit log ------------------------

func (d *docsDemo) seedDocsSettings(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pw := "demo-registry-token-not-real"
	if _, err := d.Svc.SaveRegistry(ctx, "ghcr.io/acme", "acme-deploy", &pw); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	// The channel listens to deploys only, so nothing in the demo is ever sent: there is no network.
	ch, err := d.Svc.CreateNotificationChannel(ctx, service.NotificationChannelInput{Kind: "ntfy", Name: "Ops phone",
		URL: "https://ntfy.example.com/shop-alerts", Events: []string{service.EventDeployFailed, service.EventDeploySucceeded}})
	if err != nil {
		t.Fatalf("CreateNotificationChannel: %v", err)
	}
	for i, title := range []string{"web v5 is live in shop › production", "api v3 is live in shop › production", "web v2 is live in shop › staging"} {
		at := d.ago(time.Duration(3+i*17) * time.Hour)
		done := at.Add(900 * time.Millisecond)
		if err := d.Store.EnqueueDelivery(ctx, store.Delivery{ID: fmt.Sprintf("ndl_demo%08d", i), ChannelID: ch.ID, EventType: "deploy.succeeded",
			Title: title, Payload: "{}", Status: "sent", Attempts: 1, NextAt: at, CreatedAt: at, FinishedAt: &done}); err != nil {
			t.Fatalf("EnqueueDelivery: %v", err)
		}
	}

	owner, err := d.Auth.ResolvePerson(ctx, "")
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	if _, err := d.Auth.CreateAPIToken(ctx, owner.ID, "local", new(d.now.Add(90*24*time.Hour))); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	for _, tok := range []struct{ name, app string }{{"shop-ci", "web"}, {"api-ci", "api"}} {
		if _, err := d.Auth.CreateScopedAPIToken(ctx, owner.ID, tok.name, nil, &service.TokenScope{Project: "shop", App: tok.app}); err != nil {
			t.Fatalf("CreateScopedAPIToken: %v", err)
		}
	}

	inv, err := d.Svc.InvitePerson(ctx, service.PersonInput{Email: "ben@example.com", OrgRole: service.OrgMember,
		Projects: []service.ProjectRole{{Project: "shop", Role: service.RoleDeploy, Environments: map[string]service.Role{"production": service.RoleView}}}})
	if err != nil {
		t.Fatalf("InvitePerson: %v", err)
	}
	if _, err := d.Svc.AcceptInvite(ctx, inv.Token, "ben-demo-password-not-real", "203.0.113.50"); err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}

	d.seedDocsAudit(t)
}

func (d *docsDemo) seedDocsAudit(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	ana := audit.Actor{Kind: audit.ActorPerson, Email: docsDemoEmail}
	ci := audit.Actor{Kind: audit.ActorDeployToken, Token: "shop-ci", Email: docsDemoEmail}
	appRes := func(env, name string) audit.Resource {
		return audit.Resource{Kind: audit.KindApp, ID: shopID(env, name), Name: name, Project: "shop", Environment: env}
	}
	type ev struct {
		ago time.Duration
		e   audit.Entry
	}
	evs := []ev{
		{19 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionCreated, Operation: "POST /settings/people", Resource: audit.Resource{Kind: audit.KindProject, ID: "shop", Name: "Shop"}}},
		{18 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionDeployed, Operation: "POST /apps/new", Resource: appRes("production", "db"), Record: &audit.Record{Kind: audit.RecordDeployment, ID: "db-p1"}}},
		{16 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionCreated, Operation: "POST /settings/registries", Resource: audit.Resource{Kind: audit.KindRegistry, ID: "ghcr.io/acme", Name: "ghcr.io/acme"}}},
		{15 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionCreated, Operation: "POST /settings/backups", Resource: audit.Resource{Kind: audit.KindBackupDestination, ID: "main", Name: "main"}}},
		{12 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionCreated, Operation: "POST /schedules", Resource: audit.Resource{Kind: audit.KindSchedule, ID: "nightly-report", Name: "nightly-report", Project: "shop", Environment: "production"}}},
		{9 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionInvited, Operation: "POST /settings/people", Resource: audit.Resource{Kind: audit.KindPerson, ID: "ben@example.com", Name: "ben@example.com"}}},
		{8*24*time.Hour + time.Hour, audit.Entry{Actor: audit.Actor{Kind: audit.ActorPerson, Email: "ben@example.com"}, Door: audit.DoorConsole, Address: "203.0.113.50", Action: audit.ActionAcceptedInvite, Operation: "POST /invite", Resource: audit.Resource{Kind: audit.KindPerson, ID: "ben@example.com", Name: "ben@example.com"}}},
		{6 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionDeployed, Operation: "POST /apps/shop--production--web/deployments", Resource: appRes("production", "web"), Record: &audit.Record{Kind: audit.RecordDeployment, ID: "web-p1"},
			Changes: []audit.Change{{Path: "services[web].image", Op: audit.OpChanged, From: "ghcr.io/acme/web:1.6.4", To: "ghcr.io/acme/web:1.7.0"}}}},
		{4*24*time.Hour + 3*time.Hour, audit.Entry{Actor: ci, Door: audit.DoorAPI, Address: "192.0.2.80", Action: audit.ActionDeployed, Operation: "POST /v1/apps/{id}/deployments", Resource: appRes("production", "web"), Record: &audit.Record{Kind: audit.RecordDeployment, ID: "web-p2"},
			Changes: []audit.Change{{Path: "services[web].image", Op: audit.OpChanged, From: "ghcr.io/acme/web:1.7.0", To: "ghcr.io/acme/web:1.7.1"}}}},
		{3 * 24 * time.Hour, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionPinned, Operation: "POST /apps/shop--production--db/backups/pin", Resource: appRes("production", "db")}},
		{2*24*time.Hour + 5*time.Hour, audit.Entry{Actor: ci, Door: audit.DoorAPI, Address: "192.0.2.80", Action: audit.ActionDeployed, Operation: "POST /v1/apps/{id}/deployments", Resource: appRes("production", "web"), Record: &audit.Record{Kind: audit.RecordDeployment, ID: "web-p3"},
			Changes: []audit.Change{{Path: "services[web].image", Op: audit.OpChanged, From: "ghcr.io/acme/web:1.7.1", To: "ghcr.io/acme/web:1.8.0"}}}},
		{26 * time.Hour, audit.Entry{Actor: ci, Door: audit.DoorAPI, Address: "192.0.2.80", Action: audit.ActionDeployed, Operation: "POST /v1/apps/{id}/deployments", Resource: appRes("production", "web"), Record: &audit.Record{Kind: audit.RecordDeployment, ID: "web-p4"}}},
		{20 * time.Hour, audit.Entry{Actor: audit.Actor{Kind: audit.ActorDeployToken, Token: "api-ci", Email: docsDemoEmail}, Door: audit.DoorAPI, Address: "192.0.2.80", Action: audit.ActionDeployed, Operation: "POST /v1/apps/{id}/deployments", Resource: appRes("production", "api"), Record: &audit.Record{Kind: audit.RecordDeployment, ID: "api-p3"}}},
		{3 * time.Hour, audit.Entry{Actor: ci, Door: audit.DoorAPI, Address: "192.0.2.80", Action: audit.ActionDeployed, Operation: "POST /v1/apps/{id}/deployments", Resource: appRes("production", "web"), Record: &audit.Record{Kind: audit.RecordDeployment, ID: "web-p5"},
			Changes: []audit.Change{{Path: "services[web].image", Op: audit.OpChanged, From: "ghcr.io/acme/web:1.8.0", To: "ghcr.io/acme/web:1.8.2"}}}},
		{50 * time.Minute, audit.Entry{Actor: ana, Door: audit.DoorConsole, Address: "203.0.113.7", Action: audit.ActionSignedIn, Operation: "POST /login", Resource: audit.Resource{Kind: audit.KindAccount, ID: docsDemoEmail, Name: docsDemoEmail}}},
	}
	for _, x := range evs {
		if _, err := d.Store.InsertAuditEvent(ctx, d.ago(x.ago), x.e); err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
	}
}

// ---- Domains -------------------------------------------------------------------------------

// seedDocsDomains stores the last check of each address as the domain watcher leaves it: DNS points
// at the server and Let's Encrypt has issued a certificate. The watcher itself never runs.
func (d *docsDemo) seedDocsDomains(t *testing.T) {
	t.Helper()
	here := []string{"203.0.113.4"}
	for i, h := range []struct{ host, app, service string }{
		{"shop.example.com", shopID("production", "web"), "web"},
		{"api.shop.example.com", shopID("production", "api"), "api"},
		{"staging.shop.example.com", shopID("staging", "web"), "web"},
		{"pr-31.shop.example.com", shopID("pr-31", "web"), "web"},
	} {
		rec := store.DomainCheckRecord{Host: h.host, AppID: h.app, Service: h.service, State: string(edge.StateUnknown),
			DNSStatus: edge.DNSOK, DNSSeen: here, DNSExpected: here, CertStatus: edge.CertIssued, CertIssuer: "Let's Encrypt",
			CertFingerprint: fmt.Sprintf("%064x", 0xd0ca1+i), CertNotAfter: new(d.now.Add(time.Duration(52+9*i) * 24 * time.Hour)), CheckedAt: d.ago(4 * time.Minute)}
		if err := d.Store.UpsertDomainCheck(context.Background(), rec); err != nil {
			t.Fatalf("UpsertDomainCheck: %v", err)
		}
	}
}

// ---- A preview -----------------------------------------------------------------------------

func (d *docsDemo) seedDocsPreview(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.Svc.CreateEnvironment(ctx, "shop", "pr-31"); err != nil {
		t.Fatalf("CreateEnvironment pr-31: %v", err)
	}
	a := app.App{ID: shopID("pr-31", "web"), Name: "web", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "web", Image: "ghcr.io/acme/web:pr-31", Ports: []app.Port{{Name: "http", ContainerPort: 8080}},
		Domains: []app.Domain{{Host: "pr-31.shop.example.com", Port: "http"}}, Health: &app.HealthCheck{Path: "/healthz"},
		Env: []app.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}, Resources: app.Resources{MemoryMB: 256, MemoryMaxMB: 512},
	}}}
	d.history(t, a, demoDeploy{ID: "web-pr31", Ago: 40 * time.Minute, Took: 36 * time.Second, Trigger: app.TriggerManual, Token: "shop-ci", Actor: docsDemoEmail, Status: store.StatusHealthy, Version: 1,
		Origin: &app.Origin{Commit: "5d1e8c2", Ref: "refs/pull/31/merge", Actor: "ana", Repository: "acme/shop", Run: "https://github.com/acme/shop/actions/runs/8123456789"}})
	d.runningCopies(a.ID, "web", 1, 1, 40*time.Minute, 22, 88, 2)
}

// ---- A week of monitoring ------------------------------------------------------------------

// dayFactor is how busy the hour is, 0.35 at 03:00 UTC to 1 at 15:00 UTC.
func dayFactor(ts time.Time) float64 {
	h := float64(ts.UTC().Hour()) + float64(ts.UTC().Minute())/60
	return 0.35 + 0.65*(0.5-0.5*math.Cos(2*math.Pi*(h-3)/24))
}

func spreadBuckets(n int64, fractions ...float64) string {
	b := make(metrics.Buckets, len(fractions))
	var given int64
	largest := 0
	for i, f := range fractions {
		b[i] = int64(math.Floor(float64(n) * f))
		given += b[i]
		if f > fractions[largest] {
			largest = i
		}
	}
	b[largest] += n - given
	return b.String()
}

// seedDocsMetrics writes a week of history for each service with an address, as the sampler would
// have, and moves the start of the baseline back so Monitoring has "the usual hours" to compare.
// It must run before the service starts, which writes the baseline's start itself.
func (d *docsDemo) seedDocsMetrics(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	layout, err := d.Store.EnsureLayout(ctx, "0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10")
	if err != nil {
		t.Fatal(err)
	}
	until := d.now.Truncate(time.Minute)
	from := d.now.Add(-7 * 24 * time.Hour).Truncate(10 * time.Minute)
	minutesFrom := d.now.Add(-store.MinuteKeep).Truncate(time.Minute)

	write := func(res int, from, until time.Time, shape func(ts time.Time, mins int) store.MetricsSample, series int64) {
		step := time.Duration(res) * time.Second
		var rows store.MetricsRows
		for ts := from.Truncate(step); ts.Before(until); ts = ts.Add(step) {
			m := shape(ts, res/60)
			m.Series, m.TS, m.Minutes = series, ts, res/60
			rows.Samples = append(rows.Samples, m)
		}
		tick := store.MetricsTick{}
		if res == store.SlotRes {
			tick.Fold = rows
		} else {
			tick.Minute = rows
		}
		if err := d.Store.WriteMetricsTick(ctx, tick); err != nil {
			t.Fatalf("seed rows: %v", err)
		}
	}
	for _, s := range []struct {
		app, svc string
		base     float64 // requests a minute at the day's peak
		mem      float64
		copies   int64
		limit    int64
		reserved int64 // the memory a service with no address reserves
	}{
		{shopID("production", "web"), "web", 95, 96, 2, 512, 0},
		{shopID("production", "api"), "api", 60, 118, 2, 512, 0},
		{shopID("staging", "web"), "web", 6, 74, 1, 512, 0},
		{shopID("production", "db"), "db", 0, 196, 1, 1024, 512},
		{shopID("production", "search"), "search", 0, 590, 1, 1024, 768},
	} {
		series, err := d.Store.EnsureSeries(ctx, s.app, s.svc)
		if err != nil {
			t.Fatal(err)
		}
		rng := rngFor(s.app)
		shape := func(ts time.Time, mins int) store.MetricsSample {
			day := dayFactor(ts)
			noise := 0.9 + 0.2*rng.Float64()
			if s.base == 0 { // a service with no address has no request numbers
				return store.MetricsSample{
					CPUMHz: new(int64(math.Round((12 + 20*day) * noise))), CPUReservedMHz: new(int64(100)),
					MemoryMB: new(int64(math.Round(s.mem + 18*day))), MemoryLimitMB: new(s.limit),
					MemoryReservedMB: new(s.reserved), Copies: &s.copies, Restarts: new(int64(0)),
				}
			}
			reqs := int64(math.Round(s.base * day * noise * float64(mins)))
			errs := int64(math.Round(float64(reqs) * 0.002 * 2 * rng.Float64()))
			secs := int64(mins * 60)
			up := 0.04 * day
			buckets := spreadBuckets(reqs, 0.05, 0.15, 0.35, 0.25, 0.13-up, 0.06+up, 0.01)
			return store.MetricsSample{
				HTTPSecs: &secs, Requests: &reqs, Errors: &errs, Layout: &layout, Buckets: &buckets,
				CPUMHz:           new(int64(math.Round((15 + 70*day) * noise * float64(s.copies)))),
				CPUReservedMHz:   new(100 * s.copies),
				MemoryMB:         new(int64(math.Round((s.mem + 22*day) * float64(s.copies)))),
				MemoryLimitMB:    new(s.limit * s.copies),
				MemoryReservedMB: new(int64(256) * s.copies),
				Copies:           &s.copies,
				Restarts:         new(int64(0)),
			}
		}
		write(store.SlotRes, from, until.Truncate(10*time.Minute), shape, series)
		write(store.MinuteRes, minutesFrom, until, shape, series)
	}
	// The baseline began eight days ago.
	if err := d.Store.WriteMetricsTick(ctx, store.MetricsTick{At: d.now.Add(-8 * 24 * time.Hour)}); err != nil {
		t.Fatalf("start of the baseline: %v", err)
	}
}

// ---- The servers ---------------------------------------------------------------------------

// docsHostRoot is a copy of hostos's "ubuntu-clean" fixture (an Ubuntu host up to date, automatic
// updates on) with the file times set, since git keeps none.
func docsHostRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "hostos", "testdata", "ubuntu-clean"))); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"var/lib/update-notifier/updates-available", "run/reboot-required"} {
		path := filepath.Join(root, rel)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		at := time.Now().Add(-3 * time.Hour)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// demoNodes are the servers: Nomad's node ID and name for each index.
var demoNodes = []struct{ id, name string }{{"node-1", "hel-1"}, {"node-2", "hel-2"}, {"node-3", "nbg-1"}}

// seedDocsServers joins two more servers to the first one and keeps their samples fresh.
func (d *docsDemo) seedDocsServers(t *testing.T) {
	t.Helper()
	_, hel2 := d.readyServer(t, "hel-2")
	_, nbg1 := d.readyServer(t, "nbg-1")
	// The agents of both are promoted to Nomad servers one at a time: each reports its new role.
	for _, n := range []struct {
		token  string
		others []int
	}{{hel2.NodeToken, []int{1}}, {nbg1.NodeToken, []int{1, 2}}} {
		report := healthy(n.others...)
		report.Role = "server"
		if _, err := d.Svc.NodeSync(context.Background(), n.token, report); err != nil {
			t.Fatalf("NodeSync as a server: %v", err)
		}
	}
	samples := func() error {
		for _, n := range []struct {
			id                                                   string
			memUsed, memReserved, cpuUsed, cpuReserved, diskUsed int64
		}{{"node-2", 1100, 640, 180, 0, 21000}, {"node-3", 760, 384, 95, 0, 14000}} {
			if err := d.writeSample(n.id, n.memUsed, n.memReserved, 3916, n.cpuUsed, 4800, 2, n.diskUsed, 76000); err != nil {
				return err
			}
		}
		return nil
	}
	if err := samples(); err != nil {
		t.Fatal(err)
	}
	// A sample older than five minutes isn't judged, so the demo writes them again as the sampler would.
	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-t.Context().Done():
				return
			case <-tick.C:
				_ = samples()
			}
		}
	}()
}

// docsRelease is a stand-in for GitHub's releases API that says 0.25.0 is out.
func docsRelease(t *testing.T) *fakeRelease {
	g := newFakeRelease(t)
	g.tag = "v0.25.0"
	g.body = "Highlights\n\n- Console address changes without SSH.\n- Faster log search.\n"
	return g
}

// fakeTraefikMetrics serves Traefik's Prometheus text for the shop's three addresses, with counters
// that grow with the clock at the rate of the seeded history, so the sampler's reading of the
// source is "ok" and the numbers it computes agree with the week before.
func fakeTraefikMetrics(t *testing.T) *httptest.Server {
	t.Helper()
	started := time.Now()
	bounds := []string{"0.005", "0.01", "0.025", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "10", "+Inf"}
	// The share of requests at or under each bound, as the seeded rows spread them.
	cum := []float64{0.05, 0.20, 0.55, 0.80, 0.93, 0.995, 1, 1, 1, 1, 1, 1}
	rates := map[string]float64{"shop--production--web-web": 1.6, "shop--production--api-api": 1.0, "shop--staging--web-web": 0.1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		fmt.Fprintf(&b, "process_start_time_seconds %d\n", started.Unix())
		el := time.Since(started).Seconds() + 100
		for svc, rate := range rates {
			n := rate * el
			fmt.Fprintf(&b, "traefik_service_requests_total{code=\"200\",method=\"GET\",protocol=\"http\",service=\"%s@nomad\"} %d\n", svc, int64(n))
			for i, le := range bounds {
				fmt.Fprintf(&b, "traefik_service_request_duration_seconds_bucket{code=\"200\",method=\"GET\",protocol=\"http\",service=\"%s@nomad\",le=\"%s\"} %d\n", svc, le, int64(n*cum[i]))
			}
		}
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ---- The image scanner ---------------------------------------------------------------------

// seedDocsScanner installs the image scanner and stores the result of the last scan of every image the
// apps run, as the daily pass would have left them, without running one. The notify level is "high",
// so both the critical and the high findings with a fix reach the needs-you list. api has two critical
// packages, web in production three high ones (one known exploited); every other image is clean. The
// images are the demo's own (ghcr.io/acme/...), so the findings are as made up as the images.
// It must run before the service starts. The daily pass is marked as done for today so it never runs.
func (d *docsDemo) seedDocsScanner(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := d.Store.SaveAddon(ctx, store.Addon{Name: store.AddonImageScanner, State: store.AddonInstalled, Version: "0.117.0",
		InstalledAt: d.ago(9 * 24 * time.Hour), UpdatedAt: d.ago(9 * 24 * time.Hour)}); err != nil {
		t.Fatalf("SaveAddon: %v", err)
	}
	if err := d.Store.SaveScanSettings(ctx, store.ScanSettings{NotifyLevel: "high", DailyHour: 4}); err != nil {
		t.Fatalf("SaveScanSettings: %v", err)
	}
	if err := d.Store.SetLastDaily(ctx, d.now); err != nil {
		t.Fatalf("SetLastDaily: %v", err)
	}

	webwebp := secFindingFor("a1f3c5e7b9d20416", "CVE-2023-4863", vulns.High, "libwebp", "1.3.1-r0", "1.3.2-r0")
	webwebp.KEV = true
	webwebp.EPSS = &vulns.EPSS{Score: 0.94, Percentile: 0.99}
	findings := map[string][]vulns.Finding{
		shopID("production", "api"): {
			secFindingFor("b2e4d6f8a0c31527", "CVE-2022-37434", vulns.Critical, "zlib", "1.2.11-r3", "1.2.12-r0"),
			secFindingFor("c3f5e7a9b1d42638", "CVE-2022-32221", vulns.Critical, "curl", "7.79.1-r5", "7.86.0-r0"),
		},
		shopID("production", "web"): {
			webwebp,
			secFindingFor("d40618bac2e53749", "CVE-2022-0778", vulns.High, "openssl", "3.0.1-r0", "3.0.2-r0"),
			secFindingFor("e51729cbd3f6485a", "CVE-2022-40303", vulns.High, "libxml2", "2.9.14-r0", "2.10.3-r0"),
		},
	}

	apps, err := d.Store.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	scannedAt, built := lastAt(d.now, 4).Add(7*time.Second), lastAt(d.now, 3)
	n := 0
	for _, rec := range apps {
		dep, err := d.Store.LatestHealthyDeployment(ctx, rec.App.ID)
		if err != nil {
			continue // a preview or an app with no healthy deployment has no live image
		}
		for _, svc := range slices.Sorted(maps.Keys(dep.Deployment.Digests)) {
			image := dep.Deployment.Digests[svc]
			res := vulns.Result{Findings: findings[rec.App.ID], DBBuilt: built, DBSchema: "6", Scanner: "0.117.0"}
			if err := d.Store.SaveScanResult(ctx, image, res, true, scannedAt.Add(time.Duration(n)*45*time.Second)); err != nil {
				t.Fatalf("SaveScanResult(%s): %v", image, err)
			}
			run := store.ScanRun{ID: fmt.Sprintf("run_demo%02d", n), Image: image, Trigger: store.ScanTriggerDaily, Source: store.ScanSourceSBOM,
				CreatedAt: scannedAt.Add(time.Duration(n)*45*time.Second - 20*time.Second)}
			if _, _, err := d.Store.QueueScanRun(ctx, run); err != nil {
				t.Fatalf("QueueScanRun: %v", err)
			}
			if err := d.Store.StartScanRun(ctx, run.ID, store.ScanSourceSBOM, run.CreatedAt.Add(2*time.Second)); err != nil {
				t.Fatalf("StartScanRun: %v", err)
			}
			if err := d.Store.EndScanRun(ctx, run.ID, store.ScanOK, "", "0.117.0", built, run.CreatedAt.Add(time.Duration(14+n%5)*time.Second)); err != nil {
				t.Fatalf("EndScanRun: %v", err)
			}
			n++
		}
	}
}
