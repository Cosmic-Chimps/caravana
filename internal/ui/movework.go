package ui

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Moving work off a server in the console (feature 042): the actions in the server page's head, the
// move panel that leads its body, the confirmation page (/ops/servers/{name}/move) and the two posts.
// It reads and changes only through internal/service (MoveWorkOff, AcceptWork, PreviewMoveOff).

// moveShownFor is how long the panel of a move that ended stays on the page of a server that takes work
// again: a move that ended and was accepted is history, and the Overview's feed has it.
const moveShownFor = time.Hour

// moveDeadlines are the choices of how long a move may take, in minutes; moveDeadlineDefault is the one
// selected.
var moveDeadlines = []int{5, 15, 30, 60}

const moveDeadlineDefault = 15

// moving says the server's newest move is going.
func moving(v service.ServerView) bool { return v.Move != nil && v.Move.State == store.MoveMoving }

// moveActions are the head's actions: Move work off for a ready server that takes work, Accept work
// again for a ready server that doesn't. None while a move goes (its panel has Cancel) and none for a
// server that isn't ready.
func moveActions(v service.ServerView) (moveHref, acceptAction string) {
	switch {
	case !v.Managed || v.State != "ready" || moving(v):
		return "", ""
	case v.AcceptsWork:
		return serverHref(v.Name) + "/move", ""
	}
	return "", serverHref(v.Name) + "/accept"
}

// srvStay is one thing a move leaves where it is: who, and why.
type srvStay struct {
	Who    sentence
	Reason string
}

// srvMove is the move panel: Title is its one line, Until the deadline of a move that goes, Detail the
// sentence under the title, Stays what the move doesn't move. Cancel and Accept are the addresses of the
// two buttons, never both.
type srvMove struct {
	State  string
	Mark   string
	Title  string
	Until  *clockTime
	Detail sentence
	// Note is the quiet line "This server takes no new work."
	Note   string
	Stays  []srvStay
	Cancel string
	Accept string
}

// moveOf builds the panel for the server's newest move, nil when there is nothing to say: no move, or
// one that ended an hour ago on a server that takes work again.
func moveOf(now time.Time, v service.ServerView, pl places) *srvMove {
	m := v.Move
	if m == nil {
		return nil
	}
	if m.State != store.MoveMoving && v.AcceptsWork && now.Sub(m.EndedAt) > moveShownFor {
		return nil
	}
	out := &srvMove{State: m.State, Stays: movedStays(m.Stays, pl)}
	total := m.Moved + m.Left
	switch m.State {
	case store.MoveMoving:
		until := clockOf(now, m.DeadlineAt)
		out.Mark, out.Until = "rolling", &until
		out.Title = fmt.Sprintf("Moving work off · %d of %d moved", m.Moved, total)
		out.Cancel = serverHref(v.Name) + "/accept"
	case store.MoveDone:
		out.Mark = "healthy"
		if m.Moved == 0 {
			out.Title = "Nothing here needed to move"
		} else {
			out.Title = "Moved " + copyCount(m.Moved) + " in " + elapsedWords(m.EndedAt.Sub(m.StartedAt))
		}
	case store.MoveIncomplete:
		out.Mark = "warning"
		out.Title = "The move didn't finish"
		if total > 0 {
			out.Title = fmt.Sprintf("The move didn't finish · %d of %d moved", m.Moved, total)
		}
		if m.Left > 0 {
			out.Detail = sentence{plain(copyCount(m.Left) + " still run here. They are listed under Running here.")}
		}
		if m.Error != "" {
			out.Detail = append(out.Detail, plain(" "+m.Error))
		}
	case store.MoveCancelled:
		out.Mark = "idle"
		out.Title = "The move was cancelled"
		if m.Moved > 0 {
			out.Title += " · " + copyCount(m.Moved) + " had moved and stay where they are"
		}
	}
	if m.State != store.MoveMoving && !v.AcceptsWork {
		out.Note = "This server takes no new work."
		out.Accept = serverHref(v.Name) + "/accept"
	}
	return out
}

// movedStays are the things a move leaves, as lines.
func movedStays(stays []service.MoveStay, pl places) []srvStay {
	var out []srvStay
	for _, st := range stays {
		out = append(out, srvStay{Who: stayWho(pl, st.App, st.Service), Reason: st.Reason})
	}
	return out
}

// stayWho names what stays: an app's service in its place, or the job's own name for what isn't an app
// (the proxy, a helper job).
func stayWho(pl places, id, svc string) sentence {
	place, err := app.PlaceOf(id)
	if err != nil {
		place = app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: id}
	}
	suffix := ""
	if svc != "" {
		suffix = "/" + svc
	}
	return pl.label(place, id, suffix)
}

// elapsedWords is a span in the words a person says: "2 minutes", "40 seconds", "a moment".
func elapsedWords(d time.Duration) string {
	secs := int(d.Round(time.Second) / time.Second)
	if secs < 1 {
		return "a moment"
	}
	if secs >= 120 {
		return plural(int((d+30*time.Second)/time.Minute), "minute")
	}
	return durationWords(secs)
}

// ---- the confirmation page ---------------------------------------------------------------------

// moveServerPage is the confirmation page. Refusal, when set, takes the panel's and the button's place.
type moveServerPage struct {
	pageData
	Name      string
	Headline  sentence
	Refusal   sentence
	Moves     []moveRow
	NothingTo bool
	Stays     []srvStay
	Deadlines []moveDeadline
	// Error is about the deadline select: a post with a value the select can't have sent.
	Error sentence
	Back  string
}

