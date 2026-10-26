package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// sampleHashes are SHA-256 hashes (hex, of the lowercase text) of names in the
// design handoff's sample data: its apps, servers, people, projects, commits and
// domains. The handoff is local only and its content must never enter this
// repository, so the test keeps the hashes and not the names. A page that shows
// one of these names has copied the mock-up's data (SC-007).
var sampleHashes = func() map[string]bool {
	set := map[string]bool{}
	for _, h := range strings.Fields(`
001d057c49888db925f47ba4f0cbb75edf527893381dc5a27d7a965c766508c4
05b997682328376b8ea7864194b60b7e18719c6601cd1c590216b66fb6c354e3
06b96b36cc975e3505af34076f89fb58c51814044c6f50a0c5dbe50c982a6ff5
074bca944a23aa76d46052c4fc5f4aee4393edc3fad732fdbb40988d4bad79d7
0af3bf2dec6a94a16f85d969495446830985f3ad716394286257f9231d439eee
0b6ea1ef81bc4bb96da43747c7182c17485f9cc33d301d715eeace9d7177bd6e
0ee8ca47de65af3b2a6a400f9afb608e971aa063db98e010be3c855b34aec149
148df5d23c28beaa2a5eb6dd20ed61925082be913d9e0a4ba94b4ce3cfad2f39
153d934ccedc5a9191de289601d482f9054b52558b67e92cb0fca7810785d6b8
17a4baae2062e376e1137eec3eef51251a98189019caeadfc42a09ca33136e4e
1ce52853fae188403c9068643eaa790fc35567800bdfb330b11d6b4100670435
1d630127108f1feaf1f7beee59b66dd679daf712a441f3d0a39ee9ea0f2b7a95
1e38dba8ffb6af2ad51b796b22211f5ba3c1f657431ef3f086af85e345eb60b8
203bb9e535b4704ea1a22a8738057cc6c96e2a76ee8662d3c20e06957a4cdaa1
21a405d6a56e067adbc1be09cc776767f2817af7c0abdaa168aa484128c6fc04
22dea5ec012f38a7a42fa76df895d8972448090464aff55d60bb4e25a63f2830
2407452e1a73e02fd401a2ddda708945449e72c72d8856b95b1d26937c3e9a7b
24d4b96f58da6d4a8512313bbd02a28ebf0ca95dec6e4c86ef78ce7f01e788ac
25a9dba42e0fac74c3d2729a953f1aa36f9da55f6fa61b443d016e7bbfb5ab77
25ff2eeb3cc4ff53eae66a29ab950d57282e21d43919d0bdd9853c7721ab8a21
26842aa71d1639ab19248811c9b97d236ca029b73edbff83bcb814c7898218e5
28c1bc8a3655ef90a94aceb16832934d321883da8a9bf7b975086772bb744167
293270b9a8493dc98845c8c6efdce87e1b98879303fc3d19fdbcc39605685aa0
2b22d7ff06512b8d7f6f2bd41676d47bf12ab1ccdbbe8547d54b23974f9ff734
30f84dedb4d80076f2929ac8d862c385e00c6285db0e9a50c0788d92153b0683
35da10259bfb22d9a06560fcb8858df26865c790a88f085cf723fbe24a369eb6
37107a4a82e4f3dfac7496dad679c68f3ac1aa4143372df9cad5b428a28b2544
38b131d22186765aa9d450a07d8ed835b4200ad89146a02e3844e777a4cac428
47521ef5c0e0d0d8377ee056a1c41280f7ed054ce2b0a5b2ed1b133348735fc0
47e70b4887f5a09eb4e9542203fbdea2d48bf75fba60a419c4ddc56c17f7abd8
4e6fa12102ffee741889e20ef77a14c36edc2b0fd2eda6da36dfb069f31be1e2
4fa2f2b3fe43b996eeb55d865c645eefdf7d62f2a3d025daddcea6269a4d8af7
519c81f3be1249d6d080b376b13176dcc5469b39673f0f02266b2b89de38ddf2
51aecaad8f23f3f4860bbaa3793d9adf6647ab982e826713a38e7d15c62584e4
56eec035d20ac4475ca650623e0d48641d7bfcfb735a20742e1868724d15ce3b
582885de1aa85c53e2e1ccf4da6768c90fe41978adeb6b0145401b782efdfa2e
5b2b87617a7310c864f34471f46dc2c313f7ef771a70a29d41515e7725b24965
5b3318fbc1dc0cecca133994c1fdaa19a2f37d0f237fba712389dfea19a2ee68
5fcb1a9374943f8e2be376d3541d27224719a529245eebe4f3020a8c0f603b00
61abadfe224716b749c724bdf2831f589798414f3c802c4d28ed4c3fbfac3a84
61e8ce65c3b576f85f56eaa1ed3d99d84389feaf76805606e7ff8ed5c68e3763
64314540be8e36b5f1f790e6173cb563a29335c2dea90b8f4dfdd02fd840cdff
69c91d0668c28a0dad58422d8346745ecc8e31698ff33c4e39376dd9d1ca39c3
721ffa554e561cbfb65c52d03e491e4fd614b93cf4c2a975ddfd96762729eb1d
7885bcc469c6787f80c0ca18333479b985fd40fbbda8ebd2e19762bbbab246f1
789fde1b772f7bc228db68ae8af7f71c8bd95cf423f85fb0b3658877a2c2186f
7930172fa3e0d4a2f46670c653a38b4f24c77fae5d9d7097c09673dd2475f535
7c8ccc86c11654af029457d90fdd9d013ce6fb011ee8fdb1374832268cc8d967
7c9296ae087774638ac32cc722ae1bde9040ef8d8782c08605eeaeb8e3f70118
7cc3c6da902a0bc413fc99b4e4f45581a42420ee7508ef15bcbf8f9330f75a5c
7dd1fc6aa9fd93ccabe1448ca702e894047851ad643392de5b8885bb921fb088
85624b2ce5ce0171b15ea5d3a12c244f4bb906d62c5a67c6b27372201a3046de
86e299fb5e1494bfe35e3539d7e411c0f980303f38812d8d69a7e4d62435206a
88184bcd2c4f2f689195bbb1c2ef767f107e609e0697504646e72e144c283990
89ad661569855831bc6ebfb1f33d6e2f10ee1dfff1034b03b4bf3bf9ad0e9897
8aee3d375bbea9347b2e76d5ab83a655eb96c0c88af3cb411b420b8755d33755
8c79c70c1389ab8c5ac169ceb45eb4bae54e2489b73ea3bb9e267441d2df27d3
8ece9445091300df5a089f323118a59d2990ab0e07f70aff7adc63ba38bc9fa0
8ed84a3f1a3154aceed4cc0cc82712dd7a77ffccff12489606787bb528053ea5
8f84f1a32107939d5d7157cc2861f802b0df40ca0df5f98a29d18ad2f27c7956
92e493e9e2ec54e1f0cc4600f1284742d28b2f19c1a38a63a34712d64c855752
93468c8e3c4411a4c1fb775a286d269b1804cf9ac75a9815eb8cb1c9158d533c
94cc4e792f0afb79ca0b1fb67ae656e581efd449bd9ea339a8627606904d1535
950f63b987a7482abde46173aecb04d8fe695b8c44051170ea53e4534687c0f0
954e46311d66fefa1d129287d87b69ceca4636095da9b1881aad4450efdcb845
955ed10b73d6265b1adcf768b94f8dd5d91f33309db94b6b3af4efa822f1d9af
95aef18ba7d344a0eed16913a1d4a4b8c290443bff8bee8f6ed54e2b0c0ad68b
97c6b9a0f5bec2738f8cc9ce084d6c5178abe6357a8f14d6bdbcc2fcf24b8ec3
9d81e50c8146f2bf7640f7cc199849bf970d828d06ae23702b49956ab568e520
9ec2a3d32b383c54ad4126b54d470fdf36fa0f843bca0b90703e6ca598462d4c
a0397533c040e31c26e59114213f35e27b7d36caac2b4756a98c6e6ad4fcbf2d
a1087dc1786f52158d18121a90de8187ed743694613df8dc6702bcb06c423cf1
a35b734bdba52a9ebab0206c73d3727e5aa92366356c30526154a9a8a99e1ffd
a63281945cc50d0b6a515ca07afcd654cddfe56be3c50b5281a43ccbf401f548
b0187187ee00c608ae6d56022c47737302de12dbea68e80c2f4941253137fbce
b6b14b32ea4ad9f97eea77d489045bd7dd667f2e37698304d856eb5cb2ebcf9c
b6e2aba1065730c9e65d7019f099546235b3f3b607973e7c1825269d857f5cfd
bc29350c7c73264be1faae58243f8ea14ea991e14d405b5c275f1df38609178a
bf3356df7ad1ad242d997adc87f2d6f173d0b2f9a5d464bb2c64b6062e270bab
c46f2f838995e373a989807c734fb38aa424b26ff2b8709d2a9fd4ca01e749c5
cd50fc998e7e535b8908c8efc8233cfc25ffa79ed9067abf1ebd32c52ffc87df
d4889ea5dd837692f32b7385f8d599f5bed1d608bf5ba55554b5e813926e7f1c
d9d1fd8427b98dadb0dd4a72a65e250cc6565f3aa45cb2753969228217026111
da2d62d3c46b958b3a910fdbd4089032537a3ab9b0e878b9af24b8cbe237593d
db384e2c3bdd16a3479dfd52e2ca8dc8527e715dbe90c59cd8a8e569dce7244d
dc98a121f51a09a77577e7014eb13be63b62fc544bc61954926d914b10a5b0e2
ded8efd546894cb514202bb1dabd2b98a3cc0436e9a07398fbbd5884869f6623
e00b38ee9ef76a3bc1ec63c997778d9e354f7cb3446c2384c8985cea282bb08d
e0a5aad559be20dd54a38addc7b7299fc7226f27e3b41ccba948d708ba9e3d57
ea16d29ae8dd60085f5f1136105a8bce3da9ff9028ea9a4d153dd53010e48fcc
ed22b802619c6cb6f4cd98b528c328fb45a974424a7f1c0f09757e4ee06d9240
ed90afe4a68387262499379f3c229fcfe7c0ba739a7c23e7d3cd8b0279a431f7
f03d5367ff261d040a825d09bdc487afb3c933931884b8a8c6b1326fc24dcb8f
f8147505536f59bcca3cc83c741ae67867d139e28c4d0408628b1f985c011c79
fa3cd45d0ae8af0cde6fe49361e73f50e87ffc8ef3edc02d2c937d90155edaa1
fe7b084064bb7fec526f5c881967a217e0cb904b0f8de70f840041d6e31a08e2
ff18f71be5c2715a788edea08bc31c7f8b6cbc452ca08e4a241fe36de8c43f6b
`) {
		set[h] = true
	}
	return set
}()

