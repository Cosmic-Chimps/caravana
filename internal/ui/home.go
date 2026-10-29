package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file is the Apps home's part of feature 021, page 4: the "Deploying now"
// cards, the filter, and the words that say who deployed. The words are pure
// (home_test.go); the loaders read the service.

// ---- who deployed --------------------------------------------------------------------

// originWords is a CI deploy's words: "pushed to main by ana · 4e0a77", the branch
// and the commit in mono, "by <actor>" only when known, and the commit only when
// withCommit says so. Nil when the origin says none of the three (a run address
// alone has nothing to say here).
func originWords(o *app.Origin, withCommit bool) sentence {
	if o == nil {
		return nil
	}
	branch, commit := o.BranchOrTag(), o.ShortCommit()
	if branch == "" && o.Actor == "" && commit == "" {
		return nil
	}
	out := sentence{plain("pushed")}
	if branch != "" {
		out = append(out, plain(" to "), mono(branch))
	}
	if o.Actor != "" {
		out = append(out, plain(" by "+o.Actor))
	}
	if commit != "" && withCommit {
		out = append(out, plain(" · "), mono(commit))
	}
	return out
}

// deployWho says who deployed, in lowercase words that follow a lead of the
// caller's: "pushed to main by ana · 4e0a77", "promoted from staging", "through the
// API" (and "by <token>" when the deployment recorded which token), "by the
// webhook", "from the console". It is the one set of words the Activity rail, the
// history entry's meta line and the app's lede share (021 page 4, critique ruling
// 1); the card capitalizes them ("Pushed to ...") or puts "Deployed" first. The
// commit is left out when withCommit is false (the lede has no room for it).
func deployWho(rec store.DeploymentRecord, withCommit bool) sentence {
	if rec.SourceApp != "" {
		return sentence{plain("promoted from "), mono(rec.SourceEnvironment)}
	}
	if o := originWords(rec.Deployment.Origin, withCommit); o != nil {
		return o
	}
	switch rec.Deployment.Trigger {
	case app.TriggerWebhook:
		return plainS("by the webhook")
	case app.TriggerManual:
		out := plainS("through the API")
		if name := rec.Deployment.TokenName; name != "" {
			out = append(out, plain(" by "), mono(name))
		}
		return out
	}
	return plainS(triggerWords(rec.Deployment.Trigger))
}

// capitalized is s with its first letter upper-cased, for a sentence that starts
// with it (the card's Change line).
func capitalized(s sentence) sentence {
	if len(s) == 0 || s[0].Text == "" {
		return s
	}
	out := slices.Clone(s)
	r, n := utf8.DecodeRuneInString(out[0].Text)
	out[0].Text = string(unicode.ToUpper(r)) + out[0].Text[n:]
	return out
}

// changeLine is the Change row of a "Deploying now" card: what the deployment
// carries, in one line, with the same who-words as the rest. again is the
// version label a redeploy repeats ("" when it can't be said).
func changeLine(rec store.DeploymentRecord, again string) sentence {
	switch {
	case rec.SourceApp != "":
		return capitalized(deployWho(rec, true))
	case rec.Source != "":
		if again == "" {
			return plainS("A past version deployed again")
		}
		return sentence{mono(again), plain(" deployed again")}
	}
	who := deployWho(rec, true)
	if originWords(rec.Deployment.Origin, true) != nil {
		return capitalized(who) // "Pushed to main by ana", not "Deployed pushed ..."
	}
	return join(plainS("Deployed "), who)
}

// ---- Deploying now -------------------------------------------------------------------

// chipView is one copy on a card: the app's name (the service's, when the app has
// several), the version the copy runs and the copy's short ID. State is "starting"
// (dashed edge, a half mark), "draining" (dashed, struck through) or "" for a copy
// that serves. Role is the same in words for a reader that doesn't see the style:
// "new, healthy", "new, starting", "serving, v5", "draining, v5".
type chipView struct {
	Name    string
	Version string // "v6"
	Short   string
	Full    string
	Mark    string // the status kind of the mark: healthy, warning, rolling, idle
	State   string
	Role    string
}

// deployingCard is one app with a rollout in progress.
type deployingCard struct {
	Name       string
	Href       string
	Place      string // "project / environment" outside Default › production, else ""
	Status     statusView
	DeployHref string // the entry on the Deployments tab
	Change     sentence
	Chips      []chipView
	// Summary says how the rollout stands: "1 of 2 new copies healthy", or why
	// there is nothing to count yet.
	Summary string
}

// deployingRegion is what the "deploying" region draws: nothing when no rollout runs.
type deployingRegion struct {
	Cards []deployingCard
}

// isRollingOut says the app's newest deployment is pending or running, and the owner
// hasn't stopped the app.
func isRollingOut(sum service.AppSummary) bool {
	d := sum.LastDeployment
	return d != nil && sum.StoppedAt == nil && (d.Status == store.StatusPending || d.Status == store.StatusRunning)
}

