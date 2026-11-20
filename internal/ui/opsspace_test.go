package ui

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 045, the console: the Space section of the server page, the clean-up page, the page that deletes a
// volume no app declares, the setting, the Overview's line and the app's Volumes panel link.

const (
	spaceInUse  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	spaceOld1   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	spaceOld2   = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	orphanBytes = int64(2_300_000_000)
)

// spaceCluster is the three servers with a space report from fsn-1's agent: three images (one in use, two that
// nothing needs), 154 leftover Docker volumes and the orphan volume shop_uploads on fsn-1's node, measured.
// It returns the environment and fsn-1's node token.
func spaceCluster(t *testing.T) (*consoleEnv, string) {
	t.Helper()
	e := serversEnv(t)
	_, ans := e.readyServer(t, "fsn-1")
	e.readyServer(t, "fsn-2")
	e.sample(t, "node-1", 480, 144, 1958, 60, 4000, 2, 6000, 19000)
	e.sample(t, "node-2", 900, 600, 1958, 300, 4000, 2, 8000, 19000)
	e.sample(t, "node-3", 100, 0, 1958, 20, 4000, 2, 3000, 19000)
	hostPath := e.Nomad.AddHostVolumeOn("shop_uploads", "node-2")
	e.syncSpace(t, ans.NodeToken, &service.NodeSpace{
		Images: []service.NodeImage{
			{ID: "sha256:a", Digests: []string{spaceInUse}, Tags: []string{"traefik:v3"}, Bytes: 200_000_000, InUse: true},
			{ID: "sha256:b", Digests: []string{spaceOld1}, Tags: []string{"web:1"}, Bytes: 1_000_000_000},
			{ID: "sha256:c", Digests: []string{spaceOld2}, Tags: []string{"web:2"}, Bytes: 146_000_000},
		},
		DockerVolumes: service.NodeCount{Count: 154, Bytes: 91_050_000},
		Volumes:       []service.NodeVolumeSize{{ID: path.Base(hostPath), Bytes: orphanBytes}},
	})
	return e, ans.NodeToken
}

// syncSpace has the server's agent report, healthy, with the space block.
func (e *consoleEnv) syncSpace(t *testing.T, token string, sp *service.NodeSpace) {
	t.Helper()
	r := healthy(1)
	r.Space = sp
	if _, err := e.Svc.NodeSync(context.Background(), token, r); err != nil {
		t.Fatalf("NodeSync with a space report: %v", err)
	}
}

