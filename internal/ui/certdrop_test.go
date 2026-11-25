package ui

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 056: Settings › Certificates drops the earlier certificates an upload covers. The service is
// real; what the agent holds is not in the pages (the record has names and one sentence), so the tests
// search every page and part for the upload's key and for PEM headers all the same.

const (
	dropHost1 = "b.byo.test"
	dropHost2 = "c.byo.test"
)

// dropEnv is a console with an upload of *.byo.test and two hosts of an app that still present an earlier
// certificate. It has no server yet.
func dropEnvUI(t *testing.T) (*consoleEnv, byoCert) {
	t.Helper()
	e := byoEnv(t)
	e.seed(t, domainsApp("shop", "a.byo.test", dropHost1, dropHost2), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	c := newByoCert(t, 61*day, "*.byo.test")
	if resp, _ := e.postForm(t, "/settings/certificates/upload", c.form()); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d", resp.StatusCode)
	}
	list, err := e.Svc.UploadedCertificates(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("uploaded = %v, %v", list, err)
	}
	here := []string{"203.0.113.1"}
	now := time.Now().UTC()
	for host, rec := range map[string]store.DomainCheckRecord{
		"a.byo.test": {CertStatus: edge.CertIssued, CertIssuer: list[0].Issuer, CertFingerprint: list[0].Fingerprint, CertNotAfter: &list[0].NotAfter},
		dropHost1:    {CertStatus: edge.CertIssued, CertIssuer: "Let's Encrypt", CertFingerprint: "aa11", CertNotAfter: new(now.Add(60 * day))},
		dropHost2:    {CertStatus: edge.CertIssued, CertIssuer: "Let's Encrypt", CertFingerprint: "bb22", CertNotAfter: new(now.Add(60 * day))},
	} {
		rec.Host, rec.AppID, rec.Service, rec.State = host, "shop", "web", string(edge.StateUnknown)
		rec.DNSStatus, rec.DNSSeen, rec.DNSExpected, rec.CheckedAt = edge.DNSOK, here, here, now.Add(-time.Minute)
		if err := e.Store.UpsertDomainCheck(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	return e, c
}

// firstServer joins the first server, whose agent says it holds Traefik's store when store is true.
func firstServer(t *testing.T, e *consoleEnv, holdsStore bool) {
	t.Helper()
	ctx := context.Background()
	caCert, caKey, err := clusterca.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(caCert), "key": string(caKey)})
	e.Nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": "c2VjcmV0LXNlbnRpbmVsLWdvc3NpcDMyYnl0ZXMhISE=\n"})
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.Svc.AdoptFirst(ctx, "hel-1", pub, "203.0.113.4")
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.AddNode("node-1", "hel-1", "ready", "10.99.0.1")
	r := service.NodeReport{Agent: "0.18.0", Nomad: "2.0.7", Docker: "27.5.1", DockerRunning: true, DiskFreeMB: 160000,
		DiskTotalMB: 190000, MemoryMB: 3916, Kernel: "6.8.0", Role: "client", Reach: []service.NodeReach{}, TraefikStore: holdsStore}
	if _, err := e.Svc.NodeSync(ctx, token, r); err != nil {
		t.Fatalf("NodeSync: %v", err)
	}
}

const dropButton = "Drop the earlier certificates"

func dropForm(page string) bool {
	return strings.Contains(page, `action="/settings/certificates/drop"`)
}

// noCertificateContent: nothing of a certificate, a key or an account is on a page or in a part. A page
// has the upload form's placeholders ("-----BEGIN CERTIFICATE-----"), so only a part is searched for PEM
// headers; both are searched for the upload's key and for base64 of a DER certificate.
func noCertificateContent(t *testing.T, what, body string, c byoCert) {
	t.Helper()
	noSecrets(t, what, body, c)
	bad := []string{"MIIB"}
	if strings.HasPrefix(what, "the part") {
		bad = append(bad, "-----BEGIN", "PRIVATE KEY")
	}
	for _, b := range bad {
		if strings.Contains(body, b) {
			t.Errorf("%s holds %q", what, b)
		}
	}
}

