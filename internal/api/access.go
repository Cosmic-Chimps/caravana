package api

import (
	"net/http"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Roles (feature 053). Every protected route has an entry in accessRules that says what it needs of
// the person behind the request: the route's Need and the Target it applies to, read from the path,
// the query and the body, never from stored state. authenticate runs the rule after the credentials
// and a deploy token's scope, and Service.Allow decides, the same function the service's own checks
// and the console's routes use. The table is default-deny: a route with no entry, or whose rule says
// ok=false (the request doesn't say what it is about), is refused for everyone, owners included.
// TestAccessRulesCoverEveryRoute makes a new route get an entry, and TestRolesWalkEveryRoute makes
// the entry agree with a table written out in the test.
//
// A few routes can't say their app from the request alone: a schedule's ID doesn't name its app. Their
// rule is "signed in", and the service method judges by the stored schedule (allowSchedule).

// accessRule says what a request needs: the Need, the Target it applies to, and ok=false when the
// request doesn't say. It reads only the request's path, query and (peeked) body.
type accessRule func(r *http.Request) (service.Need, service.Target, bool)

// errNotOpen is the refusal of a route no rule opens to the caller, or whose request says nothing.
var errNotOpen = &Error{Status: http.StatusForbidden, Code: codeNotAllowed, Message: "this isn't open to your role"}

// accessAllows is the check of the person behind the request against the route's rule: nil when
// allowed, errNotOpen for a route without a rule or a request the rule can't read, else the
// service's *NotAllowedError.
func (s *server) accessAllows(r *http.Request, rt route) error {
	rule, listed := accessRules[rt.method+" "+rt.path]
	if !listed {
		return errNotOpen
	}
	need, target, ok := rule(r)
	if !ok {
		return errNotOpen
	}
	return s.svc.Allow(r.Context(), need, target)
}

// signedIn is any person: their own account, and the lists the service filters for them.
func signedIn() accessRule {
	return func(*http.Request) (service.Need, service.Target, bool) {
		return service.NeedSignedIn(), service.Target{}, true
	}
}

// admin is an owner or an admin.
func admin() accessRule {
	return func(*http.Request) (service.Need, service.Target, bool) {
		return service.NeedAdmin(), service.Target{}, true
	}
}

// adminIfUnreadable is the rule when the body can't be read for what the request is about: the
// handler then answers the 400 it owes, to an owner or an admin, and a member is refused.
func adminIfUnreadable() (service.Need, service.Target, bool) {
	return service.NeedAdmin(), service.Target{}, true
}

// appAt is at least level in the place of the app named by {id}.
func appAt(level service.Level) accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		id := r.PathValue("id")
		return service.NeedApp(level), service.Target{AppID: id}, id != ""
	}
}

// appConfigure is the level that changes the app named by {id}: maintain, or deploy in a preview.
func appConfigure() accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
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

// placeAt is at least level in the project and environment of the path.
func placeAt(level service.Level) accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		project, env := r.PathValue("project"), r.PathValue("environment")
		return service.NeedPlace(level), service.Target{Project: project, Environment: env}, project != "" && env != ""
	}
}

// placeInQuery is at least level in the place the query names; the place left out is Default's
// production, as the reads default it.
func placeInQuery(level service.Level) accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		q := r.URL.Query()
		project, env := q.Get("project"), q.Get("environment")
		if project == "" {
			project = app.DefaultProject
		}
		if env == "" {
			env = app.DefaultEnvironment
		}
		return service.NeedPlace(level), service.Target{Project: project, Environment: env}, true
	}
}

// projectAt is at least level in the role of the project of the path.
func projectAt(level service.Level) accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		project := r.PathValue("project")
		return service.NeedProject(level), service.Target{Project: project}, project != ""
	}
}

// createsAppByBodyID is the rule of createApp and the template routes: the app the body's "id"
// names, at the level that creates it.
func createsAppByBodyID() accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		var in struct {
			ID string `json:"id"`
		}
		if !peekBody(r, &in) {
			return adminIfUnreadable()
		}
		return service.NeedApp(configureLevelOf(in.ID)), service.Target{AppID: in.ID}, true
	}
}

