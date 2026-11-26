package ui

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Building from a Git repository, in the console (feature 050, specs/050-build-from-git/plan.md D10):
// the words of a built service's refusals, the Build stage and the commit line of a history entry,
// and the build log page. The pages that make one (New app › Build from Git, Next steps, Configure's
// Build panel and Git access) are gitform.go, gitaccess.go and buildpanel.go; Settings › Builds is
// buildsettings.go. Nothing here draws the private half of a deploy key or a token: the service
// never gives them out.

// buildRefused says err is one of the refusals only a built service has: no stored access, an
// unknown SSH host key, or no registry entry to push to.
func buildRefused(err error) bool {
	return errors.Is(err, service.ErrGitAccessMissing) || errors.Is(err, service.ErrGitHostKeyUnknown) ||
		errors.Is(err, service.ErrBuildRegistryMissing)
}

// buildRefusal words a built service's refusal for a page: the HTTP status the API gives it, a
// sentence, and the field it is about (a path such as services[0].image; "" when the error holds
// none). ok is false for any other error.
func buildRefusal(err error) (status int, words sentence, field string, ok bool) {
	var refusal *service.RefusalError
	if errors.As(err, &refusal) {
		field = refusal.Field
	}
	switch {
	case errors.Is(err, service.ErrGitAccessMissing):
		return http.StatusConflict, sentence{plain("The build can't read the repository yet: make the deploy key or add the token it needs (Git access, under Configure › Source), then build again. Nothing was deployed.")}, field, true
	case errors.Is(err, service.ErrGitHostKeyUnknown):
		return http.StatusUnprocessableEntity, sentence{plain("caravana doesn't know this SSH host's key. Give it as the host key in Configure › Source, in the Build panel (the output of "), mono("ssh-keyscan -t ed25519 host"), plain(", without the host), or use github.com or gitlab.com. Nothing was deployed.")}, field, true
	case errors.Is(err, service.ErrBuildRegistryMissing):
		return http.StatusUnprocessableEntity, sentence{plain("No registry entry covers this image, so the build has nowhere to push it. Add one under Settings › Registries, with credentials that may push to it. Nothing was deployed.")}, field, true
	}
	return 0, nil, "", false
}

// setError puts s on the field and reports whether there was a field: fieldFor answers nil for a
// path the form has no input for.
func (f *formField) setError(s sentence) bool {
	if f == nil {
		return false
	}
	f.Error = s
	return true
}

// ---- the history entry ----------------------------------------------------------------

// buildLine is the line of a history entry that built: the commit, the branch and the subject it
// built, and the link to the build's log page.
type buildLine struct {
	Commit, Branch, Subject string
	LogHref                 string
}

// buildLineOf is nil for a deployment that didn't build.
func buildLineOf(rec store.DeploymentRecord) *buildLine {
	b := rec.Build
	if b == nil {
		return nil
	}
	commit := b.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	return &buildLine{Commit: commit, Branch: b.Branch, Subject: b.Subject, LogHref: buildLogHref(rec.AppID, rec.Deployment.ID)}
}

// buildStatusView is the status word of a build: what its log page's head and the history
// entry's pill (while the build is all that has happened) say.
func buildStatusView(b *store.BuildRecord) statusView {
	switch b.Status {
	case store.BuildQueued:
		return statusView{"rolling", "queued"}
	case store.BuildRunning:
		return statusView{"rolling", "building"}
	case store.BuildSucceeded:
		return statusView{"healthy", "built"}
	case store.BuildFailed:
		return statusView{"failed", "build failed"}
	}
	return statusView{"idle", "cancelled"}
}

// withBuildStage puts the Build stage before the three of a deployment that built: queued, building
// with the time it has taken, "built in 41 s", failed or cancelled. Until the build has ended well,
// Queued (Nomad accepting the job) hasn't begun, so it reads "—" whatever stagesOf said; once the
// build is done, Queued counts from its end.
func withBuildStage(three [3]stageView, rec store.DeploymentRecord, now time.Time) []stageView {
	b := rec.Build
	if b == nil {
		return three[:]
	}
	build := stageView{Name: "Build", Mark: "idle", Value: noValue}
	switch b.Status {
	case store.BuildQueued:
		build.Mark, build.Value, build.Active = "rolling", "queued", true
	case store.BuildRunning:
		build.Mark, build.Value, build.Active = "rolling", "building", true
		if !b.StartedAt.IsZero() {
			build.Value = "building " + durationText(now.Sub(b.StartedAt))
		}
	case store.BuildSucceeded:
		build.Mark, build.Value = "healthy", "built"
		if !b.StartedAt.IsZero() && !b.EndedAt.IsZero() {
			build.Value = "built in " + durationText(b.EndedAt.Sub(b.StartedAt))
		}
		if !b.EndedAt.IsZero() && rec.RegisteredAt != nil {
			three[0].Value = spanText(b.EndedAt, rec.RegisteredAt)
		}
	case store.BuildFailed:
		build.Mark, build.Value = "failed", "failed"
	case store.BuildCancelled:
		build.Value = "cancelled"
	}
	if b.Status != store.BuildSucceeded {
		three[0] = stageView{Name: "Queued", Mark: "idle", Value: noValue}
	}
	return append([]stageView{build}, three[:]...)
}

