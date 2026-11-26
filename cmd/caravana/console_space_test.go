package main

// The console-space step of the lab test (feature 045, the console's Space section): a volume that a
// redeploy dropped from its app is listed on the server's page as one no app declares, and deleting it takes
// the name typed. It runs against the lab's real Nomad. The lab's node is not managed (no cluster agent), so
// the step also reads what the section says without an agent: the images are "—" and the clean-up is
// refused. The agent's side (a real clean-up that frees images) is deploy/dev/install-test.sh's
// check_space_host, which has a host with an agent. runConsole lists the step after console-servers.

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// consoleSpaceCompose is the app of the step: busybox with two volumes. Its command writes a marker into
// each once, and serves the one called keep, so the health check (GET /v) and the call at the end read it.
// %s is the list of volumes it mounts.
const consoleSpaceCompose = `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "[ -f /keep/v ] || printf K > /keep/v; %s exec httpd -f -p 8080 -h /keep"]
    ports: ["8080"]
    mem_reservation: 64m
    volumes: [%s]
volumes:
  keep: {}%s
`

// spaceText is htmlText with the space a tag leaves before a mark of punctuation taken out: a name in its own
// element ("Delete <span>x</span>?") reads "Delete x?".
func spaceText(page string) string {
	return strings.NewReplacer(" ?", "?", " .", ".", " ,", ",", " :", ":").Replace(htmlText(page))
}

func consoleSpaceFile(withScratch bool) string {
	if withScratch {
		return fmt.Sprintf(consoleSpaceCompose, "[ -f /scratch/v ] || printf S > /scratch/v;", `"keep:/keep", "scratch:/scratch"`, "\n  scratch: {}")
	}
	return fmt.Sprintf(consoleSpaceCompose, "", `"keep:/keep"`, "")
}