// createsAppInPlace is the rule of the place-create route: the project and environment of the
// path, at the level that creates an app there.
func createsAppInPlace() accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		project, env := r.PathValue("project"), r.PathValue("environment")
		return service.NeedPlace(service.ConfigureLevel(env)), service.Target{Project: project, Environment: env}, project != "" && env != ""
	}
}

// environmentOfBody is the rule of creating an environment: the project's own role, at deploy for a
// preview (pr-<n>, which CI opens) and at maintain for any other.
func environmentOfBody() accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		var in struct {
			Name string `json:"name"`
		}
		if !peekBody(r, &in) {
			return adminIfUnreadable()
		}
		return service.NeedProject(projectLevelFor(in.Name)), service.Target{Project: r.PathValue("project")}, r.PathValue("project") != ""
	}
}

// environmentOfPath is the rule of deleting an environment: the same as creating one.
func environmentOfPath() accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
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

// intoEnvironment is the rule of promoting and copying: at least level on the app of the same name
// in the environment the body's "to" names, in the source's project. The service judges the source
// (view). A "to" that is no name, or a body that can't be read, falls back to the same level on the
// source itself, which the handler's own refusal of it follows.
func intoEnvironment(level service.Level) accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		source := r.PathValue("id")
		var in struct {
			To string `json:"to"`
		}
		place, err := app.PlaceOf(source)
		if err != nil || !peekBody(r, &in) || app.ValidName("to", in.To) != nil {
			return service.NeedApp(level), service.Target{AppID: source}, source != ""
		}
		place.Environment = in.To
		return service.NeedApp(level), service.Target{AppID: place.ID()}, true
	}
}

// checksDomain is the rule of checking a domain: with an app named, maintain on it; with none (a host
// that isn't any app's yet) the installation's, an owner or an admin.
func checksDomain() accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		var in struct {
			App string `json:"app"`
		}
		if !peekBody(r, &in) || in.App == "" {
			return adminIfUnreadable()
		}
		return service.NeedApp(service.LevelMaintain), service.Target{AppID: in.App}, true
	}
}

// createsScheduleOfBodyApp is the rule of creating a schedule: maintain on the app the body names.
func createsScheduleOfBodyApp() accessRule {
	return func(r *http.Request) (service.Need, service.Target, bool) {
		var in struct {
			App string `json:"app"`
		}
		if !peekBody(r, &in) {
			return adminIfUnreadable()
		}
		return service.NeedApp(service.LevelMaintain), service.Target{AppID: in.App}, true
	}
}

