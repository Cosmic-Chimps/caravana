package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// schedFilterAll lists every schedule.
var schedFilterAll = service.ScheduleFilter{}

// scheduleForm is a complete New schedule form, for the posts to change one field of.
func scheduleForm(set url.Values) url.Values {
	f := url.Values{"name": {"tick"}, "app": {"whoami"}, "command": {"date"}, "when": {"every day at 03:00"},
		"stopAfter": {"1800"}, "memory": {"256"}, "skip": {"1"}, "skipChoice": {"1"}, "action": {"create"}}
	for k, v := range set {
		f[k] = v
	}
	return f
}

// The blank form: the apps (a stopped one disabled), every app's services as "app › service"
// pairs until an app is picked, the presets as links, the hints, and the limits' defaults.
func TestNewScheduleForm(t *testing.T) {
	e := schedEnv(t)
	if err := e.Store.SetAppStopped(context.Background(), "blog", new(time.Now())); err != nil {
		t.Fatal(err)
	}
	body := e.pageOf(t, "/schedules/new")
	for _, want := range []string{
		`<option value="" selected>Choose an app</option>`, `<option value="blog" disabled>blog (stopped)</option>`, `<option value="whoami">whoami</option>`,
		`<option value="whoami/web">whoami › web</option>`, `<option value="blog/worker" disabled>blog › worker</option>`,
		`<a href="/schedules/new?preset=15min" class="cv-schip">every 15 minutes</a>`, `href="/schedules/new?preset=daily"`,
		`href="/schedules/new?preset=weekdays"`, `href="/schedules/new?preset=monthly"`,
		`<p class="cv-setrow-hint" id="schedule-when-hint" data-local-hint="schedule-when">Times of day are UTC.</p>`,
		`<option value="1800" selected>30 min</option>`, `<option value="256" selected>256 MB</option>`,
		`<input type="checkbox" name="skip" value="1" checked>`, `name="action" value="check"`, `name="action" value="create"`,
		`<a href="/schedules" class="cv-btn cv-btn-secondary cv-btn-sm">Cancel</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %s", want)
		}
	}
	for _, want := range []string{"New schedule", "Runs a command in a fresh copy of an app, then stops it.",
		"Uses the live version's image and variables. Doesn't take traffic.", "no shell is implied", "sh -c '…'",
		"Skip a run if the last one is still going", "Create schedule"} {
		if !strings.Contains(text(body), want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if strings.Contains(body, "cv-spreview") {
		t.Error("a blank form has a preview")
	}

	// ?app= picks the app: an app with one service has no service select; one with several lists its own.
	if body := e.pageOf(t, "/schedules/new?app=whoami"); strings.Contains(body, `name="service"`) || strings.Contains(body, "Choose an app") ||
		!strings.Contains(body, `<option value="whoami" selected>whoami</option>`) || !strings.Contains(body, `href="/schedules/new?app=whoami&amp;preset=daily"`) {
		t.Errorf("?app=whoami:\n%s", body)
	}
	if err := e.Store.SetAppStopped(context.Background(), "blog", nil); err != nil {
		t.Fatal(err)
	}
	body = e.pageOf(t, "/schedules/new?app=blog")
	if !strings.Contains(body, `<option value="web" selected>web</option>`) || !strings.Contains(body, `<option value="worker">worker</option>`) ||
		strings.Contains(body, "whoami › web") {
		t.Errorf("?app=blog:\n%s", body)
	}
	// Every service of the app mounts a volume: the hint says a schedule can't run in it.
	e.seed(t, kumaApp(), seedDeploy{ID: "k1", Status: store.StatusHealthy, Version: 1})
	if got := text(e.pageOf(t, "/schedules/new?app=kuma")); !strings.Contains(got, "Every service of this app mounts a volume, and a schedule needs a service without volumes.") {
		t.Errorf("the volumes hint:\n%s", got)
	}
}

// Each preset fills the field, is drawn chosen, and shows its preview: the cron line, the next three
// runs as clock times and "UTC" once, the timezone.
func TestScheduleFormPresets(t *testing.T) {
	e := schedEnv(t)
	for key, cron := range map[string]string{"15min": "*/15 * * * *", "daily": "0 3 * * *", "weekdays": "0 9 * * 1-5", "monthly": "0 6 1 * *"} {
		body := e.pageOf(t, "/schedules/new?app=whoami&preset="+key)
		if !strings.Contains(body, `value="`+presetWords(key)+`"`) || !strings.Contains(body, `class="cv-schip cv-schip-chosen" aria-current="true">`+presetWords(key)) {
			t.Errorf("preset %s didn't fill the field or wasn't chosen", key)
		}
		if !strings.Contains(body, `<dd class="cv-spreview-cron">`+cron+`</dd>`) || !strings.Contains(body, `<dt>timezone</dt><dd>UTC</dd>`) {
			t.Errorf("preset %s's preview:\n%s", key, text(body))
		}
		group := regexp.MustCompile(`(?s)<span class="cv-clock-group" data-clock-group>(.*?)</span>`).FindStringSubmatch(body)
		if group == nil || strings.Count(group[1], `<time class="cv-clock"`) != 3 || !strings.HasSuffix(group[1], " UTC") {
			t.Errorf("preset %s's next runs: %v", key, group)
		}
		checkClocks(t, "the form", body)
	}
}

// Check draws the preview of what was typed and saves nothing: a cron line, words it can't read
// (the warning line in the box), and a line that never fires (under the field).
func TestScheduleFormCheck(t *testing.T) {
	e := schedEnv(t)
	resp, body := e.postForm(t, "/schedules", scheduleForm(url.Values{"action": {"check"}, "when": {"5 4 * * 1"}}))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<dd class="cv-spreview-cron">5 4 * * 1</dd>`) {
		t.Errorf("Check of a cron line = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/schedules", scheduleForm(url.Values{"action": {"check"}, "when": {"whenever you like"}}))
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), `Couldn't read that. Try "every 2 hours" or paste a cron line.`) ||
		!strings.Contains(body, `class="cv-spreview-warn" id="schedule-when-error"`) {
		t.Errorf("Check of words it can't read = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/schedules", scheduleForm(url.Values{"action": {"check"}, "when": {"0 0 31 2 *"}}))
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "That time never comes") || strings.Contains(body, "cv-spreview") {
		t.Errorf("Check of a line that never fires = %d:\n%s", resp.StatusCode, text(body))
	}
	if list, _ := e.Svc.ListSchedules(context.Background(), schedFilterAll); len(list) != 0 {
		t.Errorf("Check saved %d schedules", len(list))
	}
}