var (
	nameRun   = regexp.MustCompile(`[A-Za-z0-9_]+(?:[-.@][A-Za-z0-9_]+)*`)
	namePiece = regexp.MustCompile(`[A-Za-z0-9_]+|[-.@]`)
)

// tokensOf splits a page into the names it could show: runs of letters and
// digits joined by dashes, dots or @ ("hel-1", "ana.example.com"). It returns
// each run and every stretch of whole pieces inside it, so "kit@studio-agency"
// also gives "studio-agency" and "studio". Everything is lowercase.
func tokensOf(s string) []string {
	var out []string
	for _, run := range nameRun.FindAllString(s, -1) {
		var words, seps []string
		for _, p := range namePiece.FindAllString(run, -1) {
			if strings.ContainsAny(p, "-.@") {
				seps = append(seps, p)
			} else {
				words = append(words, p)
			}
		}
		for i := range words {
			for j := i; j < len(words); j++ {
				var b strings.Builder
				b.WriteString(words[i])
				for k := i; k < j; k++ {
					b.WriteString(seps[k])
					b.WriteString(words[k+1])
				}
				out = append(out, strings.ToLower(b.String()))
			}
		}
	}
	return out
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// sampleNamesIn says which names of the denylist a page shows, as the first
// 12 digits of their hashes, so a failure doesn't print the name either.
func sampleNamesIn(page string) []string {
	var found []string
	for _, tok := range tokensOf(page) {
		if h := hashOf(tok); sampleHashes[h] {
			found = append(found, h[:12])
		}
	}
	return found
}

func TestTokensOf(t *testing.T) {
	got := tokensOf(`<a href="/apps/hel-1">Kit@studio-agency</a> v7`)
	set := map[string]bool{}
	for _, tok := range got {
		set[tok] = true
	}
	for _, want := range []string{"hel-1", "hel", "1", "kit@studio-agency", "studio-agency", "kit@studio", "studio", "agency", "kit", "v7", "apps"} {
		if !set[want] {
			t.Errorf("tokensOf lacks %q: %v", want, got)
		}
	}
	if len(sampleHashes) < 50 {
		t.Errorf("the denylist has %d entries, want the handoff's names", len(sampleHashes))
	}
	for h := range sampleHashes {
		if len(h) != 64 {
			t.Errorf("denylist entry %q is not a SHA-256 hex digest", h)
		}
	}
	if len(sampleNamesIn("<p>whoami v7</p>")) != 0 {
		t.Error("the denylist flags the names the tests use")
	}
}

// consoleData is what a populated test install holds, for the checks that a
// page shows nothing else.
type consoleData struct {
	Apps     map[string]bool // app IDs
	Versions map[string]bool // "v7"
	Digests  []string        // full digests an image may be pinned to
	Hosts    map[string]bool // domains of the apps
}

// populatedConsole is an install with four apps: the standard three, and shop
// with a deployment of every status, a webhook token and the first page of its
// history cut short, so "Show older" appears. whoami has copies with log output.
func populatedConsole(t *testing.T) (*consoleEnv, consoleData) {
	t.Helper()
	e := newConsoleEnv(t)
	e.Server.deployPage = 3
	e.seedStandard(t)
	e.seed(t, shopApp(),
		seedDeploy{ID: "s1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1},
		seedDeploy{ID: "s2", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 2},
		seedDeploy{ID: "s3", Trigger: app.TriggerWebhook, Status: store.StatusReverted, Version: 3,
			Error: "Failed due to unhealthy allocations - rolling back to job version 2",
			Warns: []app.Warning{{Field: "services[0].image", Message: `tag "2.1" is ignored: the digest pins the image`}}},
		seedDeploy{ID: "s4", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: "image not found, or private"},
		seedDeploy{ID: "s5", Trigger: app.TriggerManual, Status: store.StatusSuperseded, Version: 4, Error: "Cancelled due to newer version of job"},
		seedDeploy{ID: "s6", Trigger: app.TriggerConsole, Status: store.StatusRunning, Version: 5})
	for range 6 {
		e.Nomad.AddForeignJob(&api.Job{ID: new("shop")})
	}
	e.Nomad.Script("shop", 5, deploytest.Rolling("Deployment is running", map[string]api.DeploymentState{
		"web": {DesiredTotal: 2, PlacedAllocs: 1, HealthyAllocs: 1},
	}))
	e.copies("shop", "web", 5, 1, 1)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: logCopyA, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-1"},
		api.AllocationListStub{ID: logCopyB, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeName: "node-2"})
	e.Nomad.WriteLog(logCopyA, "stdout", "GET / 200\n")
	e.Nomad.WriteLog(logCopyB, "stderr", "warning: slow\n")

	data := consoleData{Apps: map[string]bool{}, Versions: map[string]bool{}, Hosts: map[string]bool{"caravana.example.com": true},
		Digests: []string{sampleDigest, otherDigest}}
	recs, err := e.Store.ListApps(context.Background())
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	for _, rec := range recs {
		data.Apps[rec.App.ID] = true
		for _, svc := range rec.App.Services {
			for _, d := range svc.Domains {
				data.Hosts[d.Host] = true
			}
		}
		for _, d := range e.deployments(t, rec.App.ID) {
			if d.JobVersion != nil {
				data.Versions["v"+strconv.FormatUint(*d.JobVersion, 10)] = true
			}
		}
	}
	return e, data
}

// consolePages renders every page and part of the console for the apps named,
// plus the pages of an app that isn't there, and the error pages the forms
// answer with. Each page is named, so a failure says which.
func consolePages(t *testing.T, e *consoleEnv, ids ...string) map[string]string {
	t.Helper()
	pages := map[string]string{}
	get := func(name, path string) string {
		t.Helper()
		resp, body := e.get(t, path)
		if resp.StatusCode >= 500 || !strings.Contains(body, "<") {
			t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, body)
		}
		pages[name] = body
		return body
	}
	for _, path := range []string{"/apps", "/apps/new", "/settings", "/settings/registries", "/settings/backup-storage", "/settings/notifications", "/settings/tokens", "/parts/apps", "/parts/activity", "/apps?deleted=gone",
		"/domains", "/parts/domains", "/domains?app=nope"} {
		get(path, path)
	}
	for _, id := range append(append([]string{}, ids...), "nope") {
		for _, tab := range []string{"", "/deployments", "/configure", "/logs", "/variables", "/domains", "/backups"} {
			get("/apps/"+id+tab, "/apps/"+id+tab)
		}
		for _, part := range []string{"header", "copies", "deployments"} {
			get("/parts/apps/"+id+"/"+part, "/parts/apps/"+id+"/"+part)
		}
	}
	// The history's second page, where there is one.
	for _, id := range ids {
		if m := regexp.MustCompile(`href="(/apps/` + id + `/deployments\?before=[^"]+)"`).FindStringSubmatch(pages["/apps/"+id+"/deployments"]); m != nil {
			get("older "+id, strings.ReplaceAll(m[1], "&amp;", "&"))
		}
	}
	return pages
}

