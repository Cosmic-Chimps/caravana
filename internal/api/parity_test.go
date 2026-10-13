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
type parity struct {
	ops []string
	why string
}

// consoleParity is SC-006: everything the console reads or changes can also be
// done with an API token through /v1. Keys are "METHOD path" as internal/ui
// serves them. A page that reads several things lists each operation.
var consoleParity = map[string]parity{
	// Pages with nothing of their own to read or change.
	"GET /{$}":       {why: "a redirect to /apps"},
	"GET /login":     {why: "the sign-in form; clients use API tokens"},
	"POST /login":    {why: "starts a browser session; clients send an API token"},
	"POST /logout":   {why: "ends a browser session; an API token has none"},
	"GET /apps/new":  {why: "a blank form; creating the app is POST /apps"},
	"GET /account":   {why: "a redirect to /settings"},
	"GET /static/":   {why: "the console's own stylesheet, scripts and fonts"},
	"GET /live":      {ops: []string{"listApps", "listRecentDeployments", "listAttention"}}, // notices that these changed
	"GET /settings":  {ops: []string{"listTokens"}},
	"GET /apps":      {ops: []string{"listApps", "listAttention"}},
	"GET /apps/{id}": {ops: []string{"getApp", "listCopies"}},

	"GET /apps/{id}/deployments": {ops: []string{"getApp", "listDeployments"}},
	"GET /apps/{id}/configure":   {ops: []string{"getApp", "getWebhook"}},
	"GET /apps/{id}/logs":        {ops: []string{"getLogs"}},
	"GET /live/apps/{id}":        {ops: []string{"getApp", "listCopies", "listDeployments", "getLogs"}},

	"GET /parts/apps":                  {ops: []string{"listApps"}},
	"GET /parts/activity":              {ops: []string{"listRecentDeployments"}},
	"GET /parts/attention":             {ops: []string{"listAttention"}},
	"GET /parts/apps/{id}/header":      {ops: []string{"getApp"}},
	"GET /parts/apps/{id}/copies":      {ops: []string{"listCopies"}},
	"GET /parts/apps/{id}/deployments": {ops: []string{"listDeployments"}},

	// What the console changes.
	"POST /apps": {ops: []string{"createApp"}},
	// The Compose forms (feature 014): the preview saves nothing; the create is a Compose deploy with If-None-Match: *.
	"POST /apps/new/compose/preview": {ops: []string{"previewCompose"}},
	"POST /apps/new/compose":         {ops: []string{"deployCompose"}},
	"POST /apps/{id}/deploy":         {ops: []string{"deployApp"}},
	// The edit form (feature 014): getApp gives the revision, deployApp with If-Match saves the change.
	"POST /apps/{id}/configure": {ops: []string{"getApp", "deployApp"}},
	// Past versions (feature 011).
	"POST /apps/{id}/deployments/{deploymentId}/redeploy": {ops: []string{"redeployDeployment"}},
	"GET /apps/{id}/deployments/{deploymentId}/jobspec":   {ops: []string{"exportDeployment"}},
	"GET /apps/{id}/jobspec":                              {ops: []string{"exportApp"}},
	"POST /apps/{id}/webhook":                             {ops: []string{"createWebhookToken"}},
	"POST /apps/{id}/webhook/revoke":                      {ops: []string{"deleteWebhookToken"}},
	// Setting or removing one secret is a deploy of the app with its secrets
	// listed, the others without a value (keep).
	"POST /apps/{id}/secrets":        {ops: []string{"deployApp"}},
	"POST /apps/{id}/secrets/remove": {ops: []string{"deployApp"}},
	// Backups (feature 010).
	"GET /apps/{id}/backups":                     {ops: []string{"getApp", "listBackups", "getBackupDestination"}},
	"POST /apps/{id}/backups":                    {ops: []string{"createBackup"}},
	"POST /apps/{id}/backups/{backupId}/restore": {ops: []string{"restoreBackup"}},
	"POST /settings/backups":                     {ops: []string{"setBackupDestination"}},
	"POST /apps/{id}/delete":                     {ops: []string{"deleteApp"}},
	"POST /settings/password":                    {ops: []string{"changePassword"}},
	"POST /settings/tokens":                      {ops: []string{"createToken"}},
	"POST /settings/tokens/{tokenId}/revoke":     {ops: []string{"revokeToken"}},
}

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
			case !changes && method != "GET":
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
