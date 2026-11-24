package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings › Backup storage (features 010 and 052): the backup destinations, one connection row each.
// Every form is a plain post, so the page works without script. The secret key is write-only: no
// struct here has a field for it, so no page can show it and a refused save never puts it back.

// destinationForm is what a destination form shows: the settings as saved or as typed, and the field a
// refusal names. Never the secret key.
type destinationForm struct {
	Name                                          string
	Endpoint, Region, Bucket, Prefix, AccessKeyID string
	// Default is the "Make it the default" box as typed.
	Default bool
	// Editing says the destination exists: its name is fixed and the key may stay empty. IsDefault says it is
	// the default now, so the box has nothing to do.
	Editing, IsDefault bool
	Field              string // the field at fault, after a refusal
	Err                *fieldError
}

// destRow is one destination, one connection row.
type destRow struct {
	Name    string
	Default bool
	Mark    string // backupStorageState's: failed, warning, healthy or idle
	Title   string // endpointName
	Kind    string // "S3-compatible · region eu-west-1"
	// State and Event are the row's two lines: the last test's or the last backup's outcome in Caravana's
	// words, and when the test ran (Clock, drawn by the clock part).
	State, Event string
	StateFailed  bool
	Clock        *clockTime
	Detail       string // the failed test's reason, under the row
	UsedBy       string
	Edit         destinationForm
	Open         bool // the edit form is shown open: it was refused, or asked for with ?edit=
	RemoveOpen   bool // the Remove confirmation is shown open: it was refused
	// Err is a refusal of Make default or Remove, drawn on the row.
	Err *fieldError
	// Failure is the note under a row whose backups failed; nil otherwise.
	Failure *backupFailure
	// Tested says this response is the answer of a test of this row, and TestOK and TestMessage are its outcome.
	Tested      bool
	TestOK      bool
	TestMessage string
}

// backupFailure says which run failed and why: the service and app, the plain cause
// (page 2's table; "" when the message matches none), the raw message in mono and
// where to look.
type backupFailure struct {
	Service, App, Cause, Detail, Href string
}

// destData is the whole section.
type destData struct {
	Rows    []destRow
	Add     destinationForm
	Notice  *providersNotice
	Alert   *fieldError // a refusal that names no field: Nomad being down, or a save that only half went through
	Problem bool        // no destination is set while a service has a backup policy
	// None says there is no destination, so the page offers the add form and nothing else.
	None bool
	// EditName says which row's edit form is open; Edit is what it was refused with (its Name is empty
	// when it wasn't refused).
	EditName string
	Edit     destinationForm
	// RowErrName and RowErr are a refusal of Make default or Remove, and the row it is drawn on.
	RowErrName string
	RowErr     *fieldError
	RemoveName string // the row whose Remove confirmation to open
	// Saved, Removed and MadeDefault announce the redirect's outcome; Tested names the row whose test this is.
	Saved, Removed, MadeDefault bool
	Tested                      string
	TestedOK                    bool
	TestedMessage               string
}

// destFormViewT is a destination form as the dest-fields template draws it.
type destFormViewT struct {
	Form      providerFormView
	DefaultID string
	Default   bool
	IsDefault bool
}

// destFormView builds the inputs of a destination form. idPrefix keeps the ids of the add form and of each
// row's edit form apart.
func destFormView(f destinationForm, idPrefix string) destFormViewT {
	fields := []providerFieldView{
		{ID: idPrefix + "-name", Name: "name", Kind: "text", Label: "Name", Value: f.Name, Placeholder: "offsite", ReadOnly: f.Editing, Required: true,
			Hint: "1 to 32 lowercase letters, digits and hyphens. Policies and projects use it; it can't be changed."},
		{ID: idPrefix + "-endpoint", Name: "endpoint", Kind: "text", Label: "Endpoint", Value: f.Endpoint, Placeholder: "https://s3.eu-west-1.amazonaws.com", Required: true,
			Hint: "AWS S3, Cloudflare R2, Backblaze B2, or one you run."},
		{ID: idPrefix + "-region", Name: "region", Kind: "text", Label: "Region", Value: f.Region, Placeholder: "us-east-1", Required: true,
			Hint: "us-east-1 when the provider doesn't care."},
		{ID: idPrefix + "-bucket", Name: "bucket", Kind: "text", Label: "Bucket", Value: f.Bucket, Required: true},
		{ID: idPrefix + "-prefix", Name: "prefix", Kind: "text", Label: "Prefix (optional)", Value: f.Prefix, Placeholder: "caravana"},
		{ID: idPrefix + "-accessKeyId", Name: "accessKeyId", Kind: "text", Label: "Access key ID", Value: f.AccessKeyID, Required: true},
		{ID: idPrefix + "-secretAccessKey", Name: "secretAccessKey", Kind: "password", Label: "Secret access key", Required: !f.Editing},
	}
	if f.Editing {
		fields[6].Hint = "Leave it empty to keep the current one."
	}
	if f.Err != nil {
		for i := range fields {
			if fields[i].Name == f.Field {
				fields[i].Err = f.Err.Text
			}
		}
	}
	return destFormViewT{Form: providerFormView{Fields: fields}, DefaultID: idPrefix + "-default", Default: f.Default, IsDefault: f.IsDefault}
}

