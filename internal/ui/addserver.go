package ui

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Add a server (feature 031): /ops/servers/new, the answer of POST /ops/servers with the join
// command, and the join's own page while the server calls in. The page has two states, before (the
// form) and after (the command and the live checks). The join token is shown once, in the answer of
// the POST, and in no other page, part or redirect: the join's page says the command was shown once.
// It reads only internal/service.

// The four checks' words before the server has told anything (contracts/page-servers.md).
var checkWords = []string{
	"Connected to the cluster",
	"Docker running",
	"Reaches the other servers on the private network",
	"Ready to take work",
}

// addServerPage is the whole page. Exactly one of Unavailable, Command and the form is drawn.
type addServerPage struct {
	pageData
	Name, Pool formField
	// Unavailable replaces the form: servers can't be added now.
	Unavailable *serverUnavailable
	// Join is the join once it exists; its Command is set only on the answer of the POST.
	Join   *joinView
	Checks joinChecks
	// Missing says the join asked for doesn't exist (it was cancelled, or its command expired and was cleared).
	Missing bool
}

// serverUnavailable is why no server can be added: the service's sentence, and the line to run on this
// server when the cluster isn't set up yet.
type serverUnavailable struct {
	Reason  sentence
	Install string
}

// joinView is the join as the page states it: the command (once), and the name and pool as facts.
type joinView struct {
	ID     string
	Name   string
	NoName bool
	Pool   string
	// Command is the command broken after the pipe, "" on the join's own page (the token is not kept).
	Command string
	// Note is a development build's note about the command.
	Note string
	// Insecure says the console has no trusted certificate, so the command has --insecure.
	Insecure bool
	// Waiting says nobody called in yet: only then can the join be cancelled here.
	Waiting bool
	Cancel  string
	// Expires says when the command stops working.
	Expires time.Time
}

// joinChecks is the Checks panel: the first line while nobody called in, the four lines, and the
// ending when all four pass. Open says a check isn't done: the panel asks for itself every 5 s.
type joinChecks struct {
	PartHref string
	Open     bool
	Intro    *joinIntro
	Lines    []joinLine
	Ready    *joinReady
}

// joinIntro is the first line: waiting for the call, or an expired command.
type joinIntro struct {
	Mark string
	Text string
	// Href, when set, is the link after the text, named LinkText.
	Href     string
	LinkText string
}

// joinLine is one of the four lines. Tone is done (healthy, fg-1), work (rolling, fg-2) or later
// (idle, fg-3).
type joinLine struct {
	Mark string
	Tone string
	Text string
}

// joinReady is the ending: all four checks pass.
type joinReady struct {
	Sentence string
	Name     string
	Href     string
}

// ---- handlers ---------------------------------------------------------------------------------

// showAddServer answers GET /ops/servers/new: the form, or why there is none.
func (s *server) showAddServer(w http.ResponseWriter, r *http.Request, session string) {
	page := addServerPage{Pool: formField{ID: "pool", Value: "default"}, Name: formField{ID: "name"}}
	view, err := s.svc.ListServers(r.Context())
	if err != nil {
		s.serverError(w, r, "draw Add a server", err)
		return
	}
	if !view.Cluster.CanAdd {
		page.Unavailable = s.unavailableOf(r, view.Cluster, nil)
	}
	page.Checks = joinChecks{Intro: &joinIntro{Mark: "idle", Text: "Make the command to start."}, Lines: joinLinesOf(nil)}
	s.drawAddServer(w, r, http.StatusOK, page)
}

// createServerJoin answers POST /ops/servers: it makes the join and answers 200 with the command. The
// answer is the page, not a redirect, because the token is shown once and a redirect would lose it. A
// refusal draws the form again with the reason at its field (422, 409), the form filled as typed.
func (s *server) createServerJoin(w http.ResponseWriter, r *http.Request, session string) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	pool := strings.TrimSpace(r.PostFormValue("pool"))
	page := addServerPage{Name: formField{ID: "name", Value: name}, Pool: formField{ID: "pool", Value: cmp.Or(pool, "default")}}

	j, err := s.svc.CreateJoin(r.Context(), name, pool, s.origin(r))
	var fe *app.FieldError
	var re *service.RefusalError
	switch {
	case err == nil:
		s.drawJoinAnswer(w, r, page, j)
		return
	case errors.Is(err, service.ErrClusterNotReady):
		page.Unavailable = s.unavailableOf(r, service.ClusterView{}, err)
		s.drawAddServer(w, r, http.StatusConflict, page)
	case errors.Is(err, service.ErrClusterFull):
		page.Unavailable = &serverUnavailable{Reason: plainS(clusterFullWords)}
		s.drawAddServer(w, r, http.StatusConflict, page)
	case errors.As(err, &re) && re.Field == "name":
		page.Name.Error, page.Name.Alert = plainS(refusalWords(re)), true
		s.drawAddServer(w, r, http.StatusConflict, page)
	case errors.As(err, &fe) && (fe.Field == "name" || fe.Field == "pool"):
		field := &page.Name
		if fe.Field == "pool" {
			field = &page.Pool
		}
		field.Error, field.Alert = plainS(refusalWords(errors.New(labelOf(fe.Field)+" "+fe.Message))), true
		s.drawAddServer(w, r, http.StatusUnprocessableEntity, page)
	default:
		s.serverError(w, r, "make a join command", err)
	}
}

