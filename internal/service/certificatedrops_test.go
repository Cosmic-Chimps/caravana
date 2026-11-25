package service

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	sentinelDropPEM = "-----BEGIN CERTIFICATE-----\nMIIBs3cretSentinel056"
)

// dropReport is a report of a first server whose agent holds Traefik's store.
func dropReport(o *NodeCertificateOutcome) NodeReport {
	r := healthyReport()
	r.TraefikStore = true
	r.CertificateDrop = o
	return r
}

func outcome(id, state string, dropped ...string) *NodeCertificateOutcome {
	return &NodeCertificateOutcome{ID: id, State: state, Dropped: dropped}
}

// addUpload stores an uploaded certificate row with these names: the drop reads names only.
func addUpload(t *testing.T, e *clusterEnv, id string, names ...string) {
	t.Helper()
	now := e.svc.clock.Now().UTC()
	err := e.store.InsertUploadedCertificate(context.Background(), store.UploadedCertificate{
		ID: id, Names: names, Subject: "CN=" + names[0], Issuer: "CN=Test", NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour),
		Fingerprint: strings.Repeat("a", 64), KeyType: "ECDSA P-256", Trusted: true, UploadedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// dropEnv is a cluster whose first server's agent reports Traefik's store, with one upload.
func dropEnv(t *testing.T) *clusterEnv {
	t.Helper()
	e := newClusterEnv(t)
	e.svc.SetCertificateDropPassGap(time.Hour, 2*time.Millisecond)
	addUpload(t, e, "crt_000000000001", "*.example.com", "a.example.org")
	e.firstSync(t, dropReport(nil))
	return e
}

func (e *clusterEnv) dropRow(t *testing.T, id string) store.CertificateDrop {
	t.Helper()
	d, err := e.store.GetCertificateDrop(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *clusterEnv) requestDrop(t *testing.T) store.CertificateDrop {
	t.Helper()
	d, err := e.svc.RequestCertificateDrop(context.Background())
	if err != nil {
		t.Fatalf("RequestCertificateDrop: %v", err)
	}
	return d
}

// traefikAt makes Traefik a running copy whose task started at start.
func (e *clusterEnv) traefikAt(start time.Time) {
	e.nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	stub := copyStub("tr1", "traefik", "running", start)
	stub.CreateTime = start.UnixNano()
	e.nomad.SetCopies("traefik", stub)
}

func TestValidateCertificateOutcome(t *testing.T) {
	id := strings.Repeat("a", 32)
	tests := []struct {
		name   string
		o      *NodeCertificateOutcome
		field  string // "" means accepted
		mutate func(o *NodeCertificateOutcome)
	}{
		{name: "none"},
		{name: "running", o: outcome(id, "running")},
		{name: "dropped, joined names", o: outcome(id, "dropped", "a.example.com", "b.example.com+www.example.com", "*.c.example.com")},
		{name: "dropped, no names", o: outcome(id, "dropped")},
		{name: "nothing", o: outcome(id, "nothing")},
		{name: "failed with a reason", o: &NodeCertificateOutcome{ID: id, State: "failed", Reason: "acme.json isn't a Traefik certificate store."}},
		{name: "a short id", o: outcome(strings.Repeat("a", 31), "dropped"), field: "certificateDrop.id"},
		{name: "an upper case id", o: outcome(strings.Repeat("A", 32), "dropped"), field: "certificateDrop.id"},
		{name: "an unknown state", o: outcome(id, "done"), field: "certificateDrop.state"},
		{name: "an empty state", o: outcome(id, ""), field: "certificateDrop.state"},
		{name: "101 entries", o: outcome(id, "dropped", slicesOf("a.example.com", 101)...), field: "certificateDrop.dropped"},
		{name: "100 entries", o: outcome(id, "dropped", slicesOf("a.example.com", 100)...)},
		{name: "an entry that isn't a host name", o: outcome(id, "dropped", "a.example.com", "not a host"), field: "certificateDrop.dropped[1]"},
		{name: "an entry with an empty part", o: outcome(id, "dropped", "a.example.com+"), field: "certificateDrop.dropped[0]"},
		{name: "a PEM as an entry", o: outcome(id, "dropped", sentinelDropPEM), field: "certificateDrop.dropped[0]"},
		{name: "an upper case entry", o: outcome(id, "dropped", "A.example.com"), field: "certificateDrop.dropped[0]"},
		{name: "a very long entry", o: outcome(id, "dropped", strings.Repeat("a.example.com+", 400)+"a.example.com"), field: "certificateDrop.dropped[0]"},
		{name: "a reason of 300", o: &NodeCertificateOutcome{ID: id, State: "failed", Reason: strings.Repeat("r", 300)}},
		{name: "a reason of 301", o: &NodeCertificateOutcome{ID: id, State: "failed", Reason: strings.Repeat("r", 301)}, field: "certificateDrop.reason"},
		{name: "a reason with a line break", o: &NodeCertificateOutcome{ID: id, State: "failed", Reason: sentinelDropPEM}, field: "certificateDrop.reason"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCertificateOutcome(tt.o)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("refused a valid block: %v", err)
				}
				return
			}
			if got := unreadable(t, err); got != tt.field {
				t.Errorf("field = %q, want %q (%v)", got, tt.field, err)
			}
			if err != nil && strings.Contains(err.Error(), "MIIBs3cret") {
				t.Errorf("the refusal quotes the block: %v", err)
			}
		})
	}
}

func TestRequestCertificateDropRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("no uploaded certificate", func(t *testing.T) {
		e := newClusterEnv(t)
		e.firstSync(t, dropReport(nil))
		if _, err := e.svc.RequestCertificateDrop(ctx); !errors.Is(err, ErrNoUploadedCertificates) {
			t.Errorf("err = %v, want ErrNoUploadedCertificates", err)
		}
	})
	t.Run("uploads with no name the agent accepts", func(t *testing.T) {
		e := newClusterEnv(t)
		addUpload(t, e, "crt_000000000001", "a_b.example.com", "localhost", "A B")
		e.firstSync(t, dropReport(nil))
		if _, err := e.svc.RequestCertificateDrop(ctx); !errors.Is(err, ErrNoUploadedCertificates) {
			t.Errorf("err = %v, want ErrNoUploadedCertificates", err)
		}
	})
	t.Run("a node with no row (the lab)", func(t *testing.T) {
		e := newEnv(t)
		ce := &clusterEnv{env: e}
		addUpload(t, ce, "crt_000000000001", "a.example.com")
		_, err := e.svc.RequestCertificateDrop(ctx)
		var na *DropNoAgentError
		if !errors.Is(err, ErrNoAgent) || !errors.As(err, &na) || na.Server != "" || !strings.Contains(err.Error(), "run the installer on the server") {
			t.Errorf("err = %v, want ErrNoAgent without a server name", err)
		}
	})
	t.Run("the agent never reported", func(t *testing.T) {
		e := newClusterEnv(t)
		addUpload(t, e, "crt_000000000001", "a.example.com")
		_, err := e.svc.RequestCertificateDrop(ctx)
		var na *DropNoAgentError
		if !errors.Is(err, ErrNoAgent) || !errors.As(err, &na) || na.Server != firstServerName || !strings.Contains(err.Error(), "run the installer on server "+firstServerName) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an agent that doesn't say it holds the store", func(t *testing.T) {
		e := newClusterEnv(t)
		addUpload(t, e, "crt_000000000001", "a.example.com")
		e.firstSync(t, healthyReport()) // an agent older than the feature: no traefikStore
		if _, err := e.svc.RequestCertificateDrop(ctx); !errors.Is(err, ErrNoAgent) {
			t.Errorf("err = %v, want ErrNoAgent", err)
		}
	})
	t.Run("a report that is too old", func(t *testing.T) {
		e := dropEnv(t)
		e.setClock(e.svc.clock.Now().Add(11 * time.Minute))
		if _, err := e.svc.RequestCertificateDrop(ctx); !errors.Is(err, ErrNoAgent) {
			t.Errorf("err = %v, want ErrNoAgent", err)
		}
	})
	t.Run("only an admin", func(t *testing.T) {
		e := dropEnv(t)
		member := WithPrincipal(ctx, Principal{PersonID: "p", Email: "m@example.com", OrgRole: OrgMember})
		var na *NotAllowedError
		if _, err := e.svc.RequestCertificateDrop(member); !errors.As(err, &na) {
			t.Errorf("a member's request = %v, want *NotAllowedError", err)
		}
		if _, err := e.svc.CertificateDrops(member); !errors.As(err, &na) {
			t.Errorf("a member's list = %v", err)
		}
		if _, err := e.svc.CertificateDrop(member, strings.Repeat("a", 32)); !errors.As(err, &na) {
			t.Errorf("a member's read = %v", err)
		}
		if list, _ := e.store.ListCertificateDrops(ctx, 5); len(list) != 0 {
			t.Error("a refused request left a drop")
		}
	})
	t.Run("requested, once, with the actor", func(t *testing.T) {
		e := dropEnv(t)
		admin := WithPrincipal(ctx, Principal{PersonID: "p", Email: "ana@example.com", OrgRole: OrgAdmin})
		d, err := e.svc.RequestCertificateDrop(admin)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.ID) != 32 || d.Status != store.DropRequested || d.ServerName != firstServerName || d.Actor != "ana@example.com" || d.Dropped == nil {
			t.Errorf("drop = %+v", d)
		}
		if _, err := e.svc.RequestCertificateDrop(ctx); !errors.Is(err, ErrCertificateDropRunning) {
			t.Errorf("a second request = %v, want ErrCertificateDropRunning", err)
		}
		got, err := e.svc.CertificateDrop(admin, d.ID)
		if err != nil || got.ID != d.ID {
			t.Errorf("read = %+v, %v", got, err)
		}
		var re *RefusalError
		if _, err := e.svc.CertificateDrop(admin, strings.Repeat("0", 32)); !errors.Is(err, ErrCertificateDropNotFound) || !errors.As(err, &re) || re.Field != "id" {
			t.Errorf("an unknown ID = %v", err)
		}
		if list, err := e.svc.CertificateDrops(admin); err != nil || len(list) != 1 {
			t.Errorf("list = %v, %v", list, err)
		}
	})
}

