package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 027 page 3: getUpdates, putUpdateSettings and checkUpdates, checked
// against the contract.

// fakeReleases answers GitHub's latest release and counts the requests.
type fakeReleases struct {
	*httptest.Server
	requests atomic.Int32
}

func newFakeReleases(t *testing.T) *fakeReleases {
	t.Helper()
	f := &fakeReleases{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"v0.16.0","html_url":"https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0",` +
				`"published_at":"2026-11-02T10:00:00Z","body":"- One.\n- Two.\n","draft":false,"prerelease":false}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// proFake is a pro command that answers at once.
type proFake struct{ out string }

func (proFake) Available() bool { return true }
func (p proFake) Run(context.Context) ([]byte, error) {
	return []byte(p.out), nil
}

func ubuntuFiles(mtime time.Time) fstest.MapFS {
	return fstest.MapFS{
		"etc/os-release": {Data: []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n")},
		"var/lib/update-notifier/updates-available": {
			Data:    []byte("\n14 updates can be applied immediately.\n9 of these updates are standard security updates.\n"),
			ModTime: mtime,
		},
		"etc/apt/apt.conf.d/20auto-upgrades": {Data: []byte("APT::Periodic::Unattended-Upgrade \"1\";\n")},
		"run/reboot-required":                {Data: []byte("*** System restart required ***\n"), ModTime: mtime},
		"run/reboot-required.pkgs":           {Data: []byte("linux-image-6.8.0-149-generic\n")},
		"boot/vmlinuz-6.8.0-149-generic":     {},
	}
}

func updatesEnv(t *testing.T, version string, files fstest.MapFS, pro service.ProRunner) (*testEnv, *fakeReleases) {
	t.Helper()
	gh := newFakeReleases(t)
	e := newTestEnvWith(t, "", func(s *service.Service) {
		s.SetMetricsSampleGap(time.Hour)
		s.SetVersion(version)
		s.SetReleaseURL(gh.URL + "/repos/x/releases/latest")
		s.SetReleasePinURLs(gh.URL+"/download", gh.URL+"/raw")
		s.SetHostRoot(files)
		s.SetProRunner(pro)
	})
	return e, gh
}

func TestGetUpdatesBeforeAnyCheck(t *testing.T) {
	e, gh := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now().Add(-3*time.Hour)), proFake{})
	u := getJSON(t, e, "/v1/updates", "Updates")

	c := obj(t, u["caravana"], "caravana")
	if c["current"] != "0.15.0" || c["checkEnabled"] != true || c["checkedAt"] != nil || c["checkedOkAt"] != nil || c["checkError"] != "" {
		t.Errorf("caravana = %v", c)
	}
	for _, k := range []string{"latest", "releasedAt", "url", "pins", "pinsText", "install"} {
		if _, ok := c[k]; ok {
			t.Errorf("caravana has %q before a release is known", k)
		}
	}
	if notes := list(t, c["notes"], "notes"); len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}

	o := obj(t, u["os"], "os")
	if o["readable"] != true || obj(t, o["distro"], "distro")["id"] != "ubuntu" {
		t.Errorf("os = %v", o)
	}
	up := obj(t, o["updates"], "updates")
	if up["known"] != true || up["total"] != float64(14) || up["security"] != float64(9) || up["stale"] != false ||
		up["packagesAvailable"] != true || up["packagesCheckedAt"] != nil || len(list(t, up["packages"], "packages")) != 0 {
		t.Errorf("updates = %v", up)
	}
	// With security updates waiting the clock the needs-you item waits on is told (a time, or null before
	// the sampler has written it), never left out.
	if since, has := up["securitySince"]; !has {
		t.Error("updates has no securitySince")
	} else if s, isString := since.(string); since != nil && !isString {
		t.Errorf("securitySince = %v, want a time or null", s)
	}
	rb := obj(t, o["reboot"], "reboot")
	if rb["required"] != true || rb["installedKernel"] != "6.8.0-149-generic" || rb["since"] == nil {
		t.Errorf("reboot = %v", rb)
	}
	if un := obj(t, o["unattended"], "unattended"); un["enabled"] != true || un["autoReboot"] != nil {
		t.Errorf("unattended = %v", un)
	}
	if ch := obj(t, u["checking"], "checking"); ch["release"] != false || ch["packages"] != false {
		t.Errorf("checking = %v", ch)
	}
	if n := gh.requests.Load(); n != 0 {
		t.Errorf("reading the updates made %d requests to GitHub", n)
	}
}

