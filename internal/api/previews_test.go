package api

import (
	"net/http"
	"strings"
	"testing"
)

// previewPassword is the preview password the tests search for in every answer
// and in the log.
const previewPassword = "s3cret-sentinel-049-api-password"

// previewBody is whoami with a domain of its own, for a deploy into a preview.
func previewBody(host string) string {
	return strings.Replace(whoamiBody, "whoami.localhost", host, 1)
}

func TestPreviewEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	shopProject(t, e)
	url := e.URL
	deployer := scopedToken(t, e, "shop", "web")
	as := func(auth, method, path, body string) (int, []byte) {
		status, _, raw := callWith(t, auth, method, e.RawURL+path, "application/json", body)
		return status, raw
	}

	// The deploy token opens pr-12 with the shape the contract says; Environment
	// carries the number and when it expires.
	status, raw := as(deployer, http.MethodPost, "/v1/projects/shop/environments", `{"name": "pr-12"}`)
	if status != http.StatusCreated {
		t.Fatalf("create pr-12 = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Environment")
	env := decodeAs[struct {
		Preview *struct {
			Number    int    `json:"number"`
			ExpiresAt string `json:"expiresAt"`
		} `json:"preview"`
	}](t, raw)
	if env.Preview == nil || env.Preview.Number != 12 || env.Preview.ExpiresAt == "" {
		t.Errorf("environment = %s, want preview.number 12 and an expiry", raw)
	}
	// A plain environment has no preview; the project lists both.
	status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop", "", "")
	if status != http.StatusOK {
		t.Fatalf("getProject = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Project")
	if !strings.Contains(string(raw), `"preview":{"expiresAt"`) && !strings.Contains(string(raw), `"preview":{"number":12`) {
		t.Errorf("getProject = %s, want pr-12 with its preview", raw)
	}

	// Reserved names, with a full token: 422 on name.
	for _, name := range []string{"pr-07", "pr-12-old", "pr-x"} {
		status, _, raw := call(t, http.MethodPost, url+"/v1/projects/shop/environments", "application/json", `{"name": "`+name+`"}`)
		wantError(t, "create "+name, status, raw, http.StatusUnprocessableEntity, codeInvalidField, "name")
	}

	// A preview of a project with a password: deploy with the deploy token.
	status, _, raw = call(t, http.MethodPut, url+"/v1/projects/shop/previews/settings", "application/json",
		`{"limit": 2, "expireAfterDays": 3, "password": "`+previewPassword+`"}`)
	if status != http.StatusOK {
		t.Fatalf("putPreviewSettings = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "PreviewSettingsResult")
	set := decodeAs[struct {
		Limit, ExpireAfterDays int
		PasswordSet            bool
		Warnings               []struct{ Field, Message string }
	}](t, raw)
	if set.Limit != 2 || set.ExpireAfterDays != 3 || !set.PasswordSet || len(set.Warnings) != 1 ||
		!strings.Contains(set.Warnings[0].Message, "open previews pr-12 get the password at their next deploy") {
		t.Errorf("settings = %s, want limit 2, 3 days, a password and the first-time warning", raw)
	}
	if strings.Contains(string(raw), previewPassword) {
		t.Error("the settings answer holds the password")
	}

	status, raw = as(deployer, http.MethodPost, "/v1/projects/shop/environments/pr-12/apps?deploy=false", shopBody("web"))
	if status != http.StatusCreated {
		t.Fatalf("create the app = %d %s", status, raw)
	}
	status, raw = as(deployer, http.MethodPost, "/v1/apps/shop--pr-12--web/deployments", previewBody("pr-12.shop.example.test"))
	if status != http.StatusAccepted {
		t.Fatalf("deploy into pr-12 = %d %s", status, raw)
	}
	d := decodeAs[deployment](t, raw)
	if strings.Contains(string(raw), `"teamPasswords"`) {
		t.Errorf("the deploy answer = %s, want no teamPasswords for a preview domain", raw)
	}
	waitHealthy(t, e, "shop--pr-12--web", d.ID)
	_, _, raw = call(t, http.MethodGet, url+"/v1/apps/shop--pr-12--web/deployments/"+d.ID, "", "")
	if !strings.Contains(string(raw), "domains of preview environments are Team only with project shop's preview password") ||
		!strings.Contains(string(raw), "services[0].domains[0].teamOnly") {
		t.Errorf("deployment = %s, want the Team-only warning on its field", raw)
	}

	// The limit: a third preview with the limit at 2 (pr-12 is one).
	if status, _ := as(deployer, http.MethodPost, "/v1/projects/shop/environments", `{"name": "pr-13"}`); status != http.StatusCreated {
		t.Fatalf("create pr-13 = %d", status)
	}
	status, raw = as(deployer, http.MethodPost, "/v1/projects/shop/environments", `{"name": "pr-14"}`)
	body := wantError(t, "a third preview", status, raw, http.StatusConflict, codePreviewLimit, "name")
	if !strings.Contains(body.Error.Message, "project shop has 2 open previews (pr-12, pr-13), its limit") {
		t.Errorf("message = %q, want the open ones named", body.Error.Message)
	}

	// getPreviews: settings, the open previews by number, the app with its address,
	// memory, the deploy token's name and no password anywhere.
	status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop/previews", "", "")
	if status != http.StatusOK {
		t.Fatalf("getPreviews = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "PreviewList")
	list := decodeAs[struct {
		Project  string
		Settings struct {
			Limit, ExpireAfterDays int
			PasswordSet            bool
		}
		Previews []struct {
			Environment string
			Number      int
			Apps        []struct {
				ID, Name, State string
				Addresses       []string
				MemoryMB        int
				LastDeployment  *struct {
					ID, Status, TokenName string
				}
			}
		}
	}](t, raw)
	if list.Project != "shop" || list.Settings.Limit != 2 || !list.Settings.PasswordSet || len(list.Previews) != 2 ||
		list.Previews[0].Environment != "pr-12" || list.Previews[1].Environment != "pr-13" {
		t.Fatalf("previews = %s", raw)
	}
	if apps := list.Previews[0].Apps; len(apps) != 1 || apps[0].ID != "shop--pr-12--web" || apps[0].Name != "web" ||
		len(apps[0].Addresses) != 1 || apps[0].Addresses[0] != "pr-12.shop.example.test" || apps[0].MemoryMB == 0 ||
		apps[0].LastDeployment == nil || apps[0].LastDeployment.ID != d.ID || apps[0].LastDeployment.TokenName != "shop-web-ci" {
		t.Errorf("pr-12 apps = %+v", apps)
	}
	if len(list.Previews[1].Apps) != 0 {
		t.Errorf("pr-13 apps = %+v, want none", list.Previews[1].Apps)
	}
	if status, _, raw := call(t, http.MethodGet, url+"/v1/projects/nope/previews", "", ""); status != http.StatusNotFound {
		wantError(t, "getPreviews of an unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
	}

	// A preview domain's password is the project's: rotating it is refused.
	status, _, raw = call(t, http.MethodPost, url+"/v1/apps/shop--pr-12--web/domains/pr-12.shop.example.test/team-password", "", "")
	wantError(t, "rotateTeamPassword", status, raw, http.StatusConflict, codePreviewPassword, "host")

	// Settings: refusals name their field; removal is refused while previews are open.
	for what, body := range map[string]struct{ body, field string }{
		"limit over 20":     {`{"limit": 21}`, "limit"},
		"days over 30":      {`{"expireAfterDays": 31}`, "expireAfterDays"},
		"short password":    {`{"password": "short"}`, "password"},
		"both":              {`{"password": "` + previewPassword + `", "removePassword": true}`, "removePassword"},
		"limit not integer": {`{"limit": "many"}`, ""},
	} {
		status, _, raw := call(t, http.MethodPut, url+"/v1/projects/shop/previews/settings", "application/json", body.body)
		if body.field == "" {
			if status != http.StatusBadRequest {
				t.Errorf("%s = %d %s, want 400", what, status, raw)
			}
			continue
		}
		wantError(t, what, status, raw, http.StatusUnprocessableEntity, codeInvalidField, body.field)
		if strings.Contains(string(raw), previewPassword) {
			t.Errorf("%s: the error holds the password", what)
		}
	}
	status, _, raw = call(t, http.MethodPut, url+"/v1/projects/shop/previews/settings", "application/json", `{"removePassword": true}`)
	removal := wantError(t, "removePassword with previews open", status, raw, http.StatusConflict, codePreviewsOpen, "removePassword")
	if !strings.Contains(removal.Error.Message, "pr-12, pr-13") {
		t.Errorf("message = %q, want the open previews named", removal.Error.Message)
	}
	status, _, raw = call(t, http.MethodPut, url+"/v1/projects/nope/previews/settings", "application/json", `{}`)
	wantError(t, "settings of an unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")

	// closePreview: a full token, 204; staging isn't a preview; unknown places are 404.
	status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/shop/previews/staging", "", "")
	wantError(t, "closePreview(staging)", status, raw, http.StatusConflict, codeNotAPreview, "environment")
	status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/shop/previews/pr-99", "", "")
	wantError(t, "closePreview(pr-99)", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "environment")
	status, _, raw = call(t, http.MethodDelete, url+"/v1/projects/nope/previews/pr-12", "", "")
	wantError(t, "closePreview in an unknown project", status, raw, http.StatusNotFound, codeProjectNotFound, "project")
	if status, _, raw := call(t, http.MethodDelete, url+"/v1/projects/shop/previews/pr-12", "", ""); status != http.StatusNoContent {
		t.Fatalf("closePreview(pr-12) = %d %s", status, raw)
	}
	if status, _, _ := call(t, http.MethodGet, url+"/v1/apps/shop--pr-12--web", "", ""); status != http.StatusNotFound {
		t.Errorf("the app after the close = %d, want 404", status)
	}
	if e.Nomad.Job("shop--pr-12--web") != nil {
		t.Error("the app's Nomad job is still there")
	}
	status, _, raw = call(t, http.MethodGet, url+"/v1/projects/shop/previews", "", "")
	if strings.Contains(string(raw), `"pr-12"`) || status != http.StatusOK {
		t.Errorf("getPreviews after the close = %d %s", status, raw)
	}

	// With no preview left but pr-13 (empty) closed, the password can go.
	if status, _, raw := call(t, http.MethodDelete, url+"/v1/projects/shop/previews/pr-13", "", ""); status != http.StatusNoContent {
		t.Fatalf("closePreview(pr-13) = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodPut, url+"/v1/projects/shop/previews/settings", "application/json", `{"removePassword": true}`)
	if status != http.StatusOK || !strings.Contains(string(raw), `"passwordSet":false`) {
		t.Errorf("removePassword with none open = %d %s", status, raw)
	}

	if strings.Contains(e.Logs.String(), previewPassword) {
		t.Error("the log holds the preview password")
	}
}
