package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Roles (feature 053). Every console route that needs a session has an entry in consoleAccess that
// says what it needs of the person behind it: a service.Need and the Target it applies to, read from
// the path, the query and the posted form, never from stored state (a route that must look up an
// app, a domain's, says so). The guards (requireSession, requirePart, requireSessionJSON) run the
// rule after they know who is signed in, put the principal in the request's context and refuse what
// the person's role doesn't allow: a page with a 403 message, a part with a 403 fragment, a script's
// call with JSON. The table is default-deny: a route with no entry, or whose rule says ok=false, is
// refused for everyone, owners included. TestConsoleAccessCoversEveryRoute makes a new route get an
// entry, and TestConsoleRolesWalkEveryRoute checks the entries against a table written out apart
// from them. It is the console's twin of internal/api/access.go: the same Need constructors, the
// same Principal.Allow, so one function decides everything.
//
// A few routes can't say their app from the request alone (a schedule's ID names no app); their rule
// is "signed in" and the service judges by the stored schedule (allowSchedule), the way the API's do.

// accessRule says what a request needs: the Need, the Target it applies to, and ok=false when the
// request doesn't say. It reads the request's path, its query and its (peeked) form; a rule that
// must look something up asks s.
type accessRule func(s *server, r *http.Request) (service.Need, service.Target, bool)

// openRoutes are the routes with no session on purpose: they sign in, sign out, serve the assets or
// start from an invite link. They are not in consoleAccess.
var openRoutes = map[string]bool{
	"GET /{$}":                    true,
	"GET /login":                  true,
	"POST /login":                 true,
	"POST /logout":                true,
	"POST /login/passkey/options": true,
	"POST /login/passkey":         true,
	"GET /invite":                 true,
	"POST /invite":                true,
	"GET /account":                true,
	"GET /settings/account":       true,
	"GET /favicon.ico":            true,
	"GET /static/":                true,
}

// refusal is what a guard says to a person whose role doesn't allow the request: the sentence of a
// *service.NotAllowedError, or the plain one for a route no rule opens.
func refusal(err error) string {
	var na *service.NotAllowedError
	if errors.As(err, &na) {
		return na.Error()
	}
	return "this isn't open to your role"
}

// errNotOpen is the refusal of a route no rule opens to the caller, or whose request says nothing.
var errNotOpen = fmt.Errorf("%w: this isn't open to your role", service.ErrNotAllowed)

// allowed is the check of the person behind the request against the route's rule: nil when allowed,
// errNotOpen for a route without a rule or a request the rule can't read, else the person's
// *service.NotAllowedError.
func (s *server) allowed(r *http.Request, who service.Principal) error {
	rule, listed := consoleAccess[r.Pattern]
	if !listed {
		return errNotOpen
	}
	need, target, ok := rule(s, r)
	if !ok {
		return errNotOpen
	}
	return who.Allow(need, target, s.now())
}

// formLimit bounds what a rule reads of a posted body. The handlers' own limits are as before: the
// body is put back unread.
const formLimit = 10 << 20

// peekForm is the posted form of the request, read without consuming the body: a handler that reads
// it again sees the same bytes. A body that can't be read or is over the limit is an empty form.
func peekForm(r *http.Request) url.Values {
	if r.Body == nil || r.Body == http.NoBody || r.Method == http.MethodGet ||
		!strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, formLimit+1))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
	if err != nil || len(raw) > formLimit {
		return nil
	}
	v, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil
	}
	return v
}

// ---- the rules -------------------------------------------------------------------------------

// signedIn is any person: their own account, and the lists the service filters for them.
func signedIn() accessRule {
	return func(*server, *http.Request) (service.Need, service.Target, bool) {
		return service.NeedSignedIn(), service.Target{}, true
	}
}

// admin is an owner or an admin.
func admin() accessRule {
	return func(*server, *http.Request) (service.Need, service.Target, bool) {
		return service.NeedAdmin(), service.Target{}, true
	}
}