// loadDeploying builds a card for each app that is rolling out, newest first. A
// card is a bonus: when Nomad can't be read the card says so instead of listing
// copies.
func (s *server) loadDeploying(ctx context.Context, sums []service.AppSummary, pl places) deployingRegion {
	var rolling []service.AppSummary
	for _, sum := range sums {
		if isRollingOut(sum) {
			rolling = append(rolling, sum)
		}
	}
	slices.SortStableFunc(rolling, func(a, b service.AppSummary) int {
		return b.LastDeployment.CreatedAt.Compare(a.LastDeployment.CreatedAt)
	})
	var region deployingRegion
	for _, sum := range rolling {
		region.Cards = append(region.Cards, s.deployingCardOf(ctx, sum, pl))
	}
	return region
}

func (s *server) deployingCardOf(ctx context.Context, sum service.AppSummary, pl places) deployingCard {
	rec := *sum.LastDeployment
	version := versionLabel(rec.JobVersion)
	word := "rolling out"
	if version != "" {
		word = version + " rolling out"
	}
	card := deployingCard{
		Name:       cmp.Or(sum.Place.Name, sum.App.ID),
		Href:       "/apps/" + sum.App.ID,
		Status:     statusView{"rolling", word},
		DeployHref: s.deploymentsHref(sum.App.ID),
	}
	if s.hasTab("deployments") {
		card.DeployHref += "#deployment-" + rec.Deployment.ID
	}
	if !atHome(sum.Place) {
		card.Place = pl.display(sum.Place.Project) + " / " + sum.Place.Environment
	}

	var again string
	if rec.Source != "" && rec.SourceApp == "" {
		if src, err := s.svc.GetDeployment(ctx, sum.App.ID, rec.Source); err == nil {
			again = versionLabel(src.JobVersion)
		} else {
			s.log.Warn("can't read the deployment this one redeploys, for the Deploying now card",
				"app", sum.App.ID, "deployment", rec.Source, "error", err)
		}
	}
	card.Change = changeLine(rec, again)

	copies, err := s.svc.Copies(ctx, sum.App.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("can't read copies for the Deploying now card", "app", sum.App.ID, "error", err)
		}
		card.Summary = "Nomad can't be reached right now, so the copies aren't shown."
		return card
	}
	card.Chips = chipsOf(card.Name, len(sum.App.Services), copies, rec.JobVersion)
	if len(card.Chips) == 0 {
		card.Summary = "Waiting for Nomad to place the first copy."
		return card
	}
	switch p, err := s.svc.Progress(ctx, rec); {
	case err != nil:
		s.log.Warn("can't read rollout progress", "deployment", rec.Deployment.ID, "error", err)
	case p != nil && (p.Wanted > 0 || p.Healthy > 0):
		card.Summary = newCopiesHealthy(*p)
	default:
		card.Summary = "Nomad hasn't started this rollout yet."
	}
	return card
}

// chipsOf is the copies of the app that haven't ended, as chips: the new version's
// copies first (newVersion is the version the rollout registered), then the ones that
// still serve the old version, then the ones Nomad is stopping. Within a group, by
// service and then by copy.
func chipsOf(appName string, services int, copies []service.Copy, newVersion *uint64) []chipView {
	var live []service.Copy
	for _, c := range copies {
		if !c.Terminal {
			live = append(live, c)
		}
	}
	isNew := func(c service.Copy) bool { return newVersion != nil && c.Version == *newVersion && !c.Stopping }
	rank := func(c service.Copy) int {
		switch {
		case c.Stopping:
			return 2
		case isNew(c):
			return 0
		}
		return 1
	}
	slices.SortStableFunc(live, func(x, y service.Copy) int {
		return cmp.Or(cmp.Compare(rank(x), rank(y)), strings.Compare(x.Service, y.Service), cmp.Compare(y.Version, x.Version), strings.Compare(x.ID, y.ID))
	})
	var out []chipView
	for _, c := range live {
		name := appName
		if services > 1 {
			name = c.Service
		}
		version := fmt.Sprintf("v%d", c.Version)
		status := copyStatusView(c.Status)
		chip := chipView{Name: name, Version: version, Short: shortID(c.ID), Full: c.ID, Mark: status.Kind}
		switch {
		case c.Stopping:
			// Only a copy Nomad has told to stop is draining; an old copy that still serves isn't.
			chip.State, chip.Mark, chip.Role = "draining", "idle", "draining, "+version
		case isNew(c):
			chip.Role = "new, " + status.Word
		case c.Status == deploy.CopyHealthy:
			chip.Role = "serving, " + version
		default:
			chip.Role = status.Word + ", " + version
		}
		if !c.Stopping && c.Status == deploy.CopyStarting {
			chip.State = "starting"
		}
		out = append(out, chip)
	}
	return out
}

