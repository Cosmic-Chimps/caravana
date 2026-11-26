package ui

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file is Configure in sections (feature 059): one concern per page, each with its own address and
// its own form. Overview reads what the app is made of; Source, Services and Integrations change one
// kind of thing each; Variables & secrets and Domains are their own pages, drawn inside the same frame.

// The sections of Configure, as the address and the page's Section name them.
const (
	sectionOverview     = "overview"
	sectionSource       = "source"
	sectionServices     = "services"
	sectionVariables    = "variables"
	sectionDomains      = "domains"
	sectionIntegrations = "integrations"
)

// configNav is the section nav drawn beside every section's page (contract §1).
type configNav struct {
	Items []configNavItem
}

// configNavItem is one entry of the nav; Count is "" when there is nothing to count.
type configNavItem struct {
	Key, Label, Href, Count string
	Current                 bool
}

// sectionHref is the address of a section of the app's Configure.
func sectionHref(id, section string) string {
	base := "/apps/" + id
	switch section {
	case sectionOverview:
		return base + "/configure"
	case sectionVariables, sectionDomains:
		return base + "/" + section
	}
	return base + "/configure/" + section
}

// configNavOf is the nav of app a with the named section current. The counts come from the stored app.
func configNavOf(a app.App, current string) *configNav {
	secrets := secretsPanelOf(a)
	count := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	items := []struct{ key, label, count string }{
		{sectionOverview, "Overview", ""},
		{sectionSource, "Source", ""},
		{sectionServices, "Services", count(len(a.Services))},
		{sectionVariables, "Variables & secrets", count(secrets.Variables + secrets.Count)},
		{sectionDomains, "Domains", count(len(domainRowsOf(a)))},
		{sectionIntegrations, "Integrations", ""},
	}
	nav := &configNav{}
	for _, it := range items {
		nav.Items = append(nav.Items, configNavItem{Key: it.key, Label: it.label, Href: sectionHref(a.ID, it.key), Count: it.count, Current: it.key == current})
	}
	return nav
}

// overviewView is what Overview adds to the tab (contract §2): the summaries that the other sections
// hold the forms for.
type overviewView struct {
	Source   overviewSource
	Services []overviewService
	Backups  []overviewBackup
	// NoBackups says no service has a backup policy.
	NoBackups bool
	// The panels' buttons lead to these.
	SourceHref, ServicesHref, IntegrationsHref, BackupsHref string
}

// overviewSource is the Source panel: the strong line, the second line and the button.
type overviewSource struct {
	Strong sentence
	Line   string
	Button string
}

// overviewService is one row of the Services panel, in the four columns of the grid.
type overviewService struct {
	Name, Image, Copies, Size, Check, Volumes string
}

// overviewBackup is one backed-up service of the Backups panel.
type overviewBackup struct {
	Service, Words, Where string
	Last                  when
	HasLast               bool
}

// overviewServiceOf is the row of one service: its image, copies and size, and its check and volumes.
func overviewServiceOf(svc app.Service) overviewService {
	row := overviewService{Name: svc.Name, Image: svc.Image}
	n := 1
	if svc.Replicas != nil {
		n = *svc.Replicas
	}
	row.Copies = copyCount(n)
	if sc := svc.Scale; sc != nil {
		row.Copies = strconv.Itoa(sc.Min) + "–" + strconv.Itoa(sc.Max) + " copies"
	}
	r := svc.Resources
	switch {
	case r.MemoryMB > 0 && r.MemoryMaxMB > r.MemoryMB:
		row.Size = sizeText(int64(r.MemoryMB)) + ", up to " + sizeText(int64(r.MemoryMaxMB))
	case r.MemoryMB > 0:
		row.Size = sizeText(int64(r.MemoryMB))
	case r.MemoryMaxMB > 0:
		row.Size = "up to " + sizeText(int64(r.MemoryMaxMB))
	default:
		row.Size = "default size"
	}
	switch {
	case len(svc.Ports) == 0:
		row.Check = "no port, no check"
	case svc.Health != nil && svc.Health.Path != "":
		row.Check = "GET " + svc.Health.Path
	default:
		row.Check = "TCP check"
	}
	var volumes []string
	for _, v := range svc.Volumes {
		volumes = append(volumes, "volume "+v.Name)
	}
	row.Volumes = strings.Join(volumes, ", ")
	return row
}

