package ui

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/s3/s3test"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 063: Operator › Caravana backup. The service's own tests cover the backups; these cover what the
// page says in each state, who may do what, that the recovery key is shown once and nowhere else, and that no
// secret reaches a page, a part, the log or the database.

const (
	sbVariableSecret    = "s3cret-sentinel-063-ui-variable"
	sbDestinationSecret = "s3cret-sentinel-063-ui-destination"
)

var sbKeyRE = regexp.MustCompile(`cvrk_[a-z2-7]{58}`)

type sbEnv struct {
	*consoleEnv
	s3 *s3test.Server
}

func newSBEnv(t *testing.T) *sbEnv {
	t.Helper()
	return sbOn(t, newConsoleEnvWith(t, func(s *service.Service, n *deploytest.Nomad) {
		s.SetSelfBackupPassGap(time.Hour)
	}))
}

// sbOn gives a console environment a fake bucket and a variable of an app's to back up. A test that picks its
// own environment turns the daily backup off (the pass of a minute could start one at the hour).
func sbOn(t *testing.T, ce *consoleEnv) *sbEnv {
	t.Helper()
	e := &sbEnv{consoleEnv: ce}
	e.s3 = s3test.New()
	e.s3.SecretAccessKey = sbDestinationSecret
	t.Cleanup(e.s3.Close)
	e.Nomad.SetVariable("nomad/jobs/web/web", map[string]string{"PASSWORD": sbVariableSecret})
	return e
}

// addDestination makes the fake bucket the default backup destination.
func (e *sbEnv) addDestination(t *testing.T) {
	t.Helper()
	secret := e.s3.SecretAccessKey
	if _, err := e.Svc.SaveBackupDestination(context.Background(), "main", service.DestinationInput{Endpoint: e.s3.URL, Region: e.s3.Region,
		Bucket: e.s3.Bucket, Prefix: "caravana", AccessKeyID: e.s3.AccessKeyID, SecretAccessKey: &secret}); err != nil {
		t.Fatal(err)
	}
}

// waitIdle waits until no backup of Caravana is running.
func (e *sbEnv) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, running, err := e.Store.OpenSelfBackup(context.Background()); err != nil {
			t.Fatal(err)
		} else if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a backup is still running")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// makeKey posts the form that makes the first key and returns the key, as the page showed it.
func (e *sbEnv) makeKey(t *testing.T) string {
	t.Helper()
	resp, body := e.postForm(t, "/ops/self-backup/key", url.Values{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST key = %d\n%s", resp.StatusCode, body)
	}
	key := sbKeyRE.FindString(body)
	if key == "" {
		t.Fatalf("the key page has no key:\n%s", body)
	}
	e.waitIdle(t)
	return key
}

// adminCookie signs in an admin who isn't the owner.
func (e *sbEnv) adminCookie(t *testing.T) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	inv, err := e.Svc.InvitePerson(ctx, service.PersonInput{Email: "admin2@example.com", OrgRole: service.OrgAdmin})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Svc.AcceptInvite(ctx, inv.Token, "a long enough password 1", "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := e.Auth.StartSessionFor(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "caravana_session", Value: sess.Value}
}

func (e *sbEnv) page(t *testing.T) string {
	t.Helper()
	resp, body := e.get(t, "/ops/self-backup")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d\n%s", resp.StatusCode, body)
	}
	return body
}

