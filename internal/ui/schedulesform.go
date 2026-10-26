package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/cronwords"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// New schedule and Edit (feature 022): one form, a page of setting rows. The
// When field takes words or a cron line, in UTC; its four presets are links
// that fill it, and "Check" redraws the form with the preview (the cron line
// and the next three runs). Without script the service select lists every
// app's services as "app › service"; once an app is picked (the address's
// ?app=, a Check or a refused submit) it lists that app's. A refused submit
// draws the form again with the reason under the field.

// schedulePresets are the When field's presets, as their links name them.
var schedulePresets = []struct{ Key, Words string }{
	{"15min", "every 15 minutes"},
	{"daily", "every day at 03:00"},
	{"weekdays", "weekdays at 09:00"},
	{"monthly", "first of the month"},
}

// The Limits' choices (contracts/api.md).
var (
	stopAfterChoices = []struct {
		Seconds int
		Label   string
	}{{300, "5 min"}, {1800, "30 min"}, {3600, "1 h"}, {7200, "2 h"}}
	memoryChoices = []int{256, 512, 1024, 2048}
)

const (
	defaultStopAfter = 1800
	// maxScheduleName is the longest name a schedule may have.
	maxScheduleName = 31
	// pairSep joins an app and a service in the service select's values while
	// no app is picked: app IDs and service names have no "/".
	pairSep = "/"
)

// scheduleInput is what the form posted (or the address asked for).
type scheduleInput struct {
	Name, App, Service, Command, When string
	StopAfter, Memory                 int
	Skip                              bool
}

// schedForm is the form as the page draws it.
type schedForm struct {
	Action  string // where it posts
	Editing bool
	Title   sentence // "New schedule", "Edit `rates-sync`"
	// Name, App, Service, Command and When are the fields with what was typed
	// and their errors.
	Name, App, Service, Command, When formField
	Apps                              []formOption
	// Fixed is "web of clock" in mono, drawn instead of the app and service
	// selects when the schedule exists (Edit): it can't move to another app.
	// Hidden inputs carry the two values; empty for a new schedule.
	Fixed    sentence
	FixedApp string
	FixedSvc string
	// Services is the service select: the picked app's services when it has
	// several (ServiceShown), or every app's as "app › service" (Pairs).
	Services     []formOption
	ServiceShown bool
	Pairs        bool
	// VolumesHint says every service of the picked app mounts a volume.
	VolumesHint bool
	Presets     []presetLink
	Preview     *previewBox
	StopAfter   []formOption
	Memory      []formOption
	Skip        bool
	// Notice is a refusal that isn't about one field.
	Notice sentence
	Submit string // "Create schedule", "Save schedule"
	Verb   string // the submit's action value: create or save
}

// formOption is one option of a select.
type formOption struct {
	Value, Label       string
	Selected, Disabled bool
}

// presetLink is a preset of the When field: a link that fills it.
type presetLink struct {
	Label, Href string
	Chosen      bool
}

// previewBox is what the When field reads as: the cron line, its next three
// runs (UTC) and the timezone; or, for text it can't read, the warning line.
type previewBox struct {
	Cron       string
	Next       []clockTime
	Unreadable bool
	// Alert says the warning line is the form's first error, announced.
	Alert bool
}

// readScheduleInput reads the posted form. The skip box sends nothing when it
// isn't ticked, so the form also sends skipChoice to tell that apart from a
// form that never had the box.
func readScheduleInput(v url.Values) scheduleInput {
	in := scheduleInput{Name: strings.TrimSpace(v.Get("name")), App: strings.TrimSpace(v.Get("app")),
		Service: strings.TrimSpace(v.Get("service")), Command: strings.TrimSpace(v.Get("command")),
		When: strings.TrimSpace(v.Get("when")), Skip: v.Get("skip") != "" || v.Get("skipChoice") == ""}
	in.StopAfter, _ = strconv.Atoi(v.Get("stopAfter"))
	in.Memory, _ = strconv.Atoi(v.Get("memory"))
	return in
}

// presetWords is the words of the preset key names, or "".
func presetWords(key string) string {
	for _, p := range schedulePresets {
		if p.Key == key {
			return p.Words
		}
	}
	return ""
}