// adminIfUnreadable is the rule when the request doesn't say what it is about: the handler then
// answers what it owes to an owner or an admin, and a member is refused.
func adminIfUnreadable() (service.Need, service.Target, bool) {
	return service.NeedAdmin(), service.Target{}, true
}

// owner is an owner.
func owner() accessRule {
	return func(*server, *http.Request) (service.Need, service.Target, bool) {
		return service.NeedOwner(), service.Target{}, true
	}
}

// appAt is at least level in the place of the app named by {id}.
func appAt(level service.Level) accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		id := r.PathValue("id")
		return service.NeedApp(level), service.Target{AppID: id}, id != ""
	}
}

// appConfigure is the level that changes the app named by {id}: maintain, or deploy in a preview.
func appConfigure() accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		id := r.PathValue("id")
		return service.NeedApp(configureLevelOf(id)), service.Target{AppID: id}, id != ""
	}
}

// configureLevelOf is service.ConfigureLevel of the environment an app ID is in (Default's
// production for an ID PlaceOf refuses, as the service reads it).
func configureLevelOf(appID string) service.Level {
	if p, err := app.PlaceOf(appID); err == nil {
		return service.ConfigureLevel(p.Environment)
	}
	return service.ConfigureLevel(app.DefaultEnvironment)
}

// placeInQuery is at least level in the place the query names; the place left out is Default's
// production, as the pages default it.
func placeInQuery(level service.Level) accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		project, env := placeOfQuery(r.URL.Query())
		return service.NeedPlace(level), service.Target{Project: project, Environment: env}, true
	}
}

func placeOfQuery(q url.Values) (project, environment string) {
	project, environment = q.Get("project"), q.Get("environment")
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	return project, environment
}

// projectAt is at least level in the role of the project of the path.
func projectAt(level service.Level) accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		project := r.PathValue("project")
		return service.NeedProject(level), service.Target{Project: project}, project != ""
	}
}

// creating is the rule of the routes that make an app in the place the form's "place" field names
// (Default's production when it names none): the level that configures that place. A place that is
// no place at all asks for an admin, so the handler's own refusal is drawn for one and a member is
// told no.
func creating() accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		value := strings.TrimSpace(peekForm(r).Get("place"))
		project, env := app.DefaultProject, app.DefaultEnvironment
		if value != "" {
			p, e, found := strings.Cut(value, "/")
			if !found || app.ValidName("project", p) != nil || app.ValidName("environment", e) != nil {
				return adminIfUnreadable()
			}
			project, env = p, e
		}
		return service.NeedPlace(service.ConfigureLevel(env)), service.Target{Project: project, Environment: env}, true
	}
}

// intoEnvironment is the rule of promoting and copying: at least level on the app of the same name
// in the environment the form's "to" names, in the source's project. The service judges the source
// (view). A "to" that is no name falls back to the same level on the source itself, which the
// handler's own refusal of it follows.
func intoEnvironment(level service.Level) accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		source := r.PathValue("id")
		to := peekForm(r).Get("to")
		place, err := app.PlaceOf(source)
		if err != nil || app.ValidName("to", to) != nil {
			return service.NeedApp(level), service.Target{AppID: source}, source != ""
		}
		place.Environment = to
		return service.NeedApp(level), service.Target{AppID: place.ID()}, true
	}
}

// domainOf is the rule of the routes of one domain row (/domains/{host}/…): the level on the app that
// owns the host, found in the rows the person may see. A host that is nobody's, or is hidden from
// the person, asks for an admin, so an admin gets the handler's own answer and a member is refused.
func domainOf(level service.Level) accessRule {
	return func(s *server, r *http.Request) (service.Need, service.Target, bool) {
		if s.svc != nil {
			if v, err := s.svc.Domain(r.Context(), r.PathValue("host")); err == nil && v.App != "" {
				return service.NeedApp(level), service.Target{AppID: v.App}, true
			}
		}
		return adminIfUnreadable()
	}
}

