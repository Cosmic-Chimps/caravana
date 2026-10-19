package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// This file is New app › "From a GitHub repository" (feature 020) and the deploy
// tokens the console makes: the app is created without deploying, a token that
// can deploy only it is made, and the page that answers shows the token once and
// the workflow to commit. The action it uses is Cosmic-Chimps/caravana-action.

// githubSecretName is the repository secret the workflow reads the token from.
const githubSecretName = "CARAVANA_TOKEN"

// githubRegistry is the registry the generated workflow pushes to: GitHub's own,
// where the workflow's built-in token can write.
const githubRegistry = "ghcr.io"

// githubResult is the page that answers a created GitHub app. It holds the token,
// which is shown only on this answer.
type githubResult struct {
	AppID     string
	Image     string
	TokenName string
	Token     string
	Secret    string // the repository secret to add: CARAVANA_TOKEN
	Workflow  string // .github/workflows/deploy.yml
}

// consoleURL is the address the workflow should call: the configured public
// address when there is one, else the one this request came to.
func (s *server) consoleURL(r *http.Request) string {
	if s.svc != nil {
		if u := s.svc.PublicURL(); u != "" {
			return u
		}
	}
	return s.origin(r)
}

// makeDeployToken makes a token for the app at pl that can deploy only that app
// name in that project. It is named after the app's ID with "-ci"; a name an
// active token already has (an app made again after it was deleted, say) gets a
// number after it, so making the token never fails over a name.
func (s *server) makeDeployToken(r *http.Request, id string, pl app.Place) (service.CreatedAPIToken, error) {
	scope := &service.TokenScope{Project: pl.Project, App: pl.Name}
	base := id + "-ci"
	if len(base) > 60 { // the name's limit is 64, and a number may follow
		base = base[:60]
	}
	name := base
	for n := 2; ; n++ {
		created, err := s.auth.CreateScopedAPIToken(r.Context(), name, nil, scope)
		if !errors.Is(err, service.ErrTokenNameTaken) || n > 50 {
			return created, err
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
}

// createFromGitHub answers POST /apps/new/github: it creates the app without
// deploying it, with the definition the form describes, makes it a deploy token
// and answers 200 with the token and the workflow. The answer is the page, not
// a redirect, because the token is shown once and a redirect would lose it (as
// the token page of Settings does). A refusal draws the form again.
func (s *server) createFromGitHub(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	val := func(name string) string { return strings.TrimSpace(r.PostForm.Get(name)) }
	form := newForm()
	form.GitHub = true
	form.Name.Value, form.Image.Value = val("name"), val("image")
	form.Port.Value, form.Domain.Value, form.Health.Value = val("port"), val("domain"), val("healthPath")
	form.Place.Value = val("place")

	a, ok := buildNewApp(&form, true)
	switch image := form.Image.Value; {
	case image == "":
		form.Image.Error, ok = sentence{plain("Enter the image your workflow pushes, like "), mono("ghcr.io/you/app"), plain(".")}, false
	case !strings.HasPrefix(strings.ToLower(image), githubRegistry+"/"):
		form.Image.Error, ok = sentence{plain("Use an image on "), mono(githubRegistry), plain(", like "), mono("ghcr.io/you/app"),
			plain(": the workflow pushes there with the token GitHub gives every run.")}, false
	}
	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		s.serverError(w, "list projects", err)
		return
	}
	place, refusal, placed := pl.placeFor(form.Place.Value, form.Name.Value)
	if !placed {
		form.Place.Error, ok = refusal, false
	}
	if !ok {
		form.markFirstError()
		s.renderNewApp(w, r, http.StatusUnprocessableEntity, form)
		return
	}
	a.ID = place.ID()

	if _, err := s.svc.CreateAppOnly(r.Context(), a); err != nil {
		status, known := refuseNewApp(&form, a.ID, err)
		if !known {
			s.serverError(w, "create app from a GitHub repository", err)
			return
		}
		form.markFirstError()
		s.renderNewApp(w, r, status, form)
		return
	}

	created, err := s.makeDeployToken(r, a.ID, place)
	if err != nil {
		s.log.Error("make the deploy token of a new GitHub app", "app", a.ID, "error", err)
		s.message(w, http.StatusInternalServerError, "Deploy token", "The app was created, but its deploy token wasn't.",
			"Open the app's Configure tab and create a deploy token there.")
		return
	}
	s.renderGitHubResult(w, r, githubResult{
		AppID: a.ID, Image: a.Services[0].Image, TokenName: created.Info.Name, Token: created.Token,
		Secret:   githubSecretName,
		Workflow: githubWorkflow(a.Services[0].Image, s.consoleURL(r), a.ID),
	})
}

// renderGitHubResult draws the answer of a created GitHub app: the New app page
// with the result in place of the form.
func (s *server) renderGitHubResult(w http.ResponseWriter, r *http.Request, res githubResult) {
	head, err := s.shellPage(r, "New app", "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: "New app"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, http.StatusOK, "newapp", newAppPage{pageData: head, Result: &res})
}

// githubWorkflow is the workflow to commit as .github/workflows/deploy.yml: it
// builds the image, pushes it to GHCR, and has caravana-action deploy that exact
// digest. image is as the app stores it (a tag, or none, is dropped from the
// push: the action deploys the digest). The token is not in it: it lives in the
// repository's secrets.
func githubWorkflow(image, consoleURL, appID string) string {
	repo := imageRepository(image)
	return `name: deploy
on: { push: { branches: [main] } }
permissions: { contents: read, packages: write }
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: docker/login-action@v4
        with: { registry: ghcr.io, username: ${{ github.actor }}, password: ${{ secrets.GITHUB_TOKEN }} }
      - id: build
        uses: docker/build-push-action@v7
        with: { push: true, tags: ` + repo + `:latest }
      - uses: Cosmic-Chimps/caravana-action@v1
        with:
          url: ` + consoleURL + `
          token: ${{ secrets.` + githubSecretName + ` }}
          app: ` + appID + `
          image: ` + repo + `@${{ steps.build.outputs.digest }}
`
}

// imageRepository is an image reference without its tag or digest:
// "ghcr.io/you/app:1.4" is "ghcr.io/you/app". A colon before the last slash is a
// registry port, not a tag.
func imageRepository(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}