// formPages are the pages the forms answer with when something is wrong or
// when something is shown once: a refused New app, a refused deploy, a refused
// delete, a new webhook URL, a new API token and a refused password.
func formPages(t *testing.T, e *consoleEnv, id string) map[string]string {
	t.Helper()
	pages := map[string]string{}
	post := func(name, path string, form url.Values) {
		t.Helper()
		resp, body := e.postForm(t, path, form)
		if resp.StatusCode >= 500 || !strings.Contains(body, "<html") {
			t.Fatalf("POST %s = %d:\n%s", path, resp.StatusCode, body)
		}
		pages[name] = body
	}
	post("new app, errors", "/apps", postedForm("Not A Name", "", "99999", "bad host", "x"))
	post("new webhook", "/apps/"+id+"/webhook", nil)
	post("deploy, errors", "/apps/"+id+"/deploy", url.Values{"image.web": {"Not A Ref!!"}})
	post("delete, mismatch", "/apps/"+id+"/delete", url.Values{"confirm": {"other"}})
	post("new token", "/settings/tokens", url.Values{"name": {"ci"}})
	post("password, errors", "/settings/password", url.Values{"currentPassword": {"x"}, "newPassword": {"y"}})
	post("registry, errors", "/settings/registries", url.Values{"host": {"acme"}, "username": {"u"}})
	post("backup destination, errors", "/settings/backups", url.Values{"endpoint": {"ftp://x"}, "region": {"r"}, "bucket": {"backups"}, "accessKeyId": {"AK"}})
	post("new signing secret", "/settings/notifications", url.Values{"kind": {"webhook"}, "name": {"hook"}, "url": {"https://example.com/hook"}, "events": {"problems"}})
	post("channel, errors", "/settings/notifications", url.Values{"kind": {"ntfy"}, "name": {""}, "url": {"nope"}})
	// Check now answers 303 to the page that draws the check.
	if resp, _ := e.postForm(t, "/domains", url.Values{"action": {"check"}, "host": {"new.example.com"}}); resp.StatusCode == http.StatusSeeOther {
		page, _, _ := strings.Cut(location(t, resp), "#")
		_, pages["domain, checked"] = e.get(t, page)
	} else {
		t.Fatalf("POST /domains (Check now) = %d, want 303", resp.StatusCode)
	}
	post("domain, errors", "/domains", url.Values{"action": {"add"}, "host": {"Bad Host"}})
	return pages
}

