package ui

import (
	"maps"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
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

	// keptDomains is, for an app that exists, each service's domains as the app
	// has them, with their ports and options (feature 021, ruling 12). The form
	// has no Domains input then (the Domains page edits them), so request sends
	// these and the typed text is never read. Nil for a new app, whose domains
	// come from the textarea.
	keptDomains map[string][]compose.Domain

	// keptIdentities is, for an app that exists, each service's workload
	// identities as the app has them (spec 038). The form has no input for
	// them, so request sends these. Nil for a new app, which has none.
	keptIdentities map[string][]app.Identity

	// keptPublish is, for an app that exists, each service's published ports
	// as the overlay's publish list (spec 041). The form has no input for
	// them, so request sends these. Nil for a new app, which has none.
	keptPublish map[string][]compose.Publish

	// keptPlacement is, for an app that exists, each service's placement rules
	// as the app has them (feature 042). The form has no input for them (they
	// are edited on Configure, or in the file's overlay), so request sends
	// these. Nil for a new app, which has none.
	keptPlacement map[string]*app.Placement

	// keptScale is, for an app that exists, each scaled service's scaling rule
	// (feature 043). The form has no input for it (it is edited on Configure,
	// or in the file's overlay), so request sends these. Nil for a new app.
	keptScale map[string]*app.Scale

	// keptDisconnect is, for an app that exists, each service's disconnect
	// policy (network blips). The form has no input for it (it is set in the
	// file's overlay or through the API), so request sends these. Nil for a new
	// app.
	keptDisconnect map[string]*app.Disconnect

	// keptHealth and keptBackup are, for an app that exists, each service's
	// health check and backup policy as the app has them (feature 059). The form
	// has no input for them (health is on Configure › Services, the policy on the
	// Backups tab), so request sends these. Nil for a new app, whose services take
	// them from the inputs.
	keptHealth map[string]*compose.Health
	keptBackup map[string]*app.Backup
}

// fixed says the draft is of an app that exists: it has only the file, and
// every service's settings come from the stored record (the keep functions).
// It is the one marker, as keptDomains is set for every such draft.
func (d composeDraft) fixed() bool { return d.keptDomains != nil }

// draftService is the overlay inputs of one Compose service: what Compose has
// no words for (domains, health check, secrets, backup). The formFields hold
// the text as typed; overlay holds the same as the model wants it, set only
// when the text parsed.
type draftService struct {
	Name string

	Domains, HealthPath, HealthInterval, HealthTimeout, HealthPort formField
	BackupEngine, BackupFile, BackupSchedule, BackupKeep           formField
	// BackupRestoreSchedule and BackupRestoreFrom are the policy's restore schedule (feature 036):
	// both or neither.
	BackupRestoreSchedule, BackupRestoreFrom formField
	// BackupDestination, BackupCopyTo and BackupCopyKeep are where the policy's backups go and are copied
	// (feature 052): a destination's name, other names separated by commas, spaces or lines, and a number.
	BackupDestination, BackupCopyTo, BackupCopyKeep formField

	// Secrets are the names the service has: the ones the form was drawn
	// with and the ones just typed. Their inputs are password inputs, always
	// drawn empty.
	Secrets []draftSecret
	// NewSecrets are the blank rows for naming more. Only a row with a
	// problem keeps anything to show: its error.
	NewSecrets [newSecretRows]formField

	overlay compose.Overlay
}