func TestSelfBackupPageWithNothingSetUp(t *testing.T) {
	e := newSBEnv(t)
	body := e.page(t)
	got := text(body)
	for _, want := range []string{"Caravana backup", "Caravana has nowhere to put its backup yet.", "Add a backup destination in Settings › Backup storage",
		"There is no recovery key yet.", "Make a recovery key", "Nothing yet.", "How to restore it on a new server"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(body, `href="/settings/backup-storage"`) || strings.Contains(body, `action="/ops/self-backup/runs"`) {
		t.Error("the way out is missing, or Back up now is offered with nowhere to back up to")
	}
	if strings.Contains(body, `hx-trigger="every 3s"`) || !strings.Contains(body, `hx-trigger="sse:selfbackup, sse:attention, sse:sync"`) {
		t.Error("the region polls with no backup running, or doesn't follow the notices")
	}
	// The sidebar's entry after Console address, marked current.
	if !strings.Contains(body, `<a href="/ops/self-backup" class="cv-nav-item" aria-current="page">`) {
		t.Error("no sidebar entry, or not marked")
	}
	// The key form is a plain post.
	f := formWith(t, body, "/ops/self-backup/key")
	if f.Method != "post" {
		t.Errorf("key form = %+v", f)
	}
	// With a destination and no key the way out is the key section.
	e.addDestination(t)
	body = e.page(t)
	if got := text(body); !strings.Contains(got, "Caravana can't back itself up until you make a recovery key.") || !strings.Contains(body, `href="#recovery-key"`) || strings.Contains(body, `action="/ops/self-backup/runs"`) {
		t.Errorf("with a destination and no key:\n%s", got)
	}
}

func TestSelfBackupPageStates(t *testing.T) {
	e := newSBEnv(t)
	e.addDestination(t)
	key := e.makeKey(t)

	// Backed up: when, where, how big; the facts, the key's fingerprint in mono, Back up now, a row in the past.
	body := e.page(t)
	got := text(body)
	if !strings.Contains(got, "Backed up ") || !strings.Contains(got, " UTC to main, ") || !strings.Contains(got, "when the recovery key was made") {
		t.Errorf("a backed-up page:\n%s", got)
	}
	runs, _ := e.Svc.SelfBackupRuns(context.Background())
	if len(runs) != 1 || runs[0].Status != store.SelfBackupSucceeded {
		t.Fatalf("runs = %+v", runs)
	}
	print := runs[0].KeyFingerprint
	for _, want := range []string{`<span class="cv-inline-mono">` + print + `</span>`, `<span class="cv-inline-mono">main</span>`, `action="/ops/self-backup/runs"`, "Back up now"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if strings.Contains(body, key) {
		t.Error("the key is on the page")
	}

	// Back up now: 303 to the page, and a second run in the list.
	resp, _ := e.postForm(t, "/ops/self-backup/runs", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/self-backup" {
		t.Fatalf("POST runs = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	e.waitIdle(t)
	if runs, _ := e.Svc.SelfBackupRuns(context.Background()); len(runs) != 2 || runs[0].Trigger != store.SelfBackupByOwner {
		t.Fatalf("runs = %+v", runs)
	}
	if got := text(e.page(t)); !strings.Contains(got, "By you:") || !strings.Contains(got, "Past backups") {
		t.Errorf("the past runs say %s", got)
	}

	// A running backup: the pill, no button, the region polls, the page reloads itself, and the part isn't 286.
	if _, err := e.Store.InsertSelfBackup(context.Background(), store.SelfBackup{ID: strings.Repeat("c", 32), Trigger: store.SelfBackupByOwner,
		StartedAt: time.Now().Add(time.Hour), Destination: "main"}); err != nil {
		t.Fatal(err)
	}
	body = e.page(t)
	got = text(body)
	if !strings.Contains(got, "Backing up now. It started") || strings.Contains(body, "Back up now") && strings.Contains(body, `action="/ops/self-backup/runs"`) {
		t.Errorf("a running page:\n%s", got)
	}
	for _, want := range []string{`hx-trigger="sse:selfbackup, sse:attention, sse:sync"`, `hx-trigger="every 3s" hx-target="#sb-state"`, `<meta http-equiv="refresh" content="5">`, `hx-get="/parts/ops/self-backup"`, `sse-connect="/live?watch=selfbackup"`} {
		if !strings.Contains(body, want) {
			t.Errorf("a running page lacks %s", want)
		}
	}
	resp, part := e.get(t, "/parts/ops/self-backup")
	if resp.StatusCode != http.StatusOK || !strings.Contains(part, `hx-swap-oob="innerHTML:#sb-runs"`) || !strings.Contains(text(part), "Backing up now") {
		t.Errorf("the part while running = %d\n%s", resp.StatusCode, part)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	// Back up now while one runs is refused with the reason.
	resp, body = e.postForm(t, "/ops/self-backup/runs", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "A backup of Caravana is already running.") {
		t.Errorf("a second backup = %d\n%s", resp.StatusCode, text(body))
	}
	// Settled as failed, the page says so with the reason and the last good one; the part is 286 now.
	if err := e.Store.FinishSelfBackup(context.Background(), strings.Repeat("c", 32), store.SelfBackupFailed, time.Now().Add(2*time.Hour),
		store.SelfBackupResult{Error: "The backup destination didn't take the backup (s3 put: HTTP 403 (AccessDenied))."}); err != nil {
		t.Fatal(err)
	}
	got = text(e.page(t))
	for _, want := range []string{"The latest backup failed: The backup destination didn't take the backup (s3 put: HTTP 403 (AccessDenied)).", "The last good one was"} {
		if !strings.Contains(got, want) {
			t.Errorf("a failed page lacks %q:\n%s", want, got)
		}
	}
	resp, _ = e.get(t, "/parts/ops/self-backup")
	if resp.StatusCode != 286 {
		t.Errorf("the part with nothing running = %d, want 286", resp.StatusCode)
	}
}

func TestSelfBackupWarningAndOff(t *testing.T) {
	e := newSBEnv(t)
	e.addDestination(t)
	e.makeKey(t)
	run, _ := e.Svc.SelfBackupRuns(context.Background())
	// A warning on the last good run is drawn.
	if err := e.Store.FinishSelfBackup(context.Background(), run[0].ID, store.SelfBackupSucceeded, time.Now(), store.SelfBackupResult{}); err != nil {
		t.Fatal(err) // already settled: a no-op that must not fail
	}
	id := strings.Repeat("d", 32)
	if _, err := e.Store.InsertSelfBackup(context.Background(), store.SelfBackup{ID: id, Trigger: store.SelfBackupByOwner, StartedAt: time.Now().Add(time.Hour), Destination: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.FinishSelfBackup(context.Background(), id, store.SelfBackupSucceeded, time.Now().Add(time.Hour), store.SelfBackupResult{
		Destination: "main", Bytes: 2_100_000, Warning: "The new backup is safe, but 1 old backup could not be deleted."}); err != nil {
		t.Fatal(err)
	}
	got := text(e.page(t))
	if !strings.Contains(got, "to main, 2 MB.") || !strings.Contains(got, "The new backup is safe, but 1 old backup could not be deleted.") {
		t.Errorf("the page says:\n%s", got)
	}
	// Turned off: the page says so beside the last backup.
	resp, _ := e.postForm(t, "/ops/self-backup/settings", url.Values{"hour": {"3"}, "keep": {"14"}}) // no "enabled": off
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	if got := text(e.page(t)); !strings.Contains(got, "The daily backup is off.") {
		t.Errorf("turned off:\n%s", got)
	}
}

func TestSelfBackupSettingsAreTheOwners(t *testing.T) {
	e := newSBEnv(t)
	e.addDestination(t)
	admin := e.adminCookie(t)

	// The owner's form: checkbox, hour, keep, a plain post.
	body := e.page(t)
	f := formWith(t, body, "/ops/self-backup/settings")
	if f.Method != "post" || f.Values.Get("keep") != "14" || f.Values.Get("enabled") != "1" {
		t.Errorf("settings form = %+v", f)
	}
	if !strings.Contains(body, `<option value="3" selected>03:00 UTC</option>`) || !strings.Contains(body, `<option value="22">22:00 UTC</option>`) || !strings.Contains(body, `min="1" max="90"`) {
		t.Error("the form lacks its hour select or its keep limits")
	}
	resp, _ := e.postForm(t, "/ops/self-backup/settings", url.Values{"enabled": {"1"}, "hour": {"22"}, "keep": {"7"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/self-backup?saved=1#settings" {
		t.Fatalf("save = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, saved := e.get(t, "/ops/self-backup?saved=1")
	if !strings.Contains(text(saved), "Saved.") || !strings.Contains(saved, `<option value="22" selected>22:00 UTC</option>`) || !strings.Contains(saved, `value="7"`) {
		t.Errorf("after saving:\n%s", text(saved))
	}
	// Refusals draw the page again with a sentence.
	for _, tc := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"hour": {"nope"}, "keep": {"7"}}, "Choose an hour from 00:00 UTC to 23:00 UTC."},
		{url.Values{"hour": {"24"}, "keep": {"7"}}, "Hour must be from 0 to 23 (UTC)."},
		{url.Values{"hour": {"3"}, "keep": {"0"}}, "Keep must be from 1 to 90 backups."},
		{url.Values{"hour": {"3"}, "keep": {"x"}}, "Keep a number of backups from 1 to 90."},
	} {
		resp, body := e.postForm(t, "/ops/self-backup/settings", tc.form)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), tc.want) {
			t.Errorf("%v = %d\n%s", tc.form, resp.StatusCode, text(body))
		}
	}

	// An admin reads the state, can't change the settings or the key, and sees them read-only.
	resp, adminPage := e.do(t, request{path: "/ops/self-backup", cookies: []*http.Cookie{admin}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin GET = %d", resp.StatusCode)
	}
	got := text(adminPage)
	for _, want := range []string{"Every day at 22:00 UTC, keeping the last 7.", "Only an owner changes these.", "Only an owner makes the key."} {
		if !strings.Contains(got, want) {
			t.Errorf("the admin's page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(adminPage, `action="/ops/self-backup/settings"`) || strings.Contains(adminPage, `action="/ops/self-backup/key"`) || strings.Contains(adminPage, "/ops/self-backup/key\"") {
		t.Error("the admin is offered the owner's forms")
	}
	for _, c := range []struct{ method, path string }{{"POST", "/ops/self-backup/settings"}, {"POST", "/ops/self-backup/key"}, {"GET", "/ops/self-backup/key"}} {
		resp, _ := e.do(t, request{method: c.method, path: c.path, form: url.Values{"hour": {"4"}}, cookies: []*http.Cookie{admin},
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("admin %s %s = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	// But an admin backs up now, once there is a key.
	e.makeKey(t)
	resp, _ = e.do(t, request{method: "POST", path: "/ops/self-backup/runs", form: url.Values{}, cookies: []*http.Cookie{admin},
		headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("an admin's Back up now = %d", resp.StatusCode)
	}
	e.waitIdle(t)
	if runs, _ := e.Svc.SelfBackupRuns(context.Background()); len(runs) != 2 || runs[0].Actor != "admin2@example.com" {
		t.Errorf("runs = %+v", runs)
	}
}

func TestSelfBackupWithoutKeyRefusesBackUpNow(t *testing.T) {
	e := newSBEnv(t)
	e.addDestination(t)
	resp, body := e.postForm(t, "/ops/self-backup/runs", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Caravana can't back itself up until you make a recovery key.") {
		t.Errorf("Back up now with no key = %d\n%s", resp.StatusCode, text(body))
	}
}

// The key is on one page: the answer to the post that makes it.
func TestRecoveryKeyIsShownOnce(t *testing.T) {
	e := newSBEnv(t)
	e.addDestination(t)

	// Before a key: the confirmation page says what making one means.
	resp, body := e.get(t, "/ops/self-backup/key")
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Make a recovery key") || strings.Contains(text(body), "still need the old key") {
		t.Fatalf("the first confirmation = %d\n%s", resp.StatusCode, text(body))
	}

	// The post answers 200, no-store, and shows the key once: not a redirect, no key in any address.
	resp, body = e.postForm(t, "/ops/self-backup/key", url.Values{})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("POST key = %d, Location %q, Cache-Control %q", resp.StatusCode, resp.Header.Get("Location"), resp.Header.Get("Cache-Control"))
	}
	key := sbKeyRE.FindString(body)
	for _, want := range []string{"Your recovery key", `<code class="cv-once-value" id="new-recovery-key">` + key + `</code>`, `data-copy="new-recovery-key"`,
		"password manager", "Without it they can't be read", "It isn't shown again.", "I have saved it", "Fingerprint"} {
		if !strings.Contains(strings.Join(strings.Fields(body), " "), want) && !strings.Contains(text(body), want) {
			t.Errorf("the key page lacks %q:\n%s", want, text(body))
		}
	}
	if n := len(sbKeyRE.FindAllString(body, -1)); n != 1 {
		t.Errorf("the key is on the page %d times, want once", n)
	}
	e.waitIdle(t)

	// Nowhere else: not the page, the Overview, the parts, a second visit to the key's page.
	for _, path := range []string{"/ops/self-backup", "/ops", "/parts/ops/self-backup", "/parts/ops/server", "/ops/self-backup/key", "/ops/updates", "/settings/audit"} {
		resp, other := e.get(t, path)
		if sbKeyRE.MatchString(other) || strings.Contains(other, strings.TrimPrefix(key, "cvrk_")) {
			t.Errorf("GET %s (%d) holds the key", path, resp.StatusCode)
		}
	}
	// With a key made, the confirmation names the cost; a post without it is refused and shows no key.
	_, conf := e.get(t, "/ops/self-backup/key")
	if got := text(conf); !strings.Contains(got, "Make a new recovery key?") || !strings.Contains(got, "The backups made before still need the old key.") {
		t.Errorf("the confirmation:\n%s", got)
	}
	if !strings.Contains(conf, `name="replace" value="1"`) {
		t.Error("the confirmation doesn't send replace")
	}
	resp, body = e.postForm(t, "/ops/self-backup/key", url.Values{})
	if resp.StatusCode != http.StatusConflict || sbKeyRE.MatchString(body) || !strings.Contains(text(body), "A recovery key was made already.") {
		t.Errorf("a second key without replace = %d\n%s", resp.StatusCode, text(body))
	}
	// With replace: a new key, again once.
	resp, body = e.postForm(t, "/ops/self-backup/key", url.Values{"replace": {"1"}})
	second := sbKeyRE.FindString(body)
	if resp.StatusCode != http.StatusOK || second == "" || second == key {
		t.Fatalf("the new key = %d %q", resp.StatusCode, second)
	}
	e.waitIdle(t)
	_, again := e.get(t, "/ops/self-backup")
	if sbKeyRE.MatchString(again) {
		t.Error("the page holds a key")
	}
	// The page's key section names the new key's fingerprint, never the key.
	st, _ := e.Svc.SelfBackups(context.Background())
	if !strings.Contains(again, `<span class="cv-inline-mono">`+st.KeyFingerprint+`</span>`) {
		t.Error("the key section doesn't name the fingerprint")
	}
	// The log never has a key.
	if logs := e.Logs.String(); strings.Contains(logs, key) || strings.Contains(logs, second) {
		t.Error("the log holds a key")
	}
}

func TestOverviewHasCaravanaBackupLine(t *testing.T) {
	// Three servers, all sampled: the line is on the first server's panel only.
	ce, _ := spaceCluster(t)
	e := sbOn(t, ce)
	if err := e.Store.SetSelfBackupSchedule(context.Background(), false, 3, 14); err != nil {
		t.Fatal(err)
	}
	line := func() string {
		t.Helper()
		resp, body := e.get(t, "/ops")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /ops = %d", resp.StatusCode)
		}
		if !strings.Contains(body, `sse:selfbackup`) {
			t.Error("the Overview's server region doesn't follow the selfbackup notice")
		}
		all := regexp.MustCompile(`(?s)<p class="cv-node-reclaim"><span class="cv-mark cv-mark-(\w+)" aria-hidden="true"></span> <a href="/ops/self-backup">(Caravana backup: .*?)</a></p>`).FindAllStringSubmatch(body, -1)
		if len(all) != 1 {
			t.Fatalf("the Overview has %d Caravana backup lines, want one (on the first server's panel)", len(all))
		}
		_, part := e.get(t, "/parts/ops/server")
		if !strings.Contains(part, "Caravana backup: ") {
			t.Error("the part lacks the line")
		}
		return all[0][1] + "|" + text(all[0][2])
	}
	if got := line(); got != "idle|Caravana backup: no backup destination" {
		t.Fatalf("with nothing set up: %q", got)
	}
	e.addDestination(t)
	if got := line(); got != "warning|Caravana backup: no recovery key" {
		t.Errorf("with a destination and no key: %q", got)
	}
	e.makeKey(t)
	if got := line(); !strings.HasPrefix(got, "healthy|Caravana backup: ") || !strings.Contains(got, "UTC") {
		t.Errorf("backed up: %q", got)
	}
	e.s3.Fail("PUT", 500)
	resp, _ := e.postForm(t, "/ops/self-backup/runs", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Back up now = %d", resp.StatusCode)
	}
	e.waitIdle(t)
	if got := line(); got != "failed|Caravana backup: failed" {
		t.Errorf("after a failed backup: %q", got)
	}
}

// opsBackupOf names the problem the needs-you list has, else the state.
func TestOpsBackupOf(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	good := &store.SelfBackup{Status: store.SelfBackupSucceeded, StartedAt: now.Add(-9 * time.Hour), EndedAt: now.Add(-9 * time.Hour)}
	item := func(k service.AttentionKind) []service.AttentionItem { return []service.AttentionItem{{Kind: k}} }
	for _, tc := range []struct {
		name  string
		st    service.SelfBackupStatus
		items []service.AttentionItem
		mark  string
		lead  string
		when  bool
	}{
		{"no destination", service.SelfBackupStatus{Unavailable: service.SelfBackupNoDestination}, nil, "idle", "no backup destination", false},
		{"no key", service.SelfBackupStatus{Unavailable: service.SelfBackupNoKey}, item(service.AttentionSelfBackupNoKey), "warning", "no recovery key", false},
		{"failed", service.SelfBackupStatus{LastSucceeded: good}, item(service.AttentionSelfBackupFailed), "failed", "failed", false},
		{"old, with a last good one", service.SelfBackupStatus{LastSucceeded: good}, item(service.AttentionSelfBackupOld), "warning", "none yet", true},
		{"running", service.SelfBackupStatus{Running: good, LastSucceeded: good}, nil, "rolling", "running", false},
		{"fine", service.SelfBackupStatus{LastSucceeded: good}, nil, "healthy", "", true},
		{"none yet", service.SelfBackupStatus{}, nil, "idle", "none yet", false},
	} {
		got := opsBackupOf(now, tc.st, tc.items)
		if got.Mark != tc.mark || got.Lead != tc.lead || (got.When != nil) != tc.when || got.Href != "/ops/self-backup" {
			t.Errorf("%s: %+v, want mark %s lead %q when %v", tc.name, got, tc.mark, tc.lead, tc.when)
		}
	}
}

// No key, variable secret or destination key reaches a page, a part, the log or the database.
func TestSelfBackupSentinels(t *testing.T) {
	e := newSBEnv(t)
	e.addDestination(t)
	key := e.makeKey(t)
	e.s3.Fail("PUT", 500)
	e.postForm(t, "/ops/self-backup/runs", url.Values{})
	e.waitIdle(t)
	e.s3.Fail("PUT", 0)
	e.postForm(t, "/ops/self-backup/runs", url.Values{})
	e.waitIdle(t)

	secrets := []string{key, strings.TrimPrefix(key, "cvrk_"), sbVariableSecret, sbDestinationSecret}
	for _, path := range []string{"/ops/self-backup", "/parts/ops/self-backup", "/ops", "/parts/ops/server", "/ops/self-backup/key", "/settings/audit", "/settings/backup-storage", "/apps"} {
		_, body := e.get(t, path)
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("GET %s holds %q", path, s)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, s := range secrets {
			if strings.Contains(string(b), s) {
				t.Errorf("%s holds %q", filepath.Base(f), s)
			}
		}
	}
	logs := e.Logs.String()
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("the log holds %q", s)
		}
	}
	// The audit log says a key was made, with its fingerprint.
	var noted bool
	for _, en := range e.auditEntries(t) {
		if en.Resource.ID == "self-backup" && en.Action == "created" {
			noted = true
		}
	}
	if !noted {
		t.Error("the key left no audit entry")
	}
}