// addsDomains is the rule of adding domains from the Domains page: the form says the app.
func addsDomains() accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		id := strings.TrimSpace(peekForm(r).Get("app"))
		if id == "" {
			return adminIfUnreadable()
		}
		return service.NeedApp(configureLevelOf(id)), service.Target{AppID: id}, true
	}
}

// checksDomainRow is the rule of Check now: with an app named by the form, maintain on it; with
// none (a host that isn't any app's yet) an owner or an admin.
func checksDomainRow() accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		if id := strings.TrimSpace(peekForm(r).Get("app")); id != "" {
			return service.NeedApp(service.LevelMaintain), service.Target{AppID: id}, true
		}
		return adminIfUnreadable()
	}
}

// newSchedule is the rule of the form that makes a schedule: with an app named (by the query or the
// form) maintain on it; with none the person is asked later, by the service, which refuses an app
// they may not change.
func newSchedule() accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		id := strings.TrimSpace(r.URL.Query().Get("app"))
		if id == "" {
			id = strings.TrimSpace(peekForm(r).Get("app"))
		}
		if id == "" {
			return service.NeedSignedIn(), service.Target{}, true
		}
		return service.NeedApp(service.LevelMaintain), service.Target{AppID: id}, true
	}
}

// environmentOfForm is the rule of adding an environment: the project's own role, at deploy for a
// preview (pr-<n>) and at maintain for any other.
func environmentOfForm() accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		project := r.PathValue("project")
		name := strings.TrimSpace(peekForm(r).Get("environment"))
		return service.NeedProject(projectLevelFor(name)), service.Target{Project: project}, project != ""
	}
}

// environmentOfPath is the rule of deleting an environment.
func environmentOfPath() accessRule {
	return func(_ *server, r *http.Request) (service.Need, service.Target, bool) {
		project := r.PathValue("project")
		return service.NeedProject(projectLevelFor(r.PathValue("environment"))), service.Target{Project: project}, project != ""
	}
}

// projectLevelFor is the project level that opens or closes an environment: deploy for a preview,
// maintain for the rest.
func projectLevelFor(environment string) service.Level {
	if app.IsPreviewEnvironment(environment) {
		return service.LevelDeploy
	}
	return service.LevelMaintain
}