// defaultScheduleName is a name made from the command: the last path segment
// of its last word, without its extension, in the name's alphabet
// ("node scripts/sync-rates.js" is "sync-rates"). "" when nothing is left.
func defaultScheduleName(command []string) string {
	if len(command) == 0 {
		return ""
	}
	base := path.Base(strings.TrimRight(command[len(command)-1], "/"))
	base = strings.TrimSuffix(base, path.Ext(base))
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	if len(name) > maxScheduleName {
		name = strings.TrimRight(name[:maxScheduleName], "-")
	}
	return name
}

// roundedMemory is a reservation rounded up to a memory choice, the largest
// for one above them all.
func roundedMemory(mb int) int {
	for _, c := range memoryChoices {
		if mb <= c {
			return c
		}
	}
	return memoryChoices[len(memoryChoices)-1]
}

// previewOf reads the When field: its cron line and next three runs, or the
// warning line for text that is neither words nor a cron line. A line that
// reads but can't be a schedule (it never fires, or fires more than once a
// minute) is err, for the field; the box then shows nothing.
func previewOf(s *server, when string) (*previewBox, string, error) {
	if when == "" {
		return nil, "", nil
	}
	cron, err := cronwords.Parse(when)
	switch {
	case errors.Is(err, cronwords.ErrUnreadable):
		return &previewBox{Unreadable: true}, "", err
	case err != nil:
		return nil, "", err
	}
	now := s.now()
	box := &previewBox{Cron: cron}
	for _, t := range cronwords.Next(cron, now, 3) {
		box.Next = append(box.Next, clockCompactOf(now, t))
	}
	return box, cron, nil
}

// formApp is an app the form can list: its record, and whether it is stopped
// (listed, but not to be picked).
type formApp struct {
	rec     store.AppRecord
	stopped bool
}

// buildScheduleForm draws the form for in. picked says an app was picked (the
// address's ?app=, an edit, a Check or a refused submit), so the service
// select lists that app's services rather than every app's.
func (s *server) buildScheduleForm(ctx context.Context, in scheduleInput, picked bool) (schedForm, error) {
	sums, err := s.svc.ListApps(ctx)
	if err != nil {
		return schedForm{}, err
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return schedForm{}, err
	}
	var apps []formApp
	for _, sum := range sums {
		if sum.App.Defined() {
			apps = append(apps, formApp{rec: sum.AppRecord, stopped: sum.StoppedAt != nil})
		}
	}
	f := schedForm{Action: "/schedules", Title: plainS("New schedule"), Submit: "Create schedule", Verb: "create", Skip: in.Skip,
		Name: formField{ID: "schedule-name", Value: in.Name}, App: formField{ID: "schedule-app", Value: in.App},
		Service: formField{ID: "schedule-service", Value: in.Service}, Command: formField{ID: "schedule-command", Value: in.Command},
		When: formField{ID: "schedule-when", Value: in.When}}

	// One app with services to pick from: it is the app.
	if in.App == "" && len(apps) == 1 {
		in.App, f.App.Value, picked = apps[0].rec.App.ID, apps[0].rec.App.ID, true
	}
	chosen := slices.IndexFunc(apps, func(a formApp) bool { return a.rec.App.ID == in.App })
	if !picked || chosen < 0 {
		f.Apps = append(f.Apps, formOption{Value: "", Label: "Choose an app", Selected: chosen < 0})
	}
	for i, a := range apps {
		label := pl.appLabel(a.rec)
		if a.stopped {
			label += " (stopped)"
		}
		f.Apps = append(f.Apps, formOption{Value: a.rec.App.ID, Label: label, Selected: i == chosen, Disabled: a.stopped})
	}

	memory := 0
	switch {
	case picked && chosen >= 0:
		svcs := apps[chosen].rec.App.Services
		usable := slices.IndexFunc(svcs, func(svc app.Service) bool { return len(svc.Volumes) == 0 })
		f.VolumesHint = usable < 0
		svcName := in.Service
		if _, after, ok := strings.Cut(svcName, pairSep); ok {
			svcName = after
		}
		if svcName == "" && usable >= 0 {
			svcName = svcs[usable].Name
		}
		if len(svcs) > 1 {
			f.ServiceShown = true
			for _, svc := range svcs {
				label := svc.Name
				if len(svc.Volumes) > 0 {
					label += " (mounts a volume)"
				}
				f.Services = append(f.Services, formOption{Value: svc.Name, Label: label, Selected: svc.Name == svcName, Disabled: len(svc.Volumes) > 0})
			}
		}
		if i := slices.IndexFunc(svcs, func(svc app.Service) bool { return svc.Name == svcName }); i >= 0 {
			memory = svcs[i].Resources.MemoryMB
		}
	case len(apps) > 1:
		f.ServiceShown, f.Pairs = true, true
		f.Services = append(f.Services, formOption{Value: "", Label: "The app's first service", Selected: in.Service == ""})
		for _, a := range apps {
			for _, svc := range a.rec.App.Services {
				v := a.rec.App.ID + pairSep + svc.Name
				f.Services = append(f.Services, formOption{Value: v, Label: pl.appLabel(a.rec) + " › " + svc.Name, Selected: in.Service == v,
					Disabled: a.stopped || len(svc.Volumes) > 0})
			}
		}
	}

	stop := in.StopAfter
	if stop == 0 {
		stop = defaultStopAfter
	}
	for _, c := range stopAfterChoices {
		f.StopAfter = append(f.StopAfter, formOption{Value: strconv.Itoa(c.Seconds), Label: c.Label, Selected: c.Seconds == stop})
	}
	mem := in.Memory
	if mem == 0 {
		// The service's reservation rounded up; 256 MB when it sets none,
		// the installation's default reservation unless it was changed.
		mem = roundedMemory(memory)
	}
	for _, mb := range memoryChoices {
		f.Memory = append(f.Memory, formOption{Value: strconv.Itoa(mb), Label: memoryText(mb), Selected: mb == mem})
	}
	return f, nil
}

