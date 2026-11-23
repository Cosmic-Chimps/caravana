package ui

import (
	"net/http"
	"net/url"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Next steps (feature 050, plan D10): the panel an app that builds from Git has on its page while
// nothing has been deployed yet. It walks the owner through what the first build needs: the access to
// the repository (the Git access section, the same as on Configure), the repository's push
// webhook, and the first build. It sits outside the page's live regions, so a refresh never replaces
// a token being typed.

// nextSteps is the panel.
type nextSteps struct {
	ID string
	// Summary says the source: "Builds the newest commit of main from ...".
	Summary sentence
	Branch  string
	// Git is the Git access section, drawn when the repository isn't read anonymously or a key or
	// token was made (Git.Offer).
	Git  gitAccessView
	Hook nextHook
	// DeployAction is where "Build and deploy" posts.
	DeployAction string
	Stopped      bool
}

// nextHook is the webhook step: whether a token exists, where to make one.
type nextHook struct {
	Enabled      bool
	CreateAction string
	ConfigureURL string
}

// nextStepsOf builds the panel for an app that has no deployment, or nil when it has no built
// service. git is what a refused Git access post typed.
func (s *server) nextStepsOf(r *http.Request, sum service.AppSummary, git *gitPost) (*nextSteps, error) {
	i, built := sum.App.BuiltService()
	if !built || sum.LastDeployment != nil {
		return nil, nil
	}
	svc := sum.App.Services[i]
	hook, err := s.svc.WebhookStatus(r.Context(), sum.App.ID)
	if err != nil {
		return nil, err
	}
	g, err := s.gitAccessViewOf(r.Context(), sum.App.ID, svc, gitFromNext, git)
	if err != nil {
		return nil, err
	}
	g.Offer = g.Access != app.BuildAccessPublic || g.Kind != service.GitAccessNone
	id := url.PathEscape(sum.App.ID)
	return &nextSteps{
		ID: sum.App.ID, Summary: buildSummary(*svc.Build), Branch: svc.Build.Defaults().Branch, Git: g,
		Hook:         nextHook{Enabled: hook.Enabled, CreateAction: "/apps/" + id + "/webhook", ConfigureURL: "/apps/" + id + "/configure#webhook"},
		DeployAction: "/apps/" + id + "/deploy", Stopped: sum.StoppedAt != nil,
	}, nil
}