// overviewSourceOf says what the app is made from (contract §2, panel 1). The strong line puts the
// template first; the second line and the button follow the app's own kind.
func overviewSourceOf(a app.App, rec store.AppRecord, templateFact sentence) overviewSource {
	var kind sentence
	var line, button string
	switch {
	case !a.Defined():
		kind, line, button = sentence{plain("Nothing yet")}, "Not defined yet.", "Define the app"
	case rec.Compose != nil:
		kind, line, button = sentence{plain("Compose file")}, "Made from a Compose file, kept as you typed it.", "Edit the Compose file"
	case a.Source.Kind == app.SourceCompose:
		kind, line, button = sentence{plain("Compose file, not kept")}, "Made from a Compose file that wasn't kept. Paste it once to edit it here.", "Edit the Compose file"
	default:
		if i, built := a.BuiltService(); built {
			b := a.Services[i].Build
			kind = sentence{plain("Git · "), mono(b.Repository), plain(" "), mono(b.Branch)}
			line, button = "Built from "+b.Branch+" on each deploy.", "Change the build"
		} else {
			word := "Image"
			if len(a.Services) > 1 {
				word = "Images"
			}
			kind, line, button = sentence{plain(word)}, "Each service runs the image it names.", "Change the images"
		}
	}
	if len(templateFact) > 0 {
		kind = templateFact
	}
	return overviewSource{Strong: kind, Line: line, Button: button}
}

// overviewOf builds the Overview's summaries from the stored app. backups are the app's backups,
// newest first (nil when they couldn't be read: the panel then leaves the last backup out).
func (s *server) overviewOf(r *http.Request, sum service.AppSummary, templateFact sentence, backups []store.BackupRecord) overviewView {
	a := sum.App
	v := overviewView{
		Source:     overviewSourceOf(a, sum.AppRecord, templateFact),
		SourceHref: sectionHref(a.ID, sectionSource), ServicesHref: sectionHref(a.ID, sectionServices),
		IntegrationsHref: sectionHref(a.ID, sectionIntegrations), BackupsHref: "/apps/" + a.ID + "/backups",
	}
	for _, svc := range a.Services {
		v.Services = append(v.Services, overviewServiceOf(svc))
	}
	for _, svc := range a.Services {
		b := svc.Backup
		if b == nil {
			continue
		}
		row := overviewBackup{Service: svc.Name}
		if b.Automatic() {
			row.Words = scheduleText(b.ScheduleOrDefault()) + ", keeping " + strconv.Itoa(b.KeepOrDefault())
		} else {
			row.Words = scheduleText(b.ScheduleOrDefault())
		}
		if primary, copies, err := s.svc.BackupPlaces(r.Context(), a.ID, *b); err == nil && primary != "" {
			row.Where = "Go to " + primary
			if len(copies) > 0 {
				row.Where += ", copied to " + strings.Join(copies, ", ")
			}
			row.Where += "."
		}
		for _, rec := range backups {
			if rec.Service == svc.Name && rec.Status == store.BackupSucceeded && rec.FinishedAt != nil {
				row.Last, row.HasLast = whenOf(s.now(), rec.FinishedAt, ""), true
				break
			}
		}
		v.Backups = append(v.Backups, row)
	}
	v.NoBackups = len(v.Backups) == 0
	return v
}

// sourceFields are the inputs of the Source form; the others of a service's form are the Services page's.
var sourceFields = map[string]bool{"image": true, "port": true, "domainPort": true}