var (
	appLink     = regexp.MustCompile(`href="/apps/([^"/?#]+)`)
	versionText = regexp.MustCompile(`\bv([0-9]+)\b`)
	digestText  = regexp.MustCompile(`sha256:[0-9a-f]{6,}`)
	httpsLink   = regexp.MustCompile(`href="https://([^"/]+)`)
)

// Every name, version, digest and address on a page comes from the store or
// Nomad, and none comes from the design handoff (spec FR-004, SC-007).
func TestNoSampleData(t *testing.T) {
	check := func(t *testing.T, pages map[string]string, data consoleData, requested ...string) {
		t.Helper()
		names := make([]string, 0, len(pages))
		for name := range pages {
			names = append(names, name)
		}
		sort.Strings(names)
		asked := map[string]bool{"new": true}
		for _, r := range requested {
			asked[r] = true
		}
		for _, name := range names {
			body := pages[name]
			if found := sampleNamesIn(body); len(found) > 0 {
				t.Errorf("page %q shows names from the design handoff's sample data (hashes %v)", name, found)
			}
			for _, m := range appLink.FindAllStringSubmatch(body, -1) {
				if !data.Apps[m[1]] && !asked[m[1]] {
					t.Errorf("page %q links to app %q, which isn't in the store", name, m[1])
				}
			}
			// "/v1/" is the API's path, and "v0.7.0" in the sidebar is Caravana's
			// own version: neither is a deployment's.
			plain := strings.ReplaceAll(strings.ReplaceAll(text(body), "/v1/", "/"), "v0.7.0", "")
			for _, m := range versionText.FindAllString(plain, -1) {
				if !data.Versions[m] {
					t.Errorf("page %q shows version %s, which no deployment has", name, m)
				}
			}
			for _, m := range digestText.FindAllString(body, -1) {
				known := false
				for _, d := range data.Digests {
					known = known || strings.HasPrefix(d, m)
				}
				if !known {
					t.Errorf("page %q shows digest %s, which no image was pinned to", name, m)
				}
			}
			for _, m := range httpsLink.FindAllStringSubmatch(body, -1) {
				if !data.Hosts[m[1]] {
					t.Errorf("page %q links to https://%s, which isn't an app's domain", name, m[1])
				}
			}
		}
	}

	t.Run("empty install", func(t *testing.T) {
		e := newConsoleEnv(t)
		pages := consolePages(t, e)
		for name, body := range formPages(t, e, "nope") {
			pages[name] = body
		}
		check(t, pages, consoleData{Hosts: map[string]bool{"caravana.example.com": true}}, "nope")
		// An empty install says it is empty and offers the next step; it lists nothing.
		if body := text(pages["/apps"]); !strings.Contains(body, "No apps yet.") || strings.Contains(pages["/apps"], `href="/apps/whoami`) {
			t.Errorf("the empty Apps page: %s", body)
		}
		if !strings.Contains(text(pages["/parts/activity"]), "Nothing has been deployed yet.") {
			t.Errorf("the empty activity rail: %s", text(pages["/parts/activity"]))
		}
	})

	t.Run("populated install", func(t *testing.T) {
		e, data := populatedConsole(t)
		ids := []string{"whoami", "blog", "api", "shop"}
		pages := consolePages(t, e, ids...)
		for name, body := range formPages(t, e, "shop") {
			pages[name] = body
		}
		for _, name := range []string{"older shop", "/apps/shop/configure"} {
			if pages[name] == "" {
				t.Fatalf("page %q wasn't rendered", name)
			}
		}
		check(t, pages, data, "nope")
		// The data really is on the pages: this guards the test against passing on empty ones.
		all := ""
		for _, body := range pages {
			all += text(body) + "\n"
		}
		for _, want := range []string{"whoami", "blog", "shop", "v7", "whoami.example.com", "sha256:c4717a8d1f01"} {
			if !strings.Contains(all, want) {
				t.Errorf("no page shows %q, so the populated install wasn't rendered", want)
			}
		}
	})

	t.Run("the denylist catches a copied name", func(t *testing.T) {
		probe := hashOf("probe-name")
		sampleHashes[probe] = true
		defer delete(sampleHashes, probe)
		if got := sampleNamesIn(`<a href="/apps/probe-name">Probe-Name</a>`); len(got) < 2 {
			t.Errorf("sampleNamesIn finds %v, want the probe in the link and in the text", got)
		}
	})
}
