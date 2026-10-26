package api

import (
	"sort"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/ui"
)

// parity says what a console route does in API terms: the operations of
// api/openapi.yaml that do the same thing, or, for a route that reads and
// changes nothing, why (every page of chrome has a reason, so an entry with
// no operation and no reason is a mistake, not a decision).
// settingsReads are what every Settings page reads for its nav.
var settingsReads = []string{"listTokens", "listRegistries", "listNotificationChannels", "listNotificationDeliveries",
	"getBackupDestination", "listApps", "listBackups", "listCertificates"}

type parity struct {
	ops []string
	why string
}

// consoleParity is SC-006: everything the console reads or changes can also be
// done with an API token through /v1. Keys are "METHOD path" as internal/ui
// serves them. A page that reads several things lists each operation.
var consoleParity = map[string]parity{
	// Pages with nothing of their own to read or change.
	"GET /{$}":      {why: "a redirect to /apps"},
	"GET /login":    {why: "the sign-in form; clients use API tokens"},
	"POST /login":   {why: "starts a browser session; clients send an API token"},
	"POST /logout":  {why: "ends a browser session; an API token has none"},
	"GET /apps/new": {ops: []string{"listProjects"}}, // a blank form, with the places the app can go in
	"GET /account":  {why: "a redirect to /settings"},
	"GET /static/":  {why: "the console's own stylesheet, scripts and fonts"},
	"GET /live":     {ops: []string{"listApps", "listRecentDeployments", "listAttention", "listSchedules", "listDomains", "listLogs", "getMetrics"}}, // notices that these changed
	// Settings (feature 021 page 3): one page per entry. Each draws the nav's counts and marks, which read
	// every list (tokens, registries, channels and their deliveries, the destination, the apps and a
	// backup's outcome); the entry then shows its own rows. Account is the email and the password form.
	"GET /settings":                {ops: settingsReads},
	"GET /settings/account":        {why: "a redirect to /settings"},
	"GET /settings/registries":     {ops: settingsReads},
	"GET /settings/backup-storage": {ops: settingsReads},
	"GET /settings/notifications":  {ops: settingsReads},
	// Certificates (feature 023): the table is listCertificates; the issuer, the contact email and Warn when are getCertificateSettings.
	"GET /settings/certificates":  {ops: append([]string{"getCertificateSettings"}, settingsReads...)},
	"POST /settings/certificates": {ops: []string{"putCertificateSettings"}},
	"GET /settings/tokens":        {ops: append([]string{"listProjects"}, settingsReads...)},
	"GET /apps":                   {ops: []string{"listProjects", "listApps", "listAttention"}},
	// The Running tab reads listCopies with usage=true: CPU and memory per copy. The header names the
	// app's project and environments (getProject, and getEnvironmentApp for where the app exists).
	// It also lists the app's recent actions (feature 020.5).
	"GET /apps/{id}": {ops: []string{"getApp", "listCopies", "getProject", "getEnvironmentApp", "listAppActions"}},

	"GET /apps/{id}/deployments": {ops: []string{"getApp", "listDeployments"}},
	"GET /apps/{id}/configure":   {ops: []string{"getApp", "getWebhook", "getCompose", "listSchedules"}},
	// The Logs tab (feature 024) is the Logs page filtered to the app: listLogs with ?app= (and the lines around one,
	// getLogsAround), the app's services for the rail (listApps), and the app for its header (getApp).
	"GET /apps/{id}/logs": {ops: []string{"getApp", "listLogs", "getLogsAround", "listApps"}},
	// ?tab=running adds a "usage" event every 5 s that refetches the copies part (listCopies, usage=true).
	// ?tab=domains adds the domains notice (listDomains ?app=). ?watch=logs adds the logs notice, on which the
	// Logs tab reads its region again (listLogs ?app=).
	"GET /live/apps/{id}": {ops: []string{"getApp", "listCopies", "listDeployments", "listLogs", "listDomains"}},

	"GET /parts/apps":             {ops: []string{"listProjects", "listApps"}},
	"GET /parts/activity":         {ops: []string{"listRecentDeployments"}},
	"GET /parts/attention":        {ops: []string{"listAttention"}},
	"GET /parts/deploying":        {ops: []string{"listApps", "listCopies"}},
	"GET /parts/apps/{id}/header": {ops: []string{"getApp", "getProject", "getEnvironmentApp"}},
	// listCopies with usage=true, as the Running tab's copies region.
	"GET /parts/apps/{id}/copies":      {ops: []string{"listCopies"}},
	"GET /parts/apps/{id}/deployments": {ops: []string{"listDeployments"}},
	"GET /parts/apps/{id}/actions":     {ops: []string{"listAppActions"}},

	// What the console changes.
	// The place select (feature 019): an app that goes outside Default's production is created in its environment.
	// "Create without deploying" (feature 019.5) is the same two operations with deploy=false.
	"POST /apps": {ops: []string{"createApp", "createEnvironmentApp"}},
	// The Compose forms (feature 014): the preview saves nothing; the create is a Compose deploy with If-None-Match: *.
	"POST /apps/new/compose/preview": {ops: []string{"previewCompose"}},
	"POST /apps/new/compose":         {ops: []string{"deployCompose"}},
	// Deploy from Compose: getApp gives the revision and what the overlay starts from; deployCompose with If-Match deploys.
	"GET /apps/{id}/compose":          {ops: []string{"getApp", "getCompose"}},
	"POST /apps/{id}/compose/preview": {ops: []string{"getApp", "previewCompose"}},
	"POST /apps/{id}/compose":         {ops: []string{"deployCompose"}},
	"POST /apps/{id}/deploy":          {ops: []string{"deployApp"}},
	// The edit form (feature 014): getApp gives the revision, deployApp with If-Match saves the change.
	"POST /apps/{id}/configure": {ops: []string{"getApp", "deployApp"}},
	// Define the app (feature 019.5): an app with no services gets its first definition, deployed with If-Match.
	"POST /apps/{id}/define": {ops: []string{"getApp", "deployApp"}},
	// Past versions (feature 011).
	"POST /apps/{id}/deployments/{deploymentId}/redeploy": {ops: []string{"redeployDeployment"}},
	"GET /apps/{id}/deployments/{deploymentId}/jobspec":   {ops: []string{"exportDeployment"}},
	"GET /apps/{id}/jobspec":                              {ops: []string{"exportApp"}},
	// The kept Compose file's download (feature 015).
	"GET /apps/{id}/deployments/{deploymentId}/compose": {ops: []string{"getDeploymentCompose"}},
	"POST /apps/{id}/webhook":                           {ops: []string{"createWebhookToken"}},
	"POST /apps/{id}/webhook/revoke":                    {ops: []string{"deleteWebhookToken"}},
	// Deploy tokens (feature 020): a token scoped to this app's name in its project; the GitHub tab creates the app without deploying, then the token.
	"POST /apps/{id}/deploy-token": {ops: []string{"createToken"}},
	"POST /apps/new/github":        {ops: []string{"createApp", "createEnvironmentApp", "createToken"}},
	// Removing one secret is a deploy of the app with its secrets listed, the others without a value (keep).
	"POST /apps/{id}/secrets/remove": {ops: []string{"deployApp"}},
	// Configure's own pages (feature 021). Variables & secrets: a variable is a change to the service's env, a
	// secret a value with its name, and the .env editor replaces one service's env; every change is a deploy of
	// the app on the revision getApp gave. Domains: adding or removing a host changes the service's domains;
	// the page draws the app's domains with their state (listDomains ?app=, feature 023), and its Add panel's
	// Check now is checkDomain.
	"GET /apps/{id}/variables":      {ops: []string{"getApp"}},
	"POST /apps/{id}/variables":     {ops: []string{"getApp", "deployApp"}},
	"POST /apps/{id}/variables/env": {ops: []string{"getApp", "deployApp"}},
	"GET /apps/{id}/domains":        {ops: []string{"getApp", "listDomains", "listApps"}},
	"POST /apps/{id}/domains":       {ops: []string{"getApp", "listDomains", "checkDomain", "deployApp"}},
	// "Back up daily" sets a service's backup policy: a deploy of the app with backup {engine, schedule, keep}.
	"POST /apps/{id}/backups/policy": {ops: []string{"getApp", "deployApp"}},
	// Backups (feature 010).
	"GET /apps/{id}/backups":                     {ops: []string{"getApp", "listBackups", "listVolumes", "getBackupDestination"}},
	"POST /apps/{id}/backups":                    {ops: []string{"createBackup"}},
	"POST /apps/{id}/backups/{backupId}/restore": {ops: []string{"restoreBackup"}},
	"POST /settings/backups":                     {ops: []string{"setBackupDestination"}},
	// Registry credentials (feature 017): the add and edit forms are one PUT, an empty password leaves it out.
	"POST /settings/registries":               {ops: []string{"putRegistry"}},
	"POST /settings/registries/{host}/test":   {ops: []string{"testRegistry"}},
	"POST /settings/registries/{host}/remove": {ops: []string{"deleteRegistry"}},
	// Notification channels (feature 018): the add form is a create; a row's edit form is a patch, and so are
	// Pause and Resume (a patch of "paused"); a new signing secret and the test are their own operations.
	"POST /settings/notifications":             {ops: []string{"createNotificationChannel"}},
	"POST /settings/notifications/{id}":        {ops: []string{"updateNotificationChannel"}},
	"POST /settings/notifications/{id}/test":   {ops: []string{"testNotificationChannel"}},
	"POST /settings/notifications/{id}/secret": {ops: []string{"rotateNotificationSecret"}},
	"POST /settings/notifications/{id}/remove": {ops: []string{"deleteNotificationChannel"}},
	"POST /apps/{id}/delete":                   {ops: []string{"deleteApp"}},
	// App actions (feature 020.5). The Stop page reads the app (its copies and backups) before the post stops it.
	"GET /apps/{id}/stop":                 {ops: []string{"getApp"}},
	"POST /apps/{id}/stop":                {ops: []string{"stopApp"}},
	"POST /apps/{id}/start":               {ops: []string{"startApp"}},
	"POST /apps/{id}/restart":             {ops: []string{"restartApp"}},
	"POST /apps/{id}/copies/{copyId}/run": {ops: []string{"runCommand"}},
	// The terminal page (feature 020.6) holds xterm.js, which connects to the websocket.
	"GET /apps/{id}/copies/{copyId}/terminal": {ops: []string{"openTerminal"}},
	// Projects and environments (feature 019).
	"GET /projects":                         {ops: []string{"listProjects"}},
	"GET /projects/new":                     {why: "a blank form; creating the project is POST /projects"},
	"POST /projects":                        {ops: []string{"createProject"}},
	"GET /projects/{project}":               {ops: []string{"getProject", "listEnvironmentApps"}},
	"POST /projects/{project}":              {ops: []string{"updateProject"}},
	"POST /projects/{project}/delete":       {ops: []string{"deleteProject"}},
	"POST /projects/{project}/environments": {ops: []string{"createEnvironment"}},
	"POST /projects/{project}/environments/{environment}/delete": {ops: []string{"deleteEnvironment"}},
	// Promote and copy between environments. The page reads the preview, which is a POST in the API
	// because it takes a body but changes nothing; the form posts the promotion.
	"GET /apps/{id}/promote":                 {ops: []string{"getApp", "previewPromotion"}},
	"POST /apps/{id}/promote":                {ops: []string{"promoteApp"}},
	"POST /apps/{id}/copy":                   {ops: []string{"copyApp"}},
	"POST /settings/password":                {ops: []string{"changePassword"}},
	"POST /settings/tokens":                  {ops: []string{"createToken"}},
	"POST /settings/tokens/{tokenId}/revoke": {ops: []string{"revokeToken"}},
	// Schedules (feature 022). The list reads the schedules (built-in ones included) and each one's last
	// runs, the apps and projects for its filter, the backup destination for a built-in's words and the
	// channels for its footer; its live region is the same reads. The form lists the apps, and Check
	// changes nothing; the run's page reads the run and its output.
	"GET /schedules":                         {ops: []string{"listSchedules", "listScheduleRuns", "listApps", "listProjects", "getBackupDestination", "listNotificationChannels"}},
	"GET /parts/schedules":                   {ops: []string{"listSchedules", "listScheduleRuns", "listApps", "getBackupDestination", "listNotificationChannels"}},
	"GET /schedules/new":                     {ops: []string{"listApps"}},
	"POST /schedules":                        {ops: []string{"listApps", "createSchedule"}},
	"GET /schedules/{id}/edit":               {ops: []string{"getSchedule", "listApps"}},
	"POST /schedules/{id}":                   {ops: []string{"getSchedule", "listApps", "updateSchedule"}},
	"POST /schedules/{id}/run":               {ops: []string{"runSchedule"}},
	"POST /schedules/{id}/pause":             {ops: []string{"pauseSchedule"}},
	"POST /schedules/{id}/resume":            {ops: []string{"resumeSchedule"}},
	"POST /schedules/{id}/delete":            {ops: []string{"getSchedule", "deleteSchedule"}},
	"GET /schedules/{id}/runs/{runId}":       {ops: []string{"getSchedule", "getScheduleRun", "getScheduleRunOutput"}},
	"POST /schedules/{id}/runs/{runId}/stop": {ops: []string{"stopScheduleRun"}},
	// Domains (feature 023). The list reads every domain with its state (listDomains, with the filter's
	// ?project= and ?app=), the apps for the filter and the Add panel, and the projects for the filter.
	// Check again and the Add panel's Check now are checkDomain; Add is a deploy of the app with one more
	// domain on its revision, refused when another app has the host (listDomains).
	"GET /domains":                   {ops: []string{"listDomains", "listApps", "listProjects"}},
	"GET /parts/domains":             {ops: []string{"listDomains", "listApps"}},
	"GET /parts/domains/add-service": {ops: []string{"getApp"}}, // the Add panel's service field for a picked app
	"POST /domains":                  {ops: []string{"listApps", "getApp", "listDomains", "checkDomain", "deployApp"}},
	"POST /domains/{host}/check":     {ops: []string{"checkDomain"}},
	// The Options form (feature 023): Team only, the www redirect and the path of one domain, and Remove…, are a
	// deploy of the app with that service's domains replaced, on the revision getApp gave (deployApp with
	// If-Match; the Compose-file app goes through deployCompose's overlay). New password is rotateTeamPassword.
	"POST /domains/{host}/options":       {ops: []string{"getApp", "deployApp"}},
	"POST /domains/{host}/remove":        {ops: []string{"getApp", "deployApp"}},
	"POST /domains/{host}/team-password": {ops: []string{"getApp", "rotateTeamPassword"}},
	// Logs (feature 024). The page reads the environment's output (listLogs, with the filters of its address:
	// project, environment, app, service, copy, q, range, levels, before), the lines around one line
	// (getLogsAround, for ?copy=&at=), and the apps and projects for the rail and the place bars. The part is
	// the stream and the histogram alone, drawn again on the logs notice. Download is the page's filter as
	// a text file, oldest first.
	"GET /logs":          {ops: []string{"listLogs", "getLogsAround", "listApps", "listProjects"}},
	"GET /parts/logs":    {ops: []string{"listLogs"}},
	"GET /logs/download": {ops: []string{"downloadLogs"}},
	// Monitoring (feature 025). The page reads the place's numbers (getMetrics, with its range and sort), the host's
	// (getHost: the sparkline titles say cores, and the "This host" section is the host's meters, room and
	// seven days of memory) and the projects for the place bars. The part is the sources, issues, charts and
	// table alone, drawn again on the metrics notice.
	"GET /monitoring":       {ops: []string{"getMetrics", "getHost", "listProjects"}},
	"GET /parts/monitoring": {ops: []string{"getMetrics", "getHost"}},
}

