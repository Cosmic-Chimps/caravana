package main

// The projects-promote gate step (feature 019, quickstart.md "Lab" 1), and the
// helpers it needs. It is in its own file only because gate_test.go is long;
// runGate lists it with the other steps.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// gatePlaceDeployment is a deployment as the API answers it, with where a
// promotion came from.
type gatePlaceDeployment struct {
	gateDeployment
	Source       string `json:"source"`
	PromotedFrom *struct {
		App         string `json:"app"`
		Environment string `json:"environment"`
		Deployment  string `json:"deployment"`
	} `json:"promotedFrom"`
}

// extraHost is a host name for an app of the projects steps, next to the
// gate's own: label.localhost in the lab, and label under the app host's
// domain on a real host.
func (g *gate) extraHost(label string) string {
	if g.target.Public == nil {
		return label + ".localhost"
	}
	_, parent, _ := strings.Cut(g.target.AppHost, ".")
	return label + "." + parent
}

// hostStatus calls https://host/ the way a browser would reach the node:
// through the lab's port 8443 on 127.0.0.1, or the host's public IP on 443,
// with host as the server name, so Traefik routes it by name even where DNS
// has no record for it. It doesn't verify the certificate (the lab's is
// self-signed and a new name may have none yet): the https-trusted step is the
// one that does. It returns the status and the start of the body.
func (g *gate) hostStatus(host string) (int, string, error) {
	addr, url := "127.0.0.1:8443", "https://"+host+":8443/"
	if g.target.Public != nil {
		addr, url = net.JoinHostPort(g.target.Public.IP, "443"), "https://"+host+"/"
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: host}, // routing only; see above
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(raw), nil
}

// waitHostAnswers waits for https://host/ to answer 200.
func (g *gate) waitHostAnswers(t *testing.T, host string) {
	t.Helper()
	var status int
	var lastErr error
	if !waitUntil(time.Minute, func() bool {
		status, _, lastErr = g.hostStatus(host)
		return lastErr == nil && status == http.StatusOK
	}) {
		t.Fatalf("https://%s didn't answer 200 within a minute (status %d, error %v)", host, status, lastErr)
	}
}

// send calls the API with the gate's token and returns the status and the
// body. headers are name, value pairs; a body is sent as JSON.
func (g *gate) send(t *testing.T, method, path, body string, headers ...string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, g.target.API+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+g.target.APIToken)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := g.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// sendWant is send that fails the step unless the status is want.
func (g *gate) sendWant(t *testing.T, want int, method, path, body string, headers ...string) []byte {
	t.Helper()
	status, raw := g.send(t, method, path, body, headers...)
	if status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
	}
	return raw
}

// refusal checks that the call is refused with the status, error code and
// field (the field is not checked when empty). It returns the error's message.
func (g *gate) refusal(t *testing.T, status int, code, field, method, path, body string, headers ...string) string {
	t.Helper()
	got, raw := g.send(t, method, path, body, headers...)
	var e struct {
		Error struct{ Code, Message, Field string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if got != status || e.Error.Code != code || (field != "" && e.Error.Field != field) {
		t.Errorf("%s %s = %d %s (field %q), want %d %s (field %q): %s", method, path, got, e.Error.Code, e.Error.Field, status, code, field, raw)
	}
	return e.Error.Message
}

// deploymentOf reads one deployment of an app.
func (g *gate) deploymentOf(t *testing.T, appID, id string) gatePlaceDeployment {
	t.Helper()
	var d gatePlaceDeployment
	g.getJSON(t, "/v1/apps/"+appID+"/deployments/"+id, &d)
	return d
}

// labWeb is the web service of the projects steps: whoami with FOO in its env,
// the secret LAB_SECRET (its value is kept when secret is empty), and a domain
// when host is not empty.
func labWeb(image, foo, secret, host string) map[string]any {
	s := map[string]any{
		"name":   "web",
		"image":  image,
		"env":    []map[string]string{{"name": "FOO", "value": foo}},
		"ports":  []map[string]any{{"name": "http", "containerPort": 80}},
		"health": map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		// Small reservations: two environments of web and worker, plus a
		// blue/green canary, must fit beside the lab's other jobs (1.9 GiB).
		"resources": labResources,
	}
	sec := map[string]any{"name": "LAB_SECRET"}
	if secret != "" {
		sec["value"] = secret
	}
	s["secrets"] = []map[string]any{sec}
	if host != "" {
		s["domains"] = []map[string]any{{"host": host, "port": "http"}}
	}
	return s
}

// labWorker is a service that only sleeps, with no port and no domain.
func labWorker(name string) map[string]any {
	return map[string]any{"name": name, "image": "busybox:1.37", "command": "sh", "args": []string{"-c", "exec sleep 86400"},
		"resources": labResources}
}

// labResources is what each service of this step reserves (see labWeb).
var labResources = map[string]int{"memoryMB": 16, "memoryMaxMB": 32}

// labApp is the body of deployApp for these services.
func labApp(services ...map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"source": map[string]string{"kind": "image"}, "services": services})
	return string(raw)
}