func TestCertificateDropHandoff(t *testing.T) {
	ctx := context.Background()
	e := dropEnv(t)
	// A name that is a duplicate in another upload, an upper case one and one the agent wouldn't take.
	addUpload(t, e, "crt_000000000002", "A.Example.org", "b.example.net", "10.0.0.1x_", "127.0.0.1")

	if ans := e.firstSync(t, dropReport(nil)); ans.CertificateDrop != nil {
		t.Fatalf("a block with no drop: %+v", ans.CertificateDrop)
	}
	var d store.CertificateDrop
	got := notedOne(t, e.store, func() { d = e.requestDrop(t) })
	wantEntry(t, got, audit.ActionRequested, audit.KindSettings, "certificates")
	if got.Record == nil || got.Record.Kind != audit.RecordCertificateDrop || got.Record.ID != d.ID {
		t.Errorf("the drop's entry links %+v, want the drop %s", got.Record, d.ID)
	}

	// Another server's sync never carries it, whatever its report says.
	_, joined := e.join(t, "two", "fsn-2.example.com")
	if ans, err := e.svc.NodeSync(ctx, joined.NodeToken, healthyReport(1)); err != nil || ans.CertificateDrop != nil {
		t.Fatalf("a client's sync: %+v, %v", ans.CertificateDrop, err)
	}
	if got := e.dropRow(t, d.ID); got.Status != store.DropRequested {
		t.Fatalf("another server's sync moved the drop: %+v", got)
	}

	// The ingress server's does, with the sorted names, and marks it running from that moment.
	e.setClock(e.svc.clock.Now().Add(30 * time.Second))
	handed := e.svc.clock.Now().UTC()
	ans := e.firstSync(t, dropReport(nil))
	want := &NodeCertificateDrop{ID: d.ID, Names: []string{"*.example.com", "127.0.0.1", "a.example.org", "b.example.net"}}
	if !reflect.DeepEqual(ans.CertificateDrop, want) {
		t.Fatalf("answer = %+v, want %+v", ans.CertificateDrop, want)
	}
	if got := e.dropRow(t, d.ID); got.Status != store.DropRunning || !got.StartedAt.Equal(handed) {
		t.Errorf("after the hand-out = %+v", got)
	}
	// Again while the agent hasn't ended it: handed out again, the start doesn't move; a report that says it
	// is running, or another ID's outcome, settles nothing.
	e.setClock(e.svc.clock.Now().Add(30 * time.Second))
	for _, r := range []NodeReport{dropReport(nil), dropReport(outcome(d.ID, "running")), dropReport(outcome(strings.Repeat("0", 32), "dropped", "a.example.com"))} {
		if ans := e.firstSync(t, r); ans.CertificateDrop == nil || ans.CertificateDrop.ID != d.ID {
			t.Errorf("hand-out for %+v = %+v", r.CertificateDrop, ans.CertificateDrop)
		}
	}
	if got := e.dropRow(t, d.ID); got.Status != store.DropRunning || !got.StartedAt.Equal(handed) {
		t.Errorf("after the repeats = %+v", got)
	}
}

