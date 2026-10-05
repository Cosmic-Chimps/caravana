package ui

import "net/http"

// shellView is what shell.html needs to draw the sidebar and the top bar
// around a console page. The console's pages get one from shellPage; the
// sign-in page and the plain messages have none and stand alone.
type shellView struct {
	Host    string  // shown in mono under the wordmark
	Email   string  // the admin, in the account row
	Current string  // "apps" or "settings": the nav link marked as current
	Crumbs  []crumb // the top bar's breadcrumbs, the last one being this page
}

// crumb is one step of the breadcrumbs. The last has no link.
type crumb struct {
	Label string
	Href  string
	Mono  bool // names Nomad or the user made (an app ID) are set in mono
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

// shellPage starts the data of a console page: the title, and the shell
// around it with the host, the admin's email, the nav link to mark and the
// breadcrumbs. Pages add their own fields on top (embedding pageData).
func (s *server) shellPage(r *http.Request, title, current string, crumbs ...crumb) (pageData, error) {
	email, err := s.auth.AdminEmail(r.Context())
	if err != nil {
		return pageData{}, err
	}
	host := s.host
	if host == "" {
		host = r.Host
	}
	d := s.page(title)
	d.Shell = &shellView{Host: host, Email: email, Current: current, Crumbs: crumbs}
	return d, nil
}