// consoleSpace: through the console's pages, the API and Nomad.
//
//  1. An app with two volumes (keep, scratch) is made from the Compose form with a domain, and is healthy.
//     Its volumes are not orphans: the server's page lists none of them, and the delete page of one is a
//     404 and its post is refused with "declares the volume".
//  2. The same file without scratch is deployed from Configure. The deployment says, as a warning, that the
//     data of "scratch" stays on the server.
//  3. The server's page has the Space section: the images, Docker's volumes and the cache are "—" with the
//     sentence about the missing agent, there is no Clean up now (and its page and post refuse with that
//     sentence), and cspace_scratch is a volume no app declares: "from app cspace", "not measured yet", with
//     Delete….
//  4. The delete page asks for the name. A wrong name is 422 at the field and deletes nothing; the right
//     name is 303 to the server's page with ?deleted=, Nomad no longer has the volume, the page says it was
//     deleted and no longer lists it, and the activity feed has "Deleted the volume cspace_scratch".
//  5. The app still serves what is in keep.
//  6. The setting is saved from the form and read back from the API; the step puts it back.
func (c *console) consoleSpace(t *testing.T) {
	const id, host = "cspace", "cspace.localhost"
	const orphan = id + "_scratch"
	remove := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(remove)
	t.Cleanup(func() { // the setting, as the step found it
		if resp, err := c.g.apiRequest(http.MethodPut, "/v1/cleanup/settings", `{"automatic":true,"keepDeployments":3}`, "application/json"); err == nil {
			resp.Body.Close()
		}
	})
	volumesOf := func() []string {
		t.Helper()
		stubs, _, err := c.g.target.Nomad.HostVolumes().List(&api.HostVolumeListRequest{}, &api.QueryOptions{Prefix: id + "_"})
		if err != nil {
			t.Fatalf("list Nomad's host volumes: %v", err)
		}
		var names []string
		for _, s := range stubs {
			names = append(names, s.Name)
		}
		return names
	}

	var cluster struct {
		Servers []struct {
			Name string `json:"name"`
		} `json:"servers"`
	}
	c.g.getJSON(t, "/v1/servers", &cluster)
	if len(cluster.Servers) != 1 {
		t.Fatalf("GET /v1/servers has %d servers, want the lab's one", len(cluster.Servers))
	}
	server := cluster.Servers[0].Name
	serverPage := "/ops/servers/" + url.PathEscape(server)
	deletePage := func(volume string) string { return serverPage + "/volumes/" + volume + "/delete" }

	// 1. The app, with both volumes.
	resp, preview := c.post(t, "/apps/new/compose/preview", url.Values{"name": {id}, "compose": {consoleSpaceFile(true)}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(preview, "Create and deploy") {
		t.Fatalf("POST the preview = %d, want 200 with the create button: %s", resp.StatusCode, preview)
	}
	form := formValuesOf(t, preview, "compose-form")
	form.Set("svc.web.domains", host)
	form.Set("svc.web.healthPath", "/v")
	resp, body := c.post(t, "/apps/new/compose", form)
	c.deployedFrom(t, id, resp, body)
	if got := volumesOf(); len(got) != 2 {
		t.Fatalf("Nomad's volumes of %s = %v, want %s_keep and %s_scratch", id, got, id, id)
	}

	_, page := c.get(t, serverPage)
	if strings.Contains(page, id+"_keep") || strings.Contains(page, orphan) {
		t.Errorf("the server's page lists a volume the app declares:\n%s", spaceText(page))
	}
	if resp, _ := c.get(t, deletePage(id+"_keep")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET the delete page of a declared volume = %d, want 404", resp.StatusCode)
	}
	resp, body = c.post(t, deletePage(id+"_keep"), url.Values{"confirm": {id + "_keep"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(spaceText(body), "declares the volume "+id+"_keep") {
		t.Errorf("POST the delete of a declared volume = %d, want 409 saying the app declares it:\n%s", resp.StatusCode, spaceText(body))
	}
	if got := volumesOf(); len(got) != 2 {
		t.Fatalf("a refused delete changed Nomad's volumes: %v", got)
	}

	// 2. Redeploy without scratch.
	_, page = c.get(t, "/apps/"+id+"/configure/source")
	kept := formValuesOf(t, page, "compose-form")
	kept.Set("compose", consoleSpaceFile(false))
	resp, preview = c.post(t, "/apps/"+id+"/compose/preview", kept)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the preview of the file without scratch = %d: %s", resp.StatusCode, preview)
	}
	resp, body = c.post(t, "/apps/"+id+"/compose", formValuesOf(t, preview, "compose-form"))
	second := c.deployedFrom(t, id, resp, body)
	var dep struct {
		Warnings []struct {
			Message string `json:"message"`
		} `json:"warnings"`
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/deployments/"+second, &dep)
	found := false
	for _, w := range dep.Warnings {
		if strings.Contains(w.Message, `volume "scratch" is no longer mounted: its data stays on server `+server) {
			found = true
		}
	}
	if !found {
		t.Errorf("the deployment's warnings = %+v, want one that says the data of scratch stays on server %s", dep.Warnings, server)
	}

	// 3. The Space section.
	resp, page = c.get(t, serverPage)
	words := spaceText(page)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d:\n%s", serverPage, resp.StatusCode, words)
	}
	for _, want := range []string{"Space", "Unused images —", "Docker's leftover volumes —", "Build cache —",
		"This server has no agent reporting its images: run the installer on it.",
		"Volumes no app declares", orphan, "from app " + id, "not measured yet", "Delete…"} {
		if !strings.Contains(words, want) {
			t.Errorf("the Space section lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "Clean up now") || strings.Contains(page, `href="`+serverPage+`/clean"`) {
		t.Errorf("a server with no agent offers Clean up now")
	}
	if !strings.Contains(page, `href="`+deletePage(orphan)+`"`) || !strings.Contains(page, `id="space"`) {
		t.Errorf("the section lacks the link to the delete page or its anchor")
	}
	resp, body = c.get(t, serverPage+"/clean")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(spaceText(body), "has no agent reporting its images") {
		t.Errorf("GET the clean-up page of a server with no agent = %d, want 409 with the sentence:\n%s", resp.StatusCode, spaceText(body))
	}
	resp, body = c.post(t, serverPage+"/clean", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(spaceText(body), "has no agent reporting its images") {
		t.Errorf("POST the clean-up of a server with no agent = %d, want 409 with the sentence:\n%s", resp.StatusCode, spaceText(body))
	}
	if resp, part := c.get(t, "/parts"+serverPage+"/space"); resp.StatusCode != http.StatusOK || !strings.Contains(part, orphan) {
		t.Errorf("GET the Space part = %d, want 200 listing %s", resp.StatusCode, orphan)
	}

	// 4. Delete it: the name is asked for.
	resp, page = c.get(t, deletePage(orphan))
	words = spaceText(page)
	if resp.StatusCode != http.StatusOK || !strings.Contains(words, "Delete "+orphan+"?") || !strings.Contains(words, "Type "+orphan+" to confirm") {
		t.Fatalf("GET the delete page = %d, want 200 asking for the name:\n%s", resp.StatusCode, words)
	}
	resp, body = c.post(t, deletePage(orphan), url.Values{"confirm": {"scratch"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(spaceText(body), "Type "+orphan+" to confirm.") || !strings.Contains(body, `aria-invalid="true"`) {
		t.Errorf("POST a wrong name = %d, want 422 at the field:\n%s", resp.StatusCode, spaceText(body))
	}
	if got := volumesOf(); len(got) != 2 {
		t.Fatalf("a wrong name changed Nomad's volumes: %v", got)
	}
	resp, body = c.post(t, deletePage(orphan), url.Values{"confirm": {orphan}})
	if want := serverPage + "?deleted=" + orphan + "#space"; resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
		t.Fatalf("POST the right name = %d → %q, want 303 to %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), want, spaceText(body))
	}
	waitFor(t, orphan+" to be gone from Nomad", 30*time.Second, func() bool { return len(volumesOf()) == 1 })
	if got := volumesOf(); got[0] != id+"_keep" {
		t.Errorf("Nomad's volumes after the delete = %v, want only %s_keep", got, id)
	}
	_, page = c.get(t, serverPage+"?deleted="+orphan)
	if words = spaceText(page); !strings.Contains(words, "Deleted the volume "+orphan+".") || strings.Contains(words, "Volumes no app declares") {
		t.Errorf("the page after the delete:\n%s", words)
	}
	re := regexp.MustCompile(`Deleted the volume ` + orphan)
	waitFor(t, "the activity feed to have the deletion", 10*time.Second, func() bool { return c.opsHasEntry(t, re) })

	// 5. The other volume's data is still served.
	c.g.waitDomainStatus(t, http.StatusOK, 30*time.Second, host, http.MethodGet, "/v", "", "")
	if _, _, got, err := c.g.domainCall(host, http.MethodGet, "/v", "", ""); err != nil || got != "K" {
		t.Errorf("GET https://%s/v = %q, %v, want the K written into keep", host, got, err)
	}

	// 6. The setting.
	_, page = c.get(t, serverPage)
	settings := formValuesOf(t, page, "srv-space-settings")
	if settings.Get("server") != server {
		t.Fatalf("the settings form = %v, want it to name the server %s", settings, server)
	}
	settings.Set("keep", "4") // a select, which formValuesOf doesn't read
	settings.Del("automatic") // an unchecked box sends nothing
	resp, body = c.post(t, "/ops/cleanup/settings", settings)
	if want := serverPage + "?saved=1#space"; resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
		t.Fatalf("POST the settings = %d → %q, want 303 to %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), want, spaceText(body))
	}
	var saved struct {
		Automatic       bool `json:"automatic"`
		KeepDeployments int  `json:"keepDeployments"`
	}
	c.g.getJSON(t, "/v1/cleanup/settings", &saved)
	if saved.Automatic || saved.KeepDeployments != 4 {
		t.Errorf("GET /v1/cleanup/settings = %+v, want the daily run off and 4 deployments", saved)
	}
	_, page = c.get(t, serverPage+"?saved=1")
	if !strings.Contains(spaceText(page), "Saved.") || !strings.Contains(page, `<option value="4" selected>4</option>`) || strings.Contains(page, `name="automatic" value="1" checked`) {
		t.Errorf("the page after saving doesn't show the setting:\n%s", spaceText(page))
	}
}