// accessRules is every protected route's rule, keyed by "METHOD pattern" as routes() writes them.
// The classes are plan D4's table: own account and the lists the service filters for the caller
// (signedIn); installation routes (admin); an app's reads (view), deploys (deploy) and changes
// (maintain); an environment's reads and creates; a project's own settings.
var accessRules = map[string]accessRule{
	// Own account.
	"GET /v1/me":                              signedIn(),
	"PUT /v1/account/password":                signedIn(),
	"GET /v1/account/passkeys":                signedIn(),
	"PATCH /v1/account/passkeys/{passkeyId}":  signedIn(),
	"DELETE /v1/account/passkeys/{passkeyId}": signedIn(),
	"GET /v1/account/sign-in":                 signedIn(),
	"PUT /v1/account/sign-in":                 signedIn(),
	"POST /v1/tokens":                         signedIn(), // a deploy token needs deploy on its project: the service says
	"GET /v1/tokens":                          signedIn(),
	"DELETE /v1/tokens/{tokenId}":             signedIn(),

	// Lists that span projects: the service shows each person their own places.
	"GET /v1/apps":                        signedIn(),
	"GET /v1/deployments":                 signedIn(),
	"GET /v1/attention":                   signedIn(),
	"GET /v1/schedules":                   signedIn(),
	"GET /v1/domains":                     signedIn(),
	"GET /v1/domains/{host}":              signedIn(),
	"GET /v1/projects":                    signedIn(),
	"GET /v1/projects/{project}":          signedIn(),
	"GET /v1/projects/{project}/previews": signedIn(),
	"GET /v1/logs/around":                 signedIn(), // by copy: the service finds the app and refuses what isn't theirs
	"GET /v1/templates":                   signedIn(),
	"GET /v1/templates/{name}":            signedIn(),
	"GET /v1/templates/{name}/logo":       signedIn(),

	// Reads of one place.
	"GET /v1/metrics":       placeInQuery(service.LevelView),
	"GET /v1/logs":          placeInQuery(service.LevelView),
	"GET /v1/logs/download": placeInQuery(service.LevelView),
	"GET /v1/requests":      placeInQuery(service.LevelView),
	"GET /v1/projects/{project}/environments/{environment}/apps":        placeAt(service.LevelView),
	"GET /v1/projects/{project}/environments/{environment}/apps/{name}": placeAt(service.LevelView),

	// Creating an app: the place's own level.
	"POST /v1/apps": createsAppByBodyID(),
	"POST /v1/projects/{project}/environments/{environment}/apps": createsAppInPlace(),
	"POST /v1/templates/{name}/preview":                           createsAppByBodyID(),
	"POST /v1/templates/{name}/install":                           createsAppByBodyID(),

	// A project's own settings, its environments and previews.
	"PATCH /v1/projects/{project}":                             projectAt(service.LevelMaintain),
	"POST /v1/projects/{project}/environments":                 environmentOfBody(),
	"DELETE /v1/projects/{project}/environments/{environment}": environmentOfPath(),
	"PUT /v1/projects/{project}/previews/settings":             projectAt(service.LevelMaintain),
	"DELETE /v1/projects/{project}/previews/{environment}":     projectAt(service.LevelDeploy),

	// An app: reads.
	"GET /v1/apps/{id}":                                    appAt(service.LevelView),
	"GET /v1/apps/{id}/copies":                             appAt(service.LevelView),
	"GET /v1/apps/{id}/volumes":                            appAt(service.LevelView),
	"GET /v1/apps/{id}/actions":                            appAt(service.LevelView),
	"GET /v1/apps/{id}/logs":                               appAt(service.LevelView),
	"GET /v1/apps/{id}/metrics":                            appAt(service.LevelView),
	"GET /v1/apps/{id}/compose":                            appAt(service.LevelView),
	"GET /v1/apps/{id}/deployments":                        appAt(service.LevelView),
	"GET /v1/apps/{id}/deployments/{deploymentId}":         appAt(service.LevelView),
	"GET /v1/apps/{id}/deployments/{deploymentId}/compose": appAt(service.LevelView),
	"GET /v1/apps/{id}/deployments/{deploymentId}/jobspec": appAt(service.LevelView),
	"GET /v1/apps/{id}/deployments/{deploymentId}/build":   appAt(service.LevelView),
	"GET /v1/apps/{id}/deployments/{deploymentId}/scan":    appAt(service.LevelView),
	"GET /v1/apps/{id}/jobspec":                            appAt(service.LevelView),
	"GET /v1/apps/{id}/scan":                               appAt(service.LevelView),
	"GET /v1/apps/{id}/backups":                            appAt(service.LevelView),
	"GET /v1/apps/{id}/webhook":                            appAt(service.LevelView),
	"GET /v1/apps/{id}/services/{service}/git-access":      appAt(service.LevelView),
	"POST /v1/apps/{id}/promote/preview":                   appAt(service.LevelView), // what a promotion would do; the target is the deploy's

	// An app: deploy.
	"POST /v1/apps/{id}/deployments":                         appAt(service.LevelDeploy), // a body that changes more than images needs maintain: the service says
	"POST /v1/apps/{id}/deployments/{deploymentId}/redeploy": appAt(service.LevelDeploy),
	"POST /v1/apps/{id}/restart":                             appAt(service.LevelDeploy),
	"POST /v1/apps/{id}/start":                               appAt(service.LevelDeploy),
	"POST /v1/apps/{id}/secrets/refresh":                     appAt(service.LevelDeploy),
	"POST /v1/apps/{id}/backups":                             appAt(service.LevelDeploy), // Back up now
	"POST /v1/apps/{id}/promote":                             intoEnvironment(service.LevelDeploy),

	// An app: change.
	"DELETE /v1/apps/{id}":                                        appConfigure(),
	"POST /v1/apps/{id}/compose":                                  appConfigure(),
	"POST /v1/apps/{id}/compose/preview":                          appConfigure(),
	"POST /v1/apps/{id}/copy":                                     intoEnvironment(service.LevelMaintain),
	"POST /v1/apps/{id}/stop":                                     appAt(service.LevelMaintain),
	"POST /v1/apps/{id}/copies/{copyId}/run":                      appAt(service.LevelMaintain), // a command can print any secret
	"GET /v1/apps/{id}/copies/{copyId}/terminal":                  appAt(service.LevelMaintain),
	"POST /v1/apps/{id}/backups/{backupId}/restore":               appAt(service.LevelMaintain),
	"PUT /v1/apps/{id}/backups/{backupId}/pin":                    appAt(service.LevelMaintain),
	"DELETE /v1/apps/{id}/backups/{backupId}/pin":                 appAt(service.LevelMaintain),
	"POST /v1/apps/{id}/webhook":                                  appAt(service.LevelMaintain),
	"DELETE /v1/apps/{id}/webhook":                                appAt(service.LevelMaintain),
	"POST /v1/apps/{id}/domains/{host}/team-password":             appAt(service.LevelMaintain),
	"POST /v1/apps/{id}/services/{service}/git-access/deploy-key": appAt(service.LevelMaintain),
	"PUT /v1/apps/{id}/services/{service}/git-access/token":       appAt(service.LevelMaintain),
	"DELETE /v1/apps/{id}/services/{service}/git-access":          appAt(service.LevelMaintain),
	"POST /v1/domains/check":                                      checksDomain(),

	// Schedules: the app is the schedule's, which only the stored schedule says, so the service
	// judges (allowSchedule); creating one names its app.
	"POST /v1/schedules":                                 createsScheduleOfBodyApp(),
	"GET /v1/schedules/{scheduleId}":                     signedIn(),
	"PUT /v1/schedules/{scheduleId}":                     signedIn(),
	"DELETE /v1/schedules/{scheduleId}":                  signedIn(),
	"POST /v1/schedules/{scheduleId}/run":                signedIn(),
	"POST /v1/schedules/{scheduleId}/pause":              signedIn(),
	"POST /v1/schedules/{scheduleId}/resume":             signedIn(),
	"GET /v1/schedules/{scheduleId}/runs":                signedIn(),
	"GET /v1/schedules/{scheduleId}/runs/{runId}":        signedIn(),
	"POST /v1/schedules/{scheduleId}/runs/{runId}/stop":  signedIn(),
	"GET /v1/schedules/{scheduleId}/runs/{runId}/output": signedIn(),

	// People: owners and admins; the service holds an admin to members only.
	"GET /v1/people":                    admin(),
	"POST /v1/people":                   admin(),
	"GET /v1/people/{personId}":         admin(),
	"PUT /v1/people/{personId}":         admin(),
	"DELETE /v1/people/{personId}":      admin(),
	"POST /v1/people/{personId}/invite": admin(),

	// The installation: owners and admins.
	"POST /v1/projects":                              admin(),
	"DELETE /v1/projects/{project}":                  admin(),
	"GET /v1/host":                                   admin(),
	"GET /v1/activity":                               admin(),
	"GET /v1/addons":                                 admin(),
	"PUT /v1/addons/{name}":                          admin(),
	"DELETE /v1/addons/{name}":                       admin(),
	"GET /v1/scans":                                  admin(),
	"POST /v1/scans":                                 admin(),
	"GET /v1/scans/runs":                             admin(),
	"GET /v1/scans/settings":                         admin(),
	"PUT /v1/scans/settings":                         admin(),
	"GET /v1/updates":                                admin(),
	"PUT /v1/updates/settings":                       admin(),
	"POST /v1/updates/check":                         admin(),
	"POST /v1/updates/runs":                          admin(),
	"GET /v1/updates/runs":                           admin(),
	"GET /v1/console-address":                        admin(),
	"POST /v1/console-address/changes":               admin(),
	"GET /v1/console-address/changes/{id}":           admin(),
	"DELETE /v1/console-address/changes/{id}":        admin(),
	"POST /v1/console-address/changes/{id}/switch":   admin(),
	"POST /v1/templates/check":                       admin(),
	"PUT /v1/templates/settings":                     admin(),
	"GET /v1/servers":                                admin(),
	"POST /v1/servers":                               admin(),
	"GET /v1/servers/joins/{id}":                     admin(),
	"GET /v1/servers/{name}":                         admin(),
	"DELETE /v1/servers/{name}":                      admin(),
	"PUT /v1/servers/{name}/labels":                  admin(),
	"POST /v1/servers/{name}/drain":                  admin(),
	"DELETE /v1/servers/{name}/drain":                admin(),
	"GET /v1/servers/{name}/space":                   admin(),
	"GET /v1/servers/{name}/cleanups":                admin(),
	"POST /v1/servers/{name}/cleanups":               admin(),
	"GET /v1/servers/{name}/cleanups/{id}":           admin(),
	"POST /v1/volumes/{name}/deletions":              admin(),
	"GET /v1/volumes/{name}/deletions/{id}":          admin(),
	"GET /v1/cleanup/settings":                       admin(),
	"PUT /v1/cleanup/settings":                       admin(),
	"GET /v1/backups/destination":                    admin(),
	"PUT /v1/backups/destination":                    admin(),
	"GET /v1/backups/destinations":                   admin(),
	"PUT /v1/backups/destinations/{name}":            admin(),
	"DELETE /v1/backups/destinations/{name}":         admin(),
	"POST /v1/backups/destinations/{name}/test":      admin(),
	"GET /v1/registries":                             admin(),
	"PUT /v1/registries/{host}":                      admin(),
	"DELETE /v1/registries/{host}":                   admin(),
	"POST /v1/registries/{host}/test":                admin(),
	"GET /v1/secret-providers":                       admin(),
	"PUT /v1/secret-providers/{name}":                admin(),
	"DELETE /v1/secret-providers/{name}":             admin(),
	"POST /v1/secret-providers/{name}/test":          admin(),
	"GET /v1/dns/zones":                              admin(),
	"PUT /v1/dns/zones/{zone}":                       admin(),
	"DELETE /v1/dns/zones/{zone}":                    admin(),
	"POST /v1/dns/zones/{zone}/test":                 admin(),
	"GET /v1/dns/records":                            admin(),
	"GET /v1/notifications/channels":                 admin(),
	"POST /v1/notifications/channels":                admin(),
	"PATCH /v1/notifications/channels/{id}":          admin(),
	"DELETE /v1/notifications/channels/{id}":         admin(),
	"POST /v1/notifications/channels/{id}/test":      admin(),
	"GET /v1/notifications/channels/{id}/deliveries": admin(),
	"POST /v1/notifications/channels/{id}/secret":    admin(),
	"GET /v1/settings/certificates":                  admin(),
	"PUT /v1/settings/certificates":                  admin(),
	"GET /v1/certificates":                           admin(),
	"PUT /v1/settings/certificates/issuer":           admin(),
	"DELETE /v1/settings/certificates/issuer":        admin(),
	"GET /v1/certificates/uploaded":                  admin(),
	"POST /v1/certificates/uploaded":                 admin(),
	"GET /v1/certificates/uploaded/{id}":             admin(),
	"DELETE /v1/certificates/uploaded/{id}":          admin(),
	"POST /v1/certificates/drops":                    admin(),
	"GET /v1/certificates/drops":                     admin(),
	"GET /v1/certificates/drops/{id}":                admin(),
	"GET /v1/builds/settings":                        admin(),
	"PUT /v1/builds/settings":                        admin(),
}
