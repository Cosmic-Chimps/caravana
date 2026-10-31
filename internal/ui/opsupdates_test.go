package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 027, page 3: the Updates page at /ops/updates. The page is checked on an environment
// whose sampler has read a copy of one of internal/hostos's fixture hosts (with the file times set
// here, since git keeps none), a fake release server and a fake pro; the builders (the mark, the
// settings, the reboot row) are also checked on hand-made data.

// fakeRelease is a stand-in for GitHub's releases API: the latest release, and 404 for everything
// else (the installer and the Traefik job a release is read for its pins).
type fakeRelease struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	requests int
}

func newFakeRelease(t *testing.T) *fakeRelease {
	t.Helper()
	g := &fakeRelease{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		g.requests++
		if g.status != 0 {
			w.WriteHeader(g.status)
			return
		}
		fmt.Fprint(w, `{"tag_name":"v0.16.0","html_url":"https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0",`+
			`"published_at":"2026-10-28T10:00:00Z","body":"Highlights\n\n- Operator view.\n- Updates page.\n","draft":false,"prerelease":false}`)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeRelease) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requests
}

// uiPro is a pro command that gives a fixed answer, after a gate when there is one.
type uiPro struct {
	ok   bool
	out  []byte
	gate chan struct{}
}

func (p uiPro) Available() bool { return p.ok }

func (p uiPro) Run(ctx context.Context) ([]byte, error) {
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.out, nil
}

// updOpts says what a test's host looks like: one of internal/hostos/testdata's fixtures copied
// to a temporary folder, how old Ubuntu's list and the reboot file are, and what edits the copy.
type updOpts struct {
	fixture   string
	listAge   time.Duration // the age of updates-available; 0 is three hours
	rebootAge time.Duration // the age of reboot-required; 0 is thirty hours
	edit      func(root string)
	pro       service.ProRunner
	version   string // the running version; "" is 0.15.0
	kernel    string // the node's kernel; "" is 6.8.0-147-generic
}

// updEnv is a console whose sampler has read the node and a copy of the fixture host.
func updEnv(t *testing.T, o updOpts) (*consoleEnv, *fakeRelease) {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "hostos", "testdata", o.fixture))); err != nil {
		t.Fatal(err)
	}
	if o.edit != nil {
		o.edit(root)
	}
	age := func(rel string, d time.Duration) {
		path := filepath.Join(root, rel)
		if _, err := os.Stat(path); err != nil {
			return
		}
		at := time.Now().Add(-d)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	age("var/lib/update-notifier/updates-available", cmpDur(o.listAge, 3*time.Hour))
	age("run/reboot-required", cmpDur(o.rebootAge, 30*time.Hour))

	gh := newFakeRelease(t)
	kernel := cmpStr(o.kernel, "6.8.0-147-generic")
	e := newConsoleEnvWith(t, func(svc *service.Service, n *deploytest.Nomad) {
		svc.SetMetricsSampleGap(500 * time.Millisecond)
		svc.SetHostRoot(hostos.Dir(root))
		svc.SetVersion(cmpStr(o.version, "0.15.0"))
		svc.SetReleaseURL(gh.URL + "/repos/x/releases/latest")
		svc.SetReleasePinURLs(gh.URL+"/download", gh.URL+"/raw")
		svc.SetReleaseCheckGap(time.Hour)
		if o.pro != nil {
			svc.SetProRunner(o.pro)
		}
		n.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
		n.SetHostStats(deploytest.LabHostStats())
		attrs := deploytest.X86NodeAttributes()
		attrs["kernel.version"] = kernel
		n.SetNodeFacts(attrs, "default", []*api.NodeEvent{{Subsystem: "Cluster", Message: "Node registered", Timestamp: time.Now().Add(-9 * 24 * time.Hour)}})
	})
	srvWait(t, "the sampler's first reading of the host's files", func() bool {
		h, err := e.Svc.Host(context.Background())
		return err == nil && h.Node != nil && h.Reboot != nil
	})
	return e, gh
}

func cmpDur(d, def time.Duration) time.Duration {
	if d != 0 {
		return d
	}
	return def
}

func cmpStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// securityOn is an edit that switches Ubuntu's automatic updates off, so that the security updates
// are a needs-you item at once (with them on, the item waits 24 hours).
func automaticUpdatesOff(t *testing.T) func(root string) {
	return func(root string) {
		path := filepath.Join(root, "etc/apt/apt.conf.d/20auto-upgrades")
		if err := os.WriteFile(path, []byte("APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"0\";\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// releaseFound asks the page's own Check now for the fake release and waits for it to be recorded.
func releaseFound(t *testing.T, e *consoleEnv) {
	t.Helper()
	resp, body := e.postForm(t, "/ops/updates/check", url.Values{"what": {"release"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/updates#available" {
		t.Fatalf("POST check release = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	srvWait(t, "the release check", func() bool {
		v, err := e.Svc.Updates(context.Background())
		return err == nil && v.Caravana.Latest != "" && !v.Checking.Release
	})
}

// rowsOf are the Available rows' names, in the order drawn.
func rowNames(body string) []string {
	var out []string
	for _, part := range strings.Split(body, `data-up="`)[1:] {
		out = append(out, part[:strings.Index(part, `"`)])
	}
	return out
}

func TestOpsUpdatesPage(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: automaticUpdatesOff(t), pro: uiPro{ok: true}})
	releaseFound(t, e)
	resp, body := e.get(t, "/ops/updates")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /ops/updates = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)

	// The head, the live region, the script and the sidebar.
	for _, want := range []string{
		`<h1 class="cv-headline">Updates</h1>`, "What's waiting for caravana and this server's OS, and how to apply it.",
		`id="up-top"`, `hx-get="/parts/ops/updates"`, `hx-trigger="sse:updates, sse:host, sse:attention, sse:sync"`,
		`sse-connect="/live?watch=ops"`, `data-live`, `<script src="/static/js/ops.js?v=`,
		`<a href="/ops/updates" class="cv-nav-item" aria-current="page">`,
		`id="available"`, `id="apply"`, `id="how"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// The banners: the reboot (■) before the updates (◆), each the item's own sentence, its consequence,
	// and the link to How to apply.
	banners := bannersOf(body)
	if len(banners) != 2 ||
		banners[0] != "This server needs a reboot: kernel 6.8.0-149 is installed, but 6.8.0-147 is still running. Until it reboots, the server keeps running the old kernel. How to apply" ||
		banners[1] != "14 updates are waiting, 9 of them security updates. Until they are installed, the server keeps running the old versions. How to apply" {
		t.Fatalf("banners = %q", banners)
	}
	first := regionBetween(t, body, `cv-dmbanner`, `How to apply`)
	if !strings.Contains(first, `cv-mark-lg cv-mark-failed`) {
		t.Errorf("the reboot banner's mark isn't ■:\n%s", first)
	}
	if !strings.Contains(body, `<a href="#apply" class="cv-btn cv-btn-secondary cv-btn-sm">How to apply</a>`) {
		t.Error("a banner's action isn't the link to #apply")
	}

	// Available: three rows, ■ ◆ ○ in that order, with the contract's words.
	avail := regionBetween(t, body, `id="available"`, `id="apply"`)
	if got := rowNames(avail); strings.Join(got, " ") != "reboot ubuntu release" {
		t.Errorf("rows = %v, want reboot ubuntu release", got)
	}
	for _, want := range []string{
		"3 updates",
		"Reboot since 2026-", " UTC kernel 6.8.0-147 → 6.8.0-149",
		"Rebooting stops every app for about a minute; Nomad starts them again when the server is back.",
		"Ubuntu updates checked by Ubuntu 3 h ago 14 packages · 9 security",
		"caravana released Oct 28 0.15.0 → 0.16.0", "Operator view.", "Updates page.",
	} {
		if !strings.Contains(text(avail), want) {
			t.Errorf("Available lacks %q:\n%s", want, text(avail))
		}
	}
	for _, want := range []string{
		`cv-mark cv-mark-lg cv-mark-failed`, `cv-mark cv-mark-lg cv-mark-warning`, `cv-mark cv-mark-lg cv-mark-idle`,
		`<time class="cv-clock" datetime="`, // the reboot's time takes localtime.js
		`name="what" value="packages"`, `>Check package names</button>`,
		`name="what" value="release"`, `>Check now</button>`,
	} {
		if !strings.Contains(avail, want) {
			t.Errorf("Available lacks %q", want)
		}
	}
	// Nothing says a vulnerability's name.
	if strings.Contains(strings.ToLower(words), "cve") {
		t.Error("the page names a CVE")
	}

	// How to apply: the steps that apply, the OS updates, caravana's installer, the reboot last.
	apply := regionBetween(t, body, `id="apply"`, `id="how"`)
	applyText := text(apply)
	for _, want := range []string{
		"How to apply run these on the server, over SSH",
		"1 Install the waiting updates: sudo apt update && sudo apt upgrade Copy",
		"2 Run the installer of 0.16.0. It upgrades caravana, and Nomad and Traefik when their versions change; your apps keep running, and it puts 0.15.0 back if 0.16.0 fails its health check. curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/download/v0.16.0/install.sh | sudo bash Copy",
		"3 Reboot the server: sudo reboot Copy Every app stops for about a minute. The console comes back when the server does.",
		"caravana doesn't apply updates from the console yet.",
	} {
		if !strings.Contains(applyText, want) {
			t.Errorf("How to apply lacks %q:\n%s", want, applyText)
		}
	}
	for _, id := range []string{"up-cmd-os", "up-cmd-caravana", "up-cmd-reboot"} {
		// The command box is selectable text, and its Copy button is hidden until the script shows it.
		if !strings.Contains(apply, `id="`+id+`"`) || !strings.Contains(apply, `data-copy="`+id+`" hidden`) {
			t.Errorf("step %s has no command box or a hidden copy button", id)
		}
	}

	// How this server updates: the host's two settings (read-only) and the one caravana owns.
	how := regionBetween(t, body, `id="how"`, `aria-labelledby="up-past-title"`)
	for _, want := range []string{
		"Ubuntu's automatic updates Set on the server, in unattended-upgrades. Off",
		"Automatic reboot Set on the server, in unattended-upgrades. Off: reboots wait for you.",
		"New caravana versions Only the release list is fetched; nothing is sent about this server.",
		"Check GitHub for new releases once a day", "Save", "checked ", "Check now",
	} {
		if !strings.Contains(text(how), want) {
			t.Errorf("How this server updates lacks %q:\n%s", want, text(how))
		}
	}
	if !strings.Contains(how, `type="checkbox" name="checkEnabled" value="1" checked`) {
		t.Error("the release check isn't on by default")
	}
	if strings.Contains(how, "<input") && strings.Contains(strings.ReplaceAll(how, `name="what"`, ""), `type="text"`) {
		t.Error("a settings row has a text input")
	}

	// Past updates, said with its times in UTC.
	past := regionBetween(t, body, `aria-labelledby="up-past-title"`, `</section>`)
	if !strings.Contains(text(past), "times in UTC") || !strings.Contains(past, `id="up-past"`) {
		t.Errorf("Past updates:\n%s", text(past))
	}

	// The sidebar's mark: ■ for the reboot, with its words for a screen reader.
	side := sidebarRegion.FindString(body)
	if !strings.Contains(side, `id="nav-updates-mark"><span title="reboot needed"><span aria-hidden="true">■</span><span class="sr-only">reboot needed</span></span>`) {
		t.Errorf("the sidebar's mark:\n%s", side)
	}
}

// Nothing waiting: the sentence with the oldest check, no How to apply, no banner, no mark.
func TestOpsUpdatesNothingWaiting(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean"})
	_, body := e.get(t, "/ops/updates")
	for _, want := range []string{"Nothing waiting. Checked 3 hours ago."} {
		if !strings.Contains(text(body), want) {
			t.Errorf("the page lacks %q:\n%s", want, text(body))
		}
	}
	for _, not := range []string{`id="apply"`, `cv-dmbanner`, `data-up=`, `<span title=`} {
		if strings.Contains(body, not) {
			t.Errorf("the page has %q with nothing waiting", not)
		}
	}
	if len(rowNames(body)) != 0 {
		t.Error("rows with nothing waiting")
	}
	// The settings are read from the fixture: Ubuntu's automatic updates are on, reboots wait.
	if !strings.Contains(text(body), "On: security updates install by themselves.") {
		t.Errorf("the automatic-updates setting:\n%s", text(body))
	}
}

// A list Ubuntu hasn't refreshed in two days still shows, dim, with its age and what to run, and
// it opens no banner and takes no ◆ from the sidebar.
func TestOpsUpdatesStaleList(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-stale-list", listAge: 3 * 24 * time.Hour, edit: automaticUpdatesOff(t)})
	_, body := e.get(t, "/ops/updates")
	if !strings.Contains(text(body), "Ubuntu's list is 3 days old. Run `sudo apt update` on the server to refresh it.") {
		t.Errorf("the stale sentence:\n%s", text(body))
	}
	if !strings.Contains(body, `class="cv-uprow cv-uprow-dim"`) {
		t.Error("the stale row isn't dim")
	}
	if got := bannersOf(body); len(got) != 0 {
		t.Errorf("a stale list opened banners: %q", got)
	}
	if !strings.Contains(text(body), "Ubuntu updates checked by Ubuntu 3 d ago 14 packages · 9 security") {
		t.Errorf("the row:\n%s", text(body))
	}
	side := sidebarRegion.FindString(body)
	if strings.Contains(side, "◆") || !strings.Contains(side, `<span aria-hidden="true">1</span>`) {
		t.Errorf("the sidebar's mark for a stale list (the count, never ◆):\n%s", side)
	}
}

// A host that isn't Ubuntu or Debian: the OS rows say it can't be read, with ○, and both settings
// say they can't be read.
func TestOpsUpdatesNotUbuntu(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "non-debian-host"})
	_, body := e.get(t, "/ops/updates")
	words := text(body)
	if !strings.Contains(words, "caravana can't read this server's pending updates (not Ubuntu or Debian).") {
		t.Errorf("the OS sentence:\n%s", words)
	}
	if strings.Count(words, "caravana can't read this setting.") != 2 {
		t.Errorf("the two settings:\n%s", words)
	}
	for _, not := range []string{"Check package names", `data-up=`, `id="apply"`, "Nothing waiting"} {
		if strings.Contains(body, not) {
			t.Errorf("the page has %q on a host it can't read", not)
		}
	}
	if !strings.Contains(body, `cv-mark cv-mark-idle`) {
		t.Error("the OS sentence has no ○")
	}
}

// Check package names: it answers 303, the row says it is checking until pro answers, and then the
// names show with when they were read. A second check in 10 minutes is refused with the sentence,
// 429, and the page drawn again.
func TestOpsUpdatesPackageNames(t *testing.T) {
	gate := make(chan struct{})
	pro, err := os.ReadFile(filepath.Join("..", "hostos", "testdata", "pro-security-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: automaticUpdatesOff(t), pro: uiPro{ok: true, out: pro, gate: gate}})

	resp, _ := e.postForm(t, "/ops/updates/check", url.Values{"what": {"packages"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/updates#available" {
		t.Fatalf("POST check packages = %d to %q", resp.StatusCode, location(t, resp))
	}
	_, body := e.get(t, "/ops/updates")
	if !strings.Contains(text(body), "Checking which packages… this can take a minute.") || strings.Contains(body, ">Check package names</button>") {
		t.Errorf("the checking state:\n%s", text(regionBetween(t, body, `id="available"`, `id="apply"`)))
	}
	if !strings.Contains(body, `cv-mark cv-mark-rolling`) {
		t.Error("the checking state has no ◐")
	}

	// Again within 10 minutes: refused with the service's sentence, the page drawn with 429.
	resp, body = e.postForm(t, "/ops/updates/check", url.Values{"what": {"packages"}})
	if resp.StatusCode != http.StatusTooManyRequests ||
		!strings.Contains(text(body), "That check ran less than 10 minutes ago. It can run again at ") || !strings.Contains(body, `role="alert"`) {
		t.Errorf("the second check = %d:\n%s", resp.StatusCode, text(body))
	}

	close(gate)
	srvWait(t, "the package names", func() bool {
		_, body = e.get(t, "/ops/updates")
		return strings.Contains(body, `class="cv-upnamelist"`)
	})
	names := text(regionBetween(t, body, `class="cv-upnames"`, `</p>`))
	if !strings.Contains(names, "linux-image-6.8.0-149-generic") || !strings.Contains(names, "checked just now") && !strings.Contains(names, "checked 0 s ago") {
		t.Errorf("the names: %s", names)
	}
	if strings.Contains(body, ">Check package names</button>") {
		t.Error("the button is still there with the names read")
	}
}

// Without pro (or on a host that can't have it) the row has no button and says nothing about names.
func TestOpsUpdatesNoPro(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: automaticUpdatesOff(t), pro: uiPro{ok: false}})
	_, body := e.get(t, "/ops/updates")
	if strings.Contains(body, "Check package names") {
		t.Error("a host without pro offers Check package names")
	}
	if !strings.Contains(text(body), "14 packages · 9 security") {
		t.Error("the Ubuntu row is gone")
	}
}

// The release check: turned off, it makes no request, the page says "off" with no Check now, a check
// asked for is refused with the sentence (409), and the box is unchecked. Turned on again, it works.
func TestOpsUpdatesReleaseCheckSetting(t *testing.T) {
	e, gh := updEnv(t, updOpts{fixture: "ubuntu-clean"})

	resp, _ := e.postForm(t, "/ops/updates/settings", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/updates?saved=1#how" {
		t.Fatalf("POST settings = %d to %q", resp.StatusCode, location(t, resp))
	}
	resp, body := e.get(t, "/ops/updates?saved=1")
	how := regionBetween(t, body, `id="how"`, `aria-labelledby="up-past-title"`)
	if resp.StatusCode != 200 || !strings.Contains(text(how), "Saved.") || !strings.Contains(text(how), " off") ||
		strings.Contains(how, "checkEnabled\" value=\"1\" checked") || strings.Contains(how, ">Check now</button>") {
		t.Errorf("the page with the check off:\n%s", text(how))
	}

	resp, body = e.postForm(t, "/ops/updates/check", url.Values{"what": {"release"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The release check is turned off.") {
		t.Errorf("a release check while it is off = %d:\n%s", resp.StatusCode, text(body))
	}
	if n := gh.count(); n != 0 {
		t.Errorf("the check was off and GitHub saw %d requests", n)
	}

	// On again: the box is checked, and Check now answers 303 and asks GitHub once.
	if resp, _ := e.postForm(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST settings = %d", resp.StatusCode)
	}
	releaseFound(t, e)
	if n := gh.count(); n != 1 {
		t.Errorf("GitHub saw %d requests, want 1", n)
	}
	// And again within 10 minutes: refused, 429.
	resp, body = e.postForm(t, "/ops/updates/check", url.Values{"what": {"release"}})
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(text(body), "That check ran less than 10 minutes ago.") {
		t.Errorf("the second release check = %d:\n%s", resp.StatusCode, text(body))
	}
	// A check that names neither is 422, drawn on the page.
	resp, body = e.postForm(t, "/ops/updates/check", url.Values{"what": {"kernel"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Choose what to check") {
		t.Errorf("an unknown check = %d:\n%s", resp.StatusCode, text(body))
	}
}

// GitHub that can't be reached: the caravana row is left out (and its step), the meta says so, and
// the line by the form says why.
func TestOpsUpdatesGitHubUnreachable(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean"})
	rc, err := e.Store.ReleaseCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc.LatestTag, rc.PublishedAt, rc.URL = "v0.16.0", time.Now().Add(-48*time.Hour), "https://example.invalid/v0.16.0"
	rc.CheckedAt, rc.Error = time.Now().Add(-3*24*time.Hour-time.Hour), "GitHub didn't answer"
	if err := e.Store.SaveReleaseCheck(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(t, "/ops/updates")
	words := text(body)
	if !strings.Contains(words, "Couldn't check GitHub (last checked 3 days ago).") {
		t.Errorf("the meta:\n%s", words)
	}
	for _, not := range []string{`data-up="release"`, "0.15.0 → 0.16.0", "Run the installer"} {
		if strings.Contains(body, not) || strings.Contains(words, not) {
			t.Errorf("the page has %q though GitHub couldn't be reached", not)
		}
	}
	if !strings.Contains(words, "checked 3 d ago · GitHub didn't answer") {
		t.Errorf("the line by the form:\n%s", words)
	}
	if strings.Contains(words, "Nothing waiting") && !strings.Contains(words, "Nothing waiting. Checked") {
		t.Errorf("the meta of an empty list:\n%s", words)
	}
}

// The part is the main region and, by id, the settings, the line by the form, Past updates and the
// sidebar's mark, and the page's own body is made of the same templates.
func TestOpsUpdatesPart(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: automaticUpdatesOff(t)})
	resp, body := e.get(t, "/parts/ops/updates")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /parts/ops/updates = %d", resp.StatusCode)
	}
	for _, want := range []string{
		`id="available"`, `id="apply"`, `hx-swap-oob="innerHTML:#up-auto"`, `hx-swap-oob="innerHTML:#up-checked"`,
		`hx-swap-oob="innerHTML:#up-past"`, `hx-swap-oob="innerHTML:#nav-updates-mark"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the part lacks %q", want)
		}
	}
	if strings.Contains(body, `<h1`) || strings.Contains(body, `name="checkEnabled"`) {
		t.Error("the part draws the head or the checkbox, which a refresh must never replace")
	}
	// The page's regions hold the part's own text.
	_, page := e.get(t, "/ops/updates")
	top := regionBetween(t, page, `id="up-top"`, `<section id="how"`)
	if !strings.Contains(text(top), text(regionBetween(t, body, `<section id="available"`, `<div hx-swap-oob`))[:80]) {
		t.Error("the page and its part draw the main region differently")
	}
	// A part needs a session.
	if resp, _ := e.do(t, request{path: "/parts/ops/updates"}); resp.StatusCode == 200 {
		t.Errorf("GET /parts/ops/updates without a session = %d", resp.StatusCode)
	}
}

// The forms post only with a session and from the console's own origin.
func TestOpsUpdatesFormsAreProtected(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean"})
	for _, path := range []string{"/ops/updates/check", "/ops/updates/settings"} {
		resp, _ := e.do(t, request{method: http.MethodPost, path: path, form: url.Values{"what": {"release"}},
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, cookies: []*http.Cookie{e.Cookie}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s cross-site = %d, want 403", path, resp.StatusCode)
		}
		resp, _ = e.do(t, request{method: http.MethodPost, path: path, form: url.Values{"what": {"release"}},
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode == http.StatusSeeOther && location(t, resp) == "/ops/updates#available" {
			t.Errorf("POST %s without a session started a check", path)
		}
	}
}

// The Overview and the Developer home's needs-you rows say the OS items in the item's own words and
// offer "Open updates".
func TestAttentionRowsOfTheOSItems(t *testing.T) {
	s := &server{}
	now := time.Now()
	for _, tc := range []struct {
		kind service.AttentionKind
		mark string
	}{
		{service.AttentionOSRebootRequired, "failed"},
		{service.AttentionOSSecurityUpdates, "warning"},
	} {
		it := service.AttentionItem{Kind: tc.kind, InstalledKernel: "6.8.0-149-generic", RunningKernel: "6.8.0-147-generic", Updates: 14, Security: 9}
		row := s.attentionRow(now, it, places{}, nil)
		if row.Mark != tc.mark || row.Action != "Open updates" || row.Href != "/ops/updates" {
			t.Errorf("%s row = %+v", tc.kind, row)
		}
		want := "This server needs a reboot: kernel 6.8.0-149 is installed, but 6.8.0-147 is still running."
		if tc.kind == service.AttentionOSSecurityUpdates {
			want = "14 updates are waiting, 9 of them security updates."
		}
		if row.Text.String() != want {
			t.Errorf("%s says %q, want %q", tc.kind, row.Text, want)
		}
	}
}

func TestUpdatesMarkOf(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Hour)
	release := service.CaravanaUpdate{Current: "0.15.0", Latest: "0.16.0"}
	ubuntu := func(total, security int, stale bool) service.OSUpdates {
		return service.OSUpdates{Readable: true, Updates: service.UpdateCounts{Known: true, Total: total, Security: security, CheckedAt: fresh, Stale: stale}}
	}
	for _, tc := range []struct {
		name string
		v    service.UpdatesView
		want navMark
	}{
		{"nothing", service.UpdatesView{OS: ubuntu(0, 0, false)}, navMark{}},
		{"a reboot is ■ over everything", service.UpdatesView{Caravana: release, OS: func() service.OSUpdates {
			o := ubuntu(14, 9, false)
			o.Reboot.Required = true
			return o
		}()}, navMark{"■", "reboot needed"}},
		{"security is ◆", service.UpdatesView{OS: ubuntu(14, 9, false)}, navMark{"◆", "security updates waiting"}},
		{"a stale list is no ◆: the count", service.UpdatesView{OS: ubuntu(14, 9, true)}, navMark{"1", "1 update available"}},
		{"a newer caravana is 1", service.UpdatesView{Caravana: release, OS: ubuntu(0, 0, false)}, navMark{"1", "1 update available"}},
		{"a newer caravana and other updates are 2", service.UpdatesView{Caravana: release, OS: ubuntu(3, 0, false)}, navMark{"2", "2 updates available"}},
		{"a release GitHub couldn't confirm is no row", service.UpdatesView{Caravana: service.CaravanaUpdate{Current: "0.15.0", Latest: "0.16.0", CheckError: "GitHub didn't answer"}, OS: ubuntu(0, 0, false)}, navMark{}},
		{"a host that can't be read", service.UpdatesView{}, navMark{}},
	} {
		if got := updatesMarkOf(now, tc.v); got != tc.want {
			t.Errorf("%s: mark = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestRebootChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    service.RebootView
		want string
	}{
		{"two kernels", service.RebootView{InstalledKernel: "6.8.0-149-generic", RunningKernel: "6.8.0-147-generic"}, "kernel 6.8.0-147 → 6.8.0-149"},
		{"the same kernel", service.RebootView{InstalledKernel: "6.8.0-149-generic", RunningKernel: "6.8.0-149-generic", Packages: []string{"libc6"}}, "libc6"},
		{"an older kernel is no kernel change", service.RebootView{InstalledKernel: "6.8.0-100-generic", RunningKernel: "6.8.0-149-generic", Packages: []string{"libc6", "dbus"}}, "libc6, dbus"},
		{"three packages", service.RebootView{Packages: []string{"a", "b", "c"}}, "a, b, c"},
		{"five packages", service.RebootView{Packages: []string{"a", "b", "c", "d", "e"}}, "a, b, c and 2 more"},
		{"no names", service.RebootView{}, "needed"},
	} {
		if got := rebootChange(tc.r); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestKernelNumber(t *testing.T) {
	for in, want := range map[string]string{
		"6.8.0-149-generic": "6.8.0-149", "6.8.0-9-generic": "6.8.0-9", "7.0.0-38": "7.0.0-38", "custom": "custom", "": "",
	} {
		if got := kernelNumber(in); got != want {
			t.Errorf("kernelNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpSettings(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name         string
		o            service.OSUpdates
		auto, reboot string
	}{
		{"not readable", service.OSUpdates{}, unreadable, unreadable},
		{"files unreadable", service.OSUpdates{Readable: true}, unreadable, unreadable},
		{"on, reboots wait", service.OSUpdates{Readable: true, Unattended: hostos.Unattended{Enabled: &yes, AutoReboot: &no}},
			"On: security updates install by themselves.", "Off: reboots wait for you."},
		{"off", service.OSUpdates{Readable: true, Unattended: hostos.Unattended{Enabled: &no}}, "Off", unreadable},
		{"reboots at 02:00", service.OSUpdates{Readable: true, Unattended: hostos.Unattended{Enabled: &yes, AutoReboot: &yes, AutoRebootTime: "02:00"}},
			"On: security updates install by themselves.", "On, at 02:00 server time"},
		{"reboots at once", service.OSUpdates{Readable: true, Unattended: hostos.Unattended{AutoReboot: &yes, AutoRebootTime: "now"}},
			unreadable, "On, right after an update that needs one."},
	} {
		got := upSettings(tc.o)
		if len(got) != 2 || got[0].Value != tc.auto || got[1].Value != tc.reboot || got[0].Label != "Ubuntu's automatic updates" || got[1].Label != "Automatic reboot" {
			t.Errorf("%s: %+v, want %q and %q", tc.name, got, tc.auto, tc.reboot)
		}
	}
}

func TestRefusalWords(t *testing.T) {
	for in, want := range map[string]string{
		"the release check is turned off":                                         "The release check is turned off.",
		"That check ran less than 10 minutes ago. It can run again at 14:20 UTC.": "That check ran less than 10 minutes ago. It can run again at 14:20 UTC.",
		"This is a development build, which has no release to compare with.":      "This is a development build, which has no release to compare with.",
	} {
		if got := refusalWords(fmt.Errorf("%s", in)); got != want {
			t.Errorf("refusalWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpPastOf(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	var history []service.UpdateEvent
	for i := range 12 {
		history = append(history, service.UpdateEvent{At: now.Add(-time.Duration(i) * 24 * time.Hour), Kind: "version", Text: fmt.Sprintf("caravana 0.%d.0 → 0.%d.0", i, i+1)})
	}
	history[1].Kind, history[1].Detail = "boot", "kernel 6.8.0-149 since then"
	history = append(history, service.UpdateEvent{At: time.Date(2025, 12, 30, 1, 0, 0, 0, time.UTC), Kind: "os_packages", Text: "Ubuntu updates · 2 packages"})
	got := upPastOf(now, history)
	if len(got) != 10 {
		t.Fatalf("%d rows, want the last 10", len(got))
	}
	if got[0].Date.Text != "Nov 2" || got[0].Date.Exact != "2026-11-02 12:00:00 UTC" || got[0].Mark != "healthy" || got[0].Text != "caravana 0.0.0 → 0.1.0" {
		t.Errorf("first row = %+v", got[0])
	}
	if got[1].Mark != "idle" || got[1].Detail != "kernel 6.8.0-149 since then" {
		t.Errorf("a boot's row = %+v", got[1])
	}
	one := upPastOf(now, history[12:])
	if len(one) != 1 || one[0].Date.Text != "Dec 30 2025" {
		t.Errorf("a date of another year = %+v", one)
	}
	if upPastOf(now, nil) != nil {
		t.Error("no history has rows")
	}
}

// The Updates item's mark reads the same facts on every Operator page.
func TestOperatorPagesCarryTheUpdatesMark(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot"})
	for _, path := range []string{"/ops", "/ops/servers/lab", "/ops/updates"} {
		_, body := e.get(t, path)
		if side := sidebarRegion.FindString(body); !strings.Contains(side, `<span aria-hidden="true">■</span>`) {
			t.Errorf("%s: the sidebar has no ■ for the reboot:\n%s", path, side)
		}
	}
	// A page both views share draws the Operator sidebar, and so the mark, when the cookie says Operator.
	_, body := e.do(t, request{path: "/settings", cookies: []*http.Cookie{e.Cookie, {Name: "caravana_mode", Value: "ops"}}})
	if side := sidebarRegion.FindString(body); !strings.Contains(side, `<span aria-hidden="true">■</span>`) {
		t.Errorf("/settings in the Operator view: the sidebar has no ■:\n%s", side)
	}
	// The Developer view has no such item.
	_, body = e.get(t, "/apps")
	if strings.Contains(body, "nav-updates-mark") {
		t.Error("the Developer sidebar has the Updates mark")
	}
}

// The Overview's stream forwards the updates notice, which the Updates page's regions refresh on.
func TestOpsLiveForwardsTheUpdatesNotice(t *testing.T) {
	e := opsEnv(t)
	feed := make(chan service.Change, 8)
	e.Server.live.subscribe = func(string) (<-chan service.Change, func()) { return feed, func() {} }
	e.Server.live.coalesce = time.Millisecond
	ls := openStream(t, e.URL, "/live?watch=ops", e.Cookie.Value)
	ls.expect("sync")
	feed <- service.Change{What: service.ChangeUpdates}
	if got := ls.next(); got != "updates" {
		t.Errorf("event = %q, want updates", got)
	}
}

var _ = store.ErrNotFound // the store's types, used above
