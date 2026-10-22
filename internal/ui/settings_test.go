package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 021, page 3: Settings is one page per entry under a grouped nav, and
// the nav and the rows say the same thing because one function per entry says it.

var settingsPages = []struct{ path, entry string }{
	{"/settings", "Account"},
	{"/settings/registries", "Registries"},
	{"/settings/backup-storage", "Backup storage"},
	{"/settings/notifications", "Notifications"},
	{"/settings/tokens", "API tokens"},
}

// settingsNavOf reads the Settings nav of a page: its groups (the title, "" for
// the first block) and their entries as "Label=href", with a "*" on the current
// one.
func settingsNavOf(t *testing.T, body string) map[string][]string {
	t.Helper()
	nav := regexp.MustCompile(`(?s)<nav aria-label="Settings" class="cv-snav">(.*?)</nav>`).FindStringSubmatch(body)
	if nav == nil {
		t.Fatalf("no Settings nav in:\n%s", text(body))
	}
	out := map[string][]string{}
	for _, g := range regexp.MustCompile(`(?s)<div class="cv-snav-group">(.*?)\n    </div>`).FindAllStringSubmatch(nav[1], -1) {
		title := ""
		if m := regexp.MustCompile(`cv-snav-label">([^<]*)<`).FindStringSubmatch(g[1]); m != nil {
			title = m[1]
		}
		for _, a := range regexp.MustCompile(`(?s)<a href="([^"]+)" class="cv-snav-item"( aria-current="page")?>(.*?)</a>`).FindAllStringSubmatch(g[1], -1) {
			label := regexp.MustCompile(`cv-snav-text">([^<]*)<`).FindStringSubmatch(a[3])[1]
			item := label + "=" + a[1]
			if a[2] != "" {
				item += "*"
			}
			out[title] = append(out[title], item)
		}
	}
	return out
}

// Every Settings page has the same nav: Account, then Connections (Registries,
// Backup storage, Notifications) and Security (API tokens), with this page's entry current.
func TestSettingsNavOnEveryPage(t *testing.T) {
	e := newConsoleEnv(t)
	for _, tc := range settingsPages {
		resp, body := e.get(t, tc.path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", tc.path, resp.StatusCode)
		}
		want := map[string][]string{
			"":            {"Account=/settings"},
			"Connections": {"Registries=/settings/registries", "Backup storage=/settings/backup-storage", "Notifications=/settings/notifications"},
			"Security":    {"API tokens=/settings/tokens"},
		}
		for title, items := range want {
			for i, item := range items {
				if strings.HasPrefix(item, tc.entry+"=") {
					want[title][i] += "*"
				}
			}
		}
		got := settingsNavOf(t, body)
		if len(got) != len(want) {
			t.Errorf("%s: nav groups = %v, want %v", tc.path, got, want)
		}
		for title, items := range want {
			if strings.Join(got[title], " ") != strings.Join(items, " ") {
				t.Errorf("%s: group %q = %v, want %v", tc.path, title, got[title], items)
			}
		}
		// Settings stays active in the sidebar; the crumbs say which entry.
		if !strings.Contains(body, `<a href="/settings" class="cv-nav-item" aria-current="page">`) {
			t.Errorf("%s: the sidebar's Settings isn't current", tc.path)
		}
		if got := text(crumbLine.FindString(body)); got != "Settings /"+tc.entry {
			t.Errorf("%s: crumbs = %q, want %q", tc.path, got, "Settings /"+tc.entry)
		}
		// The one h1 is the entry's name.
		if m := regexp.MustCompile(`<h1[^>]*>([^<]*)</h1>`).FindStringSubmatch(body); m == nil || m[1] != tc.entry {
			t.Errorf("%s: h1 = %v, want %q", tc.path, m, tc.entry)
		}
	}
}

