package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file is promoting and copying an app between the environments of its
// project (feature 019): the preview page, the confirming post, and the copy
// post. The service decides everything; these pages say it in words.

// promoteService is one service of the preview: what it runs now and what it
// will run, and which parts of its shape the promotion replaces.
type promoteService struct {
	Name                  string
	Added                 bool // the target doesn't have it yet
	ImageFrom, ImageTo    string
	DigestFrom, DigestTo  string // short forms; the full reference is the title
	FullFrom, FullTo      string
	Changed               string // "command, ports"; "" when the shape is the same
	ImageChanged, Differs bool
}

// copyOffer is the button that makes the missing target, shown with the refusal
// that says it isn't there.
type copyOffer struct {
	Action string
	To     string
}

// promoteView is the promote page: the preview, or the reason there is none.
type promoteView struct {
	AppID, AppName string
	From, To       string
	Version        string // the source deployment's version, "v7"; "" when unknown
	Headline       sentence

	Services []promoteService
	Warnings []app.Warning
	Keeps    sentence

	Action, Revision string
	// Refusal replaces the button: why this can't be promoted. Copy is set when the
	// reason is that the target has no such app. Notice is a failed attempt that
	// leaves the preview and its button in place. Stale says the target changed
	// since the preview was drawn.
	Refusal sentence
	Copy    *copyOffer
	Notice  sentence
	Stale   sentence
	Back    string // the app's Deployments tab
}

// promotePage is the whole promote page.
type promotePage struct {
	pageData
	promoteView
}

// promoNames are the names a refusal puts in its sentence.
type promoNames struct{ App, From, To string }

// promotionRefusal says in the console's words why a promotion or a copy was
// refused, with the HTTP status the API would give. ok is false for errors the
// pages have no words for.
func promotionRefusal(err error, n promoNames) (status int, text sentence, ok bool) {
	var re *service.RefusalError
	var pe *service.PlaceError
	var fe *app.FieldError
	var ie *deploy.ImageError
	switch {
	case errors.Is(err, service.ErrNoLiveDeployment):
		return http.StatusConflict, sentence{mono(n.App), plain(" has no healthy deployment in "), mono(n.From),
			plain(" yet, so there is nothing to promote. Deploy it there first.")}, true
	case errors.Is(err, service.ErrDeploymentNotHealthy):
		return http.StatusConflict, sentence{plain("The newest deployment of "), mono(n.App), plain(" in "), mono(n.From),
			plain(" isn't healthy, so it can't be promoted. Fix it or deploy it again there first.")}, true
	case errors.Is(err, service.ErrDefinitionNotRecorded):
		return http.StatusConflict, sentence{plain("The newest deployment of "), mono(n.App), plain(" in "), mono(n.From),
			plain(" was made before caravana recorded what each deployment deployed. Deploy it there again, then promote.")}, true
	case errors.Is(err, service.ErrAppStopped):
		return http.StatusConflict, sentence{mono(n.App), plain(" in "), mono(n.To), plain(" is stopped: start it there first.")}, true
	case errors.Is(err, service.ErrAppNotInEnvironment):
		return http.StatusNotFound, sentence{mono(n.To), plain(" has no app called "), mono(n.App), plain(". Copy it there first.")}, true
	case errors.Is(err, service.ErrAppExists):
		return http.StatusConflict, sentence{mono(n.To), plain(" has an app called "), mono(n.App),
			plain(" already. Promote it from "), mono(n.From), plain(" instead of copying.")}, true
	case errors.Is(err, service.ErrPromotionRemovesService):
		name := ""
		if errors.As(err, &re) {
			name = strings.TrimPrefix(re.Field, "services.")
		}
		return http.StatusConflict, sentence{mono(n.To), plain(" has a service "), mono(name), plain(" that "), mono(n.From),
			plain(" doesn't. Promoting would remove it: remove it from " + n.To + " yourself, or add it to " + n.From + ".")}, true
	case errors.As(err, &pe):
		if errors.Is(err, service.ErrProjectNotFound) {
			return http.StatusNotFound, sentence{plain("This app's project doesn't exist any more.")}, true
		}
		return http.StatusNotFound, sentence{plain("There's no environment "), mono(n.To), plain(" in this project.")}, true
	case errors.As(err, &fe) && fe.Field == "to":
		if strings.Contains(fe.Message, "the app is in already") {
			return http.StatusUnprocessableEntity, sentence{mono(n.App), plain(" is in "), mono(n.From), plain(" already. Choose another environment.")}, true
		}
		return http.StatusUnprocessableEntity, sentence{plain("Choose an environment of this project.")}, true
	case errors.As(err, &fe):
		return http.StatusUnprocessableEntity, sentence{plain("caravana can't deploy "), mono(n.App), plain(" in "), mono(n.To),
			plain(": "), mono(fe.Field), plain(" " + fe.Message)}, true
	case errors.As(err, &ie):
		status = http.StatusUnprocessableEntity
		if !errors.Is(err, deploy.ErrImageNotFound) && !errors.Is(err, deploy.ErrRegistryAuth) {
			status = http.StatusBadGateway // the registry couldn't answer: not the app's fault
		}
		return status, sentence{plain(ie.Error())}, true
	case errors.Is(err, secrets.ErrUnavailable) && !errors.Is(err, deploy.ErrNomadUnavailable):
		return http.StatusBadGateway, sentence{plain("The secret store can't be reached right now, so nothing was changed.")}, true
	}
	return refuseDeploy(&deployForm{}, err)
}

