package ui

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Remove a server (feature 031): the confirmation page of a server that is down, and its post. The
// page is the Stop page's shape (the update confirmation's too): what goes, what stays, the one
// primary button and Cancel. A server that runs is never offered: the page says why instead, with only
// the way back, and the post refuses it too.

// removeServerPage is the confirmation page. Refusal, when set, takes the panel's and the button's place.
type removeServerPage struct {
	pageData
	Name     string
	Headline sentence
	Paras    []sentence
	Refusal  sentence
	Back     string
}

// showServerSub answers GET /ops/servers/{kind}/{rest}. Two pages live under that shape and Go's router
// can't tell /ops/servers/joins/{id} from /ops/servers/{name}/remove (both match
// /ops/servers/joins/remove, and neither is the more specific), so one pattern takes both and this
// dispatches: ".../{name}/remove" is the confirmation, ".../joins/{id}" the join's page.
func (s *server) showServerSub(w http.ResponseWriter, r *http.Request, session string) {
	kind, rest := r.PathValue("kind"), r.PathValue("rest")
	switch {
	case rest == "remove":
		s.drawRemoveServer(w, r, kind, nil)
	case kind == "joins":
		r.SetPathValue("id", rest)
		s.showServerJoin(w, r, session)
	default:
		s.notFound(w, r)
	}
}

// removeServer answers POST /ops/servers/{name}/remove: the server is removed and the browser goes to the
// list (303). A server that is gone already is the same answer. A refusal draws the confirmation again with
// the service's sentence and its status, never a redirect.
func (s *server) removeServer(w http.ResponseWriter, r *http.Request, session string) {
	name := r.PathValue("name")
	err := s.svc.RemoveServer(r.Context(), name)
	switch {
	case err == nil, errors.Is(err, service.ErrServerNotFound):
		http.Redirect(w, r, "/ops/servers", http.StatusSeeOther)
	case errors.Is(err, service.ErrServerRunning), errors.Is(err, service.ErrFirstServer), errors.Is(err, deploy.ErrNomadUnavailable):
		s.drawRemoveServer(w, r, name, err)
	default:
		s.serverError(w, r, "remove a server", err)
	}
}

// drawRemoveServer draws the page. refused is why a post was turned down; with none, the page asks the
// same question of the server's state.
func (s *server) drawRemoveServer(w http.ResponseWriter, r *http.Request, name string, refused error) {
	v, err := s.svc.Server(r.Context(), name)
	if errors.Is(err, service.ErrServerNotFound) {
		s.message(w, r, http.StatusNotFound, "Server not found", "No such server.", "There is no server called "+name+". It may have been removed already.")
		return
	}
	if err != nil {
		s.serverError(w, r, "draw the removal", err)
		return
	}
	if refused == nil {
		refused = removeRefusal(v)
	}
	page := removeServerPage{Name: v.Name, Back: "/ops/servers/" + url.PathEscape(v.Name),
		Headline: sentence{plain("Remove "), mono(v.Name), plain("?")}}
	status := http.StatusOK
	if refused != nil {
		status = http.StatusConflict
		if errors.Is(refused, deploy.ErrNomadUnavailable) {
			status = http.StatusBadGateway
		}
		page.Refusal = plainS(removeRefusalWords(refused, v.Name))
	} else {
		page.Paras = []sentence{
			{plain("Its place in the cluster and its token are deleted. The other servers stop talking to it at their next sync.")},
			{plain("Nothing on the server is touched.")},
		}
	}
	head, err := s.shellPage(r, "Remove "+v.Name, navOpsServer, crumb{Label: "Servers", Href: "/ops/servers"},
		crumb{Label: v.Name, Href: page.Back}, crumb{Label: "Remove"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.pageData = head
	s.render(w, status, "removeserver", page)
}

// removeRefusal is why the service would refuse to remove v: the server Caravana runs on, one that
// isn't managed, and one that runs. A server that is down, or never reached Nomad, is removable.
func removeRefusal(v service.ServerView) error {
	switch {
	case v.First:
		return service.ErrFirstServer
	case !v.Managed:
		return errNotManaged
	case v.State == "down", v.State == "waiting":
		return nil
	case v.State == "joining" && v.NodeID == "":
		return nil
	}
	return &service.ServerRunningError{Name: v.Name}
}

// errNotManaged is the refusal for a Nomad node Caravana doesn't manage: it has no place in the cluster to remove.
var errNotManaged = errors.New("this server isn't managed by Caravana, so it has no place in the cluster to remove")

// serverRefusalWords is a refusal as a sentence. The service words a running server's refusal whole, with the
// server's name first ("fsn-1 is running. …"), which a capital would spoil; every other refusal is a clause
// that refusalWords gives its capital and full stop.
func serverRefusalWords(err error) string {
	var running *service.ServerRunningError
	if errors.As(err, &running) {
		return running.Error()
	}
	return refusalWords(err)
}