// draftSecret is one secret of a service. A secret the stored app has travels by its name, as a
// reference or a template when it is one (keepStored); the form has an input only for a new one.
type draftSecret struct {
	Name  string
	Field formField
	// From is where the secret reads its value (feature 037); Template and Refs are a template
	// secret (feature 053). The form has no input for them: they are kept through the deploy.
	From     *app.SecretRef
	Template string
	Refs     map[string]app.SecretRef
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
		{"backupEngine", &s.BackupEngine}, {"backupFile", &s.BackupFile}, {"backupSchedule", &s.BackupSchedule}, {"backupKeep", &s.BackupKeep},
		{"backupRestoreSchedule", &s.BackupRestoreSchedule}, {"backupRestoreFrom", &s.BackupRestoreFrom},
		{"backupDestination", &s.BackupDestination}, {"backupCopyTo", &s.BackupCopyTo}, {"backupCopyKeep", &s.BackupCopyKeep},
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

// hasError says some input of the draft carries an error.
func (d composeDraft) hasError() bool {
	if len(d.Name.Error) > 0 || len(d.File.Error) > 0 {
		return true
	}
	for i := range d.Services {
		for _, f := range d.Services[i].allFields() {
			if len(f.Error) > 0 {
				return true
			}
		}
	}
	return false
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
	s.BackupFile.Value = text("backupFile")
	s.BackupRestoreSchedule.Value, s.BackupRestoreFrom.Value = text("backupRestoreSchedule"), text("backupRestoreFrom")
	s.BackupDestination.Value, s.BackupCopyTo.Value, s.BackupCopyKeep.Value = text("backupDestination"), text("backupCopyTo"), text("backupCopyKeep")

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

	// The restore schedule: a schedule and a label together, or neither (feature 036).
	var restore *app.RestoreSchedule
	switch spec, from := s.BackupRestoreSchedule.Value, s.BackupRestoreFrom.Value; {
	case spec == "" && from == "":
	case spec == "":
		refuse(&s.BackupRestoreSchedule, "Give both a schedule and a label, or neither.")
	case from == "":
		refuse(&s.BackupRestoreFrom, "Give both a schedule and a label, or neither.")
	case !app.ValidLabel(from):
		refuse(&s.BackupRestoreFrom, "A label is 1 to 32 lowercase letters, digits or hyphens, starting and ending with a letter or digit.")
	default:
		restore = &app.RestoreSchedule{Schedule: spec, From: from}
	}

	// Where backups go and are copied (feature 052): names, which the model checks (app.Validate and the
	// destinations that exist), and how many a copy keeps.
	copyTo := strings.FieldsFunc(s.BackupCopyTo.Value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
	if len(copyTo) == 0 {
		copyTo = nil // as the model has it, so a form without copies stores what it did before
	}
	copyKeep := 0
	if s.BackupCopyKeep.Value != "" {
		if n, err := strconv.Atoi(s.BackupCopyKeep.Value); err != nil || n < 1 || n > 100 {
			refuse(&s.BackupCopyKeep, "Enter a whole number from 1 to 100.")
		} else {
			copyKeep = n
		}
	}

	switch engine := s.BackupEngine.Value; {
	case engine == "" && restore != nil:
		refuse(&s.BackupEngine, "Choose an engine to restore on a schedule.")
	case engine == "" && (s.BackupSchedule.Value != "" || s.BackupKeep.Value != "" || s.BackupFile.Value != ""):
		refuse(&s.BackupEngine, "Choose an engine to set a schedule, a file or how many to keep.")
	case engine == "" && (s.BackupDestination.Value != "" || len(copyTo) > 0 || s.BackupCopyKeep.Value != ""):
		refuse(&s.BackupEngine, "Choose an engine to set where backups go.")
	case engine != app.BackupSQLite && s.BackupFile.Value != "":
		refuse(&s.BackupFile, "Only the SQLite engine backs up a file; clear it, or choose SQLite file.")
	case app.DumpEngine(engine) || engine == app.BackupVolume || engine == app.BackupSQLite:
		// The file's rules are the model's (app.Validate); its errors come back
		// on this input (fieldFor).
		b := app.Backup{Engine: engine, File: s.BackupFile.Value, Schedule: s.BackupSchedule.Value, Restore: restore,
			Destination: s.BackupDestination.Value, CopyTo: copyTo, CopyKeep: copyKeep}
		if s.BackupKeep.Value != "" {
			if n, err := strconv.Atoi(s.BackupKeep.Value); err != nil || n < 1 || n > 100 {
				refuse(&s.BackupKeep, "Enter a whole number from 1 to 100.")
			} else {
				b.Keep = n
			}
		}
		s.overlay.Backup = &b
	case engine != "":
		refuse(&s.BackupEngine, "Choose none, PostgreSQL, MySQL or MariaDB, MongoDB, Volume or SQLite file.")
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
		if d.keptDomains != nil {
			ov.Domains = d.keptDomains[s.Name]
		}
		for _, sec := range s.Secrets {
			secret := app.Secret{Name: sec.Name, From: sec.From, Template: sec.Template, Refs: maps.Clone(sec.Refs)}
			if v, ok := d.values[s.Name][sec.Name]; ok {
				secret.Value = &v
			}
			ov.Secrets = append(ov.Secrets, secret)
		}
		if d.keptIdentities != nil {
			ov.Identities = d.keptIdentities[s.Name]
		}
		if d.keptPublish != nil {
			ov.Publish = d.keptPublish[s.Name]
		}
		if d.keptPlacement != nil {
			ov.Placement = d.keptPlacement[s.Name]
		}
		if d.keptScale != nil {
			ov.Scale = d.keptScale[s.Name]
		}
		if d.keptDisconnect != nil {
			ov.Disconnect = d.keptDisconnect[s.Name]
		}
		if d.keptHealth != nil {
			ov.Health = d.keptHealth[s.Name]
		}
		if d.keptBackup != nil {
			ov.Backup = d.keptBackup[s.Name]
		}
		if ov.Empty() {
			continue
		}
		if req.Services == nil {
			req.Services = map[string]compose.Overlay{}
		}
		req.Services[s.Name] = ov
	}
	return req
}

// keepStored adds, to each service the stored app also has, the secrets it
// has that the draft lacks: this form can't remove a secret (Variables &
// secrets does), so a deploy from a file never drops one by accident
// (Constitution IV).
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
			// The form has no input for a stored secret, so the stored one is what the deploy keeps.
			s.Secrets[i].From, s.Secrets[i].Template, s.Secrets[i].Refs = sec.From, sec.Template, sec.Refs
		}
		slices.SortFunc(s.Secrets, func(a, b draftSecret) int { return strings.Compare(a.Name, b.Name) })
	}
}

