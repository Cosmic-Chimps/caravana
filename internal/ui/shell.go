package ui

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// shellView is what shell.html needs to draw the sidebar and the top bar
// around a console page. The console's pages get one from shellPage; the
// sign-in page and the plain messages have none and stand alone.
type shellView struct {
	Host     string  // shown in the sidebar's brand block, beside the logo mark
	Version  string  // Caravana's version, after "caravana"; "" for none
	Email    string  // the signed-in person, in the account row
	Initials string  // the first two letters of the email's local part, upper case; "?" for none
	Active   string  // navHome, navProjects, navPreviews, navSchedules, navDomains, navLogs, navRequests, navMonitoring or navSettings: the sidebar item marked as current
	Crumbs   []crumb // the top bar's breadcrumbs, the last one being this page
	NewApp   bool    // the top bar offers "New app"
	// Admin says the person is an owner or an admin: the Developer/Operator toggle is theirs (feature 053).
	Admin bool
	// Mode is the view the page is drawn in: "dev" (Developer) or "ops"
	// (Operator). It picks the sidebar's links and which side of the toggle is
	// current; see mode.go.
	Mode string
	// UpdatesMark is the little mark after the Operator sidebar's Updates item
	// (a ■ reboot needed, a ◆ security updates waiting, else a count); empty
	// draws nothing. shellPage fills it in the Operator view.
	UpdatesMark navMark
}

// Ops says the page is drawn in the Operator view.
func (v *shellView) Ops() bool { return v.Mode == "ops" }

// crumb is one step of the breadcrumbs. The last is the page itself and has
// no link, whatever its Href says.
type crumb struct {
	Label string
	Href  string
	// Container says the crumb names a place that holds the page (an app's
	// environment), not the page itself: as the last crumb it is plain text, but not
	// "the current page" to a screen reader.
	Container bool
}

// statusView is a status as a page shows it: a mark and a word, never color
// alone (DESIGN.md, The Shape-and-Color Rule). Kind is healthy, rolling,
// warning, failed or idle; it picks the mark's shape and the pill's color.
// The "status-mark" and "status-pill" templates in parts.html take one.
type statusView struct {
	Kind string
	Word string
}

// emptyView is an empty list's sentence, what to do about it, and the one
// action that does it. The "empty" template in parts.html takes one; with no
// ActionHref there is no button.
type emptyView struct {
	Title       string
	Text        string
	ActionLabel string
	ActionHref  string
}

// The sidebar items a page can mark as current. Home is the Apps list, and
// Projects holds every page of a project and of an app in it.
const (
	navHome       = "home"
	navProjects   = "projects"
	navPreviews   = "previews"   // every page under /previews (feature 049)
	navSchedules  = "schedules"  // every page under /schedules (feature 022)
	navDomains    = "domains"    // every page under /domains (feature 023)
	navLogs       = "logs"       // every page under /logs (feature 024)
	navRequests   = "requests"   // every page under /requests (feature 047)
	navMonitoring = "monitoring" // every page under /monitoring (feature 025)
	navSettings   = "settings"

	// The Operator view's items (feature 027): Overview (/ops), Server
	// (/ops/servers) and Updates (/ops/updates).
	navOpsOverview = "ops-overview"
	navOpsServer   = "ops-server"
	navOpsUpdates  = "ops-updates"
	// Security (feature 044): the image scanner's page, /ops/security.
	navOpsSecurity = "ops-security"
	// Console address (feature 051): the domain the console answers on, /ops/console-address.
	navOpsConsoleAddress = "ops-console-address"
	// Caravana backup (feature 063): Caravana's own backup, /ops/self-backup.
	navOpsSelfBackup = "ops-self-backup"
)

// shellPage starts the data of a console page: the title, and the shell
// around it with the host, the admin's email, the sidebar item to mark and the
// breadcrumbs. Pages add their own fields on top (embedding pageData).
func (s *server) shellPage(r *http.Request, title, active string, crumbs ...crumb) (pageData, error) {
	who, ok := service.PrincipalFrom(r.Context())
	if !ok {
		return pageData{}, errors.New("no signed-in person in the request")
	}
	email := who.Email
	host := s.host
	if host == "" {
		host = r.Host
	}
	d := s.page(r, title)
	// The last crumb is the page itself: it is not a link.
	crumbs = slices.Clone(crumbs)
	if n := len(crumbs); n > 0 {
		crumbs[n-1].Href = ""
	}
	mode := s.modeOf(r)
	if !who.Admin() {
		mode = "dev" // the Operator view is the installation's: a member's cookie can't draw it
	}
	d.Shell = &shellView{Host: host, Version: s.versionLabel(), Email: email, Initials: initialsOf(email),
		Active: activeIn(mode, active), Crumbs: crumbs, NewApp: s.newApp && s.mayCreateApps(r.Context(), who), Mode: mode, Admin: who.Admin()}
	d.Who, d.now = who, s.now()
	if mode == "ops" {
		d.Shell.UpdatesMark = s.updatesMark(r)
	}
	return d, nil
}

// activeIn is the sidebar item to mark for a page that asked for active, in
// the given view. The Operator view has its own items and Settings; a page
// both views share (an app's pages, New app) is marked Overview there, since
// the Operator sidebar has nothing closer (contracts/page1-overview.md).
func activeIn(mode, active string) string {
	if mode != "ops" {
		return active
	}
	switch active {
	case navSettings, navOpsOverview, navOpsServer, navOpsUpdates, navOpsSecurity, navOpsConsoleAddress, navOpsSelfBackup:
		return active
	}
	return navOpsOverview
}

// initialsOf is the first two letters of an email's local part, upper case:
// "ana.diaz@example.com" is "AN". An address with none has "?".
func initialsOf(email string) string {
	local, _, _ := strings.Cut(email, "@")
	if r := []rune(local); len(r) > 0 {
		return strings.ToUpper(string(r[:min(len(r), 2)]))
	}
	return "?"
}

// versionLabel is Caravana's version as the sidebar shows it: "v0.7.0" for a
// release, "dev" for any other build, "" when the server wasn't told.
func (s *server) versionLabel() string {
	switch s.version {
	case "", "dev":
		return s.version
	}
	return "v" + s.version
}