func TestCertificateDropHandoffWithNoUploadsLeft(t *testing.T) {
	ctx := context.Background()
	e := dropEnv(t)
	d := e.requestDrop(t)
	if err := e.store.DeleteUploadedCertificate(ctx, "crt_000000000001"); err != nil {
		t.Fatal(err)
	}
	if ans := e.firstSync(t, dropReport(nil)); ans.CertificateDrop != nil {
		t.Errorf("a drop with no names was handed out: %+v", ans.CertificateDrop)
	}
	if got := e.dropRow(t, d.ID); got.Status != store.DropFailed || got.Error != reasonDropNoUploads {
		t.Errorf("drop = %+v", got)
	}
}

// Each final outcome the agent reports settles the open drop.
func TestCertificateDropSettlesFromTheReport(t *testing.T) {
	tests := []struct {
		name    string
		o       func(id string) *NodeCertificateOutcome
		status  string
		errText string
	}{
		{"nothing", func(id string) *NodeCertificateOutcome { return outcome(id, "nothing") }, store.DropDone, ""},
		{"failed, with the agent's sentence", func(id string) *NodeCertificateOutcome {
			return &NodeCertificateOutcome{ID: id, State: "failed", Reason: "Traefik's certificate store couldn't be changed: acme.json couldn't be written."}
		}, store.DropFailed, "Traefik's certificate store couldn't be changed: acme.json couldn't be written."},
		{"failed, no sentence", func(id string) *NodeCertificateOutcome { return outcome(id, "failed") }, store.DropFailed, reasonDropAgentFailed},
		{"refused, with the agent's sentence", func(id string) *NodeCertificateOutcome {
			return &NodeCertificateOutcome{ID: id, State: "refused", Reason: "This server doesn't hold Traefik's certificate store."}
		}, store.DropFailed, "This server doesn't hold Traefik's certificate store."},
		{"refused, no sentence", func(id string) *NodeCertificateOutcome { return outcome(id, "refused") }, store.DropFailed, reasonDropRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := dropEnv(t)
			d := e.requestDrop(t)
			e.firstSync(t, dropReport(nil)) // handed out
			e.setClock(e.svc.clock.Now().Add(time.Minute))
			at := e.svc.clock.Now().UTC()
			if ans := e.firstSync(t, dropReport(tt.o(d.ID))); ans.CertificateDrop != nil {
				t.Errorf("a settled drop was handed out again: %+v", ans.CertificateDrop)
			}
			got := e.dropRow(t, d.ID)
			if got.Status != tt.status || got.Error != tt.errText || !got.FinishedAt.Equal(at) || len(got.Dropped) != 0 {
				t.Errorf("settled = %+v", got)
			}
			// The report repeats at every sync; the drop stays as it is and a new one may be asked for.
			e.setClock(e.svc.clock.Now().Add(time.Minute))
			e.firstSync(t, dropReport(tt.o(d.ID)))
			if again := e.dropRow(t, d.ID); !again.FinishedAt.Equal(at) {
				t.Errorf("a repeated report changed the end: %+v", again)
			}
			if _, err := e.svc.RequestCertificateDrop(context.Background()); err != nil {
				t.Errorf("a new request after it ended: %v", err)
			}
		})
	}
}

