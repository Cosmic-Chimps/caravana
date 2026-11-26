package ui

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Build panel of Configure and the Next steps of an app built from Git (feature 050, plan D10).
//
// The Build panel says where a built service's image comes from and, for an app edited in the
// console, has a form (repository, branch, folder, Dockerfile, how the repository is read, the SSH
// host's key) posted to /apps/{id}/services/{service}/build with the page's revision: a deploy, like
// the Placement and Scaling panels, and so a build. Under it is the service's Git access
// (gitaccess.go). A kept-Compose-file app can't have a built service (Compose's build: is refused),
// so the panel has no read-only variant.

// buildAccessChoices are the radios of "How it is read", in the order they show.
var buildAccessChoices = []struct{ Value, Label, Hint string }{
	{app.BuildAccessPublic, "Public", "The repository is read without credentials."},
	{app.BuildAccessDeployKey, "Deploy key", "An SSH address (git@host:path). Caravana makes the key; you add its public half to the repository."},
	{app.BuildAccessToken, "Token", "An https address. You give a token the repository accepts."},
}

// buildPanel is one built service's Build panel.
type buildPanel struct {
	Service  string
	AnchorID string
	Action   string
	Revision string
	Stopped  bool
	// Summary says the source as one sentence.
	Summary sentence
	// The form's inputs.
	Repository, Branch, Folder, Dockerfile, HostKey formField
	AccessID                                        string
	AccessName                                      string
	AccessError                                     sentence
	Access                                          []buildAccessOption
	// Notice is about the panel as a whole.
	Notice sentence
	// Git is the service's Git access, drawn under the form.
	Git gitAccessView
}

// buildAccessOption is one radio of "How it is read".
type buildAccessOption struct {
	ID, Value, Label, Hint string
	Checked                bool
}

// F returns the named input of the form, ready for the "field" template. The inputs post as
// svc.<service>.build-<name>.
func (p buildPanel) F(name string) fieldView {
	f := map[string]*formField{"repository": &p.Repository, "branch": &p.Branch, "folder": &p.Folder, "dockerfile": &p.Dockerfile, "hostkey": &p.HostKey}[name]
	spec := inputSpec{Name: "build-" + name}
	switch name {
	case "repository":
		spec.Label = "Repository"
		spec.Placeholder = "https://github.com/you/app.git"
		spec.Hint = "https://host/path, ssh://git@host/path or git@host:path."
	case "branch":
		spec.Label = "Branch"
		spec.Hint = "Every deploy builds this branch's newest commit."
	case "folder":
		spec.Label = "Folder"
		spec.Hint = "The build context, relative to the repository's root. Leave it empty or write a dot for the root."
	case "dockerfile":
		spec.Label = "Dockerfile"
		spec.Hint = "Relative to the folder."
	case "hostkey":
		spec.Label = "SSH host key"
		spec.Placeholder = "ssh-ed25519 AAAA…"
		spec.Hint = "Only for an SSH address on a host other than github.com or gitlab.com: the output of ssh-keyscan -t ed25519 host, without the host's name."
	}
	return fieldView{inputSpec: spec, Field: f, Service: p.Service}
}

// buildPost is what a refused Build post typed, with its messages; configurePage draws the panel
// of that service from it instead of from the stored app.
type buildPost struct {
	Service                                                 string
	Repository, Branch, Folder, Dockerfile, Access, HostKey string
	// Errs holds the message of each input that has one, by name (repository, branch, folder,
	// dockerfile, access, hostKey).
	Errs   map[string]sentence
	Notice sentence
}

// buildSummary says a service's build source as a sentence.
func buildSummary(b app.Build) sentence {
	b = b.Defaults()
	out := sentence{plain("Builds the newest commit of "), mono(b.Branch), plain(" from "), mono(b.Repository)}
	if b.Path != app.DefaultBuildPath || b.Dockerfile != app.DefaultBuildDockerfile {
		out = append(out, plain(" (folder "), mono(b.Path), plain(", Dockerfile "), mono(b.Dockerfile), plain(")"))
	}
	switch b.Access {
	case app.BuildAccessDeployKey:
		out = append(out, plain(", reading it with a deploy key"))
	case app.BuildAccessToken:
		out = append(out, plain(", reading it with a token"))
	default:
		out = append(out, plain(", reading it without credentials"))
	}
	return append(out, plain("."))
}