// presetsFor are the preset links of a form at base (its own address, with
// the app it was opened for), the one the field holds chosen.
func presetsFor(base url.Values, page, when string) []presetLink {
	var out []presetLink
	for _, p := range schedulePresets {
		v := url.Values{}
		for k, vs := range base {
			v[k] = vs
		}
		v.Set("preset", p.Key)
		out = append(out, presetLink{Label: p.Words, Href: page + "?" + v.Encode(), Chosen: strings.EqualFold(strings.Join(strings.Fields(when), " "), p.Words)})
	}
	return out
}

// renderScheduleForm draws the form page.
func (s *server) renderScheduleForm(w http.ResponseWriter, r *http.Request, status int, f schedForm, crumbs ...crumb) {
	title := "New schedule"
	if f.Editing {
		title = "Edit " + f.Name.Value
	}
	head, err := s.shellPage(r, title, navSchedules, append([]crumb{{Label: "Schedules", Href: "/schedules"}}, crumbs...)...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, status, "schedules", schedulesPage{pageData: head, Form: &f})
}

// showNewSchedule answers GET /schedules/new: the form, with the app of
// ?app= picked and the When field filled by ?preset=.
func (s *server) showNewSchedule(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	in := scheduleInput{App: q.Get("app"), Skip: true, When: presetWords(q.Get("preset"))}
	f, err := s.buildScheduleForm(r.Context(), in, in.App != "")
	if err != nil {
		s.serverError(w, "list apps", err)
		return
	}
	base := url.Values{}
	if in.App != "" {
		base.Set("app", in.App)
	}
	f.Presets = presetsFor(base, "/schedules/new", in.When)
	if in.When != "" {
		f.Preview, _, _ = previewOf(s, in.When)
	}
	s.renderScheduleForm(w, r, http.StatusOK, f, crumb{Label: "New schedule"})
}

// showEditSchedule answers GET /schedules/{id}/edit: the form, filled with the
// schedule. A built-in schedule is changed in Backups.
func (s *server) showEditSchedule(w http.ResponseWriter, r *http.Request, session string) {
	sc, ok := s.editableSchedule(w, r)
	if !ok {
		return
	}
	in := scheduleInput{Name: sc.Name, App: sc.App, Service: sc.Service, Command: commandLine(sc.Command), When: sc.Cron,
		StopAfter: sc.StopAfterSeconds, Memory: sc.MemoryMB, Skip: sc.SkipIfRunning}
	if words := strings.TrimSuffix(sc.Words, " UTC"); !strings.HasPrefix(words, "On `") {
		// The words read back as the same line: show those.
		if cron, err := cronwords.Parse(words); err == nil && cron == sc.Cron {
			in.When = strings.ToLower(words[:1]) + words[1:]
		}
	}
	if p := presetWords(r.URL.Query().Get("preset")); p != "" {
		in.When = p
	}
	f, err := s.editForm(r.Context(), sc, in)
	if err != nil {
		s.serverError(w, "list apps", err)
		return
	}
	f.Preview, _, _ = previewOf(s, in.When)
	s.renderScheduleForm(w, r, http.StatusOK, f, crumb{Label: sc.Name, Href: scheduleHref(sc.ID)}, crumb{Label: "Edit"})
}