func TestGetUpdatesOnAHostThatIsNotDebian(t *testing.T) {
	e, _ := updatesEnv(t, "0.15.0", fstest.MapFS{"etc/os-release": {Data: []byte("ID=fedora\nVERSION_ID=40\n")}}, nil)
	u := getJSON(t, e, "/v1/updates", "Updates")
	o := obj(t, u["os"], "os")
	up := obj(t, o["updates"], "updates")
	if o["readable"] != false || up["known"] != false || up["packagesAvailable"] != false || up["checkedAt"] != nil {
		t.Errorf("os = %v", o)
	}
	if un := obj(t, o["unattended"], "unattended"); un["enabled"] != nil || un["autoReboot"] != nil {
		t.Errorf("unattended = %v", un)
	}
}

func TestCheckUpdatesRelease(t *testing.T) {
	e, gh := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now()), nil)

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/updates/check", "application/json", `{"what":"release"}`)
	if status != http.StatusAccepted {
		t.Fatalf("check release = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "CheckUpdatesStarted")

	var u map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		u = getJSON(t, e, "/v1/updates", "Updates")
		if obj(t, u["caravana"], "caravana")["latest"] != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c := obj(t, u["caravana"], "caravana")
	if c["latest"] != "0.16.0" || c["url"] != "https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0" ||
		c["releasedAt"] != "2026-11-02T10:00:00Z" || c["checkError"] != "" || c["checkedAt"] == nil || c["checkedOkAt"] != c["checkedAt"] ||
		c["install"] != "curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/download/v0.16.0/install.sh | sudo bash" {
		t.Errorf("caravana = %v", c)
	}
	if notes := list(t, c["notes"], "notes"); len(notes) != 2 || notes[0] != "One." {
		t.Errorf("notes = %v", notes)
	}
	if n := gh.requests.Load(); n < 1 {
		t.Errorf("requests = %d", n)
	}

	// A second one within ten minutes: 429 check_too_soon, naming when.
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/updates/check", "application/json", `{"what":"release"}`)

	eb := decodeAs[errorBody](t, raw)
	if status != http.StatusTooManyRequests || eb.Error.Code != "check_too_soon" || !strings.Contains(eb.Error.Message, "can run again at") || eb.Error.Field != "what" {
		t.Errorf("a second check = %d %s", status, raw)
	}
}

func TestCheckUpdatesRefusals(t *testing.T) {
	post := func(e *testEnv, body string) (int, errorBody) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/updates/check", "application/json", body)
		return status, decodeAs[errorBody](t, raw)
	}

	t.Run("a bad what", func(t *testing.T) {
		e, _ := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now()), nil)
		for _, body := range []string{`{"what":"disk"}`, `{"what":""}`, `{}`} {
			if status, eb := post(e, body); status != http.StatusUnprocessableEntity || eb.Error.Code != "invalid_field" || eb.Error.Field != "what" {
				t.Errorf("%s = %d %+v, want 422 invalid_field what", body, status, eb)
			}
		}
		if status, eb := post(e, `{"what":"release","also":1}`); status != http.StatusBadRequest || eb.Error.Code != "unknown_field" {
			t.Errorf("an unknown field = %d %+v", status, eb)
		}
	})
	t.Run("the check is off", func(t *testing.T) {
		e, gh := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now()), nil)
		if err := e.Service.SetReleaseCheck(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if status, eb := post(e, `{"what":"release"}`); status != http.StatusConflict || eb.Error.Code != "check_disabled" {
			t.Errorf("= %d %+v, want 409 check_disabled", status, eb)
		}
		if n := gh.requests.Load(); n != 0 {
			t.Errorf("%d requests with the check off", n)
		}
	})
	t.Run("a development build", func(t *testing.T) {
		e, gh := updatesEnv(t, "dev", ubuntuFiles(time.Now()), nil)
		status, eb := post(e, `{"what":"release"}`)
		if status != http.StatusConflict || eb.Error.Code != "check_unavailable" || !strings.Contains(eb.Error.Message, "development build") {
			t.Errorf("= %d %+v, want 409 check_unavailable", status, eb)
		}
		if n := gh.requests.Load(); n != 0 {
			t.Errorf("%d requests from a development build", n)
		}
	})
	t.Run("package names without pro", func(t *testing.T) {
		e, _ := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now()), nil)
		status, eb := post(e, `{"what":"packages"}`)
		if status != http.StatusConflict || eb.Error.Code != "check_unavailable" || !strings.Contains(eb.Error.Message, "pro command") {
			t.Errorf("= %d %+v, want 409 check_unavailable naming pro", status, eb)
		}
	})
	t.Run("package names on a host that isn't Debian", func(t *testing.T) {
		e, _ := updatesEnv(t, "0.15.0", fstest.MapFS{"etc/os-release": {Data: []byte("ID=fedora\n")}}, proFake{})
		if status, eb := post(e, `{"what":"packages"}`); status != http.StatusConflict || eb.Error.Code != "check_unavailable" {
			t.Errorf("= %d %+v", status, eb)
		}
	})
}