// consoleAccess is the table. The keys are "METHOD pattern", exactly as r.Pattern reads at the
// handler. See the comment at the top of the file.
var consoleAccess = map[string]accessRule{
	// Own account and the lists the service filters for the person.
	"GET /settings":                          signedIn(),
	"GET /settings/tokens":                   signedIn(),
	"GET /settings/appearance":               signedIn(),
	"GET /settings/sign-in":                  signedIn(),
	"POST /settings/sign-in":                 signedIn(),
	"POST /settings/sign-in/verify/options":  signedIn(),
	"POST /settings/sign-in/verify":          signedIn(),
	"POST /settings/passkeys/options":        signedIn(),
	"POST /settings/passkeys":                signedIn(),
	"POST /settings/passkeys/{id}":           signedIn(),
	"POST /settings/passkeys/{id}/remove":    signedIn(),
	"POST /settings/password":                signedIn(),
	"POST /settings/appearance":              signedIn(),
	"POST /settings/tokens":                  signedIn(),
	"POST /settings/tokens/{tokenId}/revoke": signedIn(),
	"GET /apps":                              signedIn(),
	"GET /parts/apps":                        signedIn(),
	"GET /parts/activity":                    signedIn(),
	"GET /parts/deploying":                   signedIn(),
	"GET /parts/attention":                   signedIn(),
	"GET /projects":                          signedIn(),
	"GET /projects/{project}":                signedIn(),
	"GET /parts/projects/{project}/apps":     signedIn(),
	"GET /parts/projects/{project}/rail":     signedIn(),
	"GET /previews":                          signedIn(),
	"GET /parts/previews":                    signedIn(),
	"GET /schedules":                         signedIn(),
	"GET /parts/schedules":                   signedIn(),
	"GET /domains":                           signedIn(),
	"GET /parts/domains":                     signedIn(),
	"GET /parts/domains/add-service":         signedIn(),
	"GET /live":                              signedIn(),
	"GET /apps/new":                          signedIn(),
	"GET /apps/new/template/{name}":          signedIn(),
	"GET /templates/{name}/logo.svg":         signedIn(),
	"GET /monitoring":                        placeInQuery(service.LevelView),
	"GET /parts/monitoring":                  placeInQuery(service.LevelView),
	"GET /logs":                              placeInQuery(service.LevelView),
	"GET /parts/logs":                        placeInQuery(service.LevelView),
	"GET /logs/download":                     placeInQuery(service.LevelView),
	"GET /requests":                          placeInQuery(service.LevelView),
	"GET /parts/requests":                    placeInQuery(service.LevelView),

	// Making an app: the place's own level.
	"POST /apps":                     creating(),
	"POST /apps/new/github":          creating(),
	"POST /apps/new/git":             creating(),
	"POST /apps/new/template/{name}": creating(),
	"POST /apps/new/compose/preview": creating(),
	"POST /apps/new/compose":         creating(),

	// A project's own settings, its environments and its previews.
	"GET /projects/{project}/settings":                           projectAt(service.LevelView),
	"POST /projects/{project}":                                   projectAt(service.LevelMaintain),
	"POST /projects/{project}/backups":                           projectAt(service.LevelMaintain),
	"POST /projects/{project}/environments":                      environmentOfForm(),
	"POST /projects/{project}/environments/{environment}/delete": environmentOfPath(),
	"POST /projects/{project}/previews/settings":                 projectAt(service.LevelMaintain),
	"GET /projects/{project}/previews/{environment}/close":       projectAt(service.LevelDeploy),
	"POST /projects/{project}/previews/{environment}/close":      projectAt(service.LevelDeploy),
	"GET /projects/new":                                          admin(),
	"POST /projects":                                             admin(),
	"POST /projects/{project}/delete":                            admin(),

	// An app: reads.
	"GET /apps/{id}":                                        appAt(service.LevelView),
	"GET /apps/{id}/deployments":                            appAt(service.LevelView),
	"GET /apps/{id}/configure":                              appAt(service.LevelView),
	"GET /apps/{id}/configure/source":                       appAt(service.LevelView),
	"GET /apps/{id}/configure/services":                     appAt(service.LevelView),
	"GET /apps/{id}/configure/integrations":                 appAt(service.LevelView),
	"GET /apps/{id}/logs":                                   appAt(service.LevelView),
	"GET /apps/{id}/requests":                               appAt(service.LevelView),
	"GET /apps/{id}/deployments/{deploymentId}/jobspec":     appAt(service.LevelView),
	"GET /apps/{id}/deployments/{deploymentId}/build":       appAt(service.LevelView),
	"GET /apps/{id}/deployments/{deploymentId}/compose":     appAt(service.LevelView),
	"GET /apps/{id}/jobspec":                                appAt(service.LevelView),
	"GET /apps/{id}/variables":                              appAt(service.LevelView),
	"GET /apps/{id}/domains":                                appAt(service.LevelView),
	"GET /apps/{id}/backups":                                appAt(service.LevelView),
	"GET /apps/{id}/promote":                                appAt(service.LevelView),
	"GET /parts/apps/{id}/header":                           appAt(service.LevelView),
	"GET /parts/apps/{id}/copies":                           appAt(service.LevelView),
	"GET /parts/apps/{id}/actions":                          appAt(service.LevelView),
	"GET /parts/apps/{id}/deployments":                      appAt(service.LevelView),
	"GET /parts/apps/{id}/deployments/{deploymentId}/build": appAt(service.LevelView),
	"GET /parts/apps/{id}/security":                         appAt(service.LevelView),
	"GET /live/apps/{id}":                                   appAt(service.LevelView),

	// An app: deploy.
	"POST /apps/{id}/deploy":                              appAt(service.LevelDeploy),
	"POST /apps/{id}/deployments/{deploymentId}/redeploy": appAt(service.LevelDeploy),
	"POST /apps/{id}/restart":                             appAt(service.LevelDeploy),
	"POST /apps/{id}/start":                               appAt(service.LevelDeploy),
	"POST /apps/{id}/secrets/refresh":                     appAt(service.LevelDeploy),
	"POST /apps/{id}/backups":                             appAt(service.LevelDeploy),
	"POST /apps/{id}/promote":                             intoEnvironment(service.LevelDeploy),

	// An app: change. A save that changes the definition is the configure level (maintain, deploy in a preview).
	"POST /apps/{id}/configure":                                appConfigure(),
	"POST /apps/{id}/define":                                   appConfigure(),
	"GET /apps/{id}/compose":                                   appConfigure(),
	"POST /apps/{id}/compose/preview":                          appConfigure(),
	"POST /apps/{id}/compose":                                  appConfigure(),
	"POST /apps/{id}/variables":                                appConfigure(),
	"POST /apps/{id}/variables/env":                            appConfigure(),
	"POST /apps/{id}/secrets/remove":                           appConfigure(),
	"POST /apps/{id}/domains":                                  appConfigure(),
	"POST /apps/{id}/backups/policy":                           appConfigure(),
	"POST /apps/{id}/backups/restore-schedule":                 appConfigure(),
	"POST /apps/{id}/backups/places":                           appConfigure(),
	"POST /apps/{id}/services/{service}/placement":             appConfigure(),
	"POST /apps/{id}/services/{service}/scale":                 appConfigure(),
	"POST /apps/{id}/services/{service}/build":                 appConfigure(),
	"POST /apps/{id}/delete":                                   appConfigure(),
	"POST /apps/{id}/copy":                                     intoEnvironment(service.LevelMaintain),
	"GET /apps/{id}/stop":                                      appAt(service.LevelMaintain),
	"POST /apps/{id}/stop":                                     appAt(service.LevelMaintain),
	"POST /apps/{id}/copies/{copyId}/run":                      appAt(service.LevelMaintain),
	"GET /apps/{id}/copies/{copyId}/terminal":                  appAt(service.LevelMaintain),
	"POST /apps/{id}/backups/{backupId}/restore":               appAt(service.LevelMaintain),
	"POST /apps/{id}/backups/{backupId}/pin":                   appAt(service.LevelMaintain),
	"POST /apps/{id}/backups/{backupId}/unpin":                 appAt(service.LevelMaintain),
	"POST /apps/{id}/webhook":                                  appAt(service.LevelMaintain),
	"POST /apps/{id}/webhook/revoke":                           appAt(service.LevelMaintain),
	"POST /apps/{id}/deploy-token":                             appAt(service.LevelMaintain),
	"POST /apps/{id}/services/{service}/git-access/deploy-key": appAt(service.LevelMaintain),
	"POST /apps/{id}/services/{service}/git-access/token":      appAt(service.LevelMaintain),
	"POST /apps/{id}/services/{service}/git-access/remove":     appAt(service.LevelMaintain),

	// Schedules: the schedule's ID names no app, so the service judges the stored one.
	"GET /schedules/new":                     newSchedule(),
	"POST /schedules":                        newSchedule(),
	"GET /schedules/{id}/edit":               signedIn(),
	"POST /schedules/{id}":                   signedIn(),
	"POST /schedules/{id}/run":               signedIn(),
	"POST /schedules/{id}/pause":             signedIn(),
	"POST /schedules/{id}/resume":            signedIn(),
	"POST /schedules/{id}/delete":            signedIn(),
	"GET /schedules/{id}/runs/{runId}":       signedIn(),
	"POST /schedules/{id}/runs/{runId}/stop": signedIn(),

	// Domains: each row's routes are judged by the app that owns the host.
	"POST /domains":                      addsDomains(),
	"POST /domains/{host}/check":         checksDomainRow(),
	"POST /domains/{host}/options":       domainOf(service.LevelMaintain),
	"POST /domains/{host}/remove":        domainOf(service.LevelMaintain),
	"POST /domains/{host}/team-password": domainOf(service.LevelMaintain),

	// Templates' catalog: the installation's.
	"POST /templates/check":    admin(),
	"POST /templates/settings": admin(),

	// The installation: owners and admins. The Operator view, and the Settings that are the installation's.
	"GET /ops":                                         admin(),
	"GET /parts/ops/server":                            admin(),
	"GET /ops/servers":                                 admin(),
	"GET /parts/ops/servers":                           admin(),
	"GET /ops/servers/new":                             admin(),
	"POST /ops/servers":                                admin(),
	"GET /ops/servers/{name}":                          admin(),
	"GET /ops/servers/{kind}/{rest}":                   admin(),
	"POST /ops/servers/{name}/remove":                  admin(),
	"POST /ops/servers/{name}/labels":                  admin(),
	"POST /ops/servers/{name}/move":                    admin(),
	"POST /ops/servers/{name}/accept":                  admin(),
	"POST /ops/servers/{name}/clean":                   admin(),
	"GET /ops/servers/{name}/volumes/{volume}/delete":  admin(),
	"POST /ops/servers/{name}/volumes/{volume}/delete": admin(),
	"POST /ops/cleanup/settings":                       admin(),
	"POST /ops/servers/joins/{id}/cancel":              admin(),
	"GET /parts/ops/servers/{kind}/{rest}":             admin(),
	"GET /parts/ops/servers/{name}":                    admin(),
	"GET /ops/updates":                                 admin(),
	"GET /parts/ops/updates":                           admin(),
	"GET /parts/ops/updates/run":                       admin(),
	"GET /ops/updates/caravana":                        admin(),
	"POST /ops/updates/caravana":                       admin(),
	"POST /ops/updates/check":                          admin(),
	"POST /ops/updates/settings":                       admin(),
	"GET /ops/console-address":                         admin(),
	"GET /parts/ops/console-address":                   admin(),
	"POST /ops/console-address/changes":                admin(),
	"POST /ops/console-address/changes/{id}/switch":    admin(),
	"POST /ops/console-address/changes/{id}/cancel":    admin(),
	"GET /ops/security":                                admin(),
	"GET /parts/ops/security":                          admin(),
	"POST /ops/security/install":                       admin(),
	"GET /ops/security/remove":                         admin(),
	"POST /ops/security/remove":                        admin(),
	"POST /ops/security/settings":                      admin(),
	"POST /ops/security/scan":                          admin(),
	"POST /apps/{id}/security/scan":                    admin(),
	"GET /parts/ops/apps":                              admin(),
	"GET /parts/ops/activity":                          admin(),
	"GET /parts/monitoring/host":                       admin(),
	"GET /parts/platform":                              admin(),
	"GET /settings/registries":                         admin(),
	"GET /settings/builds":                             admin(),
	"GET /settings/secret-providers":                   admin(),
	"GET /settings/backup-storage":                     admin(),
	"GET /settings/notifications":                      admin(),
	"GET /settings/certificates":                       admin(),
	"GET /settings/dns":                                admin(),
	"POST /settings/backups":                           admin(),
	"POST /settings/backups/test":                      admin(),
	"POST /settings/backups/default":                   admin(),
	"POST /settings/backups/remove":                    admin(),
	"POST /settings/certificates":                      admin(),
	"POST /settings/certificates/upload":               admin(),
	"POST /settings/certificates/uploaded/{id}/remove": admin(),
	"POST /settings/certificates/issuer":               admin(),
	"POST /settings/certificates/drop":                 admin(),
	"GET /parts/settings/certificates/drop":            admin(),
	"POST /settings/dns":                               admin(),
	"POST /settings/dns/test":                          admin(),
	"POST /settings/dns/remove":                        admin(),
	"POST /settings/registries":                        admin(),
	"POST /settings/builds":                            admin(),
	"POST /settings/registries/test":                   admin(),
	"POST /settings/registries/remove":                 admin(),
	"POST /settings/secret-providers":                  admin(),
	"POST /settings/secret-providers/test":             admin(),
	"POST /settings/secret-providers/remove":           admin(),
	"POST /settings/notifications":                     admin(),
	"POST /settings/notifications/{id}":                admin(),
	"POST /settings/notifications/{id}/test":           admin(),
	"POST /settings/notifications/{id}/secret":         admin(),
	"POST /settings/notifications/{id}/remove":         admin(),

	// The audit log (feature 057): owners and admins read and export it, owners set how long it is kept.
	"GET /settings/audit":        admin(),
	"GET /settings/audit/export": admin(),
	"POST /settings/audit":       owner(),

	// People (feature 053): owners and admins; the service holds an admin to members.
	"GET /settings/people":              admin(),
	"GET /settings/people/new":          admin(),
	"POST /settings/people":             admin(),
	"GET /settings/people/{id}":         admin(),
	"POST /settings/people/{id}":        admin(),
	"POST /settings/people/{id}/invite": admin(),
	"GET /settings/people/{id}/remove":  admin(),
	"POST /settings/people/{id}/remove": admin(),
}