// showBackupStorage answers GET /settings/backup-storage. The notice comes from the redirect after a save,
// a removal or a change of the default; ?edit=<name> opens that destination's edit form.
func (s *server) showBackupStorage(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	s.renderSettings(w, r, entryBackupStorage, settingsData{Dest: destData{
		Saved: q.Get("backups") == "saved", Removed: q.Get("backups") == "removed", MadeDefault: q.Get("backups") == "default",
		EditName: q.Get("edit"),
	}})
}

// setBackupDestination answers POST /settings/backups, for the add form (add=1), for a row's edit form
// (edit=1) and for the form of the days of one destination, which names none: that writes the default (feature
// 010). An empty secret key keeps the one saved before. A refusal draws the page again with what was typed,
// except the secret key.
func (s *server) setBackupDestination(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	add, edit := r.PostFormValue("add") == "1", r.PostFormValue("edit") == "1"
	form := destinationForm{
		Name:     strings.ToLower(strings.TrimSpace(r.PostFormValue("name"))),
		Endpoint: strings.TrimSpace(r.PostFormValue("endpoint")), Region: strings.TrimSpace(r.PostFormValue("region")),
		Bucket: strings.TrimSpace(r.PostFormValue("bucket")), Prefix: strings.TrimSpace(r.PostFormValue("prefix")),
		AccessKeyID: strings.TrimSpace(r.PostFormValue("accessKeyId")), Default: r.PostFormValue("default") == "1",
		Editing: edit,
	}
	in := service.DestinationInput{Endpoint: form.Endpoint, Region: form.Region, Bucket: form.Bucket, Prefix: form.Prefix, AccessKeyID: form.AccessKeyID}
	if v := r.PostFormValue("secretAccessKey"); v != "" {
		in.SecretAccessKey = &v
	}
	if form.Default {
		in.Default = &form.Default
	}

	var err error
	switch {
	case add && form.Name != "" && s.destinationExists(r, form.Name):
		// SaveBackupDestination creates or updates: an add under a name that is taken would
		// overwrite that destination's settings and keys.
		err = app.NewFieldError("name", "%q is already a destination; edit it in its row instead", form.Name)
	case form.Name != "":
		_, err = s.svc.SaveBackupDestination(r.Context(), form.Name, in)
	case add:
		err = app.NewFieldError("name", "is required")
	default:
		// No name: the form of a host with one destination. It writes the default.
		var d store.BackupDestination
		if d, err = s.svc.GetBackupDestination(r.Context()); err == nil {
			form.Name, form.Editing, edit = d.Name, true, true
		} else if !errors.Is(err, store.ErrNotFound) {
			s.serverError(w, r, "read the backup destination", err)
			return
		}
		_, err = s.svc.SetBackupDestination(r.Context(), in)
	}
	if err == nil {
		http.Redirect(w, r, "/settings/backup-storage?backups=saved", http.StatusSeeOther)
		return
	}

	data := destData{Add: form}
	if edit {
		data = destData{EditName: form.Name, Edit: form}
	}
	var fieldErr *app.FieldError
	status := http.StatusUnprocessableEntity
	switch {
	case errors.As(err, &fieldErr) && (fieldErr.Field == "default" || fieldErr.Field == "name" && edit):
		data.Alert = &fieldError{Text: fieldErr.Message}
	case errors.As(err, &fieldErr):
		form.Field, form.Err = fieldErr.Field, &fieldError{Text: fieldErr.Message}
		if edit {
			data.Edit = form
		} else {
			data.Add = form
		}
	case strings.HasPrefix(err.Error(), "saved, but"):
		// The destination is saved and the jobs weren't all pointed at it: say so, a redirect would lose the sentence.
		s.log.Warn("backup destination saved, jobs not repointed", "error", err)
		data = destData{Notice: &providersNotice{Text: capital(err.Error()) + ".", Bad: true}}
		status = http.StatusBadGateway
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't save the backup destination", "error", err)
		data.Alert = &fieldError{Text: "Nomad couldn't keep the secret key, so nothing was saved: " + err.Error()}
		status = http.StatusBadGateway
	default:
		s.serverError(w, r, "set backup destination", err)
		return
	}
	s.renderSettings(w, r, entryBackupStorage, settingsData{Dest: data, Status: status})
}