// onSourcePage says whether the input is drawn on Source (true) or on Services (false).
func onSourcePage(field string) bool { return sourceFields[field] }

// moveHiddenErrors moves the errors of the inputs the section doesn't draw into the form's notice, so a
// refusal is never invisible: a registry that refuses the stored image on a Services post, say, is said
// at the top of that page, naming the service.
func (f *editForm) moveHiddenErrors(section string) {
	for i := range f.Services {
		sv := &f.Services[i]
		for _, in := range sv.fields() {
			if len(in.Field.Error) == 0 || (section == sectionSource) == onSourcePage(in.Name) {
				continue
			}
			if len(f.Notice) > 0 {
				f.Notice = append(f.Notice, plain(" "))
			}
			f.Notice = append(f.Notice, mono(sv.Name), plain(": "))
			f.Notice = append(f.Notice, in.Field.Error...)
			in.Field.Error, in.Field.Alert = nil, false
		}
	}
}

// sectionOfPost is the section a posted per-service form came from: its hidden section field, else the
// section of the first input that has an error (a form drawn before Configure was split posts none).
func sectionOfPost(post interface{ Get(string) string }, f editForm) string {
	switch sec := post.Get("section"); sec {
	case sectionSource, sectionServices:
		return sec
	}
	for _, sv := range f.Services {
		for _, in := range sv.fields() {
			if len(in.Field.Error) > 0 && onSourcePage(in.Name) {
				return sectionSource
			}
		}
	}
	return sectionServices
}

// KeptEdit says the app keeps its Compose file and the person may change it: Services then draws each
// service's health check as a form that writes it into the file's overlay, and its copies and memory as
// inputs written into the file's text when the file allows it (SizeInFile), else read-only.
func (t configureTab) KeptEdit() bool { return t.ComposeForm != nil && t.ComposeForm.Kept }

// addKeptSizes says, for each service of an app that keeps its Compose file, whether Services may draw
// its copies and memory as inputs: only when compose.SetResources can write them into text, the kept
// file, without guessing. Otherwise they stay read-only text, with the reason.
func addKeptSizes(v *editView, text string) {
	for i := range v.Services {
		sv := &v.Services[i]
		err := compose.CheckResources(text, sv.Name, !sv.Scaled && !sv.OneCopy)
		sv.SizeInFile = err == nil
		if edit := (*compose.EditError)(nil); errors.As(err, &edit) {
			sv.SizeLocked = edit.Path + " " + edit.Reason
		}
	}
}

// HasBuild says a service of the app is built from a Git repository.
func (t configureTab) HasBuild() bool {
	return slices.ContainsFunc(t.Edit.Services, func(sv editServiceView) bool { return sv.Build != nil })
}

// SourceLabel is the head of the Source page: what the page holds for this app.
func (t configureTab) SourceLabel() string {
	switch {
	case t.Define != nil:
		return "Define the app"
	case t.ComposeForm != nil:
		return "Compose file"
	case t.HasBuild():
		return "Build"
	}
	return "Images"
}

// SourceIntro is the sentence under the Source head.
func (t configureTab) SourceIntro() string {
	switch {
	case t.Define != nil:
		return "This app has no definition yet. Give it an image, and saving deploys it."
	case t.NoConfigure:
		return "What this app runs. Your role here is to look: changing how this app is set up is for people who maintain it."
	case t.ComposeForm != nil && t.ComposeForm.Kept:
		return "This app is edited through its Compose file. Change the file, preview what changes, then deploy. Health checks are on Services, secrets on Variables & secrets, domains on Domains and backups on the Backups tab: a deploy from here keeps them as they are."
	case t.ComposeForm != nil:
		return "This app was made from a Compose file that wasn't kept. Paste it once to edit it here from now on."
	case t.HasBuild():
		return "Where the code is read from and how it is built. Saving builds the newest commit and deploys it."
	}
	return "The image each service runs and the port it listens on. Each service is saved on its own, and saving is a deploy."
}