// ---- Promote -------------------------------------------------------------------------

// showPromote answers GET /apps/{id}/promote?to=: the preview of promoting the
// app into the environment named by to.
func (s *server) showPromote(w http.ResponseWriter, r *http.Request, session string) {
	s.drawPromote(w, r, http.StatusOK, r.PathValue("id"), strings.TrimSpace(r.URL.Query().Get("to")), nil, nil)
}

// drawPromote draws the promote page for the app and target: the preview with
// its button, or the refusal. status is the answer when the page has its
// preview; a refusal takes the status its reason has. stale and notice are
// sentences the caller adds above (a changed target, a failed attempt).
func (s *server) drawPromote(w http.ResponseWriter, r *http.Request, status int, id, to string, stale, notice sentence) {
	ctx := r.Context()
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	names := promoNames{App: cmpName(sum), From: sum.Place.Environment, To: to}
	v := promoteView{AppID: id, AppName: names.App, From: names.From, To: to,
		Action: "/apps/" + url.PathEscape(id) + "/promote", Back: s.deploymentsHref(id), Stale: stale, Notice: notice}
	v.Headline = sentence{plain("Promote "), mono(names.App), plain(" from "), mono(names.From), plain(" to "), mono(to)}

	if to == "" {
		v.Headline = sentence{plain("Promote "), mono(names.App)}
		v.Refusal, status = sentence{plain("Choose an environment to promote to: use a Promote button on the app's page.")}, http.StatusUnprocessableEntity
	} else if prev, err := s.svc.PreviewPromotion(ctx, id, to); err == nil {
		v.fillPreview(prev)
		if dep, err := s.svc.GetDeployment(ctx, id, prev.FromDeployment); err == nil {
			v.Version = versionLabel(dep.JobVersion)
		}
		if v.Version != "" {
			v.Headline = sentence{plain("Promote "), mono(names.App), plain(" from "), mono(names.From), plain(" (deployment "),
				mono(v.Version), plain(") to "), mono(to)}
		}
	} else {
		code, text, ok := promotionRefusal(err, names)
		if !ok {
			s.serverError(w, "preview promotion", err)
			return
		}
		v.Refusal, status = text, code
		if errors.Is(err, service.ErrAppNotInEnvironment) {
			v.Copy = &copyOffer{Action: "/apps/" + url.PathEscape(id) + "/copy", To: to}
		}
	}

	head, err := s.shellPage(r, "Promote "+names.App, navProjects, s.appCrumbs(r.Context(), id,
		crumb{Label: names.App, Href: "/apps/" + url.PathEscape(id)}, crumb{Label: "Promote"})...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, status, "promote", promotePage{pageData: head, promoteView: v})
}

// fillPreview turns the service's preview into what the page lists.
func (v *promoteView) fillPreview(p service.PromotionPreview) {
	v.Revision, v.Warnings = p.TargetRevision, p.Warnings
	for _, c := range p.Services {
		ps := promoteService{Name: c.Name, ImageFrom: c.ImageFrom, ImageTo: c.ImageTo,
			DigestFrom: shortDigest(c.DigestFrom), DigestTo: shortDigest(c.DigestTo), FullFrom: c.DigestFrom, FullTo: c.DigestTo,
			Changed: strings.Join(c.Changed, ", ")}
		for _, added := range p.Added {
			ps.Added = ps.Added || added == c.Name
		}
		ps.ImageChanged = c.ImageFrom != c.ImageTo
		ps.Differs = c.DigestFrom != c.DigestTo
		v.Services = append(v.Services, ps)
	}
	v.Keeps = sentence{mono(v.To), plain(" keeps its env, secrets, domains, memory, copies and backups.")}
}