// buildPanelOf builds the panel of one built service. draft, when its service is this one, holds
// what a refused post typed.
func (s *server) buildPanelOf(r *http.Request, sum service.AppSummary, svc app.Service, draft *buildPost, git *gitPost) (buildPanel, error) {
	b := svc.Build.Defaults()
	id := sum.App.ID
	p := buildPanel{
		Service: svc.Name, AnchorID: "build-" + svc.Name, Revision: sum.Revision, Stopped: sum.StoppedAt != nil,
		Action:     "/apps/" + url.PathEscape(id) + "/services/" + url.PathEscape(svc.Name) + "/build",
		Summary:    buildSummary(*svc.Build),
		Repository: formField{ID: inputID(svc.Name, "build-repository"), Value: b.Repository},
		Branch:     formField{ID: inputID(svc.Name, "build-branch"), Value: b.Branch},
		Folder:     formField{ID: inputID(svc.Name, "build-folder"), Value: b.Path},
		Dockerfile: formField{ID: inputID(svc.Name, "build-dockerfile"), Value: b.Dockerfile},
		HostKey:    formField{ID: inputID(svc.Name, "build-hostkey"), Value: b.HostKey},
		AccessID:   inputID(svc.Name, "build-access"), AccessName: inputName(svc.Name, "build-access"),
	}
	access := b.Access
	if draft != nil && draft.Service == svc.Name {
		p.Repository.Value, p.Branch.Value, p.Folder.Value = draft.Repository, draft.Branch, draft.Folder
		p.Dockerfile.Value, p.HostKey.Value, access = draft.Dockerfile, draft.HostKey, draft.Access
		p.Repository.Error, p.Branch.Error, p.Folder.Error = draft.Errs["repository"], draft.Errs["branch"], draft.Errs["folder"]
		p.Dockerfile.Error, p.HostKey.Error, p.AccessError = draft.Errs["dockerfile"], draft.Errs["hostKey"], draft.Errs["access"]
		p.Notice = draft.Notice
		// The first message is the one announced.
		for _, f := range []*formField{&p.Repository, &p.Branch, &p.Folder, &p.Dockerfile, &p.HostKey} {
			if len(f.Error) > 0 {
				f.Alert = true
				break
			}
		}
	}
	for _, c := range buildAccessChoices {
		p.Access = append(p.Access, buildAccessOption{ID: p.AccessID + "-" + c.Value, Value: c.Value, Label: c.Label, Hint: c.Hint, Checked: c.Value == access})
	}
	g, err := s.gitAccessViewOf(r.Context(), id, svc, gitFromConfigure, git)
	g.Offer = true
	p.Git = g
	return p, err
}

// addBuild gives the built service's panel its Build section. It reports the error of reading a
// service's Git access.
func (s *server) addBuild(r *http.Request, tab *configureTab, sum service.AppSummary, draft *buildPost, git *gitPost) error {
	for _, svc := range sum.App.Services {
		if svc.Build == nil {
			continue
		}
		for i := range tab.Edit.Services {
			if tab.Edit.Services[i].Name != svc.Name {
				continue
			}
			p, err := s.buildPanelOf(r, sum, svc, draft, git)
			if err != nil {
				return err
			}
			tab.Edit.Services[i].Build = &p
		}
	}
	return nil
}

// withBuild is the app with service i's build replaced; a is not changed.
func withBuild(a app.App, i int, b *app.Build) app.App {
	a.Services = slices.Clone(a.Services)
	a.Services[i].Build = b
	return a
}

// buildFieldRE reads a model path about a build: services[<i>].build.<field>.
var buildFieldRE = regexp.MustCompile(`^services\[\d+\]\.build\.(repository|branch|path|dockerfile|access|hostKey)$`)

// buildInputOf is the name a build field's input has in the draft's Errs.
func buildInputOf(field string) string {
	if field == "path" {
		return "folder"
	}
	return field
}

// buildProblem puts a model path's message at the input it is about. It reports false for a path
// that is no build input's.
func (p *buildPost) buildProblem(path string, words sentence) bool {
	m := buildFieldRE.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	if p.Errs == nil {
		p.Errs = map[string]sentence{}
	}
	p.Errs[buildInputOf(m[1])] = words
	return true
}

// saveBuild answers POST /apps/{id}/services/{service}/build: the form's source replaces the
// service's, as a deploy on the revision the page was drawn from, which builds the branch's newest
// commit. Success goes to the deployment's entry. A refusal draws the Configure tab again with what
// was typed and the message at its input.
func (s *server) saveBuild(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id, name := r.PathValue("id"), r.PathValue("service")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "save it")
	if !ok {
		return
	}
	rec, err := s.svc.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	i, found := serviceNamed(rec.App, name)
	if !found {
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Section: sectionSource, Notice: sentence{plain("This app has no service "), mono(name), plain(".")}})
		return
	}
	if rec.App.Services[i].Build == nil {
		s.configurePage(w, r, http.StatusConflict, id, configureExtras{Section: sectionSource, Notice: sentence{mono(name), plain(" isn't built from a Git repository, so it has no build source to save.")}})
		return
	}

	typed := func(field string) string { return strings.TrimSpace(r.PostForm.Get(inputName(name, "build-"+field))) }
	post := buildPost{Service: name, Repository: typed("repository"), Branch: typed("branch"), Folder: typed("folder"),
		Dockerfile: typed("dockerfile"), Access: typed("access"), HostKey: typed("hostkey")}
	edited := withBuild(rec.App, i, &app.Build{Repository: post.Repository, Branch: post.Branch, Path: post.Folder,
		Dockerfile: post.Dockerfile, Access: post.Access, HostKey: post.HostKey})
	deployment, err := s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}

	status := http.StatusUnprocessableEntity
	var fieldErr *app.FieldError
	var deployErr *service.DeployError
	_ = errors.As(err, &deployErr)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.Is(err, service.ErrAppChanged):
		status, post.Notice = http.StatusConflict, plainS(changedSincePage)
	case errors.As(err, &fieldErr) && post.buildProblem(fieldErr.Field, plainS(fieldMessage(fieldErr.Field, fieldErr.Message))):
	case buildRefused(err):
		var words sentence
		var field string
		status, words, field, _ = buildRefusal(err)
		if !post.buildProblem(field, words) {
			post.Notice = words
		}
	default:
		var notice sentence
		if status, notice, ok = refusalOf(edited, err); !ok {
			s.serverError(w, r, "save the build source", err)
			return
		}
		post.Notice = notice
	}
	s.configurePage(w, r, status, id, configureExtras{Section: sectionSource, Build: &post})
}