// clusterFullWords is the refusal of a cluster that has the most servers it can.
var clusterFullWords = "This cluster has " + strconv.Itoa(mesh.MaxIndex) + " servers, the most it can have."

// labelOf is a field's name as the sentence starts with it: "Name", "Pool".
func labelOf(field string) string {
	if field == "" {
		return ""
	}
	return strings.ToUpper(field[:1]) + field[1:]
}

// unavailableOf is why no server can be added: the cluster isn't set up (the service's sentence and the
// installer line to run on this server) or it is full. cluster is the listed cluster, err the refusal of a
// post (nil on a read).
func (s *server) unavailableOf(r *http.Request, c service.ClusterView, err error) *serverUnavailable {
	if c.Servers >= mesh.MaxIndex || errors.Is(err, service.ErrClusterFull) {
		return &serverUnavailable{Reason: plainS(clusterFullWords)}
	}
	reason := c.CannotAddReason
	if err != nil {
		reason = err.Error()
	}
	out := &serverUnavailable{Reason: plainS(reason)}
	// The line is the one Updates shows for the installer, when this build knows a release.
	if v, uerr := s.svc.Updates(r.Context()); uerr == nil {
		out.Install = v.Caravana.Install
	}
	return out
}

// showServerJoin answers GET /ops/servers/joins/{id}: the join's page, without the command.
func (s *server) showServerJoin(w http.ResponseWriter, r *http.Request, session string) {
	page, status, err := s.loadJoinPage(r)
	if err != nil {
		s.serverError(w, r, "draw the join", err)
		return
	}
	s.drawAddServer(w, r, status, page)
}