// A dropped outcome restarts Traefik once, in the background, and the drop is done when Traefik's task
// has started again.
func TestCertificateDropRestartsTraefikAndEndsDone(t *testing.T) {
	e := dropEnv(t)
	rewrittenAt := e.svc.clock.Now().UTC()
	e.traefikAt(rewrittenAt.Add(-time.Hour)) // started long before the rewrite
	d := e.requestDrop(t)
	e.firstSync(t, dropReport(nil))
	// The restart moves the task's start: the fake only records events, so the hook plays Nomad.
	e.nomad.OnRestart(func(copyID string) {
		stub := copyStub(copyID, "traefik", "running", rewrittenAt.Add(time.Minute))
		stub.CreateTime = rewrittenAt.Add(-time.Hour).UnixNano()
		e.nomad.SetCopies("traefik", stub)
	})
	e.setClock(rewrittenAt.Add(time.Second))
	rewritten := e.svc.clock.Now().UTC()
	e.firstSync(t, dropReport(outcome(d.ID, "dropped", "a.example.com", "b.example.com+www.example.com")))

	got := e.dropRow(t, d.ID)
	if got.Status != store.DropRestarting && got.Status != store.DropDone {
		t.Fatalf("after the report = %+v", got)
	}
	waitFor(t, "the drop to be done", func() bool { return e.dropRow(t, d.ID).Status == store.DropDone })
	got = e.dropRow(t, d.ID)
	if !got.RewrittenAt.Equal(rewritten) || !reflect.DeepEqual(got.Dropped, []string{"a.example.com", "b.example.com+www.example.com"}) || got.FinishedAt.IsZero() || got.Error != "" {
		t.Errorf("done = %+v", got)
	}
	if rs := e.nomad.Restarts(); len(rs) != 1 || rs[0].Copy != "tr1" || !rs[0].AllTasks {
		t.Errorf("restarts = %+v, want one of every task of Traefik's copy", rs)
	}
	// The report repeating the outcome neither restarts again nor changes the drop.
	e.firstSync(t, dropReport(outcome(d.ID, "dropped", "a.example.com", "b.example.com+www.example.com")))
	time.Sleep(20 * time.Millisecond)
	if rs := e.nomad.Restarts(); len(rs) != 1 {
		t.Errorf("a repeated report restarted Traefik again: %+v", rs)
	}
}

// An agent that dropped nothing because a run it found `running` finished before says dropped with no
// names: Traefik is restarted all the same.
func TestCertificateDropWithNoNamesStillRestarts(t *testing.T) {
	e := dropEnv(t)
	now := e.svc.clock.Now().UTC()
	e.traefikAt(now.Add(-time.Hour))
	d := e.requestDrop(t)
	e.firstSync(t, dropReport(nil))
	e.nomad.OnRestart(func(copyID string) {
		stub := copyStub(copyID, "traefik", "running", now.Add(time.Minute))
		e.nomad.SetCopies("traefik", stub)
	})
	e.firstSync(t, dropReport(outcome(d.ID, "dropped")))
	waitFor(t, "the drop to be done", func() bool { return e.dropRow(t, d.ID).Status == store.DropDone })
	if got := e.dropRow(t, d.ID); got.Dropped == nil || len(got.Dropped) != 0 {
		t.Errorf("dropped = %#v, want []", got.Dropped)
	}
	if len(e.nomad.Restarts()) != 1 {
		t.Errorf("restarts = %+v", e.nomad.Restarts())
	}
}

