package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Git access in the console (feature 050, plan D3 and D10): how a built service's build reads its
// repository. The "Git access" section is drawn twice, by the same template: under Next steps on
// the app's page while nothing is deployed yet, and on Configure. It shows the public half of a
// deploy key (with its fingerprint) and which token is stored; the private key and the token are
// write-only: the service never gives them out, no form field has one as its value, and a refused
// post draws the token field empty.

// The two pages the Git access forms come from: each posts a hidden "from" field, so a success or a
// refusal goes back where the owner was.
const (
	gitFromNext      = "next"
	gitFromConfigure = "configure"
)

// gitAccessView is one built service's Git access section.
type gitAccessView struct {
	ID, Service string
	// From says which page draws it: gitFromNext or gitFromConfigure.
	From string
	// Access is what the service's build says it reads the repository with (public, deploy-key or
	// token); Kind what is stored (none, deploy-key or token). They differ while the owner has
	// just chosen an access and not made it yet.
	Access, Kind string
	// PublicKey and Fingerprint are the stored deploy key's public half; Username the stored
	// token's user name.
	PublicKey, Fingerprint, Username string
	Made                             when
	// AnchorID names the element of the key's text, which the Copy button reads.
	AnchorID string
	// The three posts.
	KeyAction, TokenAction, RemoveAction string
	// User and Token are the token form's inputs. Token never holds a value.
	User, Token formField
	// Notice is about the section as a whole.
	Notice sentence
	// Offer says the section offers to make a key or a token (Remove too, once one is stored): always
	// on Configure, and under Next steps once the repository isn't read anonymously.
	Offer bool
}

// Needs says the build reads with a key or token that isn't made yet: the next build is refused.
func (v gitAccessView) Needs() bool {
	return (v.Access == app.BuildAccessDeployKey || v.Access == app.BuildAccessToken) && v.Kind != v.Access
}

// gitPost is what a refused Git access post typed, with its messages: the section draws from it.
// The token itself is never in it.
type gitPost struct {
	Service  string
	Username string
	UserErr  sentence
	TokenErr sentence
	Notice   sentence
}

// gitAccessViewOf builds the section of one service. It reads the stored access (never its
// secrets). A refused post's draft, when it is for this service, puts its messages in.
func (s *server) gitAccessViewOf(ctx context.Context, appID string, svc app.Service, from string, draft *gitPost) (gitAccessView, error) {
	base := "/apps/" + url.PathEscape(appID) + "/services/" + url.PathEscape(svc.Name) + "/git-access/"
	v := gitAccessView{ID: appID, Service: svc.Name, From: from, Access: svc.Build.Defaults().Access, Kind: service.GitAccessNone,
		AnchorID: "git-key-" + svc.Name, KeyAction: base + "deploy-key", TokenAction: base + "token", RemoveAction: base + "remove",
		User:  formField{ID: "git-user-" + svc.Name},
		Token: formField{ID: "git-token-" + svc.Name}}
	g, err := s.svc.GitAccess(ctx, appID, svc.Name)
	if err != nil {
		return v, err
	}
	v.Kind, v.PublicKey, v.Fingerprint, v.Username = g.Kind, g.PublicKey, g.Fingerprint, g.Username
	if !g.CreatedAt.IsZero() {
		v.Made = whenOf(s.now(), &g.CreatedAt, "")
	}
	if draft != nil && draft.Service == svc.Name {
		v.User.Value = draft.Username
		v.User.Error, v.Token.Error, v.Notice = draft.UserErr, draft.TokenErr, draft.Notice
		v.Token.Alert = len(v.Token.Error) > 0
		v.User.Alert = len(v.User.Error) > 0 && !v.Token.Alert
	}
	return v, nil
}

// gitBack is where a Git access post goes when it worked.
func gitBack(id, from string) string {
	if from == gitFromConfigure {
		return "/apps/" + url.PathEscape(id) + "/configure#git-access"
	}
	return "/apps/" + url.PathEscape(id) + "#next-steps"
}

// gitRedraw draws the page a refused Git access post came from, with the draft.
func (s *server) gitRedraw(w http.ResponseWriter, r *http.Request, status int, id, from string, draft gitPost) {
	if from == gitFromConfigure {
		s.configurePage(w, r, status, id, configureExtras{Git: &draft})
		return
	}
	s.runningPage(w, r, status, id, runningExtras{Git: &draft})
}