// editForm is the form of an existing schedule: its app can't change.
func (s *server) editForm(ctx context.Context, sc service.Schedule, in scheduleInput) (schedForm, error) {
	in.App = sc.App
	f, err := s.buildScheduleForm(ctx, in, true)
	if err != nil {
		return f, err
	}
	f.Editing, f.Action, f.Submit, f.Verb = true, "/schedules/"+url.PathEscape(sc.ID), "Save schedule", "save"
	f.Title = sentence{plain("Edit "), mono(sc.Name)}
	f.Presets = presetsFor(url.Values{}, "/schedules/"+url.PathEscape(sc.ID)+"/edit", in.When)
	// The app and its service are fixed: drawn as text, carried in hidden inputs.
	f.Fixed = sentence{mono(sc.Service), plain(" of "), mono(appText(sc))}
	f.FixedApp, f.FixedSvc = sc.App, sc.Service
	return f, nil
}

// editableSchedule reads the schedule of the address for its form: a missing
// one is the not-found page, a built-in one a page that says where it is
// changed.
func (s *server) editableSchedule(w http.ResponseWriter, r *http.Request) (service.Schedule, bool) {
	sc, err := s.svc.GetSchedule(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, service.ErrScheduleNotFound):
		s.scheduleNotFound(w, r)
		return sc, false
	case err != nil:
		s.serverError(w, "read schedule", err)
		return sc, false
	case sc.Builtin:
		s.message(w, http.StatusConflict, "Made by Backups", scheduleName(sc)+" is made by Backups.",
			"Change its time or how many backups are kept in the app's Backups tab.")
		return sc, false
	}
	return sc, true
}

// createSchedule answers POST /schedules: action=check redraws the form with
// the preview; action=create creates the schedule and lands on its row.
func (s *server) createSchedule(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	in := readScheduleInput(r.PostForm)
	f, err := s.buildScheduleForm(r.Context(), in, in.App != "" || strings.Contains(in.Service, pairSep))
	if err != nil {
		s.serverError(w, "list apps", err)
		return
	}
	base := url.Values{}
	if in.App != "" {
		base.Set("app", in.App)
	}
	f.Presets = presetsFor(base, "/schedules/new", in.When)
	s.submitSchedule(w, r, f, in, nil)
}

// saveSchedule answers POST /schedules/{id}: action=check redraws the form
// with the preview; action=save saves the schedule and lands on its row.
func (s *server) saveSchedule(w http.ResponseWriter, r *http.Request, session string) {
	sc, ok := s.editableSchedule(w, r)
	if !ok {
		return
	}
	if !s.readForm(w, r) {
		return
	}
	in := readScheduleInput(r.PostForm)
	f, err := s.editForm(r.Context(), sc, in)
	if err != nil {
		s.serverError(w, "list apps", err)
		return
	}
	s.submitSchedule(w, r, f, in, &sc)
}