// Traefik that started after the rewrite already (someone restarted it) isn't restarted again.
func TestCertificateDropDoneWhenTraefikStartedAlready(t *testing.T) {
	e := dropEnv(t)
	now := e.svc.clock.Now().UTC()
	e.traefikAt(now.Add(time.Hour))
	d := e.requestDrop(t)
	e.firstSync(t, dropReport(nil))
	e.firstSync(t, dropReport(outcome(d.ID, "dropped", "a.example.com")))
	waitFor(t, "the drop to be done", func() bool { return e.dropRow(t, d.ID).Status == store.DropDone })
	if rs := e.nomad.Restarts(); len(rs) != 0 {
		t.Errorf("Traefik was restarted although it had started since: %+v", rs)
	}
}

// A restart Nomad refuses leaves the drop restarting, the pass tries again, and after ten minutes it fails
// with the sentence that says what the owner is left with.
func TestCertificateDropWhenTraefikCannotBeRestarted(t *testing.T) {
	ctx := context.Background()
	e := dropEnv(t)
	now := e.svc.clock.Now().UTC()
	e.traefikAt(now.Add(-time.Hour))
	e.nomad.FailRestart(403)
	d := e.requestDrop(t)
	e.firstSync(t, dropReport(nil))
	e.firstSync(t, dropReport(outcome(d.ID, "dropped", "a.example.com")))
	time.Sleep(30 * time.Millisecond)
	if got := e.dropRow(t, d.ID); got.Status != store.DropRestarting {
		t.Fatalf("a refused restart: %+v", got)
	}

	e.setClock(now.Add(5 * time.Minute))
	e.svc.certificateDropPass(ctx)
	if got := e.dropRow(t, d.ID); got.Status != store.DropRestarting {
		t.Errorf("after 5 minutes = %+v", got)
	}
	// Nomad lets the restart through now: the pass drives it again.
	e.nomad.FailRestart(0)
	e.nomad.OnRestart(func(copyID string) {
		e.nomad.SetCopies("traefik", copyStub(copyID, "traefik", "running", now.Add(10*time.Minute)))
	})
	e.svc.certificateDropPass(ctx)
	waitFor(t, "the drop to be done", func() bool { return e.dropRow(t, d.ID).Status == store.DropDone })
}

func TestCertificateDropFailsAfterTenMinutesRestarting(t *testing.T) {
	ctx := context.Background()
	e := dropEnv(t)
	now := e.svc.clock.Now().UTC()
	e.traefikAt(now.Add(-time.Hour))
	e.nomad.FailRestart(403)
	d := e.requestDrop(t)
	e.firstSync(t, dropReport(nil))
	e.firstSync(t, dropReport(outcome(d.ID, "dropped", "a.example.com")))
	time.Sleep(30 * time.Millisecond)
	e.setClock(now.Add(10*time.Minute + time.Second))
	e.svc.certificateDropPass(ctx)
	got := e.dropRow(t, d.ID)
	if got.Status != store.DropFailed || got.Error != reasonDropRestart || !strings.Contains(got.Error, "Drop again once Traefik runs.") {
		t.Errorf("drop = %+v", got)
	}
}