func TestSpaceSection(t *testing.T) {
	e, _ := spaceCluster(t)
	resp, body := e.get(t, "/ops/servers/fsn-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	words := text(body)
	for _, want := range []string{"Space · 3.3 GB reclaimable", "Unused images 2 · 1.1 GB of 3 images", "Docker's leftover volumes 154 · 87 MB",
		"Build cache none", "Volumes no app declares", "shop_uploads", "its app was deleted · 2.1 GB", "Delete…", "Clean up now…",
		"Keep the images of the last", "deployments of each app", "Clean up every day at 04:00 UTC", "Nothing deletes it for you"} {
		if !strings.Contains(words, want) {
			t.Errorf("the Space section lacks %q:\n%s", want, words)
		}
	}
	for _, want := range []string{`id="space"`, `href="/ops/servers/fsn-1/clean"`, `href="/ops/servers/fsn-1/volumes/shop_uploads/delete"`,
		`id="srv-space-settings"`, `hx-preserve="true"`, `<option value="3" selected>3</option>`, `name="automatic" value="1" checked`,
		`hx-get="/parts/ops/servers/fsn-1/space"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The section sits after the numbers panel and before Running here.
	if n, sp, run := strings.Index(body, `class="cv-panel cv-srvnums"`), strings.Index(body, `id="space"`), strings.Index(body, `id="srv-running-title"`); !(n < sp && sp < run) {
		t.Errorf("the section isn't between the numbers and Running here (%d, %d, %d)", n, sp, run)
	}
	// The script isn't needed: every action is a link or a form.
	formWith(t, body, "/ops/cleanup/settings")
}

// The part answers the section alone, and the router tells it from the join's part.
func TestSpacePart(t *testing.T) {
	e, _ := spaceCluster(t)
	resp, part := e.get(t, "/parts/ops/servers/fsn-1/space")
	if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") || !strings.Contains(part, `id="space"`) || !strings.Contains(text(part), "shop_uploads") {
		t.Errorf("the part = %d:\n%s", resp.StatusCode, part)
	}
	if resp, _ := e.get(t, "/parts/ops/servers/nobody/space"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown server's part = %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/parts/ops/servers/fsn-1/other"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another sub-path = %d, want 404", resp.StatusCode)
	}
	// The join's part keeps its address.
	j, _ := e.join(t, "fsn-9")
	if resp, _ := e.get(t, "/parts/ops/servers/joins/"+j.Server.ID); resp.StatusCode != http.StatusOK {
		t.Errorf("the join's part = %d, want 200", resp.StatusCode)
	}
}

// A server whose agent hasn't told what Docker holds says so, and offers no clean-up.
func TestSpaceSectionWithoutAnInventory(t *testing.T) {
	e := threeServers(t)
	_, body := e.get(t, "/ops/servers/fsn-1")
	words := text(body)
	for _, want := range []string{"Unused images —", "Docker's leftover volumes —", "Build cache —", "The agent hasn't reported its images yet."} {
		if !strings.Contains(words, want) {
			t.Errorf("the section lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "Clean up now") || strings.Contains(words, "reclaimable") {
		t.Errorf("a server with no inventory offers a clean-up or a total:\n%s", words)
	}
	// The settings are still there.
	formWith(t, body, "/ops/cleanup/settings")
	// And so is the page of a clean-up, as a refusal.
	resp, page := e.get(t, "/ops/servers/fsn-1/clean")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "The agent hasn't reported its images yet.") || strings.Contains(page, `action="/ops/servers/fsn-1/clean"`) {
		t.Errorf("clean page = %d:\n%s", resp.StatusCode, text(page))
	}
}

// A server with no agent at all (an installation from before it, or Nomad's own node) says to run the installer.
func TestCleanPageForAServerWithoutAnAgent(t *testing.T) {
	e := serversEnv(t) // the first server is adopted and has never reported
	resp, page := e.get(t, "/ops/servers/hel-1/clean")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "Server hel-1 has no agent reporting its images: run the installer on it.") {
		t.Errorf("clean page = %d:\n%s", resp.StatusCode, text(page))
	}
	resp, page = e.postForm(t, "/ops/servers/hel-1/clean", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "has no agent reporting its images") {
		t.Errorf("POST = %d:\n%s", resp.StatusCode, text(page))
	}
	if resp, _ := e.get(t, "/ops/servers/nobody/clean"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown server = %d, want 404", resp.StatusCode)
	}
}

func TestCleanPageAndPost(t *testing.T) {
	e, _ := spaceCluster(t)
	resp, page := e.get(t, "/ops/servers/fsn-1/clean")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(page))
	}
	words := text(page)
	for _, want := range []string{"Clean up fsn-1?", "What is removed", "web:1 954 MB", "web:2 139 MB", "2 images, 1.1 GB in all.",
		"What is kept", "The images of the last 3 healthy deployments of each app",
		"Also remove Docker's leftover volumes (154, 87 MB)", "Clean up fsn-1", "Cancel"} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "traefik:v3") {
		t.Error("an image in use is listed as removable")
	}
	f := formWith(t, page, "/ops/servers/fsn-1/clean")

	resp, _ = e.submit(t, f, url.Values{"dockerVolumes": {"1"}})
	loc := location(t, resp)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/ops/servers/fsn-1?cleaning=") || !strings.HasSuffix(loc, "#space") {
		t.Fatalf("POST = %d to %q", resp.StatusCode, loc)
	}
	list, err := e.Svc.ServerCleanups(context.Background(), "fsn-1")
	if err != nil || len(list) != 1 || !list[0].DockerVolumes || list[0].Trigger != store.CleanupByOwner {
		t.Fatalf("cleanups = %+v, %v", list, err)
	}

	// The page the redirect names says it is going, and has no way to start another.
	_, body := e.get(t, strings.TrimSuffix(loc, "#space"))
	words = text(body)
	if !strings.Contains(words, "Cleaning up. The server's agent picks it up within a minute") || !strings.Contains(words, "Cleaning up… waiting for the server's agent to pick it up") ||
		strings.Contains(words, "Clean up now") {
		t.Errorf("the page after the post:\n%s", words)
	}
	// A stale address says nothing it can't back up.
	_, body = e.get(t, "/ops/servers/fsn-1?cleaning=0123456789abcdef0123456789abcdef")
	if strings.Contains(text(body), "Cleaning up. The server's agent") {
		t.Error("an address for a clean-up that isn't going draws the notice")
	}

	// A second one is refused, and the confirmation is drawn again with the reason.
	resp, page = e.postForm(t, "/ops/servers/fsn-1/clean", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "A clean-up of server fsn-1 is already under way.") || strings.Contains(page, `action="/ops/servers/fsn-1/clean"`) {
		t.Errorf("second POST = %d:\n%s", resp.StatusCode, text(page))
	}
	resp, page = e.get(t, "/ops/servers/fsn-1/clean")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "already under way") {
		t.Errorf("GET while one goes = %d:\n%s", resp.StatusCode, text(page))
	}
}

func TestDeleteVolumePageAndPost(t *testing.T) {
	e, _ := spaceCluster(t)
	delURL := "/ops/servers/fsn-1/volumes/shop_uploads/delete"
	resp, page := e.get(t, delURL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(page))
	}
	words := text(page)
	for _, want := range []string{"Delete shop_uploads?", "shop_uploads is on fsn-1, and no app declares it any more.", "2.1 GB · its app was deleted",
		"removes its data from the server for good. No backup destination is set, so nothing keeps a copy.", "Type shop_uploads to confirm", "Delete volume", "Cancel"} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(page, `name="backUp"`) {
		t.Error("the back-up box is drawn without a backup destination")
	}
	f := formWith(t, page, delURL)

	// A wrong name is drawn at the field, and nothing is deleted.
	resp, body := e.submit(t, f, url.Values{"confirm": {"shop"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Type shop_uploads to confirm.") ||
		!strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, `value="shop"`) {
		t.Errorf("wrong name = %d:\n%s", resp.StatusCode, text(body))
	}
	if got := e.Nomad.HostVolumes(); len(got) != 1 {
		t.Fatalf("a wrong name deleted something: %v", got)
	}

	// The right name deletes it, and the page the redirect names says so.
	resp, _ = e.submit(t, f, url.Values{"confirm": {"shop_uploads"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/servers/fsn-1?deleted=shop_uploads#space" {
		t.Fatalf("POST = %d to %q", resp.StatusCode, location(t, resp))
	}
	if got := e.Nomad.HostVolumes(); len(got) != 0 {
		t.Errorf("volumes after the delete: %v", got)
	}
	_, body = e.get(t, "/ops/servers/fsn-1?deleted=shop_uploads")
	words = text(body)
	if !strings.Contains(words, "Deleted the volume shop_uploads.") || strings.Contains(words, "Volumes no app declares") {
		t.Errorf("the page after the delete:\n%s", words)
	}
	// The same address when the volume is back says nothing.
	e.Nomad.AddHostVolumeOn("shop_uploads", "node-2")
	_, body = e.get(t, "/ops/servers/fsn-1?deleted=shop_uploads")
	if strings.Contains(text(body), "Deleted the volume") {
		t.Error("a delete that isn't true is announced")
	}
	// And the volume that is gone now is a 404 page.
	e.Nomad.ClaimHostVolume("shop_uploads")
	if resp, _ := e.get(t, "/ops/servers/fsn-1/volumes/never_was/delete"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a volume that doesn't exist = %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.postForm(t, "/ops/servers/fsn-1/volumes/never_was/delete", url.Values{"confirm": {"never_was"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST for a volume that doesn't exist = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteVolumeRefusals(t *testing.T) {
	e, _ := spaceCluster(t)
	// A copy mounts it: the row has no Delete…, the page refuses, and so does the post.
	e.Nomad.ClaimHostVolume("shop_uploads", "alloc-1")
	_, body := e.get(t, "/ops/servers/fsn-1")
	words := text(body)
	if !strings.Contains(words, "mounted by a running copy") || strings.Contains(body, `/volumes/shop_uploads/delete`) {
		t.Errorf("a volume in use:\n%s", words)
	}
	resp, page := e.get(t, "/ops/servers/fsn-1/volumes/shop_uploads/delete")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "A running copy mounts this volume.") || strings.Contains(page, `name="confirm"`) {
		t.Errorf("GET = %d:\n%s", resp.StatusCode, text(page))
	}
	resp, page = e.postForm(t, "/ops/servers/fsn-1/volumes/shop_uploads/delete", url.Values{"confirm": {"shop_uploads"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "A running copy mounts this volume.") {
		t.Errorf("POST = %d:\n%s", resp.StatusCode, text(page))
	}
	e.Nomad.ClaimHostVolume("shop_uploads")

	// An app declares it: it isn't listed, and the post says whose it is.
	db := app.App{ID: "db", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "pg", Image: "postgres:17",
		Volumes: []app.Volume{{Name: "data", Path: "/var/lib/postgresql/data"}}}}}
	e.seed(t, db, seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.Nomad.AddHostVolumeOn("db_data", "node-2")
	_, body = e.get(t, "/ops/servers/fsn-1")
	if strings.Contains(body, "db_data") {
		t.Error("a volume an app declares is listed as one no app declares")
	}
	resp, page = e.postForm(t, "/ops/servers/fsn-1/volumes/db_data/delete", url.Values{"confirm": {"db_data"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "App db declares the volume db_data") {
		t.Errorf("POST for a declared volume = %d:\n%s", resp.StatusCode, text(page))
	}
	if got := e.Nomad.HostVolumes(); len(got) != 2 {
		t.Errorf("a refused post deleted something: %v", got)
	}
}

// With a backup destination set, the page offers the box (what it does is feature 045's phase D).
func TestDeleteVolumePageOffersBackUpFirstWithADestination(t *testing.T) {
	e, _ := spaceCluster(t)
	if _, err := e.Svc.SetBackupDestination(context.Background(), service.DestinationInput{Endpoint: "http://10.0.0.5:9000", Region: "r", Bucket: "backups", AccessKeyID: "AK", SecretAccessKey: new("sk")}); err != nil {
		t.Fatal(err)
	}
	_, page := e.get(t, "/ops/servers/fsn-1/volumes/shop_uploads/delete")
	if !strings.Contains(page, `name="backUp"`) || !strings.Contains(text(page), "Back up first") || strings.Contains(text(page), "so nothing keeps a copy") {
		t.Errorf("the page with a destination:\n%s", text(page))
	}
}

func TestCleanupSettingsForm(t *testing.T) {
	e, _ := spaceCluster(t)
	_, body := e.get(t, "/ops/servers/fsn-1")
	f := formWith(t, body, "/ops/cleanup/settings")
	if f.Values.Get("server") != "fsn-1" || f.Values.Get("automatic") != "1" {
		t.Errorf("the form's values = %v", f.Values)
	}
	// An unchecked box sends nothing: the daily run is off.
	resp, _ := e.submit(t, f, url.Values{"keep": {"5"}, "automatic": nil})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/servers/fsn-1?saved=1#space" {
		t.Fatalf("POST = %d to %q", resp.StatusCode, location(t, resp))
	}
	got, err := e.Svc.CleanupSettings(context.Background())
	if err != nil || got.Automatic || got.KeepDeployments != 5 {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	_, body = e.get(t, "/ops/servers/fsn-1?saved=1")
	if !strings.Contains(text(body), "Saved.") || !strings.Contains(body, `<option value="5" selected>5</option>`) || strings.Contains(body, `name="automatic" value="1" checked`) {
		t.Errorf("the page after saving:\n%s", text(body))
	}
	// A number the select can't send is refused, and nothing changes.
	resp, page := e.postForm(t, "/ops/cleanup/settings", url.Values{"server": {"fsn-1"}, "keep": {"11"}, "automatic": {"1"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(page), "That setting wasn't saved.") {
		t.Errorf("keep 11 = %d:\n%s", resp.StatusCode, text(page))
	}
	if resp, _ := e.postForm(t, "/ops/cleanup/settings", url.Values{"keep": {"many"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("keep many = %d, want 422", resp.StatusCode)
	}
	if got, _ := e.Svc.CleanupSettings(context.Background()); got.KeepDeployments != 5 || got.Automatic {
		t.Errorf("a refused post changed the settings: %+v", got)
	}
}

// The Overview's panel for each server has one line with what it could free, linking to the Space section.
func TestOverviewShowsReclaimableSpace(t *testing.T) {
	e, _ := spaceCluster(t)
	_, body := e.get(t, "/ops")
	if n := strings.Count(body, "Reclaimable 3.3 GB"); n != 1 || !strings.Contains(body, `<a href="/ops/servers/fsn-1#space">Reclaimable 3.3 GB</a>`) {
		t.Errorf("Overview has %d Reclaimable lines:\n%s", n, text(body))
	}
	if strings.Contains(body, `href="/ops/servers/fsn-2#space"`) {
		t.Error("a server with nothing to free has the line")
	}
	_, part := e.get(t, "/parts/ops/server")
	if !strings.Contains(part, "Reclaimable 3.3 GB") {
		t.Error("the part lacks the line")
	}
}

// An app's Volumes panel links a volume no service mounts to the page that deletes it.
func TestAppVolumesPanelLinksToTheDeletePage(t *testing.T) {
	e, _ := spaceCluster(t)
	db := app.App{ID: "db", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "pg", Image: "postgres:17",
		Volumes: []app.Volume{{Name: "data", Path: "/var/lib/postgresql/data"}}}}}
	e.seed(t, db, seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.Nomad.AddHostVolumeOn("db_old", "node-2")
	_, body := e.get(t, "/apps/db/backups")
	if !strings.Contains(text(body), "Volumes no service mounts") || !strings.Contains(body, `<a href="/ops/servers/fsn-1/volumes/db_old/delete">Delete…</a>`) {
		t.Errorf("the panel has no link:\n%s", text(body))
	}
}