// postsThatOnlyRead are operations that are POSTs because they take a body and
// change nothing: a console page that only reads may name them.
var postsThatOnlyRead = map[string]bool{"previewPromotion": true}

// TestConsoleParity fails when the console has a route the table doesn't know
// (so nobody checked it has an API twin), when the table names an operation the
// contract doesn't have or one of the wrong kind (a page that changes data
// must be backed by a call that changes it, and may read besides), or when the table keeps a route
// the console no longer serves.
func TestConsoleParity(t *testing.T) {
	doc := loadContract(t)
	methodOf := map[string]string{} // operationId -> HTTP method
	for _, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			methodOf[op.OperationID] = method
		}
	}

	served := map[string]bool{}
	for _, rt := range ui.Routes() {
		key := rt.Method + " " + rt.Path
		served[key] = true
		p, ok := consoleParity[key]
		if !ok {
			t.Errorf("the console serves %s, which has no entry in consoleParity: name the API operation that does the same, or say why it needs none", key)
			continue
		}
		if len(p.ops) == 0 && strings.TrimSpace(p.why) == "" {
			t.Errorf("%s names no API operation and gives no reason", key)
		}
		changes := rt.Method != "GET"
		if changes && len(p.ops) == 0 && key != "POST /login" && key != "POST /logout" {
			t.Errorf("%s changes data but names no API operation", key)
		}
		// A page that changes data may also read (the edit form reads the
		// app's revision with getApp), but one of its operations must change it.
		writes := false
		for _, op := range p.ops {
			method, found := methodOf[op]
			switch {
			case !found:
				t.Errorf("%s names operation %q, which api/openapi.yaml doesn't have", key, op)
			case !changes && method != "GET" && !postsThatOnlyRead[op]:
				t.Errorf("%s only reads, but %s is a %s", key, op, method)
			}
			writes = writes || found && method != "GET"
		}
		if changes && len(p.ops) > 0 && !writes {
			t.Errorf("%s changes data, but all of its operations (%v) are GETs", key, p.ops)
		}
	}

	var stale []string
	for key := range consoleParity {
		if !served[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("consoleParity has %s, which the console doesn't serve", key)
	}
}

// The twin of a console action must be callable with an API token: the contract
// must not make it public, or session-only (SC-006 says "with an API token").
func TestConsoleParityOperationsTakeAPITokens(t *testing.T) {
	doc := loadContract(t)
	takesToken := map[string]bool{}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			ok := op.Security == nil // no override: the document's default, token or session
			if op.Security != nil {
				for _, requirement := range *op.Security {
					_, withToken := requirement["apiToken"]
					ok = ok || withToken
				}
			}
			takesToken[op.OperationID] = ok
		}
	}
	for key, p := range consoleParity {
		for _, op := range p.ops {
			if ok, found := takesToken[op]; found && !ok {
				t.Errorf("%s: operation %s doesn't accept an API token", key, op)
			}
		}
	}
}