// ---- refusing --------------------------------------------------------------------------------

// placeDefaulted are the pages that read a place the query names and default it to Default's
// production. A person who can't see that default, and named no place, is taken to the first place
// they can see instead of being told no (feature 053, D4).
var placeDefaulted = map[string]bool{"GET /logs": true, "GET /requests": true, "GET /monitoring": true}

// firstVisiblePlace is the first project and environment the person may see: the service filters
// the projects by the principal in ctx.
func (s *server) firstVisiblePlace(r *http.Request) (project, environment string, ok bool) {
	if s.svc == nil {
		return "", "", false
	}
	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		return "", "", false
	}
	for _, pr := range pl.Projects {
		for _, e := range pr.Environments {
			return pr.ID, e.Name, true
		}
	}
	return "", "", false
}

// refusePage answers a page the person's role doesn't allow: 403 with the sentence of what their role
// is and what the page needs. A page that defaults to a place the person can't see goes to the first
// place they can.
func (s *server) refusePage(w http.ResponseWriter, r *http.Request, err error) {
	if placeDefaulted[r.Pattern] {
		q := r.URL.Query()
		if q.Get("project") == "" && q.Get("environment") == "" {
			if project, environment, ok := s.firstVisiblePlace(r); ok {
				q.Set("project", project)
				q.Set("environment", environment)
				http.Redirect(w, r, r.URL.Path+"?"+q.Encode(), http.StatusSeeOther)
				return
			}
		}
	}
	s.refuse(w, r, err)
}