// The count-or-mark rule, one function per entry: each rule of the contract.
func TestSettingsEntryRules(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { return new(now.Add(-d)) }
	day := 24 * time.Hour

	t.Run("registries", func(t *testing.T) {
		pass := &store.RegistryTest{At: now, OK: true}
		refused := &store.RegistryTest{At: now, Message: "401"}
		for _, tc := range []struct {
			name  string
			regs  []store.Registry
			rows  []string
			mark  string
			count int
		}{
			{"none", nil, nil, "", 0},
			{"never tested", []store.Registry{{Host: "a"}}, []string{"idle"}, "", 1},
			{"passed", []store.Registry{{Host: "a", LastTest: pass}}, []string{"healthy"}, "", 1},
			{"refused", []store.Registry{{Host: "a", LastTest: refused}}, []string{"failed"}, "failed", 1},
			{"a refused one wins over the count", []store.Registry{{Host: "a", LastTest: pass}, {Host: "b", LastTest: refused}, {Host: "c"}},
				[]string{"healthy", "failed", "idle"}, "failed", 3},
		} {
			got := registriesState(tc.regs)
			if strings.Join(got.Rows, ",") != strings.Join(tc.rows, ",") || got.Mark != tc.mark || got.Count != tc.count {
				t.Errorf("%s: %+v, want rows %v mark %q count %d", tc.name, got, tc.rows, tc.mark, tc.count)
			}
		}
	})

	t.Run("backup storage", func(t *testing.T) {
		ok := &store.BackupRecord{Status: store.BackupSucceeded}
		bad := &store.BackupRecord{Status: store.BackupFailed}
		for _, tc := range []struct {
			name string
			f    backupFacts
			rows []string
			mark string
		}{
			{"no destination, no policy", backupFacts{}, nil, ""},
			{"no destination with a policy", backupFacts{Policies: 2}, nil, "warning"},
			{"a destination, nothing ran", backupFacts{Destination: true, Policies: 1}, []string{"idle"}, ""},
			{"a destination, the last run worked", backupFacts{Destination: true, Last: ok}, []string{"healthy"}, ""},
			// The same ■ in the nav and on the row (ruling 9); no destination with a policy is a ◆.
			{"a destination, the last run failed", backupFacts{Destination: true, Last: bad}, []string{"failed"}, "failed"},
		} {
			got := backupStorageState(tc.f)
			if strings.Join(got.Rows, ",") != strings.Join(tc.rows, ",") || got.Mark != tc.mark || got.Count != 0 {
				t.Errorf("%s: %+v, want rows %v mark %q and no count", tc.name, got, tc.rows, tc.mark)
			}
		}
	})

	t.Run("notifications", func(t *testing.T) {
		sent := &lastDelivery{OK: true}
		failed := &lastDelivery{OK: false}
		for _, tc := range []struct {
			name  string
			chs   []channelFacts
			rows  []string
			mark  string
			count int
		}{
			{"none", nil, nil, "", 0},
			{"nothing sent yet", []channelFacts{{}}, []string{"idle"}, "", 1},
			{"sent", []channelFacts{{Last: sent}}, []string{"healthy"}, "", 1},
			{"the last delivery failed", []channelFacts{{Last: failed}}, []string{"warning"}, "warning", 1},
			// A paused channel was the owner's choice: its row is ◆, the nav shows the count.
			{"paused", []channelFacts{{Paused: true, Last: sent}}, []string{"warning"}, "", 1},
			{"paused with nothing sent", []channelFacts{{Paused: true}}, []string{"warning"}, "", 1},
			{"paused, and its last delivery had failed", []channelFacts{{Paused: true, Last: failed}}, []string{"warning"}, "warning", 1},
			{"a mix counts every channel, paused ones included", []channelFacts{{Last: sent}, {Paused: true}, {}},
				[]string{"healthy", "warning", "idle"}, "", 3},
			{"a failed one among them is the nav's mark", []channelFacts{{Last: sent}, {Paused: true}, {Last: failed}},
				[]string{"healthy", "warning", "warning"}, "warning", 3},
		} {
			got := notificationsState(tc.chs)
			if strings.Join(got.Rows, ",") != strings.Join(tc.rows, ",") || got.Mark != tc.mark || got.Count != tc.count {
				t.Errorf("%s: %+v, want rows %v mark %q count %d", tc.name, got, tc.rows, tc.mark, tc.count)
			}
		}
	})

	t.Run("API tokens", func(t *testing.T) {
		soon, later := now.Add(30*day), now.Add(200*day)
		scope := &service.TokenScope{Project: "shop", App: "web"}
		active := func(t service.APITokenInfo) service.APITokenInfo { t.State = service.TokenActive; return t }
		for _, tc := range []struct {
			name  string
			tok   service.APITokenInfo
			row   string
			notes int
		}{
			{"a full token with no expiry", active(service.APITokenInfo{CreatedAt: *ago(2 * day), LastUsedAt: ago(time.Hour)}), "warning", 1},
			{"a full token with an expiry, in use", active(service.APITokenInfo{CreatedAt: *ago(2 * day), LastUsedAt: ago(time.Hour), ExpiresAt: &soon}), "healthy", 0},
			{"a deploy token with no expiry is fine", active(service.APITokenInfo{CreatedAt: *ago(2 * day), LastUsedAt: ago(time.Hour), Scope: scope}), "healthy", 0},
			{"unused for 94 days", active(service.APITokenInfo{CreatedAt: *ago(200 * day), LastUsedAt: ago(94 * day), ExpiresAt: &later}), "warning", 1},
			{"used 90 days ago is not yet", active(service.APITokenInfo{CreatedAt: *ago(200 * day), LastUsedAt: ago(90 * day), ExpiresAt: &later}), "healthy", 0},
			{"never used, made 100 days ago", active(service.APITokenInfo{CreatedAt: *ago(100 * day), ExpiresAt: &later}), "warning", 1},
			{"never used, made yesterday", active(service.APITokenInfo{CreatedAt: *ago(day), ExpiresAt: &later}), "healthy", 0},
			{"never used, made just now: a fresh token is never flagged", active(service.APITokenInfo{CreatedAt: now, ExpiresAt: &later}), "healthy", 0},
			{"never used, made 89 days ago", active(service.APITokenInfo{CreatedAt: *ago(89 * day), ExpiresAt: &later}), "healthy", 0},
			{"never used, made 90 days ago: not yet, it takes more than 90", active(service.APITokenInfo{CreatedAt: *ago(90 * day), ExpiresAt: &later}), "healthy", 0},
			{"never used, made 91 days ago", active(service.APITokenInfo{CreatedAt: *ago(91 * day), ExpiresAt: &later}), "warning", 1},
			{"used a minute ago, made long ago", active(service.APITokenInfo{CreatedAt: *ago(300 * day), LastUsedAt: ago(time.Minute), ExpiresAt: &later}), "healthy", 0},
			{"both problems", active(service.APITokenInfo{CreatedAt: *ago(200 * day), LastUsedAt: ago(100 * day)}), "warning", 2},
			{"expired", service.APITokenInfo{State: service.TokenExpired, CreatedAt: *ago(200 * day)}, "idle", 0},
			{"revoked", service.APITokenInfo{State: service.TokenRevoked, CreatedAt: *ago(200 * day)}, "idle", 0},
		} {
			got := tokensState(now, []service.APITokenInfo{tc.tok})
			if got.Rows[0] != tc.row || len(tokenNotes(now, tc.tok)) != tc.notes {
				t.Errorf("%s: row %q with %d notes, want %q with %d", tc.name, got.Rows[0], len(tokenNotes(now, tc.tok)), tc.row, tc.notes)
			}
			// Rows mark more than the nav: a full token with no end date marks its row, and only a token
			// unused for 90 days marks the nav.
			wantNav := map[string]bool{"unused for 94 days": true, "never used, made 100 days ago": true, "never used, made 91 days ago": true, "both problems": true}[tc.name]
			if (got.Mark == "warning") != wantNav {
				t.Errorf("%s: nav mark %q, want one: %v", tc.name, got.Mark, wantNav)
			}
		}
		// The count is of active tokens only, and a mark wins over it.
		toks := []service.APITokenInfo{
			active(service.APITokenInfo{CreatedAt: *ago(day), ExpiresAt: &soon, LastUsedAt: ago(time.Hour)}),
			{State: service.TokenExpired}, {State: service.TokenRevoked},
		}
		if got := tokensState(now, toks); got.Mark != "" || got.Count != 1 {
			t.Errorf("one active token among three: %+v, want count 1 and no mark", got)
		}
		toks = append(toks, active(service.APITokenInfo{CreatedAt: *ago(day), LastUsedAt: ago(time.Hour)}))
		if got := tokensState(now, toks); got.Mark != "" || got.Count != 2 || got.Rows[3] != "warning" {
			t.Errorf("a token with no expiry: %+v, want its row marked, no nav mark and count 2", got)
		}
		toks = append(toks, active(service.APITokenInfo{CreatedAt: *ago(300 * day), LastUsedAt: ago(95 * day), ExpiresAt: &later}))
		if got := tokensState(now, toks); got.Mark != "warning" || got.Count != 3 {
			t.Errorf("a token unused for 95 days: %+v, want the warning mark and count 3", got)
		}
	})

	t.Run("the nav draws the mark instead of the count", func(t *testing.T) {
		nav := settingsNav(entryRegistries, map[string]entryState{
			entryRegistries: {Count: 3, Mark: "failed", Why: "a registry refused its credentials"},
			entryTokens:     {Count: 2},
		})
		var reg, tok settingsNavItem
		for _, g := range nav {
			for _, it := range g.Items {
				switch it.Label {
				case "Registries":
					reg = it
				case "API tokens":
					tok = it
				}
			}
		}
		if reg.Mark != "failed" || reg.Count != 0 || !reg.Current || tok.Mark != "" || tok.Count != 2 || tok.Current {
			t.Errorf("registries = %+v, tokens = %+v", reg, tok)
		}
	})
}