// partDeploying answers GET /parts/deploying: the "Deploying now" cards alone.
func (s *server) partDeploying(w http.ResponseWriter, r *http.Request, session string) {
	sums, err := s.svc.ListApps(r.Context())
	if err != nil {
		s.log.Error("list apps", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		s.log.Error("list places", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "apps", "deploying-region", s.loadDeploying(r.Context(), sums, pl))
}

// ---- the filter ----------------------------------------------------------------------

// filterMinApps is how many apps the caravana must have before the filter is drawn:
// a filter over a few rows is noise.
const filterMinApps = 5

// The states and kinds the filter selects; "" is all.
const (
	stateNeeds       = "needs"
	stateRolling     = "rolling"
	stateHealthy     = "healthy"
	stateStopped     = "stopped"
	stateNotDeployed = "notdeployed"
)

var (
	stateOptions = []selectOption{
		{Value: "", Label: "All states"}, {Value: stateNeeds, Label: "Needs you"}, {Value: stateRolling, Label: "Rolling out"},
		{Value: stateHealthy, Label: "Healthy"}, {Value: stateStopped, Label: "Stopped"}, {Value: stateNotDeployed, Label: "Not deployed"},
	}
	kindOptions = []selectOption{
		{Value: "", Label: "All kinds"}, {Value: "image", Label: "Image"}, {Value: kindCompose, Label: "Compose"},
		{Value: kindDatabase, Label: "Database"}, {Value: kindVolume, Label: "Volume"},
	}
)

// selectOption is one option of a select.
type selectOption struct {
	Value    string
	Label    string
	Selected bool
}

// appsFilter is what ?q=, ?state= and ?kind= ask for. A value the select doesn't
// offer is the same as none.
type appsFilter struct {
	Q     string
	State string
	Kind  string
}

// maxFilterQuery bounds ?q=, which is only ever compared with names and images.
const maxFilterQuery = 200

func filterOf(r *http.Request) appsFilter {
	q := r.URL.Query()
	f := appsFilter{Q: strings.TrimSpace(q.Get("q")), State: q.Get("state"), Kind: q.Get("kind")}
	f.Q = truncateRunes(f.Q, maxFilterQuery)
	if !hasOption(stateOptions, f.State) {
		f.State = ""
	}
	if !hasOption(kindOptions, f.Kind) {
		f.Kind = ""
	}
	return f
}

func hasOption(opts []selectOption, value string) bool {
	return slices.ContainsFunc(opts, func(o selectOption) bool { return o.Value == value })
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Active says some filter is set.
func (f appsFilter) Active() bool { return f.Q != "" || f.State != "" || f.Kind != "" }

// Query is the filter as a query string for the live refresh ("" when none is set),
// so the region reloads itself with the same filter.
func (f appsFilter) Query() string {
	v := url.Values{}
	if f.Q != "" {
		v.Set("q", f.Q)
	}
	if f.State != "" {
		v.Set("state", f.State)
	}
	if f.Kind != "" {
		v.Set("kind", f.Kind)
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// matches says whether an app passes the filter: its name, project and environment,
// images and domains contain q (any case), its state and its kind are the ones asked for.
func (f appsFilter) matches(sum service.AppSummary, row appRow) bool {
	if f.State != "" && row.State != f.State {
		return false
	}
	if f.Kind != "" && !(row.Kind == f.Kind || (f.Kind == "image" && row.Kind == kindImage)) {
		return false
	}
	if f.Q == "" {
		return true
	}
	needle := strings.ToLower(f.Q)
	fields := []string{sum.App.ID, row.Name, sum.Place.Project, sum.Place.Environment}
	for _, svc := range sum.App.Services {
		fields = append(fields, svc.Image)
		for _, d := range svc.Domains {
			fields = append(fields, d.Host)
		}
	}
	return slices.ContainsFunc(fields, func(v string) bool { return strings.Contains(strings.ToLower(v), needle) })
}

// filterView is the filter row: drawn only when the caravana has more than
// filterMinApps apps.
type filterView struct {
	Show   bool
	Q      string
	States []selectOption
	Kinds  []selectOption
	Clear  bool // a filter is set: offer the link to /apps
}

func filterViewOf(f appsFilter, apps int) filterView {
	v := filterView{Show: apps > filterMinApps, Q: f.Q, Clear: f.Active()}
	for _, o := range stateOptions {
		o.Selected = o.Value == f.State
		v.States = append(v.States, o)
	}
	for _, o := range kindOptions {
		o.Selected = o.Value == f.Kind
		v.Kinds = append(v.Kinds, o)
	}
	return v
}

// stateKey is the filter's state of an app: the first that applies. An app that
// needs you is one with an item in the needs-you list (needsYou), the same list the
// headline counts: a failed or reverted deploy, a failed backup or restore, a crash
// loop, unhealthy copies, and so on.
func stateKey(sum service.AppSummary, st statusView, needsYou bool) string {
	switch {
	case sum.StoppedAt != nil:
		return stateStopped
	case needsYou:
		return stateNeeds
	case st.Kind == "rolling":
		return stateRolling
	case st.Kind == "healthy":
		return stateHealthy
	case sum.LastDeployment == nil && sum.Status.State == service.StateNotRunning:
		return stateNotDeployed
	}
	return ""
}
