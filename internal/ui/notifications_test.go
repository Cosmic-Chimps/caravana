package ui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// notifyTestSecret stands for the secret path of a Slack URL or an ntfy token:
// no page, redirect or log line may contain it.
const notifyTestSecret = "SENTINEL-notify-secret-0123"

// notifyReceiver is an httptest server that records what it is sent and answers
// with a status the test can change.
type notifyReceiver struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	reqs   []*http.Request
	bodies []string
}

func newNotifyReceiver(t *testing.T) *notifyReceiver {
	t.Helper()
	r := &notifyReceiver{status: http.StatusOK}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs, r.bodies = append(r.reqs, req), append(r.bodies, string(b))
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = io.WriteString(w, "receiver says no")
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *notifyReceiver) setStatus(s int) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *notifyReceiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// notifyForms returns the add form of the Notifications section (it posts to
// /settings/notifications) and every other form of a channel row, by action.
func notifyForms(page string) (add plainForm, byAction map[string][]plainForm) {
	byAction = map[string][]plainForm{}
	for _, f := range formsIn(page) {
		switch {
		case f.Action == "/settings/notifications":
			add = f
		case strings.HasPrefix(f.Action, "/settings/notifications/"):
			byAction[f.Action] = append(byAction[f.Action], f)
		}
	}
	return add, byAction
}

// notifyEditForm is the row's edit form among the forms that post to its ID: the one
// with a name field. Pause and Resume post to the same place with only "paused".
func notifyEditForm(t *testing.T, page, id string) plainForm {
	t.Helper()
	_, forms := notifyForms(page)
	for _, f := range forms["/settings/notifications/"+id] {
		if _, ok := f.Values["name"]; ok {
			return f
		}
	}
	t.Fatalf("no edit form for %s in:\n%s", id, page)
	return plainForm{}
}

// pauseForm is the row's Pause or Resume form: the one with "paused".
func pauseForm(t *testing.T, page, id string) plainForm {
	t.Helper()
	_, forms := notifyForms(page)
	for _, f := range forms["/settings/notifications/"+id] {
		if _, ok := f.Values["paused"]; ok {
			return f
		}
	}
	t.Fatalf("no pause form for %s in:\n%s", id, page)
	return plainForm{}
}

// serviceInput is a channel to create through the service, wanting problems and
// failed deploys.
func serviceInput(kind, name, address string) service.NotificationChannelInput {
	return service.NotificationChannelInput{Kind: kind, Name: name, URL: address,
		Events: []string{service.EventProblems, service.EventDeployFailed}}
}

func addValues(kind, name, address, token string, events ...string) url.Values {
	v := url.Values{"kind": {kind}, "name": {name}, "url": {address}, "token": {token}, "events": events}
	return v
}

// channelIDs reads the channels' IDs, oldest first.
func (e *consoleEnv) channelIDs(t *testing.T) []string {
	t.Helper()
	channels, err := e.Svc.NotificationChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range channels {
		ids = append(ids, c.ID)
	}
	return ids
}

func (e *consoleEnv) notifySecretItems(id string) map[string]string {
	return e.Nomad.Variable(deploy.NotificationVariablePath(id))
}

// noSecret fails the test when any of the outputs holds a secret.
func noSecret(t *testing.T, what string, secrets []string, outputs ...string) {
	t.Helper()
	for i, out := range outputs {
		for _, s := range secrets {
			if s != "" && strings.Contains(out, s) {
				t.Errorf("%s: output %d holds a secret", what, i)
			}
		}
	}
}

