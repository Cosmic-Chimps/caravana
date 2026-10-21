package ui

import (
	"net/http"
	"slices"
	"strings"
)

// shellView is what shell.html needs to draw the sidebar and the top bar
// around a console page. The console's pages get one from shellPage; the
// sign-in page and the plain messages have none and stand alone.
type shellView struct {
	Host     string  // shown in the sidebar's brand block
	Initial  string  // the host's first letter, upper case, for the brand's mark
	Version  string  // Caravana's version, after "caravana"; "" for none
	Email    string  // the admin, in the account row
	Initials string  // the first two letters of the email's local part, upper case; "?" for none
	Active   string  // navHome, navProjects or navSettings: the sidebar item marked as current
	Crumbs   []crumb // the top bar's breadcrumbs, the last one being this page
	NewApp   bool    // the top bar offers "New app"
}

// crumb is one step of the breadcrumbs. The last is the page itself and has
// no link, whatever its Href says.
type crumb struct {
	Label string
	Href  string
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
	navHome     = "home"
	navProjects = "projects"
	navSettings = "settings"
)

// shellPage starts the data of a console page: the title, and the shell
// around it with the host, the admin's email, the sidebar item to mark and the
// breadcrumbs. Pages add their own fields on top (embedding pageData).
func (s *server) shellPage(r *http.Request, title, active string, crumbs ...crumb) (pageData, error) {
	email, err := s.auth.AdminEmail(r.Context())
	if err != nil {
		return pageData{}, err
	}
	host := s.host
	if host == "" {
		host = r.Host
	}
	d := s.page(title)
	// The last crumb is the page itself: it is not a link.
	crumbs = slices.Clone(crumbs)
	if n := len(crumbs); n > 0 {
		crumbs[n-1].Href = ""
	}
	d.Shell = &shellView{Host: host, Initial: initialOf(host), Version: s.versionLabel(), Email: email, Initials: initialsOf(email),
		Active: active, Crumbs: crumbs, NewApp: s.newApp}
	return d, nil
}

// initialOf is the first letter of s, upper case, for the brand's mark.
func initialOf(s string) string {
	if r := []rune(s); len(r) > 0 {
		return strings.ToUpper(string(r[0]))
	}
	return "?"
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