// gitAccessRefusal puts the error of a Git access call into the draft and says its status. ok is
// false for an error the pages have no words for.
func gitAccessRefusal(err error, post *gitPost) (status int, ok bool) {
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		words := plainS(refusalWords(errors.New(fieldErr.Message)))
		switch fieldErr.Field {
		case "token":
			post.TokenErr = words
		case "username":
			post.UserErr = words
		default:
			post.Notice = words
		}
		return http.StatusUnprocessableEntity, true
	case errors.Is(err, secrets.ErrUnavailable):
		post.Notice = plainS("caravana couldn't reach Nomad's variables, where the key or token is kept, so nothing was changed.")
		return http.StatusBadGateway, true
	}
	return 0, false
}

// gitAccessCall reads the form and the service and runs one Git access call. It answers for every
// outcome but success, which it reports by true: the caller then redirects.
func (s *server) gitAccessCall(w http.ResponseWriter, r *http.Request, call func(ctx context.Context, id, svc string, post *gitPost) error) bool {
	id, svc := r.PathValue("id"), r.PathValue("service")
	if !s.readForm(w, r) {
		return false
	}
	from := strings.TrimSpace(r.PostForm.Get("from"))
	post := gitPost{Service: svc, Username: strings.TrimSpace(r.PostForm.Get("username"))}
	err := call(r.Context(), id, svc, &post)
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		if _, getErr := s.svc.GetApp(r.Context(), id); errors.Is(getErr, store.ErrNotFound) {
			s.appNotFound(w, r, id)
			return false
		}
		s.message(w, r, http.StatusNotFound, "No service", "This app has no service called "+svc+".", "Open the app's Configure tab.")
		return false
	}
	status, ok := gitAccessRefusal(err, &post)
	if !ok {
		s.serverError(w, r, "change the Git access", err)
		return false
	}
	s.gitRedraw(w, r, status, id, from, post)
	return false
}

// makeDeployKey answers POST /apps/{id}/services/{service}/git-access/deploy-key: a new ed25519
// key replaces whatever access the service had. The answer is a redirect: the public half is not a
// secret, and the page it goes to shows it.
func (s *server) makeDeployKey(w http.ResponseWriter, r *http.Request, session string) {
	if s.gitAccessCall(w, r, func(ctx context.Context, id, svc string, _ *gitPost) error {
		_, err := s.svc.NewDeployKey(ctx, id, svc)
		return err
	}) {
		http.Redirect(w, r, gitBack(r.PathValue("id"), r.PostForm.Get("from")), http.StatusSeeOther)
	}
}

// setGitToken answers POST /apps/{id}/services/{service}/git-access/token: the token (and the user
// name, when the form has one) replaces the service's access. It is read from the post, handed to the
// service and dropped: no page, redirect or log has it.
func (s *server) setGitToken(w http.ResponseWriter, r *http.Request, session string) {
	if s.gitAccessCall(w, r, func(ctx context.Context, id, svc string, post *gitPost) error {
		token := r.PostForm.Get("token")
		r.PostForm.Del("token")
		_, err := s.svc.SetGitToken(ctx, id, svc, post.Username, strings.TrimSpace(token))
		return err
	}) {
		http.Redirect(w, r, gitBack(r.PathValue("id"), r.PostForm.Get("from")), http.StatusSeeOther)
	}
}

// removeGitAccess answers POST /apps/{id}/services/{service}/git-access/remove. A service with no
// access has nothing to remove, which is the state the owner asked for.
func (s *server) removeGitAccess(w http.ResponseWriter, r *http.Request, session string) {
	if s.gitAccessCall(w, r, func(ctx context.Context, id, svc string, _ *gitPost) error {
		if err := s.svc.RemoveGitAccess(ctx, id, svc); err != nil && !errors.Is(err, service.ErrGitAccessNotFound) {
			return err
		}
		return nil
	}) {
		http.Redirect(w, r, gitBack(r.PathValue("id"), r.PostForm.Get("from")), http.StatusSeeOther)
	}
}
