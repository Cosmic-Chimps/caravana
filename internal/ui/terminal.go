package ui

import (
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The terminal page (feature 020.6): an interactive shell in one copy, drawn by
// xterm.js (vendored, see static/js/LICENSES.md) and driven by static/js/terminal.js
// over the API's websocket (openTerminal). This file only serves the page that
// holds them; no typed input or output ever passes through the console's Go code.

// consoleCSP is the policy of every console page: nothing is loaded from
// elsewhere, nothing frames them, forms post only to caravana.
const consoleCSP = "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'"

// terminalCSP is the terminal page's alone. xterm.js makes <style> elements and
// sets style attributes, which `style-src 'self'` refuses, so styles (never
// scripts) may be inline on this page; connect-src names the websocket's own
// origin. Scripts stay 'self': the page has no inline script.
const terminalCSP = "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'"

// terminalHref is the page of a copy's terminal.
func terminalHref(appID, copyID string) string {
	return "/apps/" + url.PathEscape(appID) + "/copies/" + url.PathEscape(copyID) + "/terminal"
}

// shellChoice is one entry of the page's shell picker.
type shellChoice struct {
	Value, Label string
	Selected     bool
}

// terminalView is what the terminal page draws.
type terminalView struct {
	AppID   string
	Service string
	CopyID  string
	Short   string
	Back    string // the Running tab
	// Socket is the API path the script connects to, without the query.
	Socket string
	Shells []shellChoice
}

// terminalPage is the whole terminal page.
type terminalPage struct {
	pageData
	terminalView
}

// showTerminal answers GET /apps/{id}/copies/{copyId}/terminal. It refuses, with
// a plain page and the status the API gives, an app or copy that doesn't exist, a
// stopped app, and a copy that isn't running; the websocket checks it all again.
func (s *server) showTerminal(w http.ResponseWriter, r *http.Request, session string) {
	id, copyID := r.PathValue("id"), r.PathValue("copyId")
	rec, err := s.svc.GetApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	if rec.StoppedAt != nil {
		s.message(w, http.StatusConflict, "Terminal", "The app is stopped.", appStoppedText)
		return
	}
	copies, err := s.svc.Copies(r.Context(), id)
	if errors.Is(err, deploy.ErrNomadUnavailable) || errors.Is(err, deploy.ErrNomadRejected) {
		s.log.Warn("can't read copies for a terminal", "app", id, "error", err)
		s.message(w, http.StatusBadGateway, "Terminal", "Nomad can't be reached.",
			"The terminal needs to know the app's copies, and Nomad didn't answer. Try again in a moment.")
		return
	}
	if err != nil {
		s.serverError(w, "list copies", err)
		return
	}
	i := slices.IndexFunc(copies, func(c service.Copy) bool { return c.ID == copyID })
	if i < 0 {
		s.message(w, http.StatusNotFound, "Terminal", "That copy isn't part of the app.",
			"The Running tab lists the copies the app has now.")
		return
	}
	if c := copies[i]; c.Terminal || c.Stopping {
		s.message(w, http.StatusConflict, "Terminal", "That copy isn't running.",
			"A terminal opens only into a copy that is running. The Running tab lists the copies the app has now.")
		return
	}

	v := terminalView{AppID: id, Service: copies[i].Service, CopyID: copyID, Short: shortID(copyID),
		Back: "/apps/" + url.PathEscape(id), Socket: "/v1/apps/" + url.PathEscape(id) + "/copies/" + url.PathEscape(copyID) + "/terminal"}
	picked := r.URL.Query().Get("shell")
	if picked != "bash" && picked != "sh" {
		picked = "auto"
	}
	for _, c := range []shellChoice{{"auto", "Automatic", false}, {"bash", "bash", false}, {"sh", "sh", false}} {
		c.Selected = c.Value == picked
		v.Shells = append(v.Shells, c)
	}

	head, err := s.shellPage(r, "Terminal "+v.Short, navProjects, s.appCrumbs(r.Context(), id,
		crumb{Label: id, Href: v.Back}, crumb{Label: "Terminal"})...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	// The middleware has set the console's policy; this page's replaces it.
	w.Header().Set("Content-Security-Policy", terminalCSP)
	s.render(w, http.StatusOK, "terminal", terminalPage{pageData: head, terminalView: v})
}