// destinationExists reports whether a destination is called name. A read that fails counts as no: the save
// that follows reads it again and reports the failure.
func (s *server) destinationExists(r *http.Request, name string) bool {
	_, err := s.svc.GetBackupDestinationNamed(r.Context(), name)
	return err == nil
}

// capital is s with its first letter in upper case.
func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// testBackupDestination answers POST /settings/backups/test, with the destination in the form's "name"
// field. The outcome is stored, and the page it answers announces it under the row.
func (s *server) testBackupDestination(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	t, err := s.svc.TestBackupDestination(r.Context(), name)
	switch {
	case err == nil:
		s.renderSettings(w, r, entryBackupStorage, settingsData{Dest: destData{Tested: name, TestedOK: t.OK, TestedMessage: t.Message}})
	case errors.Is(err, service.ErrDestinationNotFound):
		http.Redirect(w, r, "/settings/backup-storage", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't test the backup destination", "destination", name, "error", err)
		s.renderSettings(w, r, entryBackupStorage, settingsData{Status: http.StatusBadGateway, Dest: destData{
			RowErrName: name, RowErr: &fieldError{Text: "Nomad couldn't run the check: " + err.Error()}}})
	default:
		s.serverError(w, r, "test backup destination", err)
	}
}

// makeDefaultBackupDestination answers POST /settings/backups/default, with the destination in "name".
func (s *server) makeDefaultBackupDestination(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	err := s.svc.SetDefaultBackupDestination(r.Context(), name)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/backup-storage?backups=default", http.StatusSeeOther)
	case errors.Is(err, service.ErrDestinationNotFound):
		http.Redirect(w, r, "/settings/backup-storage", http.StatusSeeOther)
	case errors.As(err, &fieldErr):
		s.renderSettings(w, r, entryBackupStorage, settingsData{Status: http.StatusUnprocessableEntity,
			Dest: destData{RowErrName: name, RowErr: &fieldError{Text: fieldErr.Message}}})
	case strings.HasPrefix(err.Error(), "saved, but"):
		s.log.Warn("backup default changed, jobs not repointed", "error", err)
		s.renderSettings(w, r, entryBackupStorage, settingsData{Status: http.StatusBadGateway,
			Dest: destData{Notice: &providersNotice{Text: capital(err.Error()) + ".", Bad: true}}})
	default:
		s.serverError(w, r, "make the backup destination the default", err)
	}
}

// removeBackupDestination answers POST /settings/backups/remove, with the destination in "name". The page asks
// first (a <details>); this is the post it sends. The default, and a destination something uses, are refused on
// the row.
func (s *server) removeBackupDestination(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	err := s.svc.DeleteBackupDestination(r.Context(), name)
	var inUse *service.DestinationInUseError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/backup-storage?backups=removed", http.StatusSeeOther)
	case errors.Is(err, service.ErrDestinationNotFound):
		http.Redirect(w, r, "/settings/backup-storage", http.StatusSeeOther)
	case errors.Is(err, service.ErrDestinationIsDefault), errors.As(err, &inUse):
		s.renderSettings(w, r, entryBackupStorage, settingsData{Status: http.StatusConflict,
			Dest: destData{RowErrName: name, RowErr: &fieldError{Text: capital(err.Error()) + "."}, RemoveName: name}})
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected), strings.HasPrefix(err.Error(), "removed, but"):
		s.log.Warn("can't remove the backup destination's secret key", "destination", name, "error", err)
		s.renderSettings(w, r, entryBackupStorage, settingsData{Status: http.StatusBadGateway,
			Dest: destData{Alert: &fieldError{Text: capital(err.Error())}}})
	default:
		s.serverError(w, r, "remove backup destination", err)
	}
}