// taskEnv is the env of a task of the job Nomad runs.
func (g *gate) taskEnv(t *testing.T, jobID, task string) map[string]string {
	t.Helper()
	job, _, err := g.target.Nomad.Jobs().Info(jobID, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", jobID, err)
	}
	for _, tg := range job.TaskGroups {
		for _, tk := range tg.Tasks {
			if tk.Name == task {
				return tk.Env
			}
		}
	}
	t.Fatalf("job %s has no task %s", jobID, task)
	return nil
}

// projectsPromoteStep (feature 019): a project with a staging and a production
// environment, an app in each, and a promotion between them, with the token
// Caravana really runs with. The app IDs place the apps (lab019--staging--web),
// so every call is the one a client such as Aspire makes. In order:
//
//  1. the project and its environments are made, and a bad name or a place that
//     doesn't exist is refused;
//  2. staging deploys (whoami v1.10, FOO=staging, a secret, a domain) and answers;
//  3. copyApp makes production without deploying: no domains, the secret's name
//     listed as missing, a second copy refused;
//  4. production deploys with its own FOO, secret value and domain (without the
//     value it is refused);
//  5. staging deploys v1.11; previewPromotion lists the image change; a stale
//     If-Match is 412; promoteApp deploys production with staging's digest and
//     keeps its FOO, secret value and domain, and the deployment says
//     promotedFrom;
//  6. a service added in staging arrives in production without a domain, and a
//     service only production has refuses the promotion (409);
//  7. the project and the environment can't be deleted while apps are in them;
//     then the apps, the environment and the project are deleted, leaving no
//     Nomad job or variable.
func (g *gate) projectsPromoteStep(t *testing.T) {
	const project = "lab019"
	stagingID, prodID := project+"--staging--web", project+"--production--web"
	stagingHost, prodHost := g.extraHost(project+"-staging"), g.extraHost(project)
	stagingSecret, prodSecret := "staging-secret-"+g.run, "production-secret-"+g.run

	// remove deletes the step's apps and project, ignoring what isn't there.
	remove := func() {
		for _, path := range []string{"/v1/apps/" + stagingID, "/v1/apps/" + prodID, "/v1/projects/" + project} {
			resp, err := g.apiRequest(http.MethodDelete, path, "", "")
			if err != nil {
				t.Logf("cleanup: DELETE %s: %v", path, err)
				continue
			}
			resp.Body.Close()
		}
	}
	remove() // leftovers from an aborted run, if any
	t.Cleanup(remove)

	deploy := func(id, body string) gatePlaceDeployment {
		t.Helper()
		raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
		var d gatePlaceDeployment
		if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
			t.Fatalf("deploy %s: %s", id, raw)
		}
		if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
		return g.deploymentOf(t, id, d.ID) // with the digests, which the 202 answer doesn't have yet
	}

	// 1. The project and its environments.
	raw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"lab019","displayName":"Lab 019"}`)
	var created struct {
		Name         string
		DisplayName  string
		Environments []struct{ Name string }
	}
	if json.Unmarshal(raw, &created) != nil || created.Name != project || len(created.Environments) != 1 || created.Environments[0].Name != "production" {
		t.Fatalf("createProject = %s, want the project with its production environment", raw)
	}
	g.refusal(t, http.StatusConflict, "project_exists", "name", http.MethodPost, "/v1/projects", `{"name":"lab019"}`)
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "name", http.MethodPost, "/v1/projects", `{"name":"lab--019"}`)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/lab019/environments", `{"name":"staging"}`)
	raw = g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/projects/lab019", "")
	created.Environments = nil
	if json.Unmarshal(raw, &created) != nil || len(created.Environments) != 2 || created.Environments[0].Name != "production" || created.Environments[1].Name != "staging" {
		t.Fatalf("getProject = %s, want production then staging", raw)
	}
	// An ID that names a place that doesn't exist creates nothing.
	g.refusal(t, http.StatusNotFound, "environment_not_found", "id", http.MethodPost, "/v1/apps/lab019--nowhere--web/deployments", labApp(labWorker("web")))
	g.refusal(t, http.StatusNotFound, "project_not_found", "", http.MethodPost, "/v1/apps/nope019--staging--web/deployments", labApp(labWorker("web")))

	// 2. Staging deploys, and its ID placed it.
	stagingV1 := deploy(stagingID, labApp(labWeb("traefik/whoami:v1.10", "staging", stagingSecret, stagingHost)))
	var rec struct{ Project, Environment, AppName string }
	g.getJSON(t, "/v1/apps/"+stagingID, &rec)
	if rec.Project != project || rec.Environment != "staging" || rec.AppName != "web" {
		t.Errorf("GET /v1/apps/%s says it is in %+v, want lab019 / staging / web", stagingID, rec)
	}
	g.waitHostAnswers(t, stagingHost)

	// 3. Copy to production: stored, not deployed.
	raw = g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/apps/"+stagingID+"/copy", `{"to":"production"}`)
	if strings.Contains(string(raw), stagingSecret) {
		t.Errorf("copyApp's answer holds a secret value: %s", raw)
	}
	var copied struct {
		Environment, AppName string
		App                  struct {
			ID       string
			Services []struct {
				Name    string
				Domains []any
			}
		}
		MissingSecrets []struct{ Service, Name string }
	}
	if err := json.Unmarshal(raw, &copied); err != nil || copied.App.ID != prodID || copied.Environment != "production" || copied.AppName != "web" ||
		len(copied.App.Services) != 1 || len(copied.App.Services[0].Domains) != 0 ||
		len(copied.MissingSecrets) != 1 || copied.MissingSecrets[0].Service != "web" || copied.MissingSecrets[0].Name != "LAB_SECRET" {
		t.Fatalf("copyApp = %s (%v), want %s without domains, missing web/LAB_SECRET", raw, err, prodID)
	}
	if _, _, err := g.target.Nomad.Jobs().Info(prodID, nil); err == nil {
		t.Errorf("copyApp registered a Nomad job for %s; a copy is not deployed", prodID)
	}
	g.refusal(t, http.StatusConflict, "app_exists", "to", http.MethodPost, "/v1/apps/"+stagingID+"/copy", `{"to":"production"}`)
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "to", http.MethodPost, "/v1/apps/"+stagingID+"/copy", `{"to":"staging"}`)

	// 4. Production deploys with its own env, secret and domain.
	// A deploy that keeps LAB_SECRET's value refuses: the copy has none to keep.
	if status, raw := g.send(t, http.MethodPost, "/v1/apps/"+prodID+"/deployments",
		labApp(labWeb("traefik/whoami:v1.10", "production", "", prodHost))); status/100 != 4 || !strings.Contains(string(raw), "LAB_SECRET") {
		t.Errorf("deploying the copy before its secret has a value = %d %s, want a 4xx naming LAB_SECRET", status, raw)
	}
	prodV1 := deploy(prodID, labApp(labWeb("traefik/whoami:v1.10", "production", prodSecret, prodHost)))
	g.waitHostAnswers(t, prodHost)
	if prodV1.Digests["web"] != stagingV1.Digests["web"] {
		t.Errorf("v1.10 in production is %s, in staging %s: one tag gave two digests", prodV1.Digests["web"], stagingV1.Digests["web"])
	}

	// 5. Staging moves on, and production follows by promotion.
	stagingV2 := deploy(stagingID, labApp(labWeb("traefik/whoami:v1.11", "staging", "", stagingHost)))
	if stagingV2.Digests["web"] == stagingV1.Digests["web"] {
		t.Fatalf("v1.11 and v1.10 are one digest (%s): the step has nothing to promote", stagingV2.Digests["web"])
	}
	raw = g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+stagingID+"/promote/preview", `{"to":"production"}`)
	var preview struct {
		To, FromDeployment, TargetRevision string
		Services                           []struct{ Name, ImageFrom, ImageTo, DigestFrom, DigestTo string }
		Added                              []string
	}
	if err := json.Unmarshal(raw, &preview); err != nil || preview.To != prodID || preview.FromDeployment != stagingV2.ID ||
		preview.TargetRevision == "" || len(preview.Services) != 1 || len(preview.Added) != 0 ||
		preview.Services[0].ImageFrom != "traefik/whoami:v1.10" || preview.Services[0].ImageTo != "traefik/whoami:v1.11" ||
		preview.Services[0].DigestFrom != stagingV1.Digests["web"] || preview.Services[0].DigestTo != stagingV2.Digests["web"] {
		t.Fatalf("previewPromotion = %s (%v), want the v1.10 → v1.11 change of web from %s", raw, err, stagingV2.ID)
	}
	g.refusal(t, http.StatusPreconditionFailed, "app_changed", "", http.MethodPost, "/v1/apps/"+stagingID+"/promote", `{"to":"production"}`,
		"If-Match", `"0000000000000000"`)

	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+stagingID+"/promote", `{"to":"production"}`,
		"If-Match", `"`+preview.TargetRevision+`"`)
	var promoted gatePlaceDeployment
	if err := json.Unmarshal(raw, &promoted); err != nil || promoted.ID == "" || promoted.Source != "" || promoted.PromotedFrom == nil ||
		promoted.PromotedFrom.App != stagingID || promoted.PromotedFrom.Environment != "staging" || promoted.PromotedFrom.Deployment != stagingV2.ID {
		t.Fatalf("promoteApp = %s (%v), want a deployment promoted from %s's %s", raw, err, stagingID, stagingV2.ID)
	}
	if final := g.waitFinalOf(t, prodID, promoted.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the promotion ended %s: %s", final.Status, final.Error)
	}
	if live := g.deploymentOf(t, prodID, promoted.ID); live.Digests["web"] != stagingV2.Digests["web"] {
		t.Errorf("production runs %s after the promotion, want staging's %s", live.Digests["web"], stagingV2.Digests["web"])
	}
	job, _, err := g.target.Nomad.Jobs().Info(prodID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if image := job.TaskGroups[0].Tasks[0].Config["image"]; image != stagingV2.Digests["web"] || job.Meta["caravana.deployment"] != promoted.ID {
		t.Errorf("Nomad runs %v for deployment %s, want %s for %s", image, job.Meta["caravana.deployment"], stagingV2.Digests["web"], promoted.ID)
	}
	if env := g.taskEnv(t, prodID, "web"); env["FOO"] != "production" {
		t.Errorf("production's FOO = %q after the promotion, want production (env is the target's own)", env["FOO"])
	}
	if v, _, err := g.target.Nomad.Variables().Read("nomad/jobs/"+prodID+"/web", nil); err != nil || v.Items["LAB_SECRET"] != prodSecret {
		t.Errorf("production's secret after the promotion = %v, %v; want its own value", v, err)
	}
	var stored struct {
		App struct {
			Services []struct {
				Domains []struct{ Host string }
			}
		}
	}
	g.getJSON(t, "/v1/apps/"+prodID, &stored)
	if len(stored.App.Services) != 1 || len(stored.App.Services[0].Domains) != 1 || stored.App.Services[0].Domains[0].Host != prodHost {
		t.Errorf("production's stored app = %+v, want its own domain %s", stored.App, prodHost)
	}
	g.waitHostAnswers(t, prodHost) // the new version answers on production's own domain
	g.waitHostAnswers(t, stagingHost)
	var history struct {
		Deployments []gatePlaceDeployment `json:"deployments"`
	}
	g.getJSON(t, "/v1/apps/"+prodID+"/deployments?limit=10", &history)
	if len(history.Deployments) == 0 || history.Deployments[0].ID != promoted.ID || history.Deployments[0].PromotedFrom == nil {
		t.Errorf("production's history = %+v, want the promotion newest, with promotedFrom", history.Deployments)
	}

	// 6. A service added in staging arrives whole, without domains.
	deploy(stagingID, labApp(labWeb("traefik/whoami:v1.11", "staging", "", stagingHost), labWorker("worker")))
	raw = g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/apps/"+stagingID+"/promote/preview", `{"to":"production"}`)
	preview.Added, preview.Services = nil, nil
	if err := json.Unmarshal(raw, &preview); err != nil || len(preview.Added) != 1 || preview.Added[0] != "worker" || len(preview.Services) != 2 {
		t.Fatalf("previewPromotion with a new service = %s (%v), want worker added", raw, err)
	}
	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+stagingID+"/promote", `{"to":"production"}`)
	promoted = gatePlaceDeployment{}
	if err := json.Unmarshal(raw, &promoted); err != nil || promoted.PromotedFrom == nil {
		t.Fatalf("promoteApp = %s (%v)", raw, err)
	}
	if final := g.waitFinalOf(t, prodID, promoted.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the promotion that adds worker ended %s: %s", final.Status, final.Error)
	}
	var withWorker struct {
		App struct {
			Source   map[string]any   `json:"source"`
			Services []map[string]any `json:"services"`
		} `json:"app"`
	}
	g.getJSON(t, "/v1/apps/"+prodID, &withWorker)
	names, workerDomains := []string{}, 0
	for _, s := range withWorker.App.Services {
		names = append(names, fmt.Sprint(s["name"]))
		if d, _ := s["domains"].([]any); s["name"] == "worker" {
			workerDomains = len(d)
		}
	}
	if strings.Join(names, ",") != "web,worker" || workerDomains != 0 {
		t.Errorf("production's services = %v (worker domains %d), want web and worker without domains", names, workerDomains)
	}
	g.waitHostAnswers(t, prodHost)

	// A service only production has: the promotion refuses and names it.
	withExtra, _ := json.Marshal(map[string]any{
		"source":   withWorker.App.Source,
		"services": append(withWorker.App.Services, labWorker("extra")),
	})
	deploy(prodID, string(withExtra))
	g.refusal(t, http.StatusConflict, "promotion_removes_service", "services.extra", http.MethodPost, "/v1/apps/"+stagingID+"/promote/preview", `{"to":"production"}`)
	g.refusal(t, http.StatusConflict, "promotion_removes_service", "services.extra", http.MethodPost, "/v1/apps/"+stagingID+"/promote", `{"to":"production"}`)

	// 7. Nothing is deleted from under an app; then everything is.
	if msg := g.refusal(t, http.StatusConflict, "not_empty", "", http.MethodDelete, "/v1/projects/lab019", ""); !strings.Contains(msg, stagingID) || !strings.Contains(msg, prodID) {
		t.Errorf("deleteProject's refusal %q should name both apps", msg)
	}
	g.refusal(t, http.StatusConflict, "not_empty", "", http.MethodDelete, "/v1/projects/lab019/environments/staging", "")
	g.refusal(t, http.StatusConflict, "protected", "", http.MethodDelete, "/v1/projects/default", "")

	for _, id := range []string{stagingID, prodID} {
		g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
		if _, _, err := g.target.Nomad.Jobs().Info(id, nil); err == nil {
			t.Errorf("the Nomad job %s is still there after the app was deleted", id)
		}
		if left, _, err := g.target.Nomad.Variables().PrefixList("nomad/jobs/"+id+"/", nil); err != nil || len(left) != 0 {
			t.Errorf("variables left after deleting %s: %d (%v)", id, len(left), err)
		}
	}
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/projects/lab019/environments/staging", "")
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/projects/lab019", "")
	g.refusal(t, http.StatusNotFound, "project_not_found", "project", http.MethodGet, "/v1/projects/lab019", "")
}