// refuse draws the 403 page for a person whose role doesn't allow what they asked. Handlers that
// get a *service.NotAllowedError from the service use it too (serverError does).
func (s *server) refuse(w http.ResponseWriter, r *http.Request, err error) {
	s.message(w, r, http.StatusForbidden, "Not allowed", "You can't do this here", refusal(err)+".")
}

// refusePart answers a fragment the person's role doesn't allow: 403 with a sentence htmx doesn't swap
// in, so the region keeps what it had.
func (s *server) refusePart(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, `<p class="cv-hint">`+template.HTMLEscapeString(refusal(err))+`.</p>`)
}

// ---- what the person may do, for what a page draws ----------------------------------------------

// mayCreateApps says the person may make an app somewhere: an admin always, a member where their role
// configures a place they can see (maintain, or deploy in a preview). The top bar's New app is drawn
// for them only. A person with no principal (a test serving the account pages) is not refused.
func (s *server) mayCreateApps(ctx context.Context, who service.Principal) bool {
	if who.Admin() || s.svc == nil {
		return true
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return false
	}
	for _, pr := range pl.Projects {
		for _, e := range pr.Environments {
			if who.LevelAt(pr.ID, e.Name, s.now()) >= service.ConfigureLevel(e.Name) {
				return true
			}
		}
	}
	return false
}