// destUsedBy says what uses a destination: the services whose policy sends backups there or copies there, and
// the projects whose default it is.
func destUsedBy(uses []service.DestinationUse) string {
	services, projects := map[string]bool{}, 0
	for _, u := range uses {
		if u.Project != "" {
			projects++
			continue
		}
		services[u.App+"/"+u.Service] = true
	}
	switch {
	case len(services) == 0 && projects == 0:
		return "not used"
	case projects == 0:
		return "used by " + plural(len(services), "service")
	case len(services) == 0:
		return "default of " + plural(projects, "project")
	}
	return "used by " + plural(len(services), "service") + " and " + plural(projects, "project")
}

// destBelongs says a backup record went to the destination: it names it, or it names none (a record from before
// destinations had names) and the destination is the default.
func destBelongs(d store.BackupDestination, rec *store.BackupRecord) bool {
	return rec != nil && (rec.Destination == d.Name || rec.Destination == "" && d.IsDefault)
}

// destinationSection fills in the rows of the page and the notice the request carries, keeping what the
// handler set. The rows' marks are backupStorageState's, the same function the nav is decided with; failing
// destinations come first, then the others by name.
func destinationSection(now time.Time, f settingsFacts, st entryState, data destData) destData {
	data.None = len(f.Dests) == 0
	if data.None {
		data.Problem = policyServices(f.Apps) > 0
		if data.Add.Endpoint == "" && data.Add.Name == "" && data.Add.Err == nil {
			data.Add.Name = "main"
		}
	}
	data.Rows = make([]destRow, 0, len(f.Dests))
	for i, v := range f.Dests {
		d := v.BackupDestination
		row := destRow{Name: d.Name, Default: d.IsDefault, Mark: st.Rows[i], Title: endpointName(d), Kind: "S3-compatible · region " + d.Region,
			UsedBy: destUsedBy(v.UsedBy),
			Edit: destinationForm{Name: d.Name, Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix,
				AccessKeyID: d.AccessKeyID, Editing: true, IsDefault: d.IsDefault}}
		last := f.LastBackup
		switch t := d.Test; {
		case destBelongs(d, last) && last.Status != store.BackupSucceeded:
			at := last.StartedAt
			if last.FinishedAt != nil {
				at = *last.FinishedAt
			}
			row.State, row.StateFailed = "last backup failed "+relative(now, at), true
		case t != nil && !t.OK:
			row.State, row.StateFailed, row.Detail = "can't be reached", true, t.Message
		case t != nil:
			row.State = t.Message
		case destBelongs(d, last):
			row.State = "last backup " + backupWhen(now, last.StartedAt)
			if last.Bytes > 0 {
				row.State += " · " + humanBytes(last.Bytes)
			}
		case d.IsDefault && last == nil:
			row.State = "no backup yet"
		default:
			row.State = "not tested yet"
		}
		if t := d.Test; t != nil {
			c := clockOf(now, t.TestedAt)
			row.Event, row.Clock = "tested", &c
		}
		// The note names the failing service when it went to this destination.
		if fail := f.FailingBackup; destBelongs(d, fail) {
			row.Failure = &backupFailure{Service: fail.Service, App: fail.AppID, Cause: causeOf(fail.Message), Detail: fail.Message,
				Href: "/apps/" + fail.AppID + "/backups"}
		}
		if d.Name == data.EditName {
			row.Open = true
			if data.Edit.Name != "" {
				row.Edit = data.Edit
			}
		}
		if d.Name == data.RowErrName {
			row.Err = data.RowErr
			row.RemoveOpen = d.Name == data.RemoveName
		}
		if d.Name == data.Tested {
			row.Tested, row.TestOK, row.TestMessage = true, data.TestedOK, data.TestedMessage
		}
		data.Rows = append(data.Rows, row)
	}
	sortStable(data.Rows, func(r destRow) int { return markRank(r.Mark) })

	switch {
	case data.Saved:
		data.Notice = &providersNotice{Text: "Saved. Every backup that goes here uses it from its next run."}
	case data.Removed:
		data.Notice = &providersNotice{Text: "Removed. Backups already kept there stay in the bucket."}
	case data.MadeDefault:
		data.Notice = &providersNotice{Text: "Made the default. Backups with no destination of their own go there from their next run."}
	case data.Tested != "":
		if data.TestedOK {
			data.Notice = &providersNotice{Text: data.Tested + ": " + data.TestedMessage + "."}
		} else {
			data.Notice = &providersNotice{Text: fmt.Sprintf("%s can't be reached: %s.", data.Tested, strings.TrimSuffix(data.TestedMessage, ".")), Bad: true}
		}
	}
	return data
}