// promoteApp answers POST /apps/{id}/promote: deploy the preview. The form
// carries the target's revision the preview was drawn from; a target that
// changed meanwhile draws the page again with a fresh preview (412), so nothing
// is promoted over an edit nobody looked at. Success goes to the target's
// Deployments tab.
func (s *server) promoteApp(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	to, revision := strings.TrimSpace(r.PostForm.Get("to")), strings.TrimSpace(r.PostForm.Get("revision"))
	rec, err := s.svc.Promote(r.Context(), id, to, service.Condition{IfRevision: revision}, app.TriggerConsole)
	var deployErr *service.DeployError
	switch {
	case err == nil:
		http.Redirect(w, r, "/apps/"+url.PathEscape(rec.AppID)+"/deployments#deployment-"+rec.Deployment.ID, http.StatusSeeOther)
	case errors.As(err, &deployErr):
		// The target was saved and a deployment recorded, but Nomad didn't take it:
		// its Deployments tab shows the failed attempt.
		http.Redirect(w, r, "/apps/"+url.PathEscape(deployErr.AppID)+"/deployments#deployment-"+deployErr.DeploymentID, http.StatusSeeOther)
	case errors.Is(err, service.ErrAppChanged):
		s.drawPromote(w, r, http.StatusPreconditionFailed, id, to,
			sentence{mono(to), plain(" changed since this preview; review it again.")}, nil)
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	default:
		sum, getErr := s.svc.AppSummary(r.Context(), id)
		if getErr != nil {
			s.serverError(w, "promote from the console", err)
			return
		}
		code, text, ok := promotionRefusal(err, promoNames{App: cmpName(sum), From: sum.Place.Environment, To: to})
		if !ok {
			s.serverError(w, "promote from the console", err)
			return
		}
		s.drawPromote(w, r, code, id, to, nil, text)
	}
}

// ---- Copy ----------------------------------------------------------------------------

// copyApp answers POST /apps/{id}/copy: the app in another environment, not
// deployed. Success goes to the copy's Configure tab, which lists the secrets it
// needs values for; a refusal is a plain page with the reason.
func (s *server) copyApp(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	to := strings.TrimSpace(r.PostForm.Get("to"))
	rec, missing, err := s.svc.CopyApp(r.Context(), id, to)
	if err == nil {
		q := url.Values{"copied": {to}}
		for _, m := range missing {
			q.Add("missing", m.Service+"/"+m.Name)
		}
		http.Redirect(w, r, "/apps/"+url.PathEscape(rec.App.ID)+"/configure?"+q.Encode(), http.StatusSeeOther)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	names := promoNames{App: id, To: to}
	if sum, getErr := s.svc.GetApp(r.Context(), id); getErr == nil {
		names.App, names.From = cmpName(service.AppSummary{AppRecord: sum}), sum.Place.Environment
	}
	status, text, ok := promotionRefusal(err, names)
	if !ok {
		s.serverError(w, "copy app from the console", err)
		return
	}
	s.message(w, status, "Copy refused", "That copy wasn't made.", text.String())
}

// copiedNotice is the Configure tab's sentence after a copy: what the copy
// still needs. It reads ?copied= and ?missing=, but says only what the stored
// app confirms (the environment it is in, the secrets it names), so a made-up
// address can't put other words on the page.
func copiedNotice(r *http.Request, sum service.AppSummary) sentence {
	q := r.URL.Query()
	env := q.Get("copied")
	if env == "" || env != sum.Place.Environment {
		return nil
	}
	var names []string
	for _, m := range q["missing"] {
		svcName, secret, ok := strings.Cut(m, "/")
		if !ok || !hasSecret(sum.App, svcName, secret) {
			continue
		}
		names = append(names, m)
	}
	if !sum.App.Defined() {
		return sentence{plain("Copied to "), mono(env), plain(". It has no definition yet, like the app it was copied from.")}
	}
	out := sentence{plain("Copied to "), mono(env), plain(". Set its domains")}
	if len(names) == 0 {
		return append(out, plain(" before deploying."))
	}
	out = append(out, plain(" and these secrets before deploying: "))
	for i, n := range names {
		if i > 0 {
			out = append(out, plain(", "))
		}
		out = append(out, mono(n))
	}
	return append(out, plain("."))
}

// hasSecret says whether the app's service names the secret.
func hasSecret(a app.App, svcName, secret string) bool {
	for _, svc := range a.Services {
		if svc.Name != svcName {
			continue
		}
		for _, sec := range svc.Secrets {
			if sec.Name == secret {
				return true
			}
		}
	}
	return false
}