// moveRow is one service whose copies move: who and how many.
type moveRow struct {
	Who    sentence
	Copies string
}

// moveDeadline is one option of the deadline select.
type moveDeadline struct {
	Minutes  int
	Label    string
	Selected bool
}

// showMoveServer answers GET /ops/servers/{name}/move (through showServerSub).
func (s *server) showMoveServer(w http.ResponseWriter, r *http.Request, name string) {
	s.drawMoveServer(w, r, name, nil, moveDeadlineDefault)
}

// moveServer answers POST /ops/servers/{name}/move: work starts moving off the server and the browser
// goes to its page (303). A refusal draws the confirmation again with its sentence and its status.
func (s *server) moveServer(w http.ResponseWriter, r *http.Request, session string) {
	name := r.PathValue("name")
	if !s.readForm(w, r) {
		return
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("deadline")))
	if err != nil || !slices.Contains(moveDeadlines, minutes) {
		s.drawMoveServer(w, r, name, errBadDeadline, moveDeadlineDefault)
		return
	}
	_, err = s.svc.MoveWorkOff(r.Context(), name, time.Duration(minutes)*time.Minute)
	switch {
	case err == nil:
		http.Redirect(w, r, serverHref(name), http.StatusSeeOther)
	default:
		s.drawMoveServer(w, r, name, err, minutes)
	}
}

// errBadDeadline is the refusal of a deadline the select can't have sent.
var errBadDeadline = errors.New("choose a deadline from the list")

// acceptWork answers POST /ops/servers/{name}/accept: the server takes work again, and a move that goes
// is cancelled (the copies that moved stay where they are). The browser goes back to the server's page
// (303); a refusal draws the page with the sentence on top.
func (s *server) acceptWork(w http.ResponseWriter, r *http.Request, session string) {
	name := r.PathValue("name")
	_, err := s.svc.AcceptWork(r.Context(), name)
	switch {
	case err == nil:
		http.Redirect(w, r, serverHref(name), http.StatusSeeOther)
	case errors.Is(err, service.ErrServerNotFound):
		s.drawOpsServerWith(w, r, name, http.StatusNotFound, srvExtras{})
	case errors.Is(err, service.ErrServerNotReady):
		s.drawOpsServerWith(w, r, name, http.StatusConflict, srvExtras{Notice: plainS(serverRefusalWords(err))})
	case errors.Is(err, deploy.ErrNomadUnavailable):
		s.drawOpsServerWith(w, r, name, http.StatusBadGateway, srvExtras{Notice: plainS(refusalWords(err))})
	default:
		s.serverError(w, r, "accept work again", err)
	}
}

// drawMoveServer draws the confirmation. refused is why a post was turned down; with none, the page asks
// the service what would move, which refuses for the same reasons.
func (s *server) drawMoveServer(w http.ResponseWriter, r *http.Request, name string, refused error, selected int) {
	ctx := r.Context()
	v, err := s.svc.Server(ctx, name)
	if errors.Is(err, service.ErrServerNotFound) || err == nil && !v.Managed {
		s.message(w, r, http.StatusNotFound, "Server not found", "No such server.", "There is no server called "+name+" that Caravana manages.")
		return
	}
	if err != nil {
		s.serverError(w, r, "draw the move", err)
		return
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		s.serverError(w, r, "read projects", err)
		return
	}
	page := moveServerPage{Name: v.Name, Back: serverHref(v.Name), Headline: sentence{plain("Move work off "), mono(v.Name), plain("?")}}
	var preview service.MovePreview
	status := http.StatusOK
	if refused == nil || refused == errBadDeadline {
		var perr error
		if preview, perr = s.svc.PreviewMoveOff(ctx, name); perr != nil {
			refused, status = perr, moveRefusalStatus(perr)
		} else if refused != nil {
			page.Error, status = plainS(refusalWords(refused)), http.StatusUnprocessableEntity
			refused = nil
		}
	} else {
		status = moveRefusalStatus(refused)
	}
	if refused != nil {
		var fieldErr *app.FieldError
		if errors.As(refused, &fieldErr) {
			page.Refusal = plainS(refusalWords(errors.New(fieldErr.Message)))
		} else {
			page.Refusal = plainS(serverRefusalWords(refused))
		}
		if status == http.StatusInternalServerError {
			s.serverError(w, r, "draw the move", refused)
			return
		}
	} else {
		for _, m := range preview.Moves {
			page.Moves = append(page.Moves, moveRow{Who: stayWho(pl, m.App, m.Service), Copies: copyCount(m.Copies)})
		}
		page.NothingTo = len(preview.Moves) == 0
		page.Stays = movedStays(preview.Stays, pl)
		for _, m := range moveDeadlines {
			page.Deadlines = append(page.Deadlines, moveDeadline{Minutes: m, Label: plural(m, "minute"), Selected: m == selected})
		}
	}
	head, err := s.shellPage(r, "Move work off "+v.Name, navOpsServer, crumb{Label: "Servers", Href: "/ops/servers"},
		crumb{Label: v.Name, Href: page.Back}, crumb{Label: "Move work off"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.pageData = head
	s.render(w, status, "moveserver", page)
}

// moveRefusalStatus is the status of a refusal of moving work off: the owner's state (409), a deadline
// that is out of range (422), Nomad not answering (502).
func moveRefusalStatus(err error) int {
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		return http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrServerNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrServerNotReady), errors.Is(err, service.ErrServerMoving), errors.Is(err, service.ErrNoOtherServer):
		return http.StatusConflict
	case errors.Is(err, deploy.ErrNomadUnavailable):
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}