// partServerJoin answers GET /parts/ops/servers/joins/{id}: the Checks panel alone.
func (s *server) partServerJoin(w http.ResponseWriter, r *http.Request, session string) {
	page, status, err := s.loadJoinPage(r)
	if err != nil {
		s.log.Error("draw the join", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if status == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	s.renderPart(w, "addserver", "join-checks-region", page.Checks)
}

// cancelServerJoin answers POST /ops/servers/joins/{id}/cancel: it removes the join (303 to the list). A
// join that already joined and runs can't be cancelled here: the refusal says so.
func (s *server) cancelServerJoin(w http.ResponseWriter, r *http.Request, session string) {
	err := s.svc.RemoveServer(r.Context(), r.PathValue("id"))
	var re *service.RefusalError
	switch {
	case err == nil, errors.As(err, &re) && errors.Is(err, service.ErrServerNotFound):
		http.Redirect(w, r, "/ops/servers", http.StatusSeeOther)
	case errors.Is(err, service.ErrServerRunning), errors.Is(err, service.ErrFirstServer):
		s.message(w, r, http.StatusConflict, "Can't cancel", "This join can't be cancelled.", serverRefusalWords(err))
	default:
		s.serverError(w, r, "cancel a join", err)
	}
}

// ---- loaders ----------------------------------------------------------------------------------

// loadJoinPage reads the join named by the path and builds the page without a command. A join that
// doesn't exist is 404.
func (s *server) loadJoinPage(r *http.Request) (addServerPage, int, error) {
	id := r.PathValue("id")
	page := addServerPage{Name: formField{ID: "name"}, Pool: formField{ID: "pool", Value: "default"}}
	view, err := s.svc.ListServers(r.Context())
	if err != nil {
		return page, 0, err
	}
	for _, sv := range view.Servers {
		if sv.ID == id && sv.ID != "" && sv.Managed {
			page.Join = joinViewOf(sv, "", "", false)
			page.Checks = joinChecksOf(s.now().UTC(), sv, view)
			return page, http.StatusOK, nil
		}
	}
	page.Missing = true
	return page, http.StatusNotFound, nil
}

// drawJoinAnswer draws the page of a join just made: the command, the facts and the live checks.
func (s *server) drawJoinAnswer(w http.ResponseWriter, r *http.Request, page addServerPage, j service.ServerJoin) {
	page.Join = joinViewOf(j.Server, j.Command, j.CommandNote, strings.Contains(j.Command, " --insecure"))
	page.Join.Expires = j.ExpiresAt
	if view, err := s.svc.ListServers(r.Context()); err == nil {
		page.Checks = joinChecksOf(s.now().UTC(), j.Server, view)
	} else {
		page.Checks = joinChecksOf(s.now().UTC(), j.Server, service.ServersView{})
	}
	s.drawAddServer(w, r, http.StatusOK, page)
}

// drawAddServer writes the page with the status; a page whose checks are open reloads itself without
// script (a meta refresh of the join's own address, never of the POST).
func (s *server) drawAddServer(w http.ResponseWriter, r *http.Request, status int, page addServerPage) {
	title := "Add a server"
	if page.Missing {
		title = "Join not found"
	}
	head, err := s.shellPage(r, title, navOpsServer, crumb{Label: "Servers", Href: "/ops/servers"}, crumb{Label: title})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	if page.Join != nil && page.Checks.Open {
		head.LiveURL = "/live?watch=ops"
		head.Refresh = joinRefreshSeconds
		head.RefreshURL = "/ops/servers/joins/" + url.PathEscape(page.Join.ID)
	} else if page.Join != nil {
		head.LiveURL = "/live?watch=ops"
	}
	page.pageData = head
	s.render(w, status, "addserver", page)
}

// joinRefreshSeconds is how often a page without script reloads itself while a check is open.
const joinRefreshSeconds = 10

// ---- views ------------------------------------------------------------------------------------

// joinViewOf is the join's facts. command is the join command, "" when it isn't shown.
func joinViewOf(sv service.ServerView, command, note string, insecure bool) *joinView {
	v := &joinView{ID: sv.ID, Name: sv.Name, NoName: sv.Name == "", Pool: cmp.Or(sv.Pool, "default"), Note: note,
		Insecure: insecure, Waiting: sv.State == "waiting", Cancel: "/ops/servers/joins/" + url.PathEscape(sv.ID) + "/cancel", Expires: sv.ExpiresAt}
	if v.NoName {
		v.Name = "the machine's own name"
	}
	if command != "" {
		v.Command = formatJoinCommand(command)
	}
	return v
}

// formatJoinCommand breaks the command for reading: after the pipe, and the options each on a line of
// their own, indented two spaces, joined with backslashes so the text still runs as one command.
func formatJoinCommand(command string) string {
	head, rest, ok := strings.Cut(command, " --join ")
	if !ok {
		return command
	}
	opts := strings.Split("--join "+rest, " --")
	for i := 1; i < len(opts); i++ {
		opts[i] = "--" + opts[i]
	}
	lines := []string{head + " \\"}
	for i, o := range opts {
		line := "  " + o
		if i < len(opts)-1 {
			line += " \\"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// joinChecksOf builds the Checks panel of a server. view is the cluster, for the sentence of the third
// server.
func joinChecksOf(now time.Time, sv service.ServerView, view service.ServersView) joinChecks {
	c := joinChecks{PartHref: "/parts/ops/servers/joins/" + url.PathEscape(sv.ID)}
	switch {
	case sv.State == "waiting" && !sv.ExpiresAt.After(now):
		c.Intro = &joinIntro{Mark: "warning", Text: "The command expired.", Href: "/ops/servers/new", LinkText: "Make a new one."}
		c.Lines = joinLinesOf(nil)
		return c
	case sv.State == "waiting":
		c.Intro = &joinIntro{Mark: "rolling", Text: "Waiting for the server to call in…"}
		c.Lines = joinLinesOf(nil)
		c.Open = true
		return c
	}
	c.Lines = joinLinesOf(sv.Checks)
	if sv.State == "ready" {
		c.Ready = joinReadyOf(sv, view)
		return c
	}
	c.Open = true
	return c
}

// joinLinesOf are the four lines: the checks done with the API's words, the one being worked on with its
// words too, and the ones after it idle with the default words.
func joinLinesOf(checks []service.ServerCheck) []joinLine {
	out := make([]joinLine, len(checkWords))
	open := false
	for i, words := range checkWords {
		line := joinLine{Mark: "idle", Tone: "later", Text: words}
		if i < len(checks) {
			switch c := checks[i]; {
			case c.OK:
				line = joinLine{Mark: "healthy", Tone: "done", Text: c.Detail}
			case !open:
				line = joinLine{Mark: "rolling", Tone: "work", Text: cmp.Or(c.Detail, words)}
				open = true
			default:
				open = true
			}
		}
		out[i] = line
	}
	return out
}

// joinReadyOf is the ending of a server whose four checks pass: what it adds, and, for the third
// server, that the cluster now survives the loss of one.
func joinReadyOf(sv service.ServerView, view service.ServersView) *joinReady {
	r := &joinReady{Name: sv.Name, Href: "/ops/servers/" + url.PathEscape(sv.Name)}
	r.Sentence = sv.Name + " is ready to take work."
	if sv.Memory != nil && sv.Memory.Total > 0 {
		r.Sentence = sv.Name + " adds " + gbText(float64(sv.Memory.Total)) + " GB."
	}
	joined := 0
	var others []string
	for _, o := range view.Servers {
		if o.State == "waiting" {
			continue
		}
		joined++
		if !o.First {
			others = append(others, o.Name)
		}
	}
	if joined == 3 && len(others) == 2 {
		r.Sentence += " With three servers the cluster survives the loss of one: " + others[0] + " and " + others[1] + " are starting to run the scheduler too."
	}
	return r
}