// US3: the section lists, adds, edits, pauses, tests and removes channels, as
// plain forms, and no secret is in a page, redirect or log line.
func TestSettingsNotifications(t *testing.T) {
	e := newConsoleEnv(t)
	okRcv, failRcv := newNotifyReceiver(t), newNotifyReceiver(t)
	failRcv.setStatus(http.StatusInternalServerError)
	secrets := []string{notifyTestSecret, "tk_" + notifyTestSecret}

	_, page := e.get(t, "/settings/notifications")
	got := text(page)
	for _, want := range []string{
		"Notifications",
		"Where Caravana tells you about problems and how deploys end.",
		"No channels yet. Add one and Caravana tells you when something needs you.",
		"ntfy: the topic URL, like https://ntfy.sh/my-topic. Slack and Discord: the incoming webhook URL. Webhook: where Caravana posts signed JSON.",
		"ntfy access token", "Only for a protected topic.",
		"Problems (opened and resolved)", "Failed deploys", "Successful deploys", "Add channel",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Settings lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(page, `<form method="post" action="/settings/notifications" id="add"`) || !strings.Contains(page, `placeholder="phone"`) {
		t.Error("the page lacks the add form's anchor or the name placeholder")
	}
	for _, opt := range []string{">ntfy</option>", ">Slack</option>", ">Discord</option>", ">Webhook</option>"} {
		if !strings.Contains(page, opt) {
			t.Errorf("the kind select lacks %s", opt)
		}
	}
	// Problems and failed deploys start checked; successful deploys does not.
	for value, checked := range map[string]bool{"problems": true, "deploy_failed": true, "deploy_succeeded": false} {
		re := regexp.MustCompile(`<input type="checkbox" name="events" value="` + value + `"( checked)?>`)
		m := re.FindStringSubmatch(page)
		if m == nil || (m[1] != "") != checked {
			t.Errorf("checkbox %s: found %v, want checked=%v", value, m, checked)
		}
	}
	add, _ := notifyForms(page)
	if add.Action == "" || add.Method != "post" {
		t.Fatalf("no add form in:\n%s", page)
	}

	t.Run("add ntfy with a token", func(t *testing.T) {
		resp, body := e.submit(t, add, addValues("ntfy", "phone", okRcv.URL+"/topic", "tk_"+notifyTestSecret, "problems", "deploy_failed"))
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/notifications?notifications=saved" {
			t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
		}
		_, page := e.get(t, "/settings/notifications?notifications=saved")
		got := text(page)
		// The topic is a secret like a Slack URL: the host is shown, the topic isn't.
		for _, want := range []string{"Saved.", "phone", "ntfy", strings.TrimPrefix(okRcv.URL, "http://") + "/••••", "nothing sent yet"} {
			if !strings.Contains(got, want) {
				t.Errorf("after the add the page lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(page, "/topic") {
			t.Errorf("the page shows the ntfy topic:\n%s", got)
		}
		// What a channel sends is the kind's tooltip, and ticked in its edit form.
		if !strings.Contains(page, `title="Problems, failed deploys"`) {
			t.Errorf("the row doesn't say what it sends:\n%s", got)
		}
		if got := e.notifySecretItems(e.channelIDs(t)[0]); got["token"] != "tk_"+notifyTestSecret {
			t.Error("the token wasn't kept in the Nomad Variable")
		}
		noSecret(t, "after add ntfy", secrets, body, page, e.Logs.String())
	})

	t.Run("add Slack: the URL is a secret and the address is masked", func(t *testing.T) {
		resp, body := e.submit(t, add, addValues("slack", "team", okRcv.URL+"/services/T000/B000/"+notifyTestSecret, "", "problems", "deploy_succeeded"))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, body)
		}
		_, page := e.get(t, "/settings/notifications")
		got := text(page)
		if !strings.Contains(page, `title="Problems, successful deploys"`) || !strings.Contains(got, "/services/T…/••••") {
			t.Errorf("the Slack row lacks its events or masked address:\n%s", got)
		}
		noSecret(t, "after add slack", secrets, body, page, e.Logs.String())
	})

	t.Run("add a webhook: the signing secret shows once", func(t *testing.T) {
		resp, body := e.submit(t, add, addValues("webhook", "ci", okRcv.URL+"/hook", "", "deploy_failed", "deploy_succeeded"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST = %d, want the page with the secret:\n%s", resp.StatusCode, text(body))
		}
		ids := e.channelIDs(t)
		secret := e.notifySecretItems(ids[2])["signing_secret"]
		if len(secret) != 64 {
			t.Fatalf("stored signing secret = %q", secret)
		}
		want := "Signing secret for ci. Copy it now: it isn't shown again. Receivers check each message's Caravana-Signature header with it."
		if !strings.Contains(text(body), want) || !strings.Contains(body, `<code class="cv-once-value" id="new-signing-secret">`+secret+`</code>`) {
			t.Errorf("the page lacks %q and the secret:\n%s", want, text(body))
		}
		_, page := e.get(t, "/settings/notifications")
		if strings.Contains(page, secret) {
			t.Error("a later page shows the signing secret again")
		}
		noSecret(t, "after add webhook", secrets, page, e.Logs.String())
		if strings.Contains(e.Logs.String(), secret) {
			t.Error("the log holds the signing secret")
		}
	})

	t.Run("refusals draw the page again with the error at its input and no secret", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			values  url.Values
			errID   string
			errText string
			kept    string // what the form must still hold
		}{
			{"not a URL", addValues("ntfy", "tablet", "topic", "", "problems"), "notify-add-url-error", "The URL must be an absolute http or https URL.", `value="tablet"`},
			{"no name", addValues("webhook", "", okRcv.URL, "", "problems"), "notify-add-name-error", "The name is required.", `value="` + okRcv.URL + `"`},
			{"nothing to send", addValues("webhook", "x", okRcv.URL, "", ""), "notify-add-events-error", "Pick at least one thing to send.", `value="x"`},
			{"a token on Slack", addValues("slack", "x", okRcv.URL+"/services/T0/B0/"+notifyTestSecret, "tk_"+notifyTestSecret, "problems"), "notify-add-token-error", "The token is only for an ntfy channel.", `value="x"`},
		} {
			before := len(e.channelIDs(t))
			resp, body := e.submit(t, add, tc.values)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="`+tc.errID+`"`) || !strings.Contains(text(body), tc.errText) || !strings.Contains(body, tc.kept) {
				t.Errorf("%s: POST = %d, want the page with %q at %s and %s kept:\n%s", tc.name, resp.StatusCode, tc.errText, tc.errID, tc.kept, text(body))
			}
			noSecret(t, tc.name, secrets, body)
			if after := len(e.channelIDs(t)); after != before {
				t.Errorf("%s: a refused form stored a channel", tc.name)
			}
		}
	})

	var ntfyID, slackID, hookID string
	ids := e.channelIDs(t)
	ntfyID, slackID, hookID = ids[0], ids[1], ids[2]

	t.Run("test: accepted, signed, announced and listed", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		f := formWith(t, page, "/settings/notifications/"+hookID+"/test")
		resp, body := e.submit(t, f, nil)
		want := "/settings/notifications?notified=" + url.QueryEscape(hookID)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != want {
			t.Fatalf("POST test = %d to %q, want 303 to %q:\n%s", resp.StatusCode, location(t, resp), want, body)
		}
		_, page = e.get(t, strings.TrimSuffix(location(t, resp), "#notifications"))
		got := text(page)
		for _, w := range []string{"ci accepted the test message.", "sent just now", "Recent deliveries", "Test message from Caravana"} {
			if !strings.Contains(got, w) {
				t.Errorf("after a test the page lacks %q:\n%s", w, got)
			}
		}
		if !strings.Contains(page, `role="status" class="cv-notice"`) {
			t.Error("an accepted test isn't a status notice")
		}

		// The receiver got a signed message, which verifies with the stored secret.
		okRcv.mu.Lock()
		last := okRcv.reqs[len(okRcv.reqs)-1]
		lastBody := okRcv.bodies[len(okRcv.bodies)-1]
		okRcv.mu.Unlock()
		secret := e.notifySecretItems(hookID)["signing_secret"]
		if err := notify.Verify(secret, last.Header.Get(notify.SignatureHeader), []byte(lastBody), time.Now(), time.Minute); err != nil {
			t.Errorf("the test message's signature doesn't verify: %v", err)
		}
		noSecret(t, "test", append(secrets, secret), body, page, e.Logs.String())
	})

	t.Run("test: a refusing receiver is announced with its status", func(t *testing.T) {
		// Point the Slack channel at the failing receiver, through its edit form.
		_, page := e.get(t, "/settings/notifications")
		resp, _ := e.submit(t, notifyEditForm(t, page, slackID), url.Values{"url": {failRcv.URL + "/services/T0/B0/" + notifyTestSecret}, "events": {"problems"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST edit = %d", resp.StatusCode)
		}
		_, page = e.get(t, "/settings/notifications")
		resp, body := e.submit(t, formWith(t, page, "/settings/notifications/"+slackID+"/test"), nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST test = %d:\n%s", resp.StatusCode, body)
		}
		_, page = e.get(t, strings.TrimSuffix(location(t, resp), "#notifications"))
		got := text(page)
		for _, w := range []string{"team didn't accept the test message: ", "500 Internal Server Error", "receiver says no", "failed just now", "failed (1 attempt)"} {
			if !strings.Contains(got, w) {
				t.Errorf("after a refused test the page lacks %q:\n%s", w, got)
			}
		}
		if !strings.Contains(page, `role="alert" class="cv-notice"`) {
			t.Error("a refused test isn't an alert")
		}
		noSecret(t, "refused test", secrets, body, page, e.Logs.String())
	})

	t.Run("edit keeps the secrets when the fields are empty", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		f := notifyEditForm(t, page, ntfyID)
		if f.Values.Get("name") != "phone" || f.Values.Get("url") != "" {
			t.Errorf("the ntfy edit form holds name %q and url %q, want the name and an empty URL: the topic is a secret", f.Values.Get("name"), f.Values.Get("url"))
		}
		if v, has := f.Values["token"]; !has || v[0] != "" {
			t.Errorf("the token field is %v, want present and empty", v)
		}
		if !strings.Contains(text(page), "A token is set: leave it empty to keep it.") {
			t.Error("the ntfy edit form lacks the token hint")
		}
		if got := notifyEditForm(t, page, slackID).Values.Get("url"); got != "" {
			t.Errorf("the Slack edit form's URL field is %q, want empty: the URL is a secret", got)
		}
		if !strings.Contains(text(page), "Leave empty to keep it.") {
			t.Error("the Slack edit form lacks its hint")
		}

		resp, body := e.submit(t, f, url.Values{"name": {"my phone"}, "events": {"deploy_failed"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST edit = %d:\n%s", resp.StatusCode, text(body))
		}
		if got := e.notifySecretItems(ntfyID)["token"]; got != "tk_"+notifyTestSecret {
			t.Error("an edit with an empty token field didn't keep the token")
		}
		_, page = e.get(t, "/settings/notifications")
		if got := text(page); !strings.Contains(got, "my phone") {
			t.Errorf("the edit didn't show:\n%s", got)
		}
		channels, err := e.Svc.NotificationChannels(context.Background())
		if err == nil && channels[0].Address != okRcv.URL+"/topic" {
			t.Errorf("an edit with an empty URL changed the ntfy topic to %q", channels[0].Address)
		}
		if err != nil || channels[0].Name != "my phone" || len(channels[0].Events) != 1 || channels[0].Events[0] != "deploy_failed" {
			t.Errorf("the edit stored %+v, %v; want the new name and only failed deploys", channels[0], err)
		}
		// Slack's URL, left empty, is still there.
		if got := e.notifySecretItems(slackID)["url"]; !strings.HasSuffix(got, notifyTestSecret) {
			t.Error("an edit with an empty URL didn't keep the Slack URL")
		}

		// A refused edit shows that row's form open, with the error under its field.
		resp, body = e.submit(t, notifyEditForm(t, page, ntfyID), url.Values{"name": {""}, "events": {"problems"}})
		if resp.StatusCode != http.StatusOK || !regexp.MustCompile(`id="notify-edit-\d+-name-error"`).MatchString(body) ||
			!regexp.MustCompile(`<details class="cv-confirm cv-confirm-row cv-confirm-wide" name="conn-\d+" open>`).MatchString(body) {
			t.Errorf("a refused edit = %d, want the page with the edit form open and the error under the name:\n%s", resp.StatusCode, text(body))
		}
		noSecret(t, "edit", secrets, body, page, e.Logs.String())
	})

	t.Run("a refused Slack edit doesn't give the typed URL back", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		resp, body := e.submit(t, notifyEditForm(t, page, slackID), url.Values{"name": {""}, "url": {"https://hooks.slack.com/services/T1/B1/" + notifyTestSecret}, "events": {"problems"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST edit = %d", resp.StatusCode)
		}
		noSecret(t, "refused slack edit", secrets, body)
	})

	t.Run("pause and resume", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		resp, body := e.submit(t, pauseForm(t, page, hookID), nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST pause = %d:\n%s", resp.StatusCode, body)
		}
		_, page = e.get(t, "/settings/notifications")
		if got := text(page); !strings.Contains(page, `<span class="cv-conn-paused">paused</span>`) || !strings.Contains(got, "Resume") {
			t.Errorf("a paused channel isn't marked:\n%s", got)
		}
		// Pausing left its other settings alone.
		if got := e.notifySecretItems(hookID)["signing_secret"]; len(got) != 64 {
			t.Error("pausing lost the signing secret")
		}
		resp, _ = e.submit(t, pauseForm(t, page, hookID), nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST resume = %d", resp.StatusCode)
		}
		_, page = e.get(t, "/settings/notifications")
		if strings.Contains(page, `cv-conn-paused`) {
			t.Error("a resumed channel is still marked paused")
		}
	})

	t.Run("a new signing secret shows once and replaces the old", func(t *testing.T) {
		old := e.notifySecretItems(hookID)["signing_secret"]
		_, page := e.get(t, "/settings/notifications")
		if !strings.Contains(text(page), "The old secret stops working at once.") {
			t.Error("the rotate confirmation lacks its sentence")
		}
		resp, body := e.submit(t, formWith(t, page, "/settings/notifications/"+hookID+"/secret"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST secret = %d:\n%s", resp.StatusCode, text(body))
		}
		fresh := e.notifySecretItems(hookID)["signing_secret"]
		if fresh == old || len(fresh) != 64 {
			t.Fatalf("the secret wasn't replaced: %q", fresh)
		}
		if !strings.Contains(text(body), "Signing secret for ci. Copy it now: it isn't shown again.") || !strings.Contains(body, `id="new-signing-secret">`+fresh+`</code>`) {
			t.Errorf("the page lacks the new secret:\n%s", text(body))
		}
		_, page = e.get(t, "/settings/notifications")
		noSecret(t, "after rotate", []string{old, fresh}, page, e.Logs.String())
	})

	t.Run("only a webhook has the rotate form", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		_, forms := notifyForms(page)
		for _, id := range []string{ntfyID, slackID} {
			if len(forms["/settings/notifications/"+id+"/secret"]) != 0 {
				t.Errorf("channel %s offers a signing secret", id)
			}
		}
		// A rotate posted for another kind goes back to the page and changes nothing.
		before := e.notifySecretItems(ntfyID)["token"]
		resp, _ := e.postForm(t, "/settings/notifications/"+ntfyID+"/secret", nil)
		if resp.StatusCode != http.StatusSeeOther || e.notifySecretItems(ntfyID)["token"] != before {
			t.Errorf("rotate of an ntfy channel = %d, token kept %v", resp.StatusCode, e.notifySecretItems(ntfyID)["token"] == before)
		}
	})

	t.Run("remove asks first and forgets the channel and its secret", func(t *testing.T) {
		_, page := e.get(t, "/settings/notifications")
		if !strings.Contains(text(page), "Messages stop at once. Deliveries already listed are removed with it.") {
			t.Errorf("the Remove confirmation lacks its sentence:\n%s", text(page))
		}
		resp, body := e.submit(t, formWith(t, page, "/settings/notifications/"+slackID+"/remove"), nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/notifications" {
			t.Fatalf("POST remove = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if e.notifySecretItems(slackID) != nil {
			t.Error("the secret is still in Nomad")
		}
		_, page = e.get(t, "/settings/notifications")
		if strings.Contains(text(page), "team") && strings.Contains(page, "/settings/notifications/"+slackID) {
			t.Error("the row is still listed")
		}
		// A channel that isn't stored is not an error.
		if resp, _ := e.postForm(t, "/settings/notifications/"+slackID+"/remove", nil); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("POST remove of an unknown channel = %d, want 303", resp.StatusCode)
		}
	})

	// FR-008, SC-005: after all of it, no secret is in the log.
	noSecret(t, "the log", secrets, e.Logs.String())
}

// Each channel's Recent deliveries lists what was sent, newest first, with a
// failure's attempts and reason.
func TestSettingsNotificationDeliveriesList(t *testing.T) {
	e := newConsoleEnv(t)
	rcv := newNotifyReceiver(t)
	created, err := e.Svc.CreateNotificationChannel(context.Background(), serviceInput("webhook", "ci", rcv.URL+"/h"))
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		rcv.setStatus(status)
		if _, err := e.Svc.TestNotificationChannel(context.Background(), created.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, page := e.get(t, "/settings/notifications")
	got := text(page)
	failed := strings.Index(got, "failed (1 attempt): ")
	sent := strings.Index(got, "Test message from Caravana sent")
	if failed < 0 || sent < 0 || failed > sent {
		t.Errorf("the deliveries aren't listed newest first (failed at %d, sent at %d):\n%s", failed, sent, got)
	}
	// The last delivery cell says what the newest one did.
	if !strings.Contains(got, "failed just now") {
		t.Errorf("the Last delivery cell lacks the failure:\n%s", got)
	}
}

// The posts of the section are protected like every other: a cross-site post is
// refused and changes nothing, and without a session they go to /login.
func TestNotificationPostsAreProtected(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	rcv := newNotifyReceiver(t)
	created, err := e.Svc.CreateNotificationChannel(ctx, serviceInput("webhook", "ci", rcv.URL+"/h"))
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"kind": {"webhook"}, "name": {"forged"}, "url": {rcv.URL}, "events": {"problems"}, "paused": {"1"}}
	id := created.ID
	for _, path := range []string{
		"/settings/notifications", "/settings/notifications/" + id, "/settings/notifications/" + id + "/test",
		"/settings/notifications/" + id + "/secret", "/settings/notifications/" + id + "/remove",
	} {
		resp, body := e.do(t, request{method: http.MethodPost, path: path, form: form, cookies: []*http.Cookie{e.Cookie},
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
			t.Errorf("cross-site POST %s = %d, want the 403 page", path, resp.StatusCode)
		}
		resp, _ = e.do(t, request{method: http.MethodPost, path: path, form: form, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("POST %s without a session = %d to %q, want a redirect to /login", path, resp.StatusCode, location(t, resp))
		}
	}
	channels, _ := e.Svc.NotificationChannels(ctx)
	if len(channels) != 1 || channels[0].Name != "ci" || channels[0].Paused {
		t.Errorf("a refused post changed the channels: %+v", channels)
	}
	if rcv.count() != 0 {
		t.Error("a refused post sent a message")
	}
}