func TestCheckUpdatesPackages(t *testing.T) {
	pro := proFake{out: `{"packages":[{"package":"openssl","service_name":"standard-security","status":"upgrade_available"}],"summary":{"num_standard_security_updates":1}}`}
	e, _ := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now()), pro)

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/updates/check", "application/json", `{"what":"packages"}`)
	if status != http.StatusAccepted {
		t.Fatalf("check packages = %d %s", status, raw)
	}
	var up map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		u := getJSON(t, e, "/v1/updates", "Updates")
		up = obj(t, obj(t, u["os"], "os")["updates"], "updates")
		if up["packagesCheckedAt"] != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	pk := list(t, up["packages"], "packages")
	if len(pk) != 1 || pk[0] != "openssl" || up["packagesKernel"] != false || up["packagesError"] != "" {
		t.Errorf("updates = %v", up)
	}
	if status, _, _ := call(t, http.MethodPost, e.URL+"/v1/updates/check", "application/json", `{"what":"packages"}`); status != http.StatusTooManyRequests {
		t.Errorf("a second package check = %d, want 429", status)
	}
}

func TestPutUpdateSettings(t *testing.T) {
	e, gh := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now()), nil)
	put := func(body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/updates/settings", "application/json", body)
		return status, raw
	}

	status, raw := put(`{"checkEnabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("put false = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "UpdatesCaravana")
	if c := decodeAs[map[string]any](t, raw); c["checkEnabled"] != false || c["current"] != "0.15.0" {
		t.Errorf("answer = %v", c)
	}
	if c := obj(t, getJSON(t, e, "/v1/updates", "Updates")["caravana"], "caravana"); c["checkEnabled"] != false {
		t.Errorf("getUpdates after = %v", c)
	}
	rc, err := e.Store.ReleaseCheck(context.Background())
	if err != nil || rc.Enabled {
		t.Errorf("stored = %+v, %v", rc, err)
	}
	if n := gh.requests.Load(); n != 0 {
		t.Errorf("saving the setting made %d requests", n)
	}

	if status, raw = put(`{"checkEnabled":true}`); status != http.StatusOK || decodeAs[map[string]any](t, raw)["checkEnabled"] != true {
		t.Errorf("put true = %d %s", status, raw)
	}

	// Every field is optional: a missing one (or a null) keeps its value.
	for _, body := range []string{`{}`, `{"checkEnabled":null}`} {
		if status, raw := put(body); status != http.StatusOK || decodeAs[map[string]any](t, raw)["checkEnabled"] != true {
			t.Errorf("%s = %d %s, want 200 with the setting kept", body, status, raw)
		}
	}
	for _, c := range []struct{ body, field string }{
		{`{"checkEnabled":"yes"}`, "checkEnabled"}, {`{"checkEnabled":1}`, "checkEnabled"},
		{`{"autoUpdate":"yes"}`, "autoUpdate"}, {`{"autoUpdateHour":"3"}`, "autoUpdateHour"},
		{`{"autoUpdateHour":24}`, "autoUpdateHour"}, {`{"autoUpdateHour":-1}`, "autoUpdateHour"}, {`{"autoUpdateHour":2.5}`, "autoUpdateHour"},
	} {
		status, raw := put(c.body)
		eb := decodeAs[errorBody](t, raw)
		if status != http.StatusUnprocessableEntity || eb.Error.Code != "invalid_field" || eb.Error.Field != c.field {
			t.Errorf("%s = %d %s, want 422 invalid_field %s", c.body, status, raw, c.field)
		}
	}
	if status, raw := put(`{"checkEnabled":true,"x":1}`); status != http.StatusBadRequest {
		t.Errorf("an unknown field = %d %s", status, raw)
	}
	// What failed left the setting as it was.
	if rc, _ := e.Store.ReleaseCheck(context.Background()); !rc.Enabled {
		t.Error("a refused body changed the setting")
	}
}

// A deploy token may call none of the five: the default deny refuses them (the
// scope walk checks every route; this names these).
func TestUpdatesAreRefusedToADeployToken(t *testing.T) {
	e, _ := updatesEnv(t, "0.15.0", ubuntuFiles(time.Now()), nil)
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/updates", ""},
		{http.MethodPut, "/v1/updates/settings", `{"checkEnabled":false}`},
		{http.MethodPost, "/v1/updates/check", `{"what":"release"}`},
		{http.MethodPost, "/v1/updates/runs", `{"version":"0.16.0"}`},
		{http.MethodGet, "/v1/updates/runs", ""},
	} {
		req, err := http.NewRequest(c.method, e.RawURL+c.path, strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		req.Header.Set("Content-Type", "application/json")
		resp, err := noRedirects.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a deploy token = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	if rc, _ := e.Store.ReleaseCheck(context.Background()); !rc.Enabled {
		t.Error("a refused call changed the setting")
	}
}

// The two OS kinds come through listAttention, in the contract's shape, once the
// sampler has read the host's files.
func TestAttentionHasTheOSItems(t *testing.T) {
	files := ubuntuFiles(time.Now())
	files["etc/apt/apt.conf.d/20auto-upgrades"] = &fstest.MapFile{Data: []byte("APT::Periodic::Unattended-Upgrade \"0\";\n")} // off: the security item at once
	e := newTestEnvWith(t, "", func(s *service.Service) {
		s.SetMetricsSampleGap(300 * time.Millisecond)
		s.SetHostRoot(files)
	})
	var att attentionBody
	deadline := time.Now().Add(20 * time.Second)
	for {
		att = getAttention(t, e)
		if len(att.Items) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	got := map[string]string{}
	for _, it := range att.Items {
		if it.AppID != nil {
			t.Errorf("item %s has an app", it.Kind)
		}
		if it.Detail != nil {
			got[it.Kind] = *it.Detail
		}
	}
	if got["os_security_updates"] != "14 updates are waiting, 9 of them security updates." {
		t.Errorf("security item = %q", got["os_security_updates"])
	}
	if !strings.HasPrefix(got["os_reboot_required"], "This server needs a reboot") {
		t.Errorf("reboot item = %q", got["os_reboot_required"])
	}
}
