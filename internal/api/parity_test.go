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
	"getBackupDestination", "listBackupDestinations", "listApps", "listBackups", "listCertificates", "listSecretProviders", "listDNSZones", "listPeople"}

type parity struct {
	ops []string
	why string
}

// consoleOnly are the console's routes that change something and have no API operation, each with
// the reason. They are the sign-in and sign-out of a browser, its own theme cookie, and the
// passkey ceremonies (feature 033): a WebAuthn ceremony is between a browser, the owner's
// authenticator and the console's own origin, and an API client authenticates with a token and has
// none of them (specs/033-passkeys/plan.md, Complexity Tracking). Everything else about passkeys
// (list, rename, remove, the password setting) is an API operation, in consoleParity.
var consoleOnly = map[string]string{
	"POST /login":                           "starts a browser session; clients send an API token",
	"POST /logout":                          "ends a browser session; an API token has none",
	"POST /settings/appearance":             "sets the browser's own theme cookie; an API client has no theme",
	"POST /login/passkey/options":           "starts a WebAuthn sign-in ceremony for the browser's script; an API client has no authenticator",
	"POST /login/passkey":                   "finishes a WebAuthn sign-in and starts a browser session; clients send an API token",
	"POST /settings/sign-in/verify/options": "starts a WebAuthn confirmation ceremony for the browser's script",
	"POST /settings/sign-in/verify":         "confirms a browser session with the password or a passkey; an API token needs no confirming",
	"POST /settings/passkeys/options":       "starts the WebAuthn ceremony that adds a passkey: the credential is bound to the console's origin and the owner's device",
	"POST /settings/passkeys":               "finishes the WebAuthn ceremony that adds a passkey, for the same reason",
	"POST /invite":                          "accepts an invite and starts a browser session for the person; the API's acceptInvite sets the password and starts none (a client uses a token)",
}