// buildSentence is what a deployment that hasn't registered its job says about the build it waits
// on or failed at; nil for the other deployments, which deploymentSentence words as before.
func buildSentence(rec store.DeploymentRecord, f deployFacts) sentence {
	b := rec.Build
	if b == nil || rec.JobVersion != nil {
		return nil
	}
	switch {
	case rec.Status == store.StatusPending && b.Status == store.BuildQueued:
		return plainS("Waiting for its build to start.")
	case rec.Status == store.StatusPending && b.Status == store.BuildRunning:
		return plainS("Building the image.")
	case rec.Status == store.StatusFailed && b.Status == store.BuildFailed:
		out := plainS("The build failed, so nothing was deployed.")
		if f.Current && f.Previous != nil {
			out = append(out, plain(" "), mono(versionLabel(f.Previous)), plain(" keeps serving."))
		}
		return out
	case b.Status == store.BuildCancelled:
		return plainS("A newer deploy replaced this one while it waited for its build.")
	}
	return nil
}

// firstLine is text up to its first line break: a failed build's error is its reason and then the
// last lines of its log, which the log page has.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

// ---- the build log page ---------------------------------------------------------------

// buildLogView is the page of one build: its head, its log and, while it goes, the region that
// reads itself again.
type buildLogView struct {
	AppID, DeploymentID string
	// Short is the deployment's first characters, which the title says.
	Short           string
	DeploymentsHref string
	// RefreshHref is the part the region reloads from; Live says the build isn't over, so it does.
	RefreshHref string
	Live        bool
	Region      buildRegion
}

// buildRegion is what the log page's live region draws.
type buildRegion struct {
	Gone   sentence
	Status statusView
	Facts  []buildFact
	// Error is the deployment's error of a failed build: its reason and the last lines of the log.
	Error string
	// Log is the build's output, masked and cut to its last 64 KiB; Cut says it was longer. Empty,
	// Why says why.
	Log string
	Cut bool
	Why sentence
}

// buildFact is a row of the log page's head: a label and a sentence, or a clock time.
type buildFact struct {
	Label string
	Value sentence
	Clock *clockTime
}

// buildFactsOf lists what the page's head says of the build, leaving out what is not known yet.
// server is the name of the server the build ran on, or "".
func buildFactsOf(now time.Time, b store.BuildRecord, server string) []buildFact {
	var facts []buildFact
	add := func(label string, value sentence) {
		if len(value) > 0 {
			facts = append(facts, buildFact{Label: label, Value: value})
		}
	}
	monoOf := func(text string) sentence {
		if text == "" {
			return nil
		}
		return sentence{mono(text)}
	}
	plainOf := func(text string) sentence {
		if text == "" {
			return nil
		}
		return plainS(text)
	}
	add("Repository", monoOf(b.Repository))
	add("Branch", monoOf(b.Branch))
	add("Commit", monoOf(b.Commit))
	add("Author", plainOf(b.Author))
	add("Subject", plainOf(b.Subject))
	add("Image", monoOf(b.Image))
	add("Digest", monoOf(b.Digest))
	add("Server", monoOf(server))
	for _, t := range []struct {
		label string
		at    time.Time
	}{{"Queued", b.QueuedAt}, {"Started", b.StartedAt}, {"Ended", b.EndedAt}} {
		if !t.at.IsZero() {
			c := clockSecondsOf(now, t.at)
			facts = append(facts, buildFact{Label: t.label, Clock: &c})
		}
	}
	return facts
}

// serverNameOf names the server a build ran on: the one whose Nomad node is nodeID, when the server
// directory knows it (a single unmanaged node may have no name). "" otherwise.
func (s *server) serverNameOf(ctx context.Context, nodeID string) string {
	if nodeID == "" {
		return ""
	}
	v, err := s.svc.ListServers(ctx)
	if err != nil {
		return ""
	}
	for _, sv := range v.Servers {
		if sv.NodeID == nodeID {
			return sv.Name
		}
	}
	return ""
}