// capsOf is what the person in ctx may do about the app: the answers the app's page draws its buttons
// from. Caravana itself (a context with no principal: a test building a view) may do everything.
type caps struct {
	Deploy, Maintain, Configure, Admin bool
}

// capsFor reads what the person in ctx may do about the app.
func (s *server) capsFor(ctx context.Context, appID string) caps {
	who, ok := service.PrincipalFrom(ctx)
	if !ok {
		return caps{Deploy: true, Maintain: true, Configure: true, Admin: true}
	}
	now := s.now()
	return caps{Deploy: canAt(who, now, "deploy", appID), Maintain: canAt(who, now, "maintain", appID),
		Configure: canAt(who, now, "configure", appID), Admin: who.Admin()}
}

// can says whether the person in ctx may do what the word says ("view", "deploy", "maintain",
// "configure") about the app; Caravana itself (no principal) may do everything.
func (s *server) can(ctx context.Context, what, appID string) bool {
	who, ok := service.PrincipalFrom(ctx)
	return !ok || canAt(who, s.now(), what, appID)
}

// mustAllow is a check inside a handler, for what the route's rule couldn't say before the handler
// looked something up (a schedule's app): it draws the 403 page and answers false when the person's
// role doesn't allow it. Caravana itself (no principal) is allowed.
func (s *server) mustAllow(w http.ResponseWriter, r *http.Request, need service.Need, target service.Target) bool {
	who, ok := service.PrincipalFrom(r.Context())
	if !ok {
		return true
	}
	if err := who.Allow(need, target, s.now()); err != nil {
		s.refuse(w, r, err)
		return false
	}
	return true
}