// keepDomains makes the draft carry the domains the stored app has, as they
// are, for every service that has some (feature 021, ruling 12): the Compose
// form of an existing app has no Domains input, so a preview or a deploy from
// it must not drop a domain's port or options (constitution IV). An app that
// keeps its file has them in the file's overlay, exactly as written; any other
// app has them on its services, which are written back the way SetDomainsIf
// writes them: no port for a service's only port, else the container port of
// the domain's port name. A service the draft lacks gets its inputs, so its
// domains travel; one the new file doesn't have is dropped with them, and
// composeChanges says so.
func (d *composeDraft) keepDomains(rec store.AppRecord) {
	d.keptDomains = map[string][]compose.Domain{}
	for _, svc := range rec.App.Services {
		var domains []compose.Domain
		if rec.Compose != nil {
			domains = slices.Clone(rec.Compose.Services[svc.Name].Domains)
		} else {
			for _, dom := range svc.Domains {
				port := 0
				if len(svc.Ports) != 1 {
					if i := slices.IndexFunc(svc.Ports, func(p app.Port) bool { return p.Name == dom.Port }); i >= 0 {
						port = svc.Ports[i].ContainerPort
					}
				}
				domains = append(domains, compose.Domain{Host: dom.Host, Port: port, Path: dom.Path,
					WWWRedirect: dom.WWWRedirect, TeamOnly: dom.TeamOnly})
			}
		}
		if len(domains) == 0 {
			continue
		}
		d.keptDomains[svc.Name] = domains
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
	// Nothing typed for domains is read any more: a stale form's text must not
	// leave an error on an input that isn't drawn.
	for i := range d.Services {
		d.Services[i].Domains = formField{ID: d.Services[i].Domains.ID}
	}
}

// keepIdentities makes the draft carry the workload identities the stored app
// has (spec 038): the form doesn't edit them, so a preview or a deploy from it
// must not drop one (constitution IV). An app that keeps its file has them in
// the file's overlay, exactly as written; any other app has them on its
// services. A service the draft lacks gets its inputs, so its identities
// travel; one the new file doesn't have is dropped with them, as its other
// settings are.
func (d *composeDraft) keepIdentities(rec store.AppRecord) {
	d.keptIdentities = map[string][]app.Identity{}
	for _, svc := range rec.App.Services {
		identities := svc.Identities
		if rec.Compose != nil {
			identities = rec.Compose.Services[svc.Name].Identities
		}
		if len(identities) == 0 {
			continue
		}
		d.keptIdentities[svc.Name] = slices.Clone(identities)
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// keepPublish makes the draft carry the published ports the stored app has
// (spec 041): the form doesn't edit them, so a preview or a deploy from it
// must not unpublish one (constitution IV). An app that keeps its file has
// them in the file's overlay, exactly as written; any other app has them on its
// ports. A service the draft lacks gets its inputs, so its ports travel; one
// the new file doesn't have is dropped with them, as its other settings are.
func (d *composeDraft) keepPublish(rec store.AppRecord) {
	d.keptPublish = map[string][]compose.Publish{}
	for _, svc := range rec.App.Services {
		var publish []compose.Publish
		if rec.Compose != nil {
			publish = rec.Compose.Services[svc.Name].Publish
		} else {
			for _, p := range svc.Ports {
				if !p.IsPublished() {
					continue
				}
				entry := compose.Publish{Port: p.ContainerPort}
				if p.Published != p.ContainerPort {
					entry.Published = p.Published
				}
				publish = append(publish, entry)
			}
		}
		if len(publish) == 0 {
			continue
		}
		d.keptPublish[svc.Name] = slices.Clone(publish)
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// keepPlacement makes the draft carry the placement rules the stored app has
// (feature 042): the form doesn't edit them, so a preview or a deploy from it
// must not drop a rule (constitution IV). An app that keeps its file has them in
// the file's overlay, exactly as written; any other app has them on its
// services. A service the draft lacks gets its inputs, so its rules travel; one
// the new file doesn't have is dropped with them, as its other settings are.
func (d *composeDraft) keepPlacement(rec store.AppRecord) {
	d.keptPlacement = map[string]*app.Placement{}
	for _, svc := range rec.App.Services {
		p := svc.Placement
		if rec.Compose != nil {
			p = rec.Compose.Services[svc.Name].Placement
		}
		if p == nil {
			continue
		}
		d.keptPlacement[svc.Name] = clonePlacement(p)
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// keepScale makes the draft carry the scaling rules the stored app has (feature
// 043), as keepPlacement does for placement: an app that keeps its file has them
// in the file's overlay, exactly as written; any other app has them on its
// services. A deploy from the form must not drop a rule (constitution IV).
func (d *composeDraft) keepScale(rec store.AppRecord) {
	d.keptScale = map[string]*app.Scale{}
	for _, svc := range rec.App.Services {
		sc := svc.Scale
		if rec.Compose != nil {
			sc = rec.Compose.Services[svc.Name].Scale
		}
		if sc == nil {
			continue
		}
		kept := *sc
		d.keptScale[svc.Name] = &kept
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// keepDisconnect makes the draft carry the disconnect policies the stored app
// has, as keepScale does for scaling: an app that keeps its file has them in the
// file's overlay, exactly as written; any other app has them on its services. A
// deploy from the form must not drop a policy (constitution IV).
func (d *composeDraft) keepDisconnect(rec store.AppRecord) {
	d.keptDisconnect = map[string]*app.Disconnect{}
	for _, svc := range rec.App.Services {
		dc := svc.Disconnect
		if rec.Compose != nil {
			dc = rec.Compose.Services[svc.Name].Disconnect
		}
		if dc == nil {
			continue
		}
		kept := *dc
		d.keptDisconnect[svc.Name] = &kept
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// keepHealth makes the draft carry the health checks the stored app has
// (feature 059): the form of an app that exists has no health input, so a
// preview or a deploy from it must not reset a check to the default
// (constitution IV). An app that keeps its file has them in the file's overlay,
// exactly as written; any other app has them on its services, written back as
// keepDomains writes a domain's port: the container port, and only when the
// service has several ports. A service whose check is Caravana's default has
// nothing to keep.
func (d *composeDraft) keepHealth(rec store.AppRecord) {
	d.keptHealth = map[string]*compose.Health{}
	for _, svc := range rec.App.Services {
		var h *compose.Health
		if rec.Compose != nil {
			if kept := rec.Compose.Services[svc.Name].Health; kept != nil {
				copied := *kept
				h = &copied
			}
		} else if svc.Health != nil && len(svc.Ports) > 0 {
			out := compose.Health{Path: svc.Health.Path, Interval: svc.Health.Interval, Timeout: svc.Health.Timeout}
			if len(svc.Ports) > 1 {
				if i := slices.IndexFunc(svc.Ports, func(p app.Port) bool { return p.Name == svc.Health.Port }); i >= 0 {
					out.Port = svc.Ports[i].ContainerPort
				}
			}
			if out != (compose.Health{}) {
				h = &out
			}
		}
		if h == nil {
			continue
		}
		d.keptHealth[svc.Name] = h
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// keepBackup makes the draft carry the backup policies the stored app has
// (feature 059), as keepHealth does for health checks: the whole policy, its
// restore schedule, destination and copies included, since the form of an app
// that exists has no backup input (the Backups tab changes it).
func (d *composeDraft) keepBackup(rec store.AppRecord) {
	d.keptBackup = map[string]*app.Backup{}
	for _, svc := range rec.App.Services {
		b := svc.Backup
		if rec.Compose != nil {
			b = rec.Compose.Services[svc.Name].Backup
		}
		if b == nil {
			continue
		}
		d.keptBackup[svc.Name] = cloneBackup(b)
		if _, ok := d.service(svc.Name); !ok {
			d.Services = append(d.Services, newDraftService(svc.Name))
		}
	}
	d.sortServices()
}

// cloneBackup is a copy of b that shares no slice or pointer with it.
func cloneBackup(b *app.Backup) *app.Backup {
	out := *b
	out.CopyTo = slices.Clone(b.CopyTo)
	if b.Restore != nil {
		r := *b.Restore
		out.Restore = &r
	}
	return &out
}

// keepAll makes the draft the form of an app that exists: its services are the
// stored app's, and everything the form has no input for (secrets by name,
// domains, identities, published ports, placement, scaling, disconnect, health
// and backup) comes from the stored record. Anything the post carried for the
// services is dropped first, so a form drawn before feature 059 can't change
// what this form no longer shows.
func (d *composeDraft) keepAll(rec store.AppRecord) {
	d.Services, d.values = nil, map[string]map[string]string{}
	d.ensureServices(rec.App)
	d.keepStored(rec.App)
	d.keepDomains(rec)
	d.keepIdentities(rec)
	d.keepPublish(rec)
	d.keepPlacement(rec)
	d.keepScale(rec)
	d.keepDisconnect(rec)
	d.keepHealth(rec)
	d.keepBackup(rec)
}

// clonePlacement is a copy of p that shares no slice or map with it.
func clonePlacement(p *app.Placement) *app.Placement {
	if p == nil {
		return nil
	}
	return &app.Placement{Require: maps.Clone(p.Require), Prefer: slices.Clone(p.Prefer), Avoid: slices.Clone(p.Avoid)}
}
