package mcpserver

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// wantRequest is one request a tool must make.
type wantRequest struct {
	method string
	path   string
	query  string            // as encoded: keys sorted
	body   string            // JSON, compared by value; "" for none
	header map[string]string // headers that must have exactly this value
}

// toolCase is one tool called once against the fake API.
type toolCase struct {
	tool   string
	args   map[string]any
	routes map[string]string // "METHOD /path" to the JSON the API answers with
	want   []wantRequest
	// answer is what the tool's text must equal; "" means the answer of the
	// last route that was called is not checked here.
	answer string
}

const (
	deploymentJSON = `{"id":"d-2","appId":"web","status":"running"}`
	healthJSON     = `{"status":"ok","version":"0.9.1"}`
	appRecordJSON  = `{"app":{"id":"web","source":{"kind":"image"},"services":[{"name":"web","image":"nginx:1"}]},` +
		`"revision":"0123456789abcdef","defined":true}`
	previewJSON = `{"from":"shop--staging--web","to":"shop--production--web","targetRevision":"fedcba9876543210"}`
)

// readCases is every read tool, and actionCases every action tool.
var readCases = []toolCase{
	{tool: "list_projects", routes: map[string]string{"GET /v1/projects": `{"projects":[]}`},
		want: []wantRequest{{method: "GET", path: "/v1/projects"}}, answer: `{"projects":[]}`},
	{tool: "get_project", args: map[string]any{"project": "shop"},
		routes: map[string]string{"GET /v1/projects/shop": `{"name":"shop"}`},
		want:   []wantRequest{{method: "GET", path: "/v1/projects/shop"}}, answer: `{"name":"shop"}`},
	{tool: "list_apps", routes: map[string]string{"GET /v1/apps": `{"apps":[]}`},
		want: []wantRequest{{method: "GET", path: "/v1/apps"}}, answer: `{"apps":[]}`},
	{tool: "get_app", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web": appRecordJSON},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web"}}, answer: appRecordJSON},
	{tool: "list_recent_deployments", routes: map[string]string{"GET /v1/deployments": `{"deployments":[]}`},
		want: []wantRequest{{method: "GET", path: "/v1/deployments"}}, answer: `{"deployments":[]}`},
	{tool: "list_deployments", args: map[string]any{"app": "shop--staging--web"},
		routes: map[string]string{"GET /v1/apps/shop--staging--web/deployments": `{"deployments":[]}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/shop--staging--web/deployments"}}, answer: `{"deployments":[]}`},
	{tool: "get_deployment", args: map[string]any{"app": "web", "deployment": "d-1"},
		routes: map[string]string{"GET /v1/apps/web/deployments/d-1": deploymentJSON},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/deployments/d-1"}}, answer: deploymentJSON},
	{tool: "list_copies", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web/copies": `{"copies":[]}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/copies", query: "usage=true"}}, answer: `{"copies":[]}`},
	{tool: "list_copies", args: map[string]any{"app": "web", "usage": false},
		routes: map[string]string{"GET /v1/apps/web/copies": `{"copies":[]}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/copies", query: "usage=false"}}, answer: `{"copies":[]}`},
	{tool: "get_logs", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web/logs": `{"lines":[]}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/logs", query: "lines=200"}}, answer: `{"lines":[]}`},
	{tool: "get_logs", args: map[string]any{"app": "web", "copy": "c-1", "stream": "stderr", "lines": 50},
		routes: map[string]string{"GET /v1/apps/web/logs": `{"lines":[]}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/logs", query: "copy=c-1&lines=50&stream=stderr"}}, answer: `{"lines":[]}`},
	{tool: "list_volumes", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web/volumes": `{"volumes":[]}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/volumes"}}, answer: `{"volumes":[]}`},
	{tool: "list_backups", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web/backups": `{"backups":[]}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/backups"}}, answer: `{"backups":[]}`},
	{tool: "list_app_actions", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web/actions": `[]`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/actions"}}, answer: `[]`},
	{tool: "get_needs_you", routes: map[string]string{"GET /v1/attention": `{"items":[]}`},
		want: []wantRequest{{method: "GET", path: "/v1/attention"}}, answer: `{"items":[]}`},
	{tool: "preview_promotion", args: map[string]any{"app": "shop--staging--web", "to": "production"},
		routes: map[string]string{"POST /v1/apps/shop--staging--web/promote/preview": previewJSON},
		want: []wantRequest{{method: "POST", path: "/v1/apps/shop--staging--web/promote/preview",
			body: `{"to":"production"}`}}, answer: previewJSON},
	{tool: "export_jobspec", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web/jobspec": `job "web" {}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/jobspec"}}, answer: `job "web" {}`},
	{tool: "export_jobspec", args: map[string]any{"app": "web", "deployment": "d-1"},
		routes: map[string]string{"GET /v1/apps/web/deployments/d-1/jobspec": `job "web" {}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/deployments/d-1/jobspec"}}, answer: `job "web" {}`},
	{tool: "get_compose_file", args: map[string]any{"app": "web"},
		routes: map[string]string{"GET /v1/apps/web/compose": `{"compose":"x"}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/compose"}}, answer: `{"compose":"x"}`},
	{tool: "get_compose_file", args: map[string]any{"app": "web", "deployment": "d-1"},
		routes: map[string]string{"GET /v1/apps/web/deployments/d-1/compose": `{"compose":"x"}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/deployments/d-1/compose"}}, answer: `{"compose":"x"}`},
	{tool: "wait_for_deployment", args: map[string]any{"app": "web", "deployment": "d-1"},
		routes: map[string]string{"GET /v1/apps/web/deployments/d-1": `{"id":"d-1","status":"healthy"}`},
		want:   []wantRequest{{method: "GET", path: "/v1/apps/web/deployments/d-1"}}},
}

var actionCases = []toolCase{
	{tool: "redeploy_app", args: map[string]any{"app": "web"},
		routes: map[string]string{"POST /v1/apps/web/deployments": deploymentJSON},
		want:   []wantRequest{{method: "POST", path: "/v1/apps/web/deployments"}}, answer: deploymentJSON},
	{tool: "deploy_image", args: map[string]any{"app": "web", "image": "nginx:2"},
		routes: map[string]string{"GET /v1/apps/web": appRecordJSON, "POST /v1/apps/web/deployments": deploymentJSON},
		want: []wantRequest{
			{method: "GET", path: "/v1/apps/web"},
			{method: "POST", path: "/v1/apps/web/deployments",
				body:   `{"id":"web","source":{"kind":"image"},"services":[{"name":"web","image":"nginx:2"}]}`,
				header: map[string]string{"If-Match": `"0123456789abcdef"`}},
		}, answer: deploymentJSON},
	{tool: "redeploy_version", args: map[string]any{"app": "web", "deployment": "d-1"},
		routes: map[string]string{"POST /v1/apps/web/deployments/d-1/redeploy": deploymentJSON},
		want:   []wantRequest{{method: "POST", path: "/v1/apps/web/deployments/d-1/redeploy"}}, answer: deploymentJSON},
	{tool: "promote_app", args: map[string]any{"app": "shop--staging--web", "to": "production"},
		routes: map[string]string{
			"POST /v1/apps/shop--staging--web/promote/preview": previewJSON,
			"POST /v1/apps/shop--staging--web/promote":         deploymentJSON,
		},
		want: []wantRequest{
			{method: "POST", path: "/v1/apps/shop--staging--web/promote/preview", body: `{"to":"production"}`},
			{method: "POST", path: "/v1/apps/shop--staging--web/promote", body: `{"to":"production"}`,
				header: map[string]string{"If-Match": `"fedcba9876543210"`}},
		}, answer: deploymentJSON},
	{tool: "restart_app", args: map[string]any{"app": "web"},
		routes: map[string]string{"POST /v1/apps/web/restart": `{"id":"a-1","action":"restart"}`},
		want:   []wantRequest{{method: "POST", path: "/v1/apps/web/restart"}}, answer: `{"id":"a-1","action":"restart"}`},
	{tool: "stop_app", args: map[string]any{"app": "web"},
		routes: map[string]string{"POST /v1/apps/web/stop": `{"app":{}}`},
		want:   []wantRequest{{method: "POST", path: "/v1/apps/web/stop"}}, answer: `{"app":{}}`},
	{tool: "start_app", args: map[string]any{"app": "web"},
		routes: map[string]string{"POST /v1/apps/web/start": `{"app":{}}`},
		want:   []wantRequest{{method: "POST", path: "/v1/apps/web/start"}}, answer: `{"app":{}}`},
	{tool: "back_up_now", args: map[string]any{"app": "web", "service": "db"},
		routes: map[string]string{"POST /v1/apps/web/backups": `{"id":"b-1"}`},
		want:   []wantRequest{{method: "POST", path: "/v1/apps/web/backups", body: `{"service":"db"}`}}, answer: `{"id":"b-1"}`},
}

// serve installs a case's routes on the fake: GET answers 200, POST 202.
func (c toolCase) serve(f *fakeAPI) {
	for route, body := range c.routes {
		status := http.StatusOK
		if strings.HasPrefix(route, "POST ") {
			status = http.StatusAccepted
		}
		f.reply(route, status, body)
	}
}

// checkRequests compares what the fake received with what the case wants, and
// that every request carries what every request must: the token as a Bearer,
// JSON accepted, the user agent, and no origin header.
func checkRequests(t *testing.T, got []recorded, want []wantRequest) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("made %d requests, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Method != w.method || g.Path != w.path || g.Query != w.query {
			t.Errorf("request %d = %s %s?%s, want %s %s?%s", i, g.Method, g.Path, g.Query, w.method, w.path, w.query)
		}
		switch {
		case w.body == "" && g.Body != "":
			t.Errorf("request %d has a body, want none: %s", i, g.Body)
		case w.body != "" && !jsonEqual(t, g.Body, w.body):
			t.Errorf("request %d body = %s, want %s", i, g.Body, w.body)
		}
		for k, v := range w.header {
			if g.Header.Get(k) != v {
				t.Errorf("request %d header %s = %q, want %q", i, k, g.Header.Get(k), v)
			}
		}
		checkCommonHeaders(t, i, g)
	}
}

func checkCommonHeaders(t *testing.T, i int, g recorded) {
	t.Helper()
	if got := g.Header.Get("Authorization"); got != "Bearer "+sentinelToken {
		t.Errorf("request %d Authorization is not the Bearer token", i)
	}
	if got := g.Header.Get("Accept"); got != "application/json" {
		t.Errorf("request %d Accept = %q", i, got)
	}
	if got := g.Header.Get("User-Agent"); got != "caravana-mcp/9.9.9" {
		t.Errorf("request %d User-Agent = %q", i, got)
	}
	for k := range g.Header {
		if strings.HasPrefix(strings.ToLower(k), "caravana-origin") {
			t.Errorf("request %d sends %s: an agent's deploy is not from CI", i, k)
		}
	}
	if strings.Contains(g.Path+g.Query, sentinelToken) {
		t.Errorf("request %d has the token in its URL", i)
	}
	if g.Body != "" && g.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request %d has a body without Content-Type application/json", i)
	}
}

// TestToolRequests runs every tool once and checks the requests it makes and
// the answer it gives.
func TestToolRequests(t *testing.T) {
	for _, c := range slices.Concat(readCases, actionCases) {
		t.Run(c.tool+"/"+firstArgs(c.args), func(t *testing.T) {
			f := newFakeAPI(t)
			c.serve(f)
			cs := f.session(t, true, nil, fastTuning, nil)

			got := mustCall(t, cs, c.tool, c.args)
			if c.answer != "" && got != c.answer {
				t.Errorf("answer = %q, want the API's body %q", got, c.answer)
			}
			checkRequests(t, f.requests(), c.want)
		})
	}
}

// firstArgs names a case's subtest by its arguments.
func firstArgs(args map[string]any) string {
	var parts []string
	for k, v := range args {
		if s, ok := v.(string); ok {
			parts = append(parts, k+"="+s)
		} else {
			parts = append(parts, k)
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

func TestGetVersionAddsTheBinaryVersion(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("GET /v1/health", 200, healthJSON)
	cs := f.session(t, false, nil, fastTuning, nil)

	got := mustCall(t, cs, "get_version", nil)
	if !jsonEqual(t, got, `{"status":"ok","version":"0.9.1","mcp":"9.9.9"}`) {
		t.Errorf("answer = %s", got)
	}
}

// TestEveryToolHasACase fails when a tool is offered that the tables above
// don't cover, or the tables name one that isn't offered.
func TestEveryToolHasACase(t *testing.T) {
	f := newFakeAPI(t)
	cs := f.session(t, true, nil, fastTuning, nil)
	offered := toolNames(t, cs)

	covered := map[string]bool{"get_version": true} // has its own test above
	for _, c := range slices.Concat(readCases, actionCases) {
		covered[c.tool] = true
	}
	for name := range covered {
		if !slices.Contains(offered, name) {
			t.Errorf("a case calls %s, which is not offered", name)
		}
	}
	for _, name := range offered {
		if !covered[name] {
			t.Errorf("tool %s has no case", name)
		}
	}
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

var readToolNames = []string{
	"export_jobspec", "get_app", "get_compose_file", "get_deployment", "get_logs", "get_needs_you",
	"get_project", "get_version", "list_app_actions", "list_apps", "list_backups", "list_copies",
	"list_deployments", "list_projects", "list_recent_deployments", "list_volumes",
	"preview_promotion", "wait_for_deployment",
}

var actionToolNames = []string{
	"back_up_now", "deploy_image", "promote_app", "redeploy_app", "redeploy_version",
	"restart_app", "start_app", "stop_app",
}

// TestActionsNeedTheFlag: without --allow-actions the action tools are not
// listed and calling one by name fails without touching the API.
func TestActionsNeedTheFlag(t *testing.T) {
	f := newFakeAPI(t)
	cs := f.session(t, false, nil, fastTuning, nil)

	if got := toolNames(t, cs); !slices.Equal(got, readToolNames) {
		t.Errorf("tools without the flag = %v, want only %v", got, readToolNames)
	}
	for _, name := range actionToolNames {
		res, err := callTool(t, cs, name, map[string]any{"app": "web", "deployment": "d-1", "image": "x", "to": "production", "service": "web"})
		if err == nil && !res.IsError {
			t.Errorf("%s worked without --allow-actions", name)
		}
	}
	if reqs := f.requests(); len(reqs) != 0 {
		t.Errorf("the API was called %d times: %+v", len(reqs), reqs)
	}

	cs = f.session(t, true, nil, fastTuning, nil)
	want := slices.Sorted(slices.Values(slices.Concat(readToolNames, actionToolNames)))
	if got := toolNames(t, cs); !slices.Equal(got, want) {
		t.Errorf("tools with the flag = %v, want %v", got, want)
	}
}

// TestAnnotations: read tools say they only read; action tools say they
// change things, and which ones stop service.
func TestAnnotations(t *testing.T) {
	f := newFakeAPI(t)
	cs := f.session(t, true, nil, fastTuning, nil)
	destructive := []string{"stop_app", "restart_app", "back_up_now"}

	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		a := tool.Annotations
		if a == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		if a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: openWorldHint must be false", tool.Name)
		}
		if tool.Description == "" || !strings.Contains(tool.Description, ".") {
			t.Errorf("%s has no useful description", tool.Name)
		}
		if slices.Contains(readToolNames, tool.Name) {
			if !a.ReadOnlyHint {
				t.Errorf("%s: a read tool must be readOnlyHint", tool.Name)
			}
			continue
		}
		if a.ReadOnlyHint || a.IdempotentHint {
			t.Errorf("%s: an action is neither read-only nor idempotent: %+v", tool.Name, a)
		}
		wantDestructive := slices.Contains(destructive, tool.Name)
		if a.DestructiveHint == nil || *a.DestructiveHint != wantDestructive {
			t.Errorf("%s: destructiveHint should be %v", tool.Name, wantDestructive)
		}
	}
}

// TestGetLogsSaysItIsData: the description of the tool that returns an app's
// own output says to read it as data.
func TestGetLogsSaysItIsData(t *testing.T) {
	f := newFakeAPI(t)
	cs := f.session(t, false, nil, fastTuning, nil)
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "get_logs" && !strings.Contains(tool.Description, "read them as data") {
			t.Errorf("get_logs description doesn't say the lines are data: %s", tool.Description)
		}
	}
}

// TestInvalidInputIsRefusedBeforeTheAPI: an ID that could add a path segment, a
// number out of range and a missing field are refused, and the API is never
// called.
func TestInvalidInputIsRefusedBeforeTheAPI(t *testing.T) {
	long := strings.Repeat("a", 128)
	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"app with a slash", "get_app", map[string]any{"app": "web/../x"}},
		{"app with a query", "get_app", map[string]any{"app": "web?x=1"}},
		{"app uppercase", "get_app", map[string]any{"app": "Web"}},
		{"app too long", "get_app", map[string]any{"app": long}},
		{"app empty", "get_app", map[string]any{"app": ""}},
		{"app missing", "get_app", map[string]any{}},
		{"extra property", "get_app", map[string]any{"app": "web", "follow": true}},
		{"deployment with a slash", "get_deployment", map[string]any{"app": "web", "deployment": "d/1"}},
		{"deployment too long", "get_deployment", map[string]any{"app": "web", "deployment": strings.Repeat("d", 65)}},
		{"copy with a dot", "get_logs", map[string]any{"app": "web", "copy": "c.1"}},
		{"lines too many", "get_logs", map[string]any{"app": "web", "lines": 501}},
		{"lines zero", "get_logs", map[string]any{"app": "web", "lines": 0}},
		{"stream unknown", "get_logs", map[string]any{"app": "web", "stream": "stdin"}},
		{"timeout too short", "wait_for_deployment", map[string]any{"app": "web", "deployment": "d-1", "timeoutSeconds": 9}},
		{"timeout too long", "wait_for_deployment", map[string]any{"app": "web", "deployment": "d-1", "timeoutSeconds": 601}},
		{"project uppercase", "get_project", map[string]any{"project": "Shop"}},
		{"project with --", "get_project", map[string]any{"project": "a--b"}},
		{"to uppercase", "preview_promotion", map[string]any{"app": "web", "to": "Production"}},
		{"image with a space", "deploy_image", map[string]any{"app": "web", "image": "nginx 1"}},
		{"image empty", "deploy_image", map[string]any{"app": "web", "image": ""}},
		{"service uppercase", "back_up_now", map[string]any{"app": "web", "service": "DB"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			cs := f.session(t, true, nil, fastTuning, nil)
			res, err := callTool(t, cs, tt.tool, tt.args)
			if err == nil && !res.IsError {
				t.Errorf("accepted: %s", text(res))
			}
			if reqs := f.requests(); len(reqs) != 0 {
				t.Errorf("the API was called: %+v", reqs)
			}
		})
	}
}

// TestHandlerChecksAreTheSecondDefence: the validate methods refuse what the
// schema would, in case a schema is ever loosened.
func TestHandlerChecksAreTheSecondDefence(t *testing.T) {
	bad := []validator{
		appInput{App: "a/b"},
		projectInput{Project: "A"},
		deploymentInput{App: "a", Deployment: "x/y"},
		waitInput{App: "a", Deployment: "d", TimeoutSeconds: 5},
		logsInput{App: "a", Lines: 600},
		logsInput{App: "a", Stream: "x"},
		logsInput{App: "a", Copy: "a b"},
		promotionInput{App: "a", To: "A"},
		optionalDeploymentInput{App: "a", Deployment: "a/b"},
		deployImageInput{App: "a", Image: "a b"},
		deployImageInput{App: "a", Image: "x", Service: "A"},
		backupInput{App: "a", Service: "a/b"},
	}
	for i, v := range bad {
		if err := v.validate(); err == nil {
			t.Errorf("case %d (%#v) was accepted", i, v)
		}
	}
	good := []validator{
		appInput{App: "shop--staging--web"},
		logsInput{App: "a", Lines: 500, Stream: "both", Copy: "abc-123"},
		waitInput{App: "a", Deployment: "d"},
		deployImageInput{App: "a", Image: "ghcr.io/o/r:tag", Service: "web"},
	}
	for i, v := range good {
		if err := v.validate(); err != nil {
			t.Errorf("good case %d: %v", i, err)
		}
	}
}