// Each refusal draws the form again with the reason under its field, and saves nothing.
func TestScheduleFormRefusals(t *testing.T) {
	e := backupsEnv(t, true) // duo: web with a Postgres backup (so web-backup is a built-in's name), and worker
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Status: store.StatusHealthy, Version: 7})
	e.seed(t, kumaApp(), seedDeploy{ID: "k1", Status: store.StatusHealthy, Version: 1})
	e.schedule(t, "whoami", "taken", "0 3 * * *", "date")
	tests := []struct {
		name   string
		set    url.Values
		status int
		want   []string
	}{
		{"no name", url.Values{"name": {""}, "command": {"node scripts/sync-rates.js"}}, 422,
			[]string{`value="sync-rates"`, "Enter a name for the schedule; sync-rates is made from the command."}},
		{"no app", url.Values{"app": {""}}, 422, []string{"Choose the app the command runs in."}},
		{"a quote left open", url.Values{"command": {`echo "hi`}}, 422, []string{`A " quote isn't closed: add the closing "`}},
		{"no command", url.Values{"command": {""}}, 422, []string{"Enter the command to run."}},
		{"words it can't read", url.Values{"when": {"now and then"}}, 422, []string{`Couldn't read that. Try "every 2 hours" or paste a cron line.`}},
		{"a line that never fires", url.Values{"when": {"0 0 31 2 *"}}, 422, []string{"That time never comes"}},
		{"more than once a minute", url.Values{"when": {"* * * * * *"}}, 422, []string{"five fields"}},
		{"a built-in's name", url.Values{"app": {"duo"}, "service": {"worker"}, "name": {"web-backup"}}, 422,
			[]string{"web-backup is the name of web's backup, a built-in schedule: choose another."}},
		{"a name the app has", url.Values{"name": {"taken"}}, 409, []string{"The app already has a schedule named taken: choose another name."}},
		{"a bad name", url.Values{"name": {"Not_A_Label"}}, 422, []string{"is not a lowercase DNS label"}},
		{"a service with a volume", url.Values{"app": {"kuma"}}, 422, []string{"The service mounts a volume; a schedule needs a service without volumes."}},
		{"another app's service", url.Values{"app": {"whoami"}, "service": {"duo/worker"}}, 422,
			[]string{"That service is another app's: pick one of the chosen app's services."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.postForm(t, "/schedules", scheduleForm(tt.set))
			if resp.StatusCode != tt.status {
				t.Errorf("status = %d, want %d:\n%s", resp.StatusCode, tt.status, text(body))
			}
			for _, want := range tt.want {
				if !strings.Contains(text(body), want) && !strings.Contains(body, want) {
					t.Errorf("the form lacks %q:\n%s", want, text(body))
				}
			}
			if !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, "autofocus") && !strings.Contains(body, `role="alert"`) {
				t.Error("the field at fault isn't marked")
			}
		})
	}
	if list, _ := e.Svc.ListSchedules(context.Background(), schedFilterAll); len(list) != 3 { // taken, and the built-ins of duo and kuma
		t.Errorf("the refusals saved something: %+v", list)
	}
}