// canMakeAppsIn says the person may make an app in the place: the level that configures it (feature 053).
func (s *server) canMakeAppsIn(ctx context.Context, project, environment string) bool {
	who, ok := service.PrincipalFrom(ctx)
	return !ok || who.LevelAt(project, environment, s.now()) >= service.ConfigureLevel(environment)
}

// viewerEmail is the email of the person in ctx, "" for none.
func viewerEmail(ctx context.Context) string {
	who, _ := service.PrincipalFrom(ctx)
	return who.Email
}

// actorWords says who did something to the person looking: "you" when it was them, else the email.
func actorWords(actor, viewer string) string {
	if actor != "" && actor == viewer {
		return "you"
	}
	return actor
}

// mine is the deployment as the person in ctx reads it: its actor said as "you" when it is them, so
// the one set of who-words (deployWho) needs no viewer (feature 053).
func (s *server) mine(ctx context.Context, rec store.DeploymentRecord) store.DeploymentRecord {
	rec.Deployment.Actor = actorWords(rec.Deployment.Actor, viewerEmail(ctx))
	return rec
}

// mineSummary is sum with its last deployment read as mine reads it.
func (s *server) mineSummary(ctx context.Context, sum service.AppSummary) service.AppSummary {
	if sum.LastDeployment != nil {
		rec := s.mine(ctx, *sum.LastDeployment)
		sum.LastDeployment = &rec
	}
	return sum
}