func TestCertificateDropPassDeadlines(t *testing.T) {
	ctx := context.Background()
	t.Run("requested, never picked up", func(t *testing.T) {
		e := dropEnv(t)
		d := e.requestDrop(t)
		e.setClock(e.svc.clock.Now().Add(certDropPickupDeadline - time.Second))
		e.svc.certificateDropPass(ctx)
		if got := e.dropRow(t, d.ID); got.Status != store.DropRequested {
			t.Fatalf("before the deadline = %+v", got)
		}
		e.setClock(e.svc.clock.Now().Add(2 * time.Second))
		e.svc.certificateDropPass(ctx)
		if got := e.dropRow(t, d.ID); got.Status != store.DropFailed || got.Error != reasonDropNotPicked || got.FinishedAt.IsZero() {
			t.Errorf("after it = %+v", got)
		}
		e.firstSync(t, dropReport(nil)) // the agent reports again, as the timer makes it
		if _, err := e.svc.RequestCertificateDrop(ctx); err != nil {
			t.Errorf("a new request after it was given up: %v", err)
		}
	})
	t.Run("running, no outcome", func(t *testing.T) {
		e := dropEnv(t)
		d := e.requestDrop(t)
		e.setClock(e.svc.clock.Now().Add(time.Minute))
		e.firstSync(t, dropReport(nil)) // handed out: running from here
		e.setClock(e.svc.clock.Now().Add(certDropRunDeadline - time.Second))
		e.svc.certificateDropPass(ctx)
		if got := e.dropRow(t, d.ID); got.Status != store.DropRunning {
			t.Fatalf("before the deadline = %+v", got)
		}
		e.setClock(e.svc.clock.Now().Add(2 * time.Second))
		e.svc.certificateDropPass(ctx)
		if got := e.dropRow(t, d.ID); got.Status != store.DropLost || got.Error != reasonDropNoOutcome {
			t.Errorf("after it = %+v", got)
		}
	})
	t.Run("restarting and Traefik started since: done", func(t *testing.T) {
		e := dropEnv(t)
		now := e.svc.clock.Now().UTC()
		d := e.requestDrop(t)
		e.firstSync(t, dropReport(nil))
		// No Traefik runs: the drop waits restarting.
		e.firstSync(t, dropReport(outcome(d.ID, "dropped", "a.example.com")))
		time.Sleep(20 * time.Millisecond)
		if got := e.dropRow(t, d.ID); got.Status != store.DropRestarting {
			t.Fatalf("no Traefik: %+v", got)
		}
		e.traefikAt(now.Add(time.Minute))
		e.svc.certificateDropPass(ctx)
		if got := e.dropRow(t, d.ID); got.Status != store.DropDone {
			t.Errorf("after Traefik started = %+v", got)
		}
		if len(e.nomad.Restarts()) != 0 {
			t.Error("the pass restarted a Traefik that had started")
		}
	})
	t.Run("nothing open, nothing to do", func(t *testing.T) {
		e := dropEnv(t)
		e.svc.certificateDropPass(ctx)
	})
}

// What the agent reports never gets into a row, the stored report or the log beyond names and a short
// printable sentence: anything else refuses the sync.
func TestCertificateDropReportNeverBringsAPEMIn(t *testing.T) {
	ctx := context.Background()
	var logs strings.Builder
	e := dropEnv(t)
	e.svc.log = slog.New(slog.NewTextHandler(&logs, nil))
	d := e.requestDrop(t)
	e.firstSync(t, dropReport(nil))

	for name, o := range map[string]*NodeCertificateOutcome{
		"a PEM as the reason":     {ID: d.ID, State: "failed", Reason: sentinelDropPEM},
		"a long reason":           {ID: d.ID, State: "failed", Reason: strings.Repeat("MIIBs3cretSentinel056", 20)},
		"a PEM as a dropped name": {ID: d.ID, State: "dropped", Dropped: []string{sentinelDropPEM}},
		"a key as a dropped name": {ID: d.ID, State: "dropped", Dropped: []string{"a.example.com+MIIBs3cretSentinel056"}},
	} {
		_, err := e.svc.NodeSync(ctx, e.firstToken, dropReport(o))
		_ = unreadable(t, err) // a field error: the sync is refused
		if err != nil && strings.Contains(err.Error(), "MIIBs3cret") {
			t.Errorf("%s: the refusal quotes it: %v", name, err)
		}
		if got := e.dropRow(t, d.ID); got.Status != store.DropRunning {
			t.Errorf("%s: a refused report moved the drop: %+v", name, got)
		}
	}
	row, _ := e.store.ServerByName(ctx, firstServerName)
	list, _ := e.store.ListCertificateDrops(ctx, 5)
	if strings.Contains(row.Report, "MIIBs3cret") || strings.Contains(logs.String(), "MIIBs3cret") || strings.Contains(strings.Join(dropStrings(list), "\n"), "MIIBs3cret") {
		t.Error("a refused report left something in the stored report, the log or a drop")
	}
}

func dropStrings(list []store.CertificateDrop) []string {
	var out []string
	for _, d := range list {
		out = append(out, d.Error, d.Actor, d.ServerName, strings.Join(d.Dropped, ","))
	}
	return out
}