// consoleParity is SC-006: everything the console reads or changes can also be
// done with an API token through /v1. Keys are "METHOD path" as internal/ui
// serves them. A page that reads several things lists each operation.
var consoleParity = map[string]parity{
	// Pages with nothing of their own to read or change.
	"GET /{$}":         {why: "a redirect to /apps"},
	"GET /login":       {why: "the sign-in form; clients use API tokens"},
	"GET /invite":      {why: "the page of an invite link: it names who the link is for and asks for a password; the link's token is its credential, and the API reads nothing of it"},
	"GET /favicon.ico": {why: "the browser tab's icon, asked for at the root"},
	"GET /apps/new":    {ops: []string{"listProjects", "listTemplates"}}, // a blank form, with the places the app can go in; the template tab lists the catalog (feature 035)
	"GET /account":     {why: "a redirect to /settings"},
	"GET /static/":     {why: "the console's own stylesheet, scripts and fonts"},
	"GET /live":        {ops: []string{"listApps", "listRecentDeployments", "listAttention", "listSchedules", "listDomains", "listLogs", "getMetrics"}}, // notices that these changed
	// Settings (feature 021 page 3): one page per entry. Each draws the nav's counts and marks, which read
	// every list (tokens, registries, channels and their deliveries, the destination, the apps and a
	// backup's outcome); the entry then shows its own rows. Account is the email and the password form.
	"GET /settings":            {ops: settingsReads},
	"GET /settings/account":    {why: "a redirect to /settings"},
	"GET /settings/registries": {ops: settingsReads},
	// Secret providers (feature 037).
	"GET /settings/secret-providers": {ops: settingsReads},
	"GET /settings/backup-storage":   {ops: settingsReads},
	// DNS providers (feature 048): the rows are listDNSZones, which every Settings page reads for its nav.
	"GET /settings/dns":           {ops: settingsReads},
	"GET /settings/notifications": {ops: settingsReads},
	// Certificates (feature 023): the table is listCertificates; the issuer, the contact email and Warn when are getCertificateSettings.
	// Feature 034: the uploaded certificates are listUploadedCertificates, and the forms upload, remove and set the issuer.
	// Feature 056: the page lists the drops (listCertificateDrops), the button is dropEarlierCertificates and the panel's part is getCertificateDrop.
	"GET /settings/certificates":                       {ops: append([]string{"getCertificateSettings", "listUploadedCertificates", "listCertificateDrops"}, settingsReads...)},
	"POST /settings/certificates":                      {ops: []string{"putCertificateSettings"}},
	"POST /settings/certificates/upload":               {ops: []string{"uploadCertificate"}},
	"POST /settings/certificates/uploaded/{id}/remove": {ops: []string{"deleteUploadedCertificate"}},
	"POST /settings/certificates/issuer":               {ops: []string{"putCertificateIssuer", "deleteCertificateIssuer"}},
	"POST /settings/certificates/drop":                 {ops: []string{"dropEarlierCertificates"}},
	"GET /parts/settings/certificates/drop":            {ops: []string{"getCertificateDrop"}},
	"GET /settings/tokens":                             {ops: append([]string{"listProjects"}, settingsReads...)},
	// Appearance (feature 028): the theme is a preference of the browser, kept in its own cookie, like
	// the Developer/Operator view; the API has no look and nothing to read or change for it.
	"GET /settings/appearance": {ops: settingsReads},
	// Sign-in (feature 033): the page lists the passkeys (listPasskeys) and the setting (getSignIn); Rename, Remove and
	// the password setting are the same service calls as the API's. The ceremonies are in consoleOnly.
	// People (feature 053): the list, the invite form (it lists the projects and their environments), a person's
	// page and its confirmation of Remove.
	"GET /settings/people":              {ops: append([]string{"listPeople"}, settingsReads...)},
	"GET /settings/people/new":          {ops: append([]string{"listProjects"}, settingsReads...)},
	"POST /settings/people":             {ops: []string{"invitePerson"}},
	"GET /settings/people/{id}":         {ops: append([]string{"getPerson", "listProjects"}, settingsReads...)},
	"POST /settings/people/{id}":        {ops: []string{"updatePerson"}},
	"POST /settings/people/{id}/invite": {ops: []string{"reinvitePerson"}},
	"GET /settings/people/{id}/remove":  {ops: append([]string{"getPerson"}, settingsReads...)},
	"POST /settings/people/{id}/remove": {ops: []string{"removePerson"}},
	// Audit log (feature 057): the page reads listAuditEvents (and getAuditSettings for the retention, with the
	// nav's reads), the export is exportAuditEvents, and the retention form is putAuditSettings.
	"GET /settings/audit":                 {ops: append([]string{"listAuditEvents", "getAuditSettings"}, settingsReads...)},
	"GET /settings/audit/export":          {ops: []string{"exportAuditEvents", "listAuditEvents", "getAuditSettings"}},
	"POST /settings/audit":                {ops: []string{"putAuditSettings"}},
	"GET /settings/sign-in":               {ops: append([]string{"listPasskeys", "getSignIn"}, settingsReads...)},
	"POST /settings/sign-in":              {ops: []string{"putSignIn"}},
	"POST /settings/passkeys/{id}":        {ops: []string{"updatePasskey"}},
	"POST /settings/passkeys/{id}/remove": {ops: []string{"deletePasskey"}},
	// The rail's Platform panel (feature 025) is getHost; the needs-you list's disk row is listAttention.
	// The "Deploying now" card reads each rolling app's copies (listCopies) and the deployment it
	// redeploys from and its progress (getDeployment).
	"GET /apps": {ops: []string{"listProjects", "listApps", "listAttention", "getHost", "listCopies", "getDeployment"}},
	// The Running tab reads listCopies with usage=true: CPU and memory per copy. The header names the
	// app's project and environments (getProject, and getEnvironmentApp for where the app exists).
	// It also lists the app's recent actions (feature 020.5).
	// Next steps of an app built from Git (feature 050) read its Git access and whether it has a webhook token.
	"GET /apps/{id}": {ops: []string{"getApp", "listCopies", "getProject", "getEnvironmentApp", "listAppActions", "getGitAccess", "getWebhook"}},

	// The created notice of an app installed from a template reads the template's notes (getTemplate).
	"GET /apps/{id}/deployments": {ops: []string{"getApp", "listDeployments", "getTemplate", "getAppScan", "getDeploymentScan"}},
	// A failed deploy on an image also reads the registries, to say whether to add or fix credentials.
	// A built service's Build panel (feature 050) also reads its Git access.
	// Feature 059 split it into sections: Overview adds the schedules and the backups lines (listBackups, and
	// listBackupDestinations for where they go); Source, Services and Integrations read what their forms hold.
	"GET /apps/{id}/configure":              {ops: []string{"getApp", "getWebhook", "getCompose", "listSchedules", "listRegistries", "getTemplate", "getGitAccess", "listBackups", "listBackupDestinations"}},
	"GET /apps/{id}/configure/source":       {ops: []string{"getApp", "getWebhook", "getCompose", "listRegistries", "getTemplate", "getGitAccess"}},
	"GET /apps/{id}/configure/services":     {ops: []string{"getApp", "getWebhook", "getCompose", "listRegistries", "getTemplate", "getGitAccess"}},
	"GET /apps/{id}/configure/integrations": {ops: []string{"getApp", "getWebhook", "getCompose", "listRegistries", "getTemplate", "getGitAccess"}},
	// The Logs tab (feature 024) is the Logs page filtered to the app: listLogs with ?app= (and the lines around one,
	// getLogsAround), the app's services for the rail (listApps), and the app for its header (getApp).
	"GET /apps/{id}/logs": {ops: []string{"getApp", "listLogs", "getLogsAround", "listApps"}},
	// The Requests tab (feature 047) is the Requests page filtered to the app: listRequests with ?app=, the app's
	// services for the rail (listApps) and the app for its header (getApp).
	"GET /apps/{id}/requests": {ops: []string{"getApp", "listRequests", "listApps"}},
	// ?tab=running adds a "usage" event every 5 s that refetches the copies part (listCopies, usage=true).
	// ?tab=domains adds the domains notice (listDomains ?app=). ?watch=logs adds the logs notice, on which the
	// Logs tab reads its region again (listLogs ?app=); ?watch=requests the requests notice, on which the Requests
	// tab reads its region again (listRequests ?app=).
	"GET /live/apps/{id}": {ops: []string{"getApp", "listCopies", "listDeployments", "listLogs", "listRequests", "listDomains"}},

	"GET /parts/apps":             {ops: []string{"listProjects", "listApps"}},
	"GET /parts/activity":         {ops: []string{"listRecentDeployments"}},
	"GET /parts/attention":        {ops: []string{"listAttention"}},
	"GET /parts/deploying":        {ops: []string{"listApps", "listCopies", "getDeployment"}},
	"GET /parts/apps/{id}/header": {ops: []string{"getApp", "getProject", "getEnvironmentApp"}},
	// listCopies with usage=true, as the Running tab's copies region.
	"GET /parts/apps/{id}/copies":      {ops: []string{"listCopies"}},
	"GET /parts/apps/{id}/deployments": {ops: []string{"listDeployments", "getDeploymentScan"}},
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
	// New app › From a template (feature 035): the page reads the template and whether a backup destination is
	// set; Create is installTemplate, Preview (action=preview) previewTemplate. The footer's Check now and its
	// setting, and the logo, are the API's own operations.
	"GET /apps/new/template/{name}":  {ops: []string{"getTemplate", "getBackupDestination", "listProjects"}},
	"POST /apps/new/template/{name}": {ops: []string{"installTemplate", "previewTemplate"}},
	"POST /templates/check":          {ops: []string{"checkTemplates"}},
	"POST /templates/settings":       {ops: []string{"putTemplateSettings"}},
	"GET /templates/{name}/logo.svg": {ops: []string{"getTemplateLogo"}},
	// Removing one secret is a deploy of the app with its secrets listed, the others without a value (keep).
	"POST /apps/{id}/secrets/remove":               {ops: []string{"deployApp"}},
	"POST /apps/{id}/services/{service}/placement": {ops: []string{"getApp", "deployApp"}},
	// Building from a Git repository (feature 050): New app's Build from Git tab is createApp (a deploy that builds) or
	// the same with deploy=false; the Build panel's form is a deploy with the service's source; the build's page reads
	// the build (getDeploymentBuild, whose part is the same read); the Git access forms are the API's own operations.
	"POST /apps/new/git":                                       {ops: []string{"createApp", "createEnvironmentApp"}},
	"GET /apps/{id}/deployments/{deploymentId}/build":          {ops: []string{"getApp", "getDeploymentBuild"}},
	"GET /parts/apps/{id}/deployments/{deploymentId}/build":    {ops: []string{"getDeploymentBuild"}},
	"POST /apps/{id}/services/{service}/build":                 {ops: []string{"getApp", "deployApp"}},
	"POST /apps/{id}/services/{service}/git-access/deploy-key": {ops: []string{"createDeployKey"}},
	"POST /apps/{id}/services/{service}/git-access/token":      {ops: []string{"putGitToken"}},
	"POST /apps/{id}/services/{service}/git-access/remove":     {ops: []string{"deleteGitAccess"}},
	// Settings › Builds: the five limits.
	"GET /settings/builds":  {ops: append([]string{"getBuildSettings"}, settingsReads...)},
	"POST /settings/builds": {ops: []string{"putBuildSettings"}},
	// Scaling (feature 043): the form is a deploy with the service's scale; Stop scaling a deploy without it.
	"POST /apps/{id}/services/{service}/scale": {ops: []string{"getApp", "deployApp"}},
	"POST /apps/{id}/secrets/refresh":          {ops: []string{"refreshSecrets"}}, // feature 037
	// Configure's own pages (feature 021). Variables & secrets: a variable is a change to the service's env, a
	// secret a value with its name, and the .env editor replaces one service's env; every change is a deploy of
	// the app on the revision getApp gave. Domains: adding or removing a host changes the service's domains;
	// the page draws the app's domains with their state (listDomains ?app=, feature 023), and its Add panel's
	// Check now is checkDomain.
	"GET /apps/{id}/variables":      {ops: []string{"getApp", "listSecretProviders"}}, // the providers the Add form offers (feature 037)
	"POST /apps/{id}/variables":     {ops: []string{"getApp", "deployApp"}},
	"POST /apps/{id}/variables/env": {ops: []string{"getApp", "deployApp"}},
	"GET /apps/{id}/domains":        {ops: []string{"getApp", "listDomains", "listApps", "listDNSZones"}},
	"POST /apps/{id}/domains":       {ops: []string{"getApp", "listDomains", "checkDomain", "deployApp", "listDNSZones"}},
	// "Back up daily" sets a service's backup policy: a deploy of the app with backup {engine, schedule, keep}.
	"POST /apps/{id}/backups/policy": {ops: []string{"getApp", "deployApp"}},
	// Pin and unpin a backup, and a service's restore schedule (feature 036): the schedule is a deploy
	// of the app with backup.restore.
	"POST /apps/{id}/backups/{backupId}/pin":   {ops: []string{"pinBackup"}},
	"POST /apps/{id}/backups/{backupId}/unpin": {ops: []string{"unpinBackup"}},
	"POST /apps/{id}/backups/restore-schedule": {ops: []string{"getApp", "deployApp"}},
	// Where backups go and are copied (feature 052): a deploy with the service's backup.destination, copyTo and copyKeep.
	"POST /apps/{id}/backups/places": {ops: []string{"getApp", "deployApp"}},
	// Backups (feature 010).
	"GET /apps/{id}/backups":                     {ops: []string{"getApp", "listBackups", "listVolumes", "getBackupDestination", "listBackupDestinations", "getProject"}},
	"POST /apps/{id}/backups":                    {ops: []string{"createBackup"}},
	"POST /apps/{id}/backups/{backupId}/restore": {ops: []string{"restoreBackup"}},
	"POST /settings/backups":                     {ops: []string{"setBackupDestination", "putBackupDestination"}},
	// Backup destinations (feature 052): Test, Make default (a PUT with default true) and Remove, one destination each.
	"POST /settings/backups/test":    {ops: []string{"testBackupDestination"}},
	"POST /settings/backups/default": {ops: []string{"putBackupDestination"}},
	"POST /settings/backups/remove":  {ops: []string{"deleteBackupDestination"}},
	// The project's default backup destination (feature 052).
	"POST /projects/{project}/backups": {ops: []string{"updateProject"}},
	// Registry credentials (feature 017): the add and edit forms are one PUT, an empty password leaves it out.
	"POST /settings/registries":              {ops: []string{"putRegistry"}},
	"POST /settings/registries/test":         {ops: []string{"testRegistry"}},
	"POST /settings/registries/remove":       {ops: []string{"deleteRegistry"}},
	"POST /settings/dns":                     {ops: []string{"putDNSZone"}},
	"POST /settings/dns/test":                {ops: []string{"testDNSZone"}},
	"POST /settings/dns/remove":              {ops: []string{"deleteDNSZone"}},
	"POST /settings/secret-providers":        {ops: []string{"putSecretProvider"}},
	"POST /settings/secret-providers/test":   {ops: []string{"testSecretProvider"}},
	"POST /settings/secret-providers/remove": {ops: []string{"deleteSecretProvider"}},
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
	"GET /projects":     {ops: []string{"listProjects"}},
	"GET /projects/new": {why: "a blank form; creating the project is POST /projects"},
	"POST /projects":    {ops: []string{"createProject"}},
	// The project's page (feature 027.5): the project, its apps, what needs the owner (listAttention) and the
	// environment's newest deployments (listRecentDeployments; the API has no environment filter yet, the
	// page reads the store by place). The settings page is the project and its apps, for the delete reasons.
	"GET /projects/{project}":                                    {ops: []string{"getProject", "listEnvironmentApps", "listAttention", "listRecentDeployments"}},
	"GET /projects/{project}/settings":                           {ops: []string{"getProject", "listEnvironmentApps", "getPreviews", "listBackupDestinations"}},
	"GET /parts/projects/{project}/apps":                         {ops: []string{"getProject", "listEnvironmentApps"}},
	"GET /parts/projects/{project}/rail":                         {ops: []string{"getProject", "listEnvironmentApps", "listAttention", "listRecentDeployments"}},
	"POST /projects/{project}":                                   {ops: []string{"updateProject"}},
	"POST /projects/{project}/delete":                            {ops: []string{"deleteProject"}},
	"POST /projects/{project}/environments":                      {ops: []string{"createEnvironment"}},
	"POST /projects/{project}/environments/{environment}/delete": {ops: []string{"deleteEnvironment"}},
	// Previews (feature 049): the Previews page lists every project's previews and its setup tab saves with putPreviewSettings; Close preview is closePreview.
	"GET /previews":       {ops: []string{"listProjects", "getPreviews"}},
	"GET /parts/previews": {ops: []string{"listProjects", "getPreviews"}},
	"POST /projects/{project}/previews/settings":            {ops: []string{"getPreviews", "putPreviewSettings"}},
	"GET /projects/{project}/previews/{environment}/close":  {ops: []string{"getProject", "listEnvironmentApps"}},
	"POST /projects/{project}/previews/{environment}/close": {ops: []string{"closePreview"}},
	// Promote and copy between environments. The page reads the preview, which is a POST in the API
	// because it takes a body but changes nothing; the form posts the promotion.
	"GET /apps/{id}/promote":                 {ops: []string{"getApp", "previewPromotion", "getDeployment"}}, // getDeployment names the source's job version
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
	// domain on its revision, refused when another app has the host (listDomains). The Add panel also names the
	// zones Caravana makes records in (listDNSZones, feature 048).
	"GET /domains":                   {ops: []string{"listDomains", "listApps", "listProjects", "listDNSZones"}},
	"GET /parts/domains":             {ops: []string{"listDomains", "listApps"}},
	"GET /parts/domains/add-service": {ops: []string{"getApp", "listApps", "listDNSZones"}}, // the Add panel's service field for a picked app
	"POST /domains":                  {ops: []string{"listApps", "getApp", "listDomains", "checkDomain", "deployApp", "listDNSZones"}},
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
	// Requests (feature 047). The page reads the environment's requests (listRequests, with the filters of its address:
	// project, environment, app, service, status, method, path, minDuration, client, range, from, to, before), the apps
	// for the rail (listApps) and the projects for the place bars. The part is the histogram and the table alone,
	// drawn again on the requests notice.
	"GET /requests":       {ops: []string{"listRequests", "listApps", "listProjects"}},
	"GET /parts/requests": {ops: []string{"listRequests"}},
	// Monitoring (feature 025). The page reads the place's numbers (getMetrics, with its range and sort), the host's
	// (getHost: the sparkline titles say cores, and the "This host" section is the host's meters, room and
	// seven days of memory) and the projects for the place bars. The part is the sources, issues, charts and
	// table alone, drawn again on the metrics notice.
	"GET /monitoring":       {ops: []string{"getMetrics", "getHost", "listProjects"}},
	"GET /parts/monitoring": {ops: []string{"getMetrics", "getHost"}},
	// The "This host" section alone, drawn again on the same notice: the host's meters, room and memory.
	"GET /parts/monitoring/host": {ops: []string{"getHost"}},
	// The Apps home's Platform panel alone, drawn again on the metrics notice.
	"GET /parts/platform": {ops: []string{"getHost"}},
	// The Operator Overview (feature 027 page 1). The headline and stats are the apps (listApps, listAttention), the host's
	// use (getHost) and the copies (listCopies); the server panel is getHost's node and meters with the copies; the apps
	// table adds the CPU line and memory of 025 (getMetrics, one call per place, whose places are listProjects); Live
	// activity is listActivity. The parts are the same regions drawn again on the live notices.
	// With several servers (feature 031) the Overview has a panel for each (listServers, with getHost and listCopies of each).
	"GET /ops":              {ops: []string{"listApps", "listAttention", "getHost", "listCopies", "getMetrics", "listProjects", "listActivity", "listServers", "getServerSpace"}},
	"GET /parts/ops/server": {ops: []string{"listApps", "listAttention", "getHost", "listCopies", "getMetrics", "listProjects", "listServers", "getServerSpace"}},
	// The Servers list (feature 031): listServers is the list, the sums and whether one can be added; getHost names the
	// server Caravana runs on and its architecture. A server's page reads that server (getServer, and getHost with
	// `server`) and its copies. Add a server: the form is addServer (the command is its answer, shown once), the join's page
	// and its part are getServerJoin (the four checks), and cancelling a join or removing a server is removeServer.
	"GET /ops/servers":                    {ops: []string{"listServers", "getHost"}},
	"GET /parts/ops/servers":              {ops: []string{"listServers", "getHost"}},
	"GET /ops/servers/new":                {ops: []string{"listServers", "getUpdates"}},
	"POST /ops/servers":                   {ops: []string{"addServer"}},
	"GET /ops/servers/{name}":             {ops: []string{"getServer", "getHost", "listCopies", "listAttention", "listActivity", "listApps", "listProjects", "getServerSpace"}},
	"GET /ops/servers/{kind}/{rest}":      {ops: []string{"getServerJoin", "listServers", "getServer", "listCopies", "listApps", "getServerSpace"}},
	"POST /ops/servers/{name}/remove":     {ops: []string{"removeServer"}},
	"POST /ops/servers/{name}/labels":     {ops: []string{"putServerLabels"}},
	"POST /ops/servers/{name}/move":       {ops: []string{"drainServer"}},
	"POST /ops/servers/{name}/accept":     {ops: []string{"undrainServer"}},
	"POST /ops/servers/joins/{id}/cancel": {ops: []string{"removeServer"}},
	// Reclaiming space (feature 045): the Space section of a server's page is getServerSpace; its page and part, and the
	// Overview's "Reclaimable" line, read it. Clean up now is cleanServer, Delete… of a volume no app declares is
	// deleteOrphanVolume (the back-up-first box reads the backup destination), and the setting is putCleanupSettings.
	"POST /ops/servers/{name}/clean":                   {ops: []string{"cleanServer"}},
	"GET /ops/servers/{name}/volumes/{volume}/delete":  {ops: []string{"getServerSpace", "getBackupDestination"}},
	"POST /ops/servers/{name}/volumes/{volume}/delete": {ops: []string{"deleteOrphanVolume"}},
	"POST /ops/cleanup/settings":                       {ops: []string{"putCleanupSettings"}},
	"GET /parts/ops/servers/{kind}/{rest}":             {ops: []string{"getServerJoin", "listServers", "getServerSpace"}},
	"GET /parts/ops/servers/{name}":                    {ops: []string{"getServer", "getHost", "listCopies", "listAttention", "listActivity", "listApps", "listProjects", "getServerSpace"}},
	"GET /parts/ops/apps":                              {ops: []string{"listApps", "listAttention", "getHost", "listCopies", "getMetrics", "listProjects"}},
	"GET /parts/ops/activity":                          {ops: []string{"listActivity"}},
	// The console's address (feature 051): the page and its panel read getConsoleAddress (and getUpdates for the sidebar's
	// mark) and the change (getConsoleAddressChange). Check starts a change, Switch and Cancel act on it.
	"GET /ops/console-address":                      {ops: []string{"getConsoleAddress", "getUpdates"}},
	"GET /parts/ops/console-address":                {ops: []string{"getConsoleAddressChange"}},
	"POST /ops/console-address/changes":             {ops: []string{"startConsoleAddressChange"}},
	"POST /ops/console-address/changes/{id}/switch": {ops: []string{"switchConsoleAddress"}},
	"POST /ops/console-address/changes/{id}/cancel": {ops: []string{"cancelConsoleAddressChange"}},
	// The Operator Updates page (feature 027 page 3): what is waiting (getUpdates) and the needs-you items about
	// the OS (listAttention). The part is the same regions drawn again on the updates notice. The two forms start a
	// check in the background (checkUpdates, what=release or packages) and save the one setting caravana owns
	// (putUpdateSettings). Every Operator page also reads getUpdates for the sidebar's mark.
	"GET /ops/updates":           {ops: []string{"getUpdates", "listAttention"}},
	"GET /parts/ops/updates":     {ops: []string{"getUpdates", "listAttention"}},
	"POST /ops/updates/check":    {ops: []string{"checkUpdates"}},
	"POST /ops/updates/settings": {ops: []string{"putUpdateSettings"}},
	// Updating caravana from the page (feature 030). The confirmation page reads what the update changes
	// (getUpdates: the versions and the pins) and asks whether it would be accepted; its post is requestUpdate. The
	// run panel's part reads the run (getUpdates), and the main region and Past updates too once it has finished
	// (with listAttention for the banners).
	"GET /ops/updates/caravana":  {ops: []string{"getUpdates"}},
	"POST /ops/updates/caravana": {ops: []string{"requestUpdate"}},
	"GET /parts/ops/updates/run": {ops: []string{"getUpdates", "listAttention"}},
	// Operator › Security (feature 044): the add-on (listAddons), its images (listImageScans), the recent runs
	// (listScanRuns) and the settings (getScanSettings). The part is the same regions drawn again on the scans
	// notice. The confirmation page reads the add-on; its post is removeAddon. Install is installAddon, the
	// settings form putScanSettings and Scan all now requestScan with no app.
	"GET /ops/security":           {ops: []string{"listAddons", "listImageScans", "listScanRuns", "getScanSettings"}},
	"GET /parts/ops/security":     {ops: []string{"listAddons", "listImageScans", "listScanRuns"}},
	"POST /ops/security/install":  {ops: []string{"installAddon"}},
	"GET /ops/security/remove":    {ops: []string{"listAddons"}},
	"POST /ops/security/remove":   {ops: []string{"removeAddon"}},
	"POST /ops/security/settings": {ops: []string{"putScanSettings"}},
	"POST /ops/security/scan":     {ops: []string{"requestScan"}},
	// An app's Security panel (feature 044) is getAppScan; Scan now is requestScan for the app. The history's
	// one-line counts read the scans of the listed deployments' images (getDeploymentScan).
	"GET /parts/apps/{id}/security": {ops: []string{"getAppScan", "listAddons"}},
	"POST /apps/{id}/security/scan": {ops: []string{"requestScan"}},
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
		if why, only := consoleOnly[key]; only {
			if ok {
				t.Errorf("%s is in both consoleParity and consoleOnly", key)
			}
			p, ok = parity{why: why}, true
		}
		if !ok {
			t.Errorf("the console serves %s, which has no entry in consoleParity: name the API operation that does the same, or say why it needs none", key)
			continue
		}
		if len(p.ops) == 0 && strings.TrimSpace(p.why) == "" {
			t.Errorf("%s names no API operation and gives no reason", key)
		}
		// The routes in consoleOnly have no operation, and say why. Any other route that changes data
		// names one.
		changes := rt.Method != "GET"
		if _, only := consoleOnly[key]; changes && len(p.ops) == 0 && !only {
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
	for key := range consoleOnly {
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