func TestBackupWhenAndName(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC), "02:00 today"},
		{time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC), "23:30 yesterday"},
		{now.Add(-72 * time.Hour), "3 days ago"},
	} {
		if got := backupWhen(now, tc.at); got != tc.want {
			t.Errorf("backupWhen(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
	for _, tc := range []struct {
		d    store.BackupDestination
		want string
	}{
		{store.BackupDestination{Endpoint: "https://hel1.example.com", Bucket: "vee"}, "hel1.example.com/vee"},
		{store.BackupDestination{Endpoint: "http://10.0.0.5:9000/", Bucket: "b", Prefix: "/prod/"}, "10.0.0.5:9000/b/prod"},
	} {
		if got := endpointName(tc.d); got != tc.want {
			t.Errorf("endpointName(%+v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// The old addresses: /settings/account moved to /settings for good, /account keeps its redirect.
func TestSettingsAccountRedirects(t *testing.T) {
	e := newConsoleEnv(t)
	for _, path := range []string{"/settings/account", "/account"} {
		resp, _ := e.do(t, request{path: path})
		if resp.StatusCode != http.StatusMovedPermanently || location(t, resp) != "/settings" {
			t.Errorf("GET %s = %d to %q, want 301 to /settings", path, resp.StatusCode, location(t, resp))
		}
	}
}

// The pages need a session, as /settings always did.
func TestSettingsPagesNeedASession(t *testing.T) {
	e := newConsoleEnv(t)
	for _, tc := range settingsPages {
		resp, _ := e.do(t, request{path: tc.path})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("GET %s without a session = %d to %q, want 303 to /login", tc.path, resp.StatusCode, location(t, resp))
		}
	}
}

// Every post keeps its path and a success answers 303 to its entry's page.
func TestSettingsPostsRedirectToTheirEntry(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	rcv := newNotifyReceiver(t)
	if _, err := e.Svc.SaveRegistry(ctx, "ghcr.io", "acme", new(registrySecret)); err != nil {
		t.Fatal(err)
	}
	ntfy, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("ntfy", "phone", rcv.URL+"/topic"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := e.Auth.CreateAPIToken(ctx, "victim", nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path string
		form       url.Values
		want       string
	}{
		{"password", "/settings/password", url.Values{"currentPassword": {testPassword}, "newPassword": {"another long password"}}, "/settings?password=changed"},
		{"destination", "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"}, "bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {"s"}},
			"/settings/backup-storage?backups=saved"},
		{"registry save", "/settings/registries", url.Values{"host": {"quay.io"}, "username": {"u"}, "password": {"p"}}, "/settings/registries?registries=saved"},
		{"registry test", "/settings/registries/ghcr.io/test", nil, ""},
		{"registry remove", "/settings/registries/quay.io/remove", nil, "/settings/registries"},
		{"channel add", "/settings/notifications", url.Values{"kind": {"ntfy"}, "name": {"tablet"}, "url": {rcv.URL + "/t"}, "events": {"problems"}}, "/settings/notifications?notifications=saved"},
		{"channel pause", "/settings/notifications/" + ntfy.ID, url.Values{"paused": {"1"}}, "/settings/notifications?notifications=saved"},
		{"channel test", "/settings/notifications/" + ntfy.ID + "/test", nil, "/settings/notifications?notified=" + url.QueryEscape(ntfy.ID)},
		{"channel remove", "/settings/notifications/" + ntfy.ID + "/remove", nil, "/settings/notifications"},
		{"token revoke", "/settings/tokens/" + created.Info.ID + "/revoke", nil, "/settings/tokens"},
	} {
		resp, body := e.postForm(t, tc.path, tc.form)
		got := location(t, resp)
		if tc.want == "" { // the registry test names the host it tested
			tc.want = "/settings/registries?tested=ghcr.io"
		}
		if resp.StatusCode != http.StatusSeeOther || got != tc.want {
			t.Errorf("%s: POST %s = %d to %q, want 303 to %q:\n%s", tc.name, tc.path, resp.StatusCode, got, tc.want, text(body))
		}
	}
}

// A secret shown once is in the response of the post that made it, drawn once, and in no
// address, header or later page.
func TestSettingsShownOncePanels(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	rcv := newNotifyReceiver(t)

	// A token.
	resp, body := e.postForm(t, "/settings/tokens", url.Values{"name": {"ci"}})
	token := tokenIn(t, body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" || strings.Contains(resp.Request.URL.String(), token) {
		t.Errorf("the token's post = %d, location %q: a redirect or an address would hold it", resp.StatusCode, resp.Header.Get("Location"))
	}
	if strings.Count(body, token) != 2 { // the value box and the curl example
		t.Errorf("the token is drawn %d times, want in its box and its example only", strings.Count(body, token))
	}
	for _, tc := range settingsPages {
		_, page := e.get(t, tc.path)
		if strings.Contains(page, token) {
			t.Errorf("%s shows the token after its panel", tc.path)
		}
	}

	// A webhook's signing secret, when it is made and when it is replaced.
	resp, body = e.postForm(t, "/settings/notifications", url.Values{"kind": {"webhook"}, "name": {"ci"}, "url": {rcv.URL + "/hook"}, "events": {"problems"}})
	ids := e.channelIDs(t)
	secret := e.notifySecretItems(ids[0])["signing_secret"]
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" || secret == "" || strings.Count(body, secret) != 1 {
		t.Fatalf("the webhook's post = %d, secret drawn %d times, want 200 and once:\n%s", resp.StatusCode, strings.Count(body, secret), text(body))
	}
	resp, body = e.postForm(t, "/settings/notifications/"+ids[0]+"/secret", nil)
	fresh := e.notifySecretItems(ids[0])["signing_secret"]
	if resp.StatusCode != http.StatusOK || fresh == secret || strings.Count(body, fresh) != 1 || strings.Contains(body, secret) {
		t.Errorf("the new signing secret: %d, drawn %d times, old one drawn %d", resp.StatusCode, strings.Count(body, fresh), strings.Count(body, secret))
	}
	for _, tc := range settingsPages {
		_, page := e.get(t, tc.path)
		if strings.Contains(page, secret) || strings.Contains(page, fresh) {
			t.Errorf("%s shows a signing secret after its panel", tc.path)
		}
	}
	if logs := e.Logs.String(); strings.Contains(logs, token) || strings.Contains(logs, secret) || strings.Contains(logs, fresh) {
		t.Error("the log holds a token or signing secret")
	}
	_ = ctx
}

// No secret is on any Settings page: a registry's password, a channel's URL and
// token, the backup destination's secret key.
func TestSettingsPagesHoldNoSecret(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	rcv := newNotifyReceiver(t)
	const sentinel = "s3cret-sentinel-021p3"
	if _, err := e.Svc.SaveRegistry(ctx, "ghcr.io", "acme", new(sentinel)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateNotificationChannel(ctx, service.NotificationChannelInput{Kind: "ntfy", Name: "phone", URL: rcv.URL + "/topic-" + sentinel,
		Token: "tk_" + sentinel, Events: []string{service.EventProblems}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateNotificationChannel(ctx, service.NotificationChannelInput{Kind: "slack", Name: "team",
		URL: rcv.URL + "/services/T000/B000/" + sentinel, Events: []string{service.EventProblems}}); err != nil {
		t.Fatal(err)
	}
	wh, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("webhook", "ci", rcv.URL+"/hook"))
	if err != nil {
		t.Fatal(err)
	}
	if wh.SigningSecret == "" {
		t.Fatal("no signing secret")
	}
	created, err := e.Auth.CreateAPIToken(ctx, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {sentinel + "-key"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST destination = %d:\n%s", resp.StatusCode, text(body))
	}
	secrets := []string{sentinel, wh.SigningSecret, created.Token}
	for _, tc := range settingsPages {
		resp, page := e.get(t, tc.path)
		noSecret(t, "GET "+tc.path, secrets, page, resp.Header.Get("Location"))
	}
	noSecret(t, "the log", secrets, e.Logs.String())
}

// Registries: a refused one comes first with its note and "Update password" (which opens its edit form),
// each row says how many apps use it, and the page's actions.
func TestSettingsRegistryRows(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	for _, host := range []string{"ghcr.io", "quay.io", "docker.io"} {
		if _, err := e.Svc.SaveRegistry(ctx, host, "u-"+strings.TrimSuffix(host, ".io"), new("p")); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.SaveRegistryTest(ctx, "ghcr.io", store.RegistryTest{At: e.Clock.Now(), OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SaveRegistryTest(ctx, "quay.io", store.RegistryTest{At: e.Clock.Now().Add(-2 * time.Hour), Message: "401 unauthorized: the user name or password is wrong"}); err != nil {
		t.Fatal(err)
	}
	a := duoApp()
	a.Services[0].Image = "ghcr.io/acme/web:1"
	a.Services[1].Image = "ghcr.io/acme/worker:1"
	e.seed(t, a)
	b := duoApp()
	b.ID, b.Name = "other", "Other"
	b.Services[0].Image = "ghcr.io/acme/api:1"
	b.Services[1].Image = "traefik/whoami:latest"
	e.seed(t, b)

	resp, page := e.get(t, "/settings/registries")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	got := text(page)
	// Refused first (quay.io), then by host (docker.io, ghcr.io).
	quay, docker, ghcr := strings.Index(got, "quay.io user u-quay"), strings.Index(got, "docker.io user u-docker"), strings.Index(got, "ghcr.io user u-ghcr")
	if !(quay >= 0 && quay < docker && docker < ghcr) {
		t.Errorf("rows not ordered refused first, then by host (%d, %d, %d):\n%s", quay, docker, ghcr, got)
	}
	for _, want := range []string{
		"refused tested 2 hours ago", "Deploys of private images from this registry fail until it accepts the credentials.", "401 unauthorized: the user name or password is wrong",
		"not tested yet added", "works tested just now",
		"used by 2 apps", "used by 1 app", "not used", "Update password",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// "Update password" is on the refused row only, and opens that row's edit form.
	if n := strings.Count(page, ">Update password</a>"); n != 1 || !strings.Contains(page, `href="/settings/registries?edit=quay.io#registry-edit-0"`) {
		t.Errorf("Update password: %d links, want one to quay.io's edit form", n)
	}
	_, opened := e.get(t, "/settings/registries?edit=quay.io")
	if !regexp.MustCompile(`id="registry-edit-0"[^>]* open>`).MatchString(opened) {
		t.Error("?edit=quay.io doesn't open its edit form")
	}
	// The nav says ■ for the refused registry instead of the count.
	if !strings.Contains(page, `cv-mark cv-mark-failed" aria-hidden="true"></span><span class="sr-only">a registry refused its credentials`) {
		t.Errorf("the nav lacks the refused mark:\n%s", page)
	}
}

// Backup storage: the problem panel when no destination is set and a service has a policy, the
// connection row with its facts when there is one, and no Remove.
func TestSettingsBackupStorageStates(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)

	_, page := e.get(t, "/settings/backup-storage")
	if strings.Contains(page, "No backup destination is set.") || !strings.Contains(page, `id="destination"`) {
		t.Errorf("with no destination and no policy: want the form alone:\n%s", text(page))
	}

	a := duoApp()
	a.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres}
	e.seed(t, a)
	_, page = e.get(t, "/settings/backup-storage")
	if !strings.Contains(text(page), "No backup destination is set. Backups can't run until there is one.") || !strings.Contains(page, `id="destination"`) {
		t.Errorf("with no destination and a policy: want the problem panel and the form:\n%s", text(page))
	}
	if !strings.Contains(page, `cv-mark cv-mark-warning" aria-hidden="true"></span><span class="sr-only">backups have nowhere to go`) {
		t.Error("the nav lacks the warning mark")
	}

	if resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"https://hel1.example.com"}, "region": {"eu"}, "bucket": {"vee-backups"},
		"prefix": {"prod"}, "accessKeyId": {"AK"}, "secretAccessKey": {"s"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
	}
	check := func(name string, wants ...string) {
		t.Helper()
		_, page := e.get(t, "/settings/backup-storage")
		got := text(page)
		for _, want := range append(wants, "hel1.example.com/vee-backups/prod", "S3-compatible · region eu", "used by 1 service", "Edit") {
			if !strings.Contains(got, want) {
				t.Errorf("%s: the page lacks %q:\n%s", name, want, got)
			}
		}
		if strings.Contains(got, "Remove") || strings.Contains(got, "No backup destination is set.") {
			t.Errorf("%s: the page offers Remove or still has the problem panel:\n%s", name, got)
		}
	}
	check("nothing ran", "no backup yet")
	if !strings.Contains(e.mustGet(t, "/settings/backup-storage"), `cv-mark cv-mark-lg cv-mark-idle`) {
		t.Error("a destination nothing ran against isn't an empty ring")
	}

	now := e.Clock.Now()
	run := store.BackupRecord{AppID: "duo", ID: "web-1", Service: "web", JobID: "j", Status: store.BackupSucceeded, Object: "o", Bytes: 1900 << 20,
		StartedAt: now.Add(-time.Hour), FinishedAt: new(now.Add(-time.Hour + time.Minute))}
	if err := e.Store.SaveBackup(ctx, run); err != nil {
		t.Fatal(err)
	}
	check("a good run", "1.9 GB")
	if !strings.Contains(e.mustGet(t, "/settings/backup-storage"), `cv-mark cv-mark-lg cv-mark-healthy`) {
		t.Error("a good last run isn't a disc")
	}

	run.ID, run.Status, run.Message = "web-2", store.BackupFailed, "AccessDenied"
	run.StartedAt, run.FinishedAt = now.Add(-30*time.Minute), new(now.Add(-29*time.Minute)) // newer than the good one
	if err := e.Store.SaveBackup(ctx, run); err != nil {
		t.Fatal(err)
	}
	check("a failed run", "last backup failed 29 minutes ago")
	page = e.mustGet(t, "/settings/backup-storage")
	if !strings.Contains(page, `cv-mark cv-mark-lg cv-mark-failed`) || !strings.Contains(page, `cv-conn-st cv-conn-st-failed`) ||
		!strings.Contains(page, `cv-mark cv-mark-failed" aria-hidden="true"></span><span class="sr-only">the last backup failed`) {
		t.Error("a failed last run isn't a square on the row and the same square in the nav")
	}
	// The note under the row: which service of which app, page 2's plain cause, the raw message in
	// mono, and the way to its Backups tab.
	got := text(page)
	for _, want := range []string{"web of duo: The destination refused the access key.", "AccessDenied", "Open its backups"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure note lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(page, `<p class="cv-conn-detail">AccessDenied</p>`) || !strings.Contains(page, `<a href="/apps/duo/backups" class="cv-conn-link">Open its backups</a>`) {
		t.Error("the raw message isn't its own mono line, or the link doesn't go to the app's Backups tab")
	}
	if !strings.Contains(page, `aria-label="Edit backup storage"`) {
		t.Error("the destination's row has no short accessible name")
	}
}

func (e *consoleEnv) mustGet(t *testing.T, path string) string {
	t.Helper()
	resp, body := e.get(t, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", path, resp.StatusCode)
	}
	return body
}

// Notifications: problems first (a failed delivery, a paused channel), then by name; the kind's icon; the
// failed delivery's error as the row's note; the sentence in place of the rules grid.
func TestSettingsNotificationRows(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	okRcv, failRcv := newNotifyReceiver(t), newNotifyReceiver(t)
	failRcv.setStatus(http.StatusInternalServerError)
	good, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("ntfy", "phone", okRcv.URL+"/t"))
	if err != nil {
		t.Fatal(err)
	}
	bad, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("webhook", "zeta", failRcv.URL+"/h"))
	if err != nil {
		t.Fatal(err)
	}
	paused, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("webhook", "alpha", okRcv.URL+"/h"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("webhook", "quiet", okRcv.URL+"/q")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.TestNotificationChannel(ctx, good.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.TestNotificationChannel(ctx, bad.ID); err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := e.Svc.UpdateNotificationChannel(ctx, paused.ID, service.NotificationChannelPatch{Paused: &yes}); err != nil {
		t.Fatal(err)
	}

	page := e.mustGet(t, "/settings/notifications")
	got := text(page)
	// zeta (failed) and alpha (paused) first, by name; then phone (sent) and quiet (nothing sent) by name.
	order := []string{"alpha", "zeta", "phone", "quiet"}
	last := -1
	for _, name := range order {
		at := strings.Index(page, `<span class="cv-conn-title" title="`+name+`">`)
		if at < 0 || at < last {
			t.Errorf("channel %s is out of order (at %d, after %d)", name, at, last)
		}
		last = at
	}
	for _, want := range []string{
		"failed just now", "sent just now", "nothing sent yet", "paused",
		"Each channel gets the events ticked in its form. Repeats of the same problem are sent once.",
		"Messages to this channel aren't getting through.", "Remove zeta? Messages stop at once.",
		"Recent deliveries", "Send a test", "Resume", "Pause", "New signing secret",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if n := strings.Count(page, `class="cv-conn-icon"><svg`); n != 4 {
		t.Errorf("%d kind icons drawn, want one per channel", n)
	}
	// The note leads with the consequence in words, then the raw error in mono.
	if !regexp.MustCompile(`cv-conn-note">Messages to this channel aren't getting through\.<span class="cv-conn-detail">[^<]+</span></p>`).MatchString(page) {
		t.Error("the failed delivery's error isn't the row's note, after its sentence")
	}
	// Recent deliveries mark a failure with the row's ◆, not a square.
	if strings.Contains(page, `cv-deliveries`) && strings.Contains(page[strings.Index(page, `cv-deliveries`):], `cv-mark-failed`) {
		t.Error("a failed delivery in Recent deliveries is marked with a square, not the row's diamond")
	}
	if strings.Contains(got, "What goes where") {
		t.Error("the rules grid is drawn")
	}
	// A failed channel and a paused one make the nav's diamond.
	if !strings.Contains(page, `cv-mark cv-mark-warning" aria-hidden="true"></span><span class="sr-only">a channel`) {
		t.Error("the nav lacks the warning mark")
	}
}

// API tokens: the table with its notes, sorted active (problems first), expired, revoked.
func TestSettingsTokenRows(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	start := e.Clock.Now()
	later := start.Add(200 * 24 * time.Hour)
	if _, err := e.Auth.CreateScopedAPIToken(ctx, "github-actions", &later, &service.TokenScope{Project: "default", App: "web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth.CreateAPIToken(ctx, "old", &later); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth.CreateAPIToken(ctx, "forever", nil); err != nil {
		t.Fatal(err)
	}
	soon := e.Clock.Now().Add(time.Hour)
	if _, err := e.Auth.CreateAPIToken(ctx, "lapsed", &soon); err != nil {
		t.Fatal(err)
	}
	gone, _ := e.Auth.CreateAPIToken(ctx, "gone", &later)
	if err := e.Auth.RevokeAPIToken(ctx, gone.Info.ID); err != nil {
		t.Fatal(err)
	}
	// Everything above was made 95 days ago and never used; fresh-deploy is made now.
	e.Clock.Advance(95 * 24 * time.Hour)
	e.Cookie = e.signIn(t, nil) // the session idled out meanwhile
	if _, err := e.Auth.CreateScopedAPIToken(ctx, "fresh-deploy", &later, &service.TokenScope{Project: "default", App: "api"}); err != nil {
		t.Fatal(err)
	}

	page := e.mustGet(t, "/settings/tokens")
	got := text(page)
	for _, want := range []string{
		"TokenCan doLast usedExpiry", // the head, whose cells touch in the text
		"Deploy · app api of project default", "Everything", "never expires",
		"A token that can do everything and never expires. Make one with an end date, or one that only deploys an app, then revoke this one.",
		"Not used in 95 days. Revoke it if nothing needs it.",
		"Can do Everything", "Expiry never expires",
		"expired " + soon.UTC().Format("2006-01-02"), "revoked " + start.UTC().Format("2006-01-02"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// "forever" has both notes; "never expires" is in the warning hue only for it.
	if n := strings.Count(page, `cv-conn-meta cv-conn-st-warn`); n != 1 {
		t.Errorf("%d expiry cells in the warning hue, want 1 (the full token)", n)
	}
	// Active first (problems first among them), then expired, then revoked.
	pos := func(name string) int { return strings.Index(page, `<span class="cv-conn-title" title="`+name+`">`) }
	for _, name := range []string{"forever", "old", "github-actions"} { // the three with a problem lead
		if pos(name) < 0 || pos(name) > pos("fresh-deploy") {
			t.Errorf("%s isn't ahead of the token with no problem", name)
		}
	}
	if !(pos("fresh-deploy") < pos("lapsed") && pos("lapsed") < pos("gone")) {
		t.Errorf("rows not sorted active (problems first), expired, revoked:\n%s", got)
	}
	// Revoke is for the active tokens only: forever, old, github-actions, fresh-deploy.
	if n := strings.Count(page, ">Revoke</summary>"); n != 4 {
		t.Errorf("%d Revoke buttons, want 4", n)
	}
	// The nav shows the warning, not the count.
	if !strings.Contains(page, `<a href="#new" class="cv-conn-link">Make one</a>`) {
		t.Error("the no-expiry note doesn't link Make one to the New token form")
	}
	if !strings.Contains(page, `cv-mark cv-mark-warning" aria-hidden="true"></span><span class="sr-only">a token went unused for 90 days`) {
		t.Error("the nav lacks the warning mark")
	}
}

// What a channel sends, in words, for each combination of the three events.
func TestEventsWords(t *testing.T) {
	p, f, ok := service.EventProblems, service.EventDeployFailed, service.EventDeploySucceeded
	for _, tc := range []struct {
		events []string
		want   string
	}{
		{[]string{p, f, ok}, ""},
		{[]string{ok, p, f}, ""},
		{[]string{p}, "problems only"},
		{[]string{f}, "deploy failures only"},
		{[]string{ok}, "deploy successes only"},
		{[]string{p, f}, "deploy failures and problems"},
		{[]string{f, p}, "deploy failures and problems"},
		{[]string{p, ok}, "deploy successes and problems"},
		{[]string{f, ok}, "deploy failures and successes"},
		{nil, ""},
	} {
		if got := eventsWords(tc.events); got != tc.want {
			t.Errorf("eventsWords(%v) = %q, want %q", tc.events, got, tc.want)
		}
	}
	for _, tc := range []struct {
		kind   string
		events []string
		want   string
	}{
		{"webhook", []string{p}, "Webhook · problems only"},
		{"slack", []string{f, p}, "Slack · deploy failures and problems"},
		{"ntfy", []string{p, f, ok}, "ntfy"},
		{"discord", []string{ok}, "Discord · deploy successes only"},
	} {
		if got := kindLine(tc.kind, tc.events); got != tc.want {
			t.Errorf("kindLine(%s, %v) = %q, want %q", tc.kind, tc.events, got, tc.want)
		}
	}
}

// The review's fixes: the kind line says what a channel sends when that isn't all, New signing secret sits inside
// Edit before Save (so a row has four buttons), the add panels open with a label, and the Expiry column is 170px.
func TestSettingsAfterTheReview(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	rcv := newNotifyReceiver(t)
	hook, err := e.Svc.CreateNotificationChannel(ctx, service.NotificationChannelInput{Kind: "webhook", Name: "ci", URL: rcv.URL + "/h",
		Events: []string{service.EventProblems}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateNotificationChannel(ctx, service.NotificationChannelInput{Kind: "slack", Name: "team", URL: rcv.URL + "/services/T0/B0/x",
		Events: []string{service.EventProblems, service.EventDeployFailed}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateNotificationChannel(ctx, service.NotificationChannelInput{Kind: "ntfy", Name: "phone", URL: rcv.URL + "/t",
		Events: []string{service.EventProblems, service.EventDeployFailed, service.EventDeploySucceeded}}); err != nil {
		t.Fatal(err)
	}

	page := e.mustGet(t, "/settings/notifications")
	got := text(page)
	for _, want := range []string{"Webhook · problems only", "Slack · deploy failures and problems", "Add a channel"} {
		if !strings.Contains(got, want) {
			t.Errorf("Notifications lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ntfy ·") {
		t.Errorf("a channel that sends everything says so in words:\n%s", got)
	}
	// "New signing secret" is in the Edit details, after the fields and before Save, for the webhook only.
	row := page[strings.Index(page, `title="ci">`):]
	row = row[:strings.Index(row, `Recent deliveries`)]
	fields, secret, save := strings.Index(row, `id="notify-edit-form-`), strings.Index(row, `>New signing secret</summary>`), strings.Index(row, `form="notify-edit-form-`)
	editOpen, editClose := strings.Index(row, `aria-label="Edit ci"`), strings.Index(row, `aria-label="Remove ci"`)
	if !(editOpen < fields && fields < secret && secret < save && save < editClose) {
		t.Errorf("New signing secret isn't inside Edit between the fields and Save (%d, %d, %d, %d, %d)", editOpen, fields, secret, save, editClose)
	}
	if n := strings.Count(row, `<summary class="cv-btn`) + strings.Count(row, `<button type="submit" class="cv-btn cv-btn-sm cv-btn-secondary" aria-label`); n != 5 {
		// Send a test, Pause, Edit, Remove, and the nested New signing secret summary
		t.Errorf("the row has %d buttons and summaries, want 5 (four on the row, one inside Edit)", n)
	}
	if n := strings.Count(page, `>New signing secret</summary>`); n != 1 {
		t.Errorf("%d New signing secret controls, want only the webhook's", n)
	}
	if !strings.Contains(page, `/settings/notifications/`+hook.ID+`/secret`) {
		t.Error("the webhook has no signing secret form")
	}

	// The add panels open with a label, so #add and #new land on a named thing.
	for path, want := range map[string]string{"/settings/registries": "Add a registry", "/settings/notifications": "Add a channel", "/settings/tokens": "New token"} {
		body := e.mustGet(t, path)
		if !regexp.MustCompile(`<h2 class="cv-label cv-addhead">` + want + `</h2>`).MatchString(body) {
			t.Errorf("%s: the add panel has no %q label", path, want)
		}
	}

	// The stylesheet: the token table's Expiry column is 170px, and Settings drops the shell's narrow padding.
	_, css := e.get(t, "/static/app.css")
	css = strings.Join(strings.Fields(css), " ")
	if !strings.Contains(css, "110px 170px 76px") {
		t.Error("the token table's Expiry column isn't 170px")
	}
	// The narrow nav scrolls and must contain its sr-only spans (absolute), or they widen the page.
	if !regexp.MustCompile(`\.cv-snav\{[^}]*position:relative`).MatchString(css) {
		t.Error("the narrow Settings nav isn't positioned, so its sr-only spans would widen the page")
	}
	// Outside the layer, so it outranks the shell's own narrow padding for .cv-page.
	if !strings.Contains(css, "@media (max-width:899.98px){") || !strings.Contains(css, ".cv-page-settings{padding:0}") {
		t.Error(".cv-page-settings doesn't drop the shell's narrow padding after the layer")
	}
}

// The critique's rulings (2026-10-22): the nav marks only what is breaking, rows mark more; "Update password"
// opens Edit with the cursor in the password field; the narrow nav puts the current entry first and fades;
// the last five expired or revoked tokens, then "Show older".
func TestSettingsAfterTheCritique(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	rcv := newNotifyReceiver(t)

	t.Run("a paused channel and a full token without an end date mark their rows, not the nav", func(t *testing.T) {
		ch, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("webhook", "ci", rcv.URL+"/h"))
		if err != nil {
			t.Fatal(err)
		}
		yes := true
		if _, err := e.Svc.UpdateNotificationChannel(ctx, ch.ID, service.NotificationChannelPatch{Paused: &yes}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Auth.CreateAPIToken(ctx, "forever", nil); err != nil {
			t.Fatal(err)
		}
		notify, tokens := e.mustGet(t, "/settings/notifications"), e.mustGet(t, "/settings/tokens")
		if !strings.Contains(notify, `cv-conn-mark"><span class="cv-mark cv-mark-lg cv-mark-warning"`) || !strings.Contains(tokens, `cv-conn-mark"><span class="cv-mark cv-mark-lg cv-mark-warning"`) {
			t.Error("a paused channel or a token without an end date doesn't mark its row")
		}
		for name, page := range map[string]string{"notifications": notify, "tokens": tokens} {
			nav := regexp.MustCompile(`(?s)<nav aria-label="Settings".*?</nav>`).FindString(page)
			if strings.Contains(nav, "cv-mark-warning") || strings.Contains(nav, "cv-mark-failed") {
				t.Errorf("%s: the nav is marked for a chosen or chronic state:\n%s", name, nav)
			}
		}
		if !strings.Contains(text(settingsNavText(e.mustGet(t, "/settings"))), "Notifications1") || !strings.Contains(text(settingsNavText(e.mustGet(t, "/settings"))), "API tokens1") {
			t.Error("the nav doesn't fall back to the counts")
		}
	})

	t.Run("Update password opens Edit with the cursor in the password", func(t *testing.T) {
		if _, err := e.Svc.SaveRegistry(ctx, "quay.io", "bot", new("p")); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.SaveRegistryTest(ctx, "quay.io", store.RegistryTest{At: e.Clock.Now(), Message: "401"}); err != nil {
			t.Fatal(err)
		}
		focused := regexp.MustCompile(`<input[^>]*name="password"[^>]* autofocus`)
		if page := e.mustGet(t, "/settings/registries?edit=quay.io"); !focused.MatchString(page) {
			t.Errorf("?edit=quay.io doesn't focus the password:\n%s", text(page))
		}
		if page := e.mustGet(t, "/settings/registries"); focused.MatchString(page) {
			t.Error("a page that wasn't asked to open Edit focuses a password field")
		}
		// The nav's ■ for the refused one, until a save forgets the test.
		if page := e.mustGet(t, "/settings/registries"); !strings.Contains(page, `cv-mark cv-mark-failed" aria-hidden="true"></span><span class="sr-only">a registry refused its credentials`) {
			t.Error("a refused registry doesn't mark the nav with a square")
		}
		if resp, body := e.postForm(t, "/settings/registries", url.Values{"host": {"quay.io"}, "username": {"bot"}, "password": {"new"}, "edit": {"1"}}); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
		page := e.mustGet(t, "/settings/registries?registries=saved")
		if strings.Contains(page, "sr-only\">a registry refused") || !strings.Contains(text(page), "Saved. Test them to clear the warning.") || !strings.Contains(text(page), "changed · not tested since") {
			t.Errorf("after a save that changed the credentials:\n%s", text(page))
		}
	})

	t.Run("the narrow nav puts the current entry first and fades at the edge", func(t *testing.T) {
		_, css := e.get(t, "/static/app.css")
		css = strings.Join(strings.Fields(css), " ")
		for _, want := range []string{".cv-snav-group{display:contents}", ".cv-snav-item[aria-current=page]{order:0}", ".cv-snav-item{white-space:nowrap;flex:none;order:1}",
			"mask-image:linear-gradient("} {
			if !strings.Contains(css, want) {
				t.Errorf("the stylesheet lacks %q", want)
			}
		}
		// One HTML list for both orders: the wide nav keeps the contract's order, in its groups.
		got := settingsNavOf(t, e.mustGet(t, "/settings/tokens"))
		if strings.Join(got["Connections"], " ") != "Registries=/settings/registries Backup storage=/settings/backup-storage Notifications=/settings/notifications" || strings.Join(got["Security"], " ") != "API tokens=/settings/tokens*" {
			t.Errorf("the nav's order changed: %v", got)
		}
	})

	t.Run("the last five expired or revoked tokens, then Show older", func(t *testing.T) {
		later := e.Clock.Now().Add(1000 * 24 * time.Hour)
		for i := range 8 {
			made, err := e.Auth.CreateAPIToken(ctx, "old-"+strconv.Itoa(i), &later)
			if err != nil {
				t.Fatal(err)
			}
			e.Clock.Advance(time.Minute)
			if err := e.Auth.RevokeAPIToken(ctx, made.Info.ID); err != nil {
				t.Fatal(err)
			}
		}
		page := e.mustGet(t, "/settings/tokens")
		// old-7 .. old-3 were revoked last.
		for i := 3; i < 8; i++ {
			if !strings.Contains(page, `title="old-`+strconv.Itoa(i)+`">`) {
				t.Errorf("old-%d, one of the last five, isn't listed", i)
			}
		}
		for i := range 3 {
			if strings.Contains(page, `title="old-`+strconv.Itoa(i)+`">`) {
				t.Errorf("old-%d is listed before Show older", i)
			}
		}
		if !strings.Contains(page, `<a href="/settings/tokens?older=1" class="cv-conn-link">Show older (3 expired or revoked)</a>`) {
			t.Errorf("no Show older link:\n%s", text(page))
		}
		all := e.mustGet(t, "/settings/tokens?older=1")
		if strings.Count(all, `title="old-`) != 8 || strings.Contains(all, "Show older") {
			t.Errorf("?older=1 lists %d of the 8 revoked tokens, or still offers Show older", strings.Count(all, `title="old-`))
		}
	})
}

// settingsNavText is the Settings nav of a page, for reading its words.
func settingsNavText(body string) string {
	return regexp.MustCompile(`(?s)<nav aria-label="Settings" class="cv-snav">.*?</nav>`).FindString(body)
}
