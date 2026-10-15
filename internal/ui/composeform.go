package ui

import (
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// This file is the Compose forms' draft (feature 014, research R11): what
// New app › Compose file and Deploy from Compose post, as the inputs hold it
// (for drawing the form again) and as a compose.Request (for the service). It
// is not stored anywhere.

// newSecretRows is how many blank "add a secret" rows each service shows. A
// Compose file names no secrets, so the form has to take a secret's name as
// well as its value.
const newSecretRows = 3

// composeDraft is a Compose form as it was posted. Secret values are never in
// it where a page could reach them: they sit in the unexported values, which
// only request reads, and forPage clears even that before a page gets the
// draft (FR-012).
type composeDraft struct {
	Name     formField // New app only
	File     formField // the Compose file, as typed
	Revision string    // Deploy from Compose only: the revision of the app the form was drawn from
	Services []draftService

	values map[string]map[string]string // service, secret name: the value typed; absent means keep
}

// draftService is the overlay inputs of one Compose service: what Compose has
// no words for (domains, health check, secrets, backup). The formFields hold
// the text as typed; overlay holds the same as the model wants it, set only
// when the text parsed.
type draftService struct {
	Name string

	Domains, HealthPath, HealthInterval, HealthTimeout, HealthPort formField
	BackupEngine, BackupSchedule, BackupKeep                       formField

	// Secrets are the names the service has: the ones the form was drawn
	// with and the ones just typed. Their inputs are password inputs, always
	// drawn empty.
	Secrets []draftSecret
	// NewSecrets are the blank rows for naming more. Only a row with a
	// problem keeps anything to show: its error.
	NewSecrets [newSecretRows]formField

	overlay compose.Overlay
}

// draftSecret is one secret of a service. Exists says the stored app already
// has it, so an empty input keeps its value.
type draftSecret struct {
	Name   string
	Exists bool
	Field  formField
}

// newDraftService makes a service's inputs with their IDs set.
func newDraftService(name string) draftService {
	s := draftService{Name: name}
	for _, in := range s.inputs() {
		in.field.ID = inputID(name, in.name)
	}
	for i := range s.NewSecrets {
		s.NewSecrets[i].ID = inputID(name, "newSecret-"+strconv.Itoa(i))
	}
	return s
}

// inputs lists the plain inputs in the order they show, with the field part
// of their posted names.
func (s *draftService) inputs() []struct {
	name  string
	field *formField
} {
	return []struct {
		name  string
		field *formField
	}{
		{"domains", &s.Domains}, {"healthPath", &s.HealthPath}, {"healthInterval", &s.HealthInterval},
		{"healthTimeout", &s.HealthTimeout}, {"healthPort", &s.HealthPort},
		{"backupEngine", &s.BackupEngine}, {"backupSchedule", &s.BackupSchedule}, {"backupKeep", &s.BackupKeep},
	}
}

// allFields lists every input that can carry an error, in the order they show.
func (s *draftService) allFields() []*formField {
	var out []*formField
	for _, in := range s.inputs() {
		out = append(out, in.field)
	}
	for i := range s.Secrets {
		out = append(out, &s.Secrets[i].Field)
	}
	for i := range s.NewSecrets {
		out = append(out, &s.NewSecrets[i])
	}
	return out
}

// service returns the draft's inputs for the named service.
func (d *composeDraft) service(name string) (*draftService, bool) {
	for i := range d.Services {
		if d.Services[i].Name == name {
			return &d.Services[i], true
		}
	}
	return nil, false
}

// sortServices keeps the services in the order the file's services have: by name.
func (d *composeDraft) sortServices() {
	slices.SortFunc(d.Services, func(a, b draftService) int { return strings.Compare(a.Name, b.Name) })
}

// markFirstError chooses which error is announced and which input is focused.
func (d *composeDraft) markFirstError() {
	for _, f := range []*formField{&d.Name, &d.File} {
		if len(f.Error) > 0 {
			f.Alert = true
			return
		}
	}
	for i := range d.Services {
		for _, f := range d.Services[i].allFields() {
			if len(f.Error) > 0 {
				f.Alert = true
				return
			}
		}
	}
}

// forPage is the draft as a page may draw it: the same inputs, and no secret
// values at all.
func (d composeDraft) forPage() composeDraft {
	d.values = nil
	return d
}

// parseComposeDraft reads a posted Compose form. It always returns the draft
// with every value as typed; ok is false when some overlay input has an
// error, which sits on that input. A service has overlay inputs in the post
// when it carries its "overlay" marker; one without is not part of the draft
// (a form drawn before the file named it).
func parseComposeDraft(post url.Values) (composeDraft, bool) {
	d := composeDraft{
		Name:     formField{ID: "name", Value: strings.TrimSpace(post.Get("name"))},
		File:     formField{ID: "compose", Value: strings.ReplaceAll(post.Get("compose"), "\r\n", "\n")},
		Revision: strings.TrimSpace(post.Get("revision")),
		values:   map[string]map[string]string{},
	}
	var names []string
	for key := range post {
		rest, ok := strings.CutPrefix(key, "svc.")
		if !ok {
			continue
		}
		if name, field, ok := strings.Cut(rest, "."); ok && field == "overlay" {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	ok := true
	for _, name := range names {
		s := newDraftService(name)
		values := map[string]string{}
		if !s.read(post, values) {
			ok = false
		}
		d.Services = append(d.Services, s)
		if len(values) > 0 {
			d.values[name] = values
		}
	}
	return d, ok
}

// read takes the service's inputs from the post, secret values into values,
// and reports whether every input was good.
func (s *draftService) read(post url.Values, values map[string]string) bool {
	ok := true
	text := func(field string) string { return strings.TrimSpace(post.Get(inputName(s.Name, field))) }

	s.Domains.Value = strings.ReplaceAll(post.Get(inputName(s.Name, "domains")), "\r\n", "\n")
	s.HealthPath.Value = text("healthPath")
	s.HealthInterval.Value, s.HealthTimeout.Value, s.HealthPort.Value = text("healthInterval"), text("healthTimeout"), text("healthPort")
	s.BackupEngine.Value, s.BackupSchedule.Value, s.BackupKeep.Value = text("backupEngine"), text("backupSchedule"), text("backupKeep")

	// The names of the secrets drawn, then the ones typed in the blank rows.
	prefix := inputName(s.Name, "secret.")
	var drawn []string
	for key := range post {
		if name, found := strings.CutPrefix(key, prefix); found && name != "" {
			drawn = append(drawn, name)
		}
	}
	sort.Strings(drawn)
	for _, name := range drawn {
		s.Secrets = append(s.Secrets, draftSecret{Name: name, Field: formField{ID: inputID(s.Name, "secret-"+name)}})
		if v := post.Get(prefix + name); v != "" {
			values[name] = v
		}
	}
	for i := range s.NewSecrets {
		name := strings.TrimSpace(post.Get(inputName(s.Name, "newSecret."+strconv.Itoa(i))))
		value := post.Get(inputName(s.Name, "newSecretValue."+strconv.Itoa(i)))
		row := &s.NewSecrets[i]
		switch {
		case name == "" && value == "":
		case name == "":
			row.Error, ok = sentence{plain("Enter the secret's name.")}, false
		case slices.ContainsFunc(s.Secrets, func(sec draftSecret) bool { return sec.Name == name }):
			row.Error, ok = sentence{mono(name), plain(" is already listed above; type its value there.")}, false
		default:
			s.Secrets = append(s.Secrets, draftSecret{Name: name, Field: formField{ID: inputID(s.Name, "secret-"+name)}})
			if value != "" {
				values[name] = value
			}
		}
	}
	return s.build() && ok
}

// build turns the typed text into the overlay, putting a problem on the input
// it is about. Secrets aren't part of it: request adds them with their values.
func (s *draftService) build() bool {
	ok := true
	refuse := func(field *formField, text string) {
		field.Error, ok = sentence{plain(text)}, false
	}
	s.overlay = compose.Overlay{}

	for _, line := range strings.Split(s.Domains.Value, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		host, port := line, 0
		if h, p, found := strings.Cut(line, ":"); found {
			n, err := strconv.Atoi(p)
			if h == "" || err != nil || n < 1 || n > 65535 {
				refuse(&s.Domains, "Write \""+line+"\" as a host, or host:port with a port from 1 to 65535.")
				continue
			}
			host, port = h, n
		}
		s.overlay.Domains = append(s.overlay.Domains, compose.Domain{Host: host, Port: port})
	}

	h := compose.Health{Path: s.HealthPath.Value}
	if s.HealthPort.Value != "" {
		if n, err := strconv.Atoi(s.HealthPort.Value); err != nil || n < 1 || n > 65535 {
			refuse(&s.HealthPort, "Enter a number from 1 to 65535.")
		} else {
			h.Port = n
		}
	}
	for _, in := range []struct {
		field *formField
		to    *time.Duration
	}{{&s.HealthInterval, &h.Interval}, {&s.HealthTimeout, &h.Timeout}} {
		if in.field.Value == "" {
			continue
		}
		d, valid := secondsOf(in.field.Value)
		if !valid {
			refuse(in.field, "Enter a number of seconds, more than 0.")
			continue
		}
		*in.to = d
	}
	if h != (compose.Health{}) {
		s.overlay.Health = &h
	}

	switch engine := s.BackupEngine.Value; {
	case engine == "" && (s.BackupSchedule.Value != "" || s.BackupKeep.Value != ""):
		refuse(&s.BackupEngine, "Choose postgres to set a schedule or how many to keep.")
	case engine == "postgres":
		b := app.Backup{Engine: engine, Schedule: s.BackupSchedule.Value}
		if s.BackupKeep.Value != "" {
			if n, err := strconv.Atoi(s.BackupKeep.Value); err != nil || n < 1 || n > 100 {
				refuse(&s.BackupKeep, "Enter a whole number from 1 to 100.")
			} else {
				b.Keep = n
			}
		}
		s.overlay.Backup = &b
	case engine != "":
		refuse(&s.BackupEngine, "Choose none or postgres.")
	}
	return ok
}

// secondsOf reads a number of seconds, more than 0 and at most an hour, as
// the edit form does.
func secondsOf(text string) (time.Duration, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || n <= 0 || n > 3600 {
		return 0, false
	}
	return time.Duration(math.Round(n * float64(time.Second))), true
}

// request is the draft as the service wants it. A secret with a typed value
// sends it; one without sends its name alone, which keeps the value the app
// has now. A service whose overlay is empty has no entry. It must only be
// called for a draft whose inputs all parsed.
func (d composeDraft) request() compose.Request {
	req := compose.Request{Compose: d.File.Value}
	for _, s := range d.Services {
		ov := s.overlay
		ov.Secrets = nil
		for _, sec := range s.Secrets {
			secret := app.Secret{Name: sec.Name}
			if v, ok := d.values[s.Name][sec.Name]; ok {
				secret.Value = &v
			}
			ov.Secrets = append(ov.Secrets, secret)
		}
		if len(ov.Domains) == 0 && ov.Health == nil && len(ov.Secrets) == 0 && ov.Backup == nil {
			continue
		}
		if req.Services == nil {
			req.Services = map[string]compose.Overlay{}
		}
		req.Services[s.Name] = ov
	}
	return req
}

// draftFrom is the overlay a stored app has, as a draft with no file: what
// Deploy from Compose starts from. A domain or health check on a service with
// several ports names its port, since the new file may reorder them; with one
// port the file's own port is all there is to say.
func draftFrom(a app.App) composeDraft {
	d := composeDraft{values: map[string]map[string]string{}}
	for _, svc := range a.Services {
		s := newDraftService(svc.Name)
		portNumber := func(name string) string {
			if len(svc.Ports) < 2 {
				return ""
			}
			for _, p := range svc.Ports {
				if p.Name == name {
					return ":" + strconv.Itoa(p.ContainerPort)
				}
			}
			return ""
		}
		var domains []string
		for _, dom := range svc.Domains {
			domains = append(domains, dom.Host+portNumber(dom.Port))
		}
		s.Domains.Value = strings.Join(domains, "\n")
		if h := svc.Health; h != nil && len(svc.Ports) > 0 {
			s.HealthPath.Value = h.Path
			s.HealthInterval.Value, s.HealthTimeout.Value = secondsText(h.Interval), secondsText(h.Timeout)
			s.HealthPort.Value = strings.TrimPrefix(portNumber(h.Port), ":")
		}
		if b := svc.Backup; b != nil {
			s.BackupEngine.Value, s.BackupSchedule.Value = b.Engine, b.Schedule
			s.BackupKeep.Value = numberText(b.Keep)
		}
		for _, sec := range svc.Secrets {
			s.Secrets = append(s.Secrets, draftSecret{Name: sec.Name, Exists: true,
				Field: formField{ID: inputID(svc.Name, "secret-"+sec.Name)}})
		}
		s.build()
		d.Services = append(d.Services, s)
	}
	d.sortServices()
	return d
}

// draftOf is a kept Compose request as the form's draft: the inverse of
// composeDraft.request (feature 015, research R7). The owner sees what was
// sent, not what Caravana derived from it, so an overlay with no health check
// shows no health inputs. A secret is listed as existing, with no value: kept
// requests have none, and an empty input keeps the value the secret store has.
// A domain names its port only when the request does.
func draftOf(req compose.Request) composeDraft {
	d := composeDraft{
		Name:   formField{ID: "name", Value: req.Name},
		File:   formField{ID: "compose", Value: req.Compose},
		values: map[string]map[string]string{},
	}
	for name, ov := range req.Services {
		s := newDraftService(name)
		var domains []string
		for _, dom := range ov.Domains {
			host := dom.Host
			if dom.Port != 0 {
				host += ":" + strconv.Itoa(dom.Port)
			}
			domains = append(domains, host)
		}
		s.Domains.Value = strings.Join(domains, "\n")
		if h := ov.Health; h != nil {
			s.HealthPath.Value = h.Path
			s.HealthPort.Value = numberText(h.Port)
			s.HealthInterval.Value, s.HealthTimeout.Value = secondsText(h.Interval), secondsText(h.Timeout)
		}
		if b := ov.Backup; b != nil {
			s.BackupEngine.Value, s.BackupSchedule.Value = b.Engine, b.Schedule
			s.BackupKeep.Value = numberText(b.Keep)
		}
		for _, sec := range ov.Secrets {
			s.Secrets = append(s.Secrets, draftSecret{Name: sec.Name, Exists: true,
				Field: formField{ID: inputID(name, "secret-"+sec.Name)}})
		}
		s.build()
		d.Services = append(d.Services, s)
	}
	d.sortServices()
	return d
}

// keepStored adds, to each service the stored app also has, the secrets it
// has that the draft lacks, and marks the ones it has as existing: this form
// can't remove a secret (the Configure tab does), so a deploy from a file
// never drops one by accident (Constitution IV).
func (d *composeDraft) keepStored(stored app.App) {
	for _, svc := range stored.Services {
		s, ok := d.service(svc.Name)
		if !ok {
			continue
		}
		for _, sec := range svc.Secrets {
			i := slices.IndexFunc(s.Secrets, func(have draftSecret) bool { return have.Name == sec.Name })
			if i < 0 {
				s.Secrets = append(s.Secrets, draftSecret{Name: sec.Name, Field: formField{ID: inputID(svc.Name, "secret-"+sec.Name)}})
				i = len(s.Secrets) - 1
			}
			s.Secrets[i].Exists = true
		}
		slices.SortFunc(s.Secrets, func(a, b draftSecret) int { return strings.Compare(a.Name, b.Name) })
	}
}