func TestTheDropButtonIsDrawnOnlyWithAHostOnItsEarlierCertificate(t *testing.T) {
	e, c := dropEnvUI(t)
	page := e.pageOf(t, "/settings/certificates")
	got := text(page)
	for _, want := range []string{dropButton, "Traefik restarts once: a few seconds without HTTPS.",
		"Still on their earlier certificate: " + dropHost1 + ", " + dropHost2 + ".",
		"Traefik keeps a certificate it already has: drop the earlier certificates, below, to serve this one."} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if !dropForm(page) || strings.Contains(got, "run the installer again on this server to drop it") {
		t.Errorf("the form or the old sentence: form %v", dropForm(page))
	}
	if strings.Contains(page, `id="drop"`) {
		t.Error("the panel is drawn with no drop")
	}
	noCertificateContent(t, "the page", page, c)

	// Nothing keeps an earlier certificate: the form isn't there.
	e2 := byoEnv(t)
	e2.seed(t, domainsApp("shop", "a.byo.test"), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	c2 := newByoCert(t, 61*day, "*.byo.test")
	e2.postForm(t, "/settings/certificates/upload", c2.form())
	if page := e2.pageOf(t, "/settings/certificates"); dropForm(page) || strings.Contains(text(page), dropButton) {
		t.Error("the button is drawn although no host is on an earlier certificate")
	}
	// No upload at all.
	if page := byoEnv(t).pageOf(t, "/settings/certificates"); dropForm(page) {
		t.Error("the button is drawn with no upload")
	}
}

func TestDroppingRefusals(t *testing.T) {
	e, _ := dropEnvUI(t)
	post := func() (int, string) {
		resp, body := e.postForm(t, "/settings/certificates/drop", nil)
		return resp.StatusCode, body
	}
	// No managed server at all.
	status, body := post()
	if status != http.StatusConflict || !strings.Contains(text(body), "No server's agent holds Traefik's certificate store: run the installer on the server.") {
		t.Errorf("no server = %d:\n%s", status, text(body))
	}
	if !strings.Contains(body, `role="alert"`) || !dropForm(body) {
		t.Error("the refusal isn't at the form, or the form is gone")
	}
	// A first server whose agent doesn't say it holds the store.
	firstServer(t, e, false)
	status, body = post()
	if status != http.StatusConflict || !strings.Contains(text(body), "Server hel-1's agent doesn't report Traefik's certificate store: run the installer on server hel-1.") {
		t.Errorf("an agent without the store = %d:\n%s", status, text(body))
	}
	// Nothing uploaded.
	e3 := byoEnv(t)
	status3, body3 := func() (int, string) {
		resp, b := e3.postForm(t, "/settings/certificates/drop", nil)
		return resp.StatusCode, b
	}()
	if status3 != http.StatusConflict || !strings.Contains(text(body3), "No certificate is uploaded: there is nothing to drop.") {
		t.Errorf("no upload = %d:\n%s", status3, text(body3))
	}
	if list, _ := e.Svc.CertificateDrops(context.Background()); len(list) != 0 {
		t.Errorf("a refusal left %d drops", len(list))
	}
}

// A drop through the console: 303 to the panel, then every state's words.
func TestDroppingFollowsTheAgentAndTraefik(t *testing.T) {
	e, c := dropEnvUI(t)
	ctx := context.Background()
	firstServer(t, e, true)

	resp, _ := e.postForm(t, "/settings/certificates/drop", nil)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/settings/certificates?drop=") || !strings.HasSuffix(loc, "#drop") {
		t.Fatalf("POST = %d %q, want 303 to the panel", resp.StatusCode, loc)
	}
	drops, err := e.Svc.CertificateDrops(ctx)
	if err != nil || len(drops) != 1 || loc != "/settings/certificates?drop="+drops[0].ID+"#drop" {
		t.Fatalf("drops = %+v, %v (Location %q)", drops, err, loc)
	}
	id := drops[0].ID

	// Requested: the panel waits for the agent, polls every 3 s, a page without script reloads, and the
	// button is gone while the drop is open; another post is refused at the form.
	page := e.pageOf(t, "/settings/certificates?drop="+id)
	for _, want := range []string{"Waiting", "Waiting for server hel-1's agent."} {
		if !strings.Contains(text(page), want) {
			t.Errorf("the requested panel lacks %q:\n%s", want, text(page))
		}
	}
	for _, want := range []string{`id="drop"`, `hx-get="/parts/settings/certificates/drop?id=` + id + `"`, `hx-trigger="every 3s"`,
		`<meta http-equiv="refresh" content="5">`} {
		if !strings.Contains(page, want) {
			t.Errorf("the open panel lacks %s", want)
		}
	}
	if dropForm(page) {
		t.Error("the button is drawn while a drop is open")
	}
	resp, body := e.postForm(t, "/settings/certificates/drop", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "A drop of the earlier certificates is already under way.") ||
		!strings.Contains(body, `role="alert"`) {
		t.Errorf("a second post = %d:\n%s", resp.StatusCode, text(body))
	}

	part := func() (int, string) {
		resp, b := e.get(t, "/parts/settings/certificates/drop?id="+id)
		noCertificateContent(t, "the part", b, c)
		return resp.StatusCode, text(b)
	}
	if status, got := part(); status != http.StatusOK || !strings.Contains(got, "Waiting for server hel-1's agent.") {
		t.Errorf("requested part = %d %s", status, got)
	}
	// Running.
	if err := e.Store.StartCertificateDrop(ctx, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, got := part(); status != http.StatusOK || !strings.Contains(got, "Dropping…") {
		t.Errorf("running part = %d %s", status, got)
	}
	// Restarting.
	names := []string{dropHost1, dropHost2 + "+www." + dropHost2}
	if err := e.Store.RestartCertificateDrop(ctx, id, time.Now(), names); err != nil {
		t.Fatal(err)
	}
	if status, got := part(); status != http.StatusOK || !strings.Contains(got, "Restarting Traefik…") {
		t.Errorf("restarting part = %d %s", status, got)
	}
	if got := text(e.pageOf(t, "/settings/certificates")); !strings.Contains(got, "Restarting Traefik…") {
		t.Errorf("the page after the restart began:\n%s", got)
	}
	// Done, with names: 286 stops the polling, and the page keeps saying it, without a poll.
	if err := e.Store.SettleCertificateDrop(ctx, id, store.DropDone, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	status, got := part()
	if status != 286 || !strings.Contains(got, "Dropped the earlier certificates of "+dropHost1+", "+dropHost2+"+www."+dropHost2+".") ||
		!strings.Contains(got, "Done") {
		t.Errorf("done part = %d %s, want 286", status, got)
	}
	page = e.pageOf(t, "/settings/certificates")
	if !strings.Contains(text(page), "Dropped the earlier certificates of "+dropHost1) || strings.Contains(page, `hx-trigger="every 3s"`) ||
		strings.Contains(page, `http-equiv="refresh"`) || !dropForm(page) {
		t.Errorf("the page after it was done:\n%s", text(page))
	}
	noCertificateContent(t, "the page", page, c)
}

// The words of every way a drop ends.
func TestADropEndsInItsWords(t *testing.T) {
	e, c := dropEnvUI(t)
	ctx := context.Background()
	firstServer(t, e, true)
	ask := func() string {
		d, err := e.Svc.RequestCertificateDrop(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Store.StartCertificateDrop(ctx, d.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	settle := func(id, status, reason string, at time.Time) {
		t.Helper()
		if err := e.Store.SettleCertificateDrop(ctx, id, status, at, reason); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id string) (int, string) {
		resp, b := e.get(t, "/parts/settings/certificates/drop?id="+id)
		noCertificateContent(t, "the part", b, c)
		return resp.StatusCode, text(b)
	}

	// Nothing to drop: the agent found no certificate it may remove, and the store was not rewritten.
	id := ask()
	settle(id, store.DropDone, "", time.Now())
	if status, got := read(id); status != 286 || !strings.Contains(got, "Nothing to drop: every earlier certificate also names a host no upload covers.") {
		t.Errorf("nothing = %d %s", status, got)
	}
	// A run found interrupted: rewritten, no names, Traefik restarted all the same.
	id = ask()
	if err := e.Store.RestartCertificateDrop(ctx, id, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	settle(id, store.DropDone, "", time.Now())
	if status, got := read(id); status != 286 || !strings.Contains(got, "Traefik was restarted to read its certificate store again.") ||
		strings.Contains(got, "Nothing to drop") {
		t.Errorf("an interrupted run = %d %s", status, got)
	}
	// Failed and lost: the record's sentence.
	id = ask()
	settle(id, store.DropFailed, "The earlier certificates were removed from Traefik's store, but Traefik couldn't be restarted: it still serves them.", time.Now())
	status, got := read(id)
	if status != 286 || !strings.Contains(got, "Failed") || !strings.Contains(got, "but Traefik couldn't be restarted: it still serves them.") {
		t.Errorf("failed = %d %s", status, got)
	}
	if page := text(e.pageOf(t, "/settings/certificates")); !strings.Contains(page, "but Traefik couldn't be restarted") || !strings.Contains(page, dropButton) {
		t.Errorf("the page after a failure:\n%s", page)
	}
	id = ask()
	settle(id, store.DropLost, "The server's agent never told how it went.", time.Now())
	if status, got := read(id); status != 286 || !strings.Contains(got, "No answer") || !strings.Contains(got, "The server's agent never told how it went.") {
		t.Errorf("lost = %d %s", status, got)
	}
	// A drop that ended more than an hour ago is no longer on the page, though its part still reads.
	id = ask()
	settle(id, store.DropDone, "", time.Now().Add(-2*time.Hour))
	if page := e.pageOf(t, "/settings/certificates"); strings.Contains(page, `id="drop"`) {
		t.Errorf("a drop from two hours ago is on the page:\n%s", text(page))
	}
	if status, _ := read(id); status != 286 {
		t.Errorf("the old drop's part = %d", status)
	}
	// An ID that isn't one: the part says it is gone, and stops.
	if status, got := read("0123456789abcdef0123456789abcdef"); status != 286 || !strings.Contains(got, "This drop is gone.") {
		t.Errorf("an unknown drop = %d %s", status, got)
	}
}

// Who made it and when the panel says; both are plain words.
func TestTheDropPanelSaysWhoAskedAndWhen(t *testing.T) {
	e, _ := dropEnvUI(t)
	firstServer(t, e, true)
	e.postForm(t, "/settings/certificates/drop", nil)
	got := text(e.pageOf(t, "/settings/certificates"))
	if !strings.Contains(got, "Asked ") || !strings.Contains(got, " by you@example.com.") {
		t.Errorf("the panel's line:\n%s", got)
	}
}

// Both routes are an administrator's: a member is refused (403, with the page's or the part's own words)
// and nothing is requested. The walk over every route tries the same with each kind of person against
// the access table, written apart.
func TestTheDropRoutesAreAdminOnly(t *testing.T) {
	e, _ := dropEnvUI(t)
	firstServer(t, e, true)
	for _, c := range walkPeople(t, e) {
		for _, rt := range []request{
			{method: http.MethodPost, path: "/settings/certificates/drop", form: map[string][]string{}},
			{method: http.MethodGet, path: "/parts/settings/certificates/drop?id=0123456789abcdef0123456789abcdef"},
		} {
			rt.cookies = append(rt.cookies, c.cookie)
			resp, body := e.do(t, rt)
			if c.name == "member" {
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("%s %s as a member = %d, want 403:\n%s", rt.method, rt.path, resp.StatusCode, shortLine(body))
				}
			}
		}
	}
	// The member's post, then every other person's refusal, made nothing; only an owner's or admin's request counts.
	drops, err := e.Svc.CertificateDrops(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 1 {
		t.Errorf("drops = %d: the owner's post makes one, the admin's is refused as one is open, the member's and the guest's make none", len(drops))
	}
}