// submitSchedule checks the form and, for create or save, asks the service.
// editing is the schedule being edited (nil for a new one).
func (s *server) submitSchedule(w http.ResponseWriter, r *http.Request, f schedForm, in scheduleInput, editing *service.Schedule) {
	crumbs := []crumb{{Label: "New schedule"}}
	if editing != nil {
		crumbs = []crumb{{Label: editing.Name, Href: scheduleHref(editing.ID)}, {Label: "Edit"}}
		in.App = editing.App
	}
	preview, cron, whenErr := previewOf(s, in.When)
	f.Preview = preview

	if r.PostForm.Get("action") == "check" {
		if whenErr != nil && preview == nil {
			f.When.Error, f.When.Alert = plainS(upperFirst(whenErr.Error())+"."), true
		}
		if in.When == "" {
			f.When.Error, f.When.Alert = plainS("Type when it runs, or pick one of the presets."), true
		}
		s.renderScheduleForm(w, r, http.StatusOK, f, crumbs...)
		return
	}

	// What the form alone can say is wrong, in the order of the fields.
	var errs []*formField
	fail := func(field *formField, text sentence) {
		if field.Error == nil {
			field.Error = text
			errs = append(errs, field)
		}
	}
	command, cmdErr := splitCommand(in.Command)
	if in.Name == "" {
		f.Name.Value = defaultScheduleName(command)
		fail(&f.Name, plainS("Enter a name for the schedule."))
		if f.Name.Value != "" {
			f.Name.Error = sentence{plain("Enter a name for the schedule; "), mono(f.Name.Value), plain(" is made from the command.")}
		}
	}
	// A service's error is drawn under the app when there is no service select.
	svcField := &f.Service
	if !f.ServiceShown {
		svcField = &f.App
	}
	appID, svcName := in.App, in.Service
	if a, svc, ok := strings.Cut(in.Service, pairSep); ok {
		switch {
		case appID == "":
			appID, svcName = a, svc
		case a != appID:
			fail(svcField, plainS("That service is another app's: pick one of the chosen app's services."))
		default:
			svcName = svc
		}
	}
	if appID == "" {
		fail(&f.App, plainS("Choose the app the command runs in."))
	} else if in.Name != "" {
		if taken := s.builtinNameTaken(r.Context(), appID, in.Name); taken != "" {
			fail(&f.Name, sentence{mono(in.Name), plain(" is the name of "), mono(taken), plain("'s backup, a built-in schedule: choose another.")})
		}
	}
	switch {
	case in.Command == "":
		fail(&f.Command, plainS("Enter the command to run."))
	case cmdErr != nil:
		fail(&f.Command, plainS(upperFirst(cmdErr.Error())+"."))
	}
	switch {
	case in.When == "":
		fail(&f.When, plainS("Type when it runs, or pick one of the presets."))
	case preview != nil && preview.Unreadable:
		preview.Alert = len(errs) == 0
		f.When.Error = plainS(cronwords.ErrUnreadable.Error())
		errs = append(errs, &f.When)
	case whenErr != nil:
		fail(&f.When, plainS(upperFirst(whenErr.Error())+"."))
	}
	if len(errs) > 0 {
		errs[0].Alert = true
		s.renderScheduleForm(w, r, http.StatusUnprocessableEntity, f, crumbs...)
		return
	}

	ns := service.NewSchedule{App: appID, Service: svcName, Name: in.Name, Command: command, Cron: cron,
		StopAfterSeconds: in.StopAfter, MemoryMB: in.Memory, SkipIfRunning: new(in.Skip)}
	var (
		saved service.Schedule
		err   error
	)
	if editing != nil {
		saved, err = s.svc.UpdateSchedule(r.Context(), editing.ID, ns)
	} else {
		saved, err = s.svc.CreateSchedule(r.Context(), ns)
	}
	var fe *app.FieldError
	switch {
	case err == nil:
		to := "/schedules?created=" + url.QueryEscape(saved.ID)
		if editing != nil {
			to = "/schedules?saved=" + url.QueryEscape(saved.ID)
		}
		http.Redirect(w, r, to+"#schedule-"+saved.ID, http.StatusSeeOther)
		return
	case errors.Is(err, service.ErrScheduleExists):
		f.Name.Error, f.Name.Alert = sentence{plain("The app already has a schedule named "), mono(in.Name), plain(": choose another name.")}, true
		s.renderScheduleForm(w, r, http.StatusConflict, f, crumbs...)
		return
	case errors.Is(err, store.ErrNotFound):
		f.App.Error, f.App.Alert = plainS("That app doesn't exist any more: choose another."), true
	case errors.Is(err, service.ErrScheduleNotFound):
		s.scheduleNotFound(w, r)
		return
	case errors.As(err, &fe):
		field := map[string]*formField{"app": &f.App, "name": &f.Name, "command": &f.Command, "cron": &f.When, "service": svcField}[fe.Field]
		if field == nil {
			f.Notice = plainS(upperFirst(fe.Message) + ".")
		} else {
			field.Error, field.Alert = plainS(upperFirst(strings.TrimSuffix(fe.Message, "."))+"."), true
		}
	default:
		s.serverError(w, "save schedule", err)
		return
	}
	s.renderScheduleForm(w, r, http.StatusUnprocessableEntity, f, crumbs...)
}

// builtinNameTaken is the service whose backup is the built-in schedule named
// name in the app ("<service>-backup"), or "".
func (s *server) builtinNameTaken(ctx context.Context, appID, name string) string {
	rec, err := s.svc.GetApp(ctx, appID)
	if err != nil {
		return ""
	}
	for _, svc := range rec.App.Services {
		if svc.Backup != nil && name == svc.Name+"-backup" {
			return svc.Name
		}
	}
	return ""
}