// A created schedule lands on its row, open, with a notice; a service picked as "app › service" without
// an app is that app's.
func TestScheduleFormCreate(t *testing.T) {
	e := schedEnv(t)
	resp, body := e.postForm(t, "/schedules", scheduleForm(url.Values{"name": {"nightly"}, "command": {"sh -c 'echo hi'"}, "stopAfter": {"300"},
		"memory": {"512"}, "skip": nil}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create = %d:\n%s", resp.StatusCode, text(body))
	}
	list, err := e.Svc.ListSchedules(context.Background(), schedFilterAll)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSchedules = %v, %v", list, err)
	}
	sc := list[0]
	if location(t, resp) != "/schedules?created="+sc.ID+"#schedule-"+sc.ID {
		t.Errorf("Location = %q", location(t, resp))
	}
	if sc.Cron != "0 3 * * *" || strings.Join(sc.Command, "|") != "sh|-c|echo hi" || sc.StopAfterSeconds != 300 || sc.MemoryMB != 512 || sc.SkipIfRunning {
		t.Errorf("created %+v", sc)
	}
	body = e.pageOf(t, location(t, resp))
	if !strings.Contains(text(body), "nightly was created. Its first run is at") || !strings.Contains(rowOf(body, sc.ID), ` open>`) {
		t.Errorf("after create:\n%s", text(body))
	}
	if !strings.Contains(text(rowOf(body, sc.ID)), "start another") {
		t.Error("an unticked skip box should allow overlapping runs")
	}

	resp, body = e.postForm(t, "/schedules", scheduleForm(url.Values{"app": {""}, "service": {"blog/worker"}, "name": {"work"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create with a pair = %d:\n%s", resp.StatusCode, text(body))
	}
	list, _ = e.Svc.ListSchedules(context.Background(), schedFilterAll)
	found := false
	for _, s := range list {
		found = found || s.Name == "work" && s.App == "blog" && s.Service == "worker"
	}
	if !found {
		t.Errorf("the pair didn't pick blog's worker: %+v", list)
	}
}

// Edit: the form filled with the schedule (the words when they read back as its line), only its app,
// Save; a save lands on the row with a notice.
func TestScheduleFormEdit(t *testing.T) {
	e := schedEnv(t)
	sc := e.schedule(t, "blog", "nightly", "0 3 * * *", "node", "scripts/sync-rates.js")
	odd := e.schedule(t, "whoami", "odd", "0 3 */2 * *", "date")
	body := e.pageOf(t, "/schedules/"+sc.ID+"/edit")
	for _, want := range []string{`value="nightly"`, `>node scripts/sync-rates.js</textarea>`, `value="every day at 03:00"`,
		`<option value="300" selected>5 min</option>`, `<input type="checkbox" name="skip" value="1" checked>`,
		`<form method="post" action="/schedules/` + sc.ID + `"`, `name="action" value="save"`,
		`<input type="hidden" name="app" value="blog">`, `<input type="hidden" name="service" value="web">`,
		`<dd class="cv-spreview-cron">0 3 * * *</dd>`, `href="/schedules/` + sc.ID + `/edit?preset=daily"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the edit form lacks %s", want)
		}
	}
	// Run inside can't change: text ("web of blog"), not selects.
	if strings.Contains(body, `id="schedule-app"`) || strings.Contains(body, `id="schedule-service"`) ||
		!strings.Contains(body, `<p class="cv-sfixed" aria-labelledby="schedule-app-label">`) || !strings.Contains(text(body), "web of blog") {
		t.Errorf("the edit form's Run inside:\n%s", text(body))
	}
	if strings.Contains(body, `value="whoami"`) || !strings.Contains(text(body), "Edit nightly") || !strings.Contains(text(body), "Save schedule") {
		t.Errorf("the edit form's app or title:\n%s", text(body))
	}
	if body := e.pageOf(t, "/schedules/"+odd.ID+"/edit"); !strings.Contains(body, `value="0 3 */2 * *"`) {
		t.Error("a line without words isn't shown as itself")
	}

	resp, body := e.postForm(t, "/schedules/"+sc.ID, url.Values{"name": {"nightly"}, "app": {"blog"}, "service": {"worker"}, "command": {"date"},
		"when": {"weekdays at 09:00"}, "stopAfter": {"3600"}, "memory": {"1024"}, "skipChoice": {"1"}, "skip": {"1"}, "action": {"save"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/schedules?saved="+sc.ID+"#schedule-"+sc.ID {
		t.Fatalf("save = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	got, err := e.Svc.GetSchedule(context.Background(), sc.ID)
	if err != nil || got.Cron != "0 9 * * 1-5" || got.Service != "worker" || got.MemoryMB != 1024 || got.StopAfterSeconds != 3600 {
		t.Errorf("saved %+v, %v", got, err)
	}
	if body := e.pageOf(t, location(t, resp)); !strings.Contains(text(body), "nightly was saved.") || !strings.Contains(rowOf(body, sc.ID), ` open>`) {
		t.Errorf("after save:\n%s", text(body))
	}
	if resp, _ := e.postForm(t, "/schedules/"+sc.ID, url.Values{"name": {"nightly"}, "command": {"date"}, "when": {"every hour"}, "action": {"check"}}); resp.StatusCode != http.StatusOK {
		t.Errorf("Check on the edit form = %d", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/schedules/nope/edit"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("edit of an unknown schedule = %d", resp.StatusCode)
	}
}

// The form's own pages say nothing of the app's definition that a page shouldn't: it lists apps by
// name, and an app without a definition can't be picked.
func TestScheduleFormListsDefinedApps(t *testing.T) {
	e := schedEnv(t)
	if _, err := e.Svc.CreateAppOnly(context.Background(), app.App{ID: "empty", Source: app.Source{Kind: app.SourceImage}}); err != nil {
		t.Fatal(err)
	}
	if body := e.pageOf(t, "/schedules/new"); strings.Contains(body, `value="empty"`) {
		t.Error("an app with no definition is offered")
	}
}