// loadBuildRegion reads the build of a deployment and says it in the page's words. The error is
// store.ErrNotFound for an app or deployment that is gone and service.ErrBuildNotFound for one that
// didn't build.
func (s *server) loadBuildRegion(ctx context.Context, appID, deploymentID string) (buildRegion, error) {
	var region buildRegion
	rec, err := s.svc.GetDeployment(ctx, appID, deploymentID)
	if err != nil {
		return region, err
	}
	b, err := s.svc.GetBuild(ctx, appID, deploymentID)
	if err != nil {
		return region, err
	}
	region.Status = buildStatusView(&b)
	region.Facts = buildFactsOf(s.now(), b, s.serverNameOf(ctx, b.Node))
	if b.Status == store.BuildFailed {
		region.Error = cmp.Or(rec.Error, b.Error) // the deployment's error is the build's reason and its last lines
	}
	region.Log, region.Cut = b.Log, b.LogCut
	if region.Log == "" {
		switch b.Status {
		case store.BuildQueued:
			region.Why = plainS("The build is waiting its turn: only so many builds run at once (Settings › Builds).")
		case store.BuildRunning:
			region.Why = plainS("The build has started and has printed nothing yet.")
		case store.BuildCancelled:
			region.Why = plainS("The build was cancelled before it printed anything.")
		default:
			region.Why = plainS("There is no log: it was kept for the newest 50 builds of the app, or the build ended before it printed anything.")
		}
	}
	return region, nil
}

// buildLogHref is the address of a build's page.
func buildLogHref(appID, deploymentID string) string {
	return "/apps/" + url.PathEscape(appID) + "/deployments/" + url.PathEscape(deploymentID) + "/build"
}

// over says the build has ended: its region needs no refresh.
func (r buildRegion) over() bool {
	switch r.Status.Word {
	case "queued", "building":
		return false
	}
	return true
}

// showBuildLog answers GET /apps/{id}/deployments/{deploymentId}/build: the build's head and log, in
// the app's shell with the Deployments tab current. While the build is queued or running the region
// is read again on the deployments notice, and without script the page reloads itself.
func (s *server) showBuildLog(w http.ResponseWriter, r *http.Request, session string) {
	id, depID := r.PathValue("id"), r.PathValue("deploymentId")
	sum, err := s.svc.AppSummary(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	region, err := s.loadBuildRegion(r.Context(), id, depID)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, service.ErrBuildNotFound):
		s.message(w, r, http.StatusNotFound, "No build", "That deployment has no build.", "Open the Deployments tab and pick an entry that built.")
		return
	case err != nil:
		s.serverError(w, r, "read the build", err)
		return
	}
	head, err := s.shellPage(r, id+" build", navProjects, s.appCrumbs(r.Context(), id)...)
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id
	view := &buildLogView{AppID: id, DeploymentID: depID, Short: shortID(depID), DeploymentsHref: "/apps/" + url.PathEscape(id) + "/deployments",
		RefreshHref: "/parts/apps/" + url.PathEscape(id) + "/deployments/" + url.PathEscape(depID) + "/build", Live: !region.over(), Region: region}
	if view.Live {
		head.Refresh = 10
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, http.StatusOK, "app", appPage{pageData: head, appHeader: s.header(r.Context(), sum, "deployments"), BuildLog: view})
}

// partBuildLog answers GET /parts/apps/{id}/deployments/{deploymentId}/build: the region alone. A
// build that has ended answers 286, which htmx takes as "stop polling" and still swaps in.
func (s *server) partBuildLog(w http.ResponseWriter, r *http.Request, session string) {
	id, depID := r.PathValue("id"), r.PathValue("deploymentId")
	region, err := s.loadBuildRegion(r.Context(), id, depID)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, service.ErrBuildNotFound):
		region = buildRegion{Gone: sentence{plain("This build is gone: the app or the deployment was deleted.")}}
	case err != nil:
		s.log.Error("read the build", "app", id, "deployment", depID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if region.Gone != nil || region.over() {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(286)
		var buf strings.Builder
		if err := s.pages["app"].ExecuteTemplate(&buf, "build-region", region); err != nil {
			s.log.Error("render part", "part", "build-region", "error", err)
			return
		}
		_, _ = w.Write([]byte(buf.String()))
		return
	}
	s.renderPart(w, "app", "build-region", region)
}
