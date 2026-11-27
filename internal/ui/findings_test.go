package ui

import (
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Image findings at a glance (task 2npj): the counts, the Security group, the headline and the tag.

// vulnItem is one image_vulnerable item: a package with a fixable finding in an app of a place.
func vulnItem(id, project, env, name, pkg string, critical, kev bool) service.AttentionItem {
	it := service.AttentionItem{Kind: service.AttentionImageVulnerable, AppID: id, Service: "web", Package: pkg,
		Project: project, Environment: env, AppName: name, Level: metrics.DiskWarning, KEV: kev}
	if critical {
		it.Level = metrics.DiskFailed
	}
	return it
}

// placed is a summary of an app in a place.
func placed(sum service.AppSummary, project, env, name string) service.AppSummary {
	sum.Place = app.Place{Project: project, Environment: env, Name: name}
	return sum
}

func TestFindingsAreCountedByPackage(t *testing.T) {
	items := []service.AttentionItem{
		vulnItem("blog", "", "", "", "zlib", true, true),
		vulnItem("blog", "", "", "", "curl", true, false),
		vulnItem("blog", "", "", "", "libxml2", false, false),
		vulnItem("api", "", "", "", "curl", false, false),
		{Kind: service.AttentionDeployFailed, AppID: "api"},
		{Kind: service.AttentionNoBackupDestination},
	}
	got := findingsOf(items)
	if len(got) != 2 || got["blog"] != (findings{Critical: 2, High: 1, KEV: 1}) || got["api"] != (findings{High: 1}) {
		t.Errorf("findingsOf = %+v", got)
	}
	if got["blog"].worst() != "critical" || got["api"].worst() != "high" || (findings{}).worst() != "" {
		t.Error("worst level is wrong")
	}
	if listed := listedApps(items); len(listed) != 2 || !listed["api"] || listed["blog"] || !listed[""] {
		t.Errorf("listedApps = %v: only an app with an item that is trouble is listed", listed)
	}
}

func TestFindingsTag(t *testing.T) {
	tests := []struct {
		name string
		f    findings
		want *findingsTag // nil: nothing is drawn
	}{
		{"none", findings{}, nil},
		{"critical wins and counts critical packages", findings{Critical: 9, High: 4, KEV: 1},
			&findingsTag{Mark: "failed", Level: "critical", Count: 9, Href: "/apps/blog/deployments#security", Words: "9 critical packages with a fixable vulnerability"}},
		{"high only", findings{High: 4},
			&findingsTag{Mark: "warning", Level: "high", Count: 4, Href: "/apps/blog/deployments#security", Words: "4 high packages with a fixable vulnerability"}},
		{"one package", findings{Critical: 1},
			&findingsTag{Mark: "failed", Level: "critical", Count: 1, Href: "/apps/blog/deployments#security", Words: "1 critical package with a fixable vulnerability"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tagOf("blog", tc.f)
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Errorf("tagOf = %+v, want %+v", got, tc.want)
			}
		})
	}
	if tags := tagsOf(map[string]findings{"blog": {}, "api": {High: 1}}); len(tags) != 1 || tags["api"] == nil {
		t.Errorf("tagsOf = %v, want only api", tags)
	}
}

func TestSecurityGroup(t *testing.T) {
	pl := places{Projects: []service.Project{{ID: "shop", DisplayName: "Shop"}}}
	tests := []struct {
		name      string
		items     []service.AttentionItem
		admin     bool
		wantMark  string
		wantHead  string // the lead and the rest, as written
		wantLines []string
		wantOps   bool
	}{
		{"no findings, no group", []service.AttentionItem{{Kind: service.AttentionDeployFailed, AppID: "blog"}}, true, "", "", nil, false},
		{"one app is named", []service.AttentionItem{
			vulnItem("uptime-kuma", "", "", "", "zlib", true, true), vulnItem("uptime-kuma", "", "", "", "curl", true, false),
			vulnItem("uptime-kuma", "", "", "", "ssl", true, false)},
			true, "failed", "uptime-kuma has fixable vulnerabilities: 3 critical packages, 1 known exploited.",
			[]string{"uptime-kuma | 3 critical, 1 known exploited"}, true},
		{"several apps are counted, the worst first, high packages named, no known exploited when none", []service.AttentionItem{
			vulnItem("b", "", "", "", "zlib", false, false),
			vulnItem("a", "", "", "", "zlib", true, false), vulnItem("a", "", "", "", "curl", false, false),
			vulnItem("shop--staging--web", "shop", "staging", "web", "zlib", true, false)},
			false, "failed", "3 apps have fixable vulnerabilities: 2 critical packages, 2 high packages.",
			[]string{"a | 1 critical, 1 high", "Shop › staging › web | 1 critical", "b | 1 high"}, false},
		{"known exploited first, then critical, then high, then the label as it reads", []service.AttentionItem{
			vulnItem("zed", "", "", "", "zlib", true, false), vulnItem("zed", "", "", "", "curl", true, false),
			vulnItem("kev", "", "", "", "zlib", true, true),
			vulnItem("shop--staging--web", "shop", "staging", "web", "zlib", true, false),
			vulnItem("alpha", "", "", "", "zlib", true, false), vulnItem("low", "", "", "", "zlib", false, false)},
			false, "failed", "5 apps have fixable vulnerabilities: 5 critical packages, 1 high package, 1 known exploited.",
			[]string{"kev | 1 critical, 1 known exploited", "zed | 2 critical", "alpha | 1 critical", "Shop › staging › web | 1 critical", "low | 1 high"}, false},
		{"only high is a warning, a package is singular", []service.AttentionItem{vulnItem("a", "", "", "", "zlib", false, false)},
			false, "warning", "a has fixable vulnerabilities: 1 high package.", []string{"a | 1 high"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := securityGroupOf(tc.items, pl, tc.admin)
			if tc.wantHead == "" {
				if g != nil {
					t.Fatalf("group = %+v, want none", g)
				}
				return
			}
			if g == nil {
				t.Fatal("no group")
			}
			if head := g.Lead.String() + g.Rest(); head != tc.wantHead || g.Mark != tc.wantMark {
				t.Errorf("head = %q (%s), want %q (%s)", head, g.Mark, tc.wantHead, tc.wantMark)
			}
			var lines []string
			for _, l := range g.Apps {
				lines = append(lines, l.Label.String()+" | "+strings.Join(l.Counts, " "))
				if want := "/deployments#security"; !strings.HasSuffix(l.Href, want) {
					t.Errorf("link = %q, want one ending %s", l.Href, want)
				}
			}
			if strings.Join(lines, "\n") != strings.Join(tc.wantLines, "\n") {
				t.Errorf("lines = %q, want %q", lines, tc.wantLines)
			}
			if (g.OperatorHref == "/ops/security") != tc.wantOps || (g.OperatorHref != "") != tc.wantOps {
				t.Errorf("operator link = %q, want it %v", g.OperatorHref, tc.wantOps)
			}
		})
	}
}

// The list counts the Security group as one row, wherever a count of rows is told.
func TestAttentionCountsTheGroupAsOneRow(t *testing.T) {
	r := attentionRegion{Rows: make([]attentionRow, 2)}
	if r.Count() != 2 {
		t.Errorf("Count = %d, want 2", r.Count())
	}
	r.Security = &securityGroup{}
	if r.Count() != 3 {
		t.Errorf("Count with the group = %d, want 3: the group is one row", r.Count())
	}
	if (attentionRegion{Security: &securityGroup{}}).Count() != 1 {
		t.Error("a group alone is one row")
	}
}

func TestHeadlineKeepsFindingsOutOfTrouble(t *testing.T) {
	v7 := new(uint64(7))
	ok := func(id string) service.AppSummary { return summary(id, service.StateHealthy, 1, 1, v7, nil) }
	degraded := func(id string) service.AppSummary { return summary(id, service.StateDegraded, 1, 2, v7, nil) }
	flag := func(ids ...string) (out []service.AttentionItem) {
		for _, id := range ids {
			out = append(out, vulnItem(id, "", "", "", "zlib", true, false))
		}
		return out
	}
	high := func(id string) service.AttentionItem { return vulnItem(id, "", "", "", "zlib", false, false) }

	tests := []struct {
		name  string
		in    []service.AppSummary
		items []service.AttentionItem
		want  string
	}{
		{"only findings: the apps are healthy, and a clause says how many have them",
			[]service.AppSummary{ok("a"), ok("b"), ok("c"), ok("d"), ok("e"), ok("f"), ok("g"), ok("h"), ok("i"), ok("j"), ok("k")}, flag("a", "b", "c"),
			"11 apps healthy. 3 apps have fixable critical vulnerabilities."},
		{"one", []service.AppSummary{ok("a"), ok("b")}, flag("a"), "2 apps healthy. 1 app has fixable critical vulnerabilities."},
		{"the worst across the apps is high", []service.AppSummary{ok("a"), ok("b")}, []service.AttentionItem{high("a"), high("b")},
			"2 apps healthy. 2 apps have fixable high vulnerabilities."},
		{"one critical among highs says critical", []service.AppSummary{ok("a"), ok("b")}, []service.AttentionItem{high("a"), flag("b")[0]},
			"2 apps healthy. 2 apps have fixable critical vulnerabilities."},
		{"after trouble, and the troubled app's findings count in the clause", []service.AppSummary{degraded("api"), ok("a")},
			append(flag("api", "a"), service.AttentionItem{Kind: service.AttentionRestarting, AppID: "api"}),
			"1 app needs you: api has 1 of 2 copies healthy. 1 app healthy. 2 apps have fixable critical vulnerabilities."},
		{"findings never hide an item that is trouble, in whichever order the list has them", []service.AppSummary{ok("a")},
			[]service.AttentionItem{flag("a")[0], {Kind: service.AttentionBackupFailed, AppID: "a"}},
			"1 app needs you: a has a failed backup. 1 app has fixable critical vulnerabilities."},
		{"nothing is said without any", []service.AppSummary{ok("a")}, nil, "1 app healthy."},
		{"a finding of an app not in the list is nobody's", []service.AppSummary{ok("a")}, flag("gone"), "1 app healthy."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := headlineOf(tc.in, tc.items).String(); got != tc.want {
				t.Errorf("headline = %q, want %q", got, tc.want)
			}
		})
	}

	// With no sentence of counts before it the clause says "apps".
	if got := findingsClause([]service.AppSummary{ok("a"), ok("b")}, flag("a", "b")).String(); got != "2 apps have fixable critical vulnerabilities." {
		t.Errorf("clause = %q", got)
	}
	if got := findingsClause([]service.AppSummary{ok("a")}, flag("a")).String(); got != "1 app has fixable critical vulnerabilities." {
		t.Errorf("clause = %q", got)
	}
}

func TestHeadlineNamesAppsByName(t *testing.T) {
	v7 := new(uint64(7))
	down := func(id, project, env, name string) service.AppSummary {
		return placed(summary(id, service.StateDegraded, 1, 2, v7, nil), project, env, name)
	}
	banana := down("banana--production--banana", "banana", "production", "banana")
	web1, web2 := down("shop--staging--web", "shop", "staging", "web"), down("shop--production--web", "shop", "production", "web")
	api := down("shop--staging--api", "shop", "staging", "api")
	tests := []struct {
		name string
		in   []service.AppSummary
		want string
	}{
		{"one app outside the home place", []service.AppSummary{banana}, "1 app needs you: banana has 1 of 2 copies healthy."},
		{"an app at home keeps its ID, which is its name", []service.AppSummary{down("blog", "default", "production", "blog")},
			"1 app needs you: blog has 1 of 2 copies healthy."},
		{"two apps with the name web say where", []service.AppSummary{web1, web2, api}, "3 apps need you: web (staging), web (production), api."},
		{"different names say nothing more", []service.AppSummary{banana, api}, "2 apps need you: banana, api."},
		{"the same name beyond the three shown is not told", []service.AppSummary{banana, api, down("x--staging--z", "x", "staging", "z"), web1, web2},
			"5 apps need you: banana, api, z and 2 more."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := headlineOf(tc.in, nil).String()
			if got != tc.want {
				t.Errorf("headline = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "--") {
				t.Errorf("headline %q names an app by its ID", got)
			}
		})
	}
	// An app's name is mono, and the environment is plain beside it.
	got := headlineOf([]service.AppSummary{web1, web2}, nil)
	if !got[1].Mono || got[1].Text != "web" || got[2].Mono || got[2].Text != " (staging)" {
		t.Errorf("parts = %+v", got)
	}
}

func TestFindingsCounts(t *testing.T) {
	f := findings{Critical: 14, High: 1, KEV: 2}
	for _, tc := range []struct {
		packages bool
		want     string
	}{{false, "14 critical, 1 high, 2 known exploited"}, {true, "14 critical packages, 1 high package, 2 known exploited"}} {
		if got := f.counts(tc.packages); got != tc.want {
			t.Errorf("counts(%v) = %q, want %q", tc.packages, got, tc.want)
		}
	}
	if got := (findings{}).counts(true); got != "" {
		t.Errorf("counts of none = %q", got)
	}
	// The phrases a narrow line may break between: the comma stays with its count.
	if got := f.phrases(false); strings.Join(got, "|") != "14 critical,|1 high,|2 known exploited" {
		t.Errorf("phrases = %q", got)
	}
	if got := (findings{}).phrases(true); got != nil {
		t.Errorf("phrases of none = %q", got)
	}
}

// Each count phrase is drawn whole, in the head and in an app's line, so "1 known exploited" never breaks.
func TestSecurityGroupKeepsEachCountWhole(t *testing.T) {
	g := securityGroupOf([]service.AttentionItem{vulnItem("a", "", "", "", "zlib", true, true)}, places{}, false)
	var b strings.Builder
	if err := newTestServer(t, newUIEnv(t, true, "")).pages["apps"].ExecuteTemplate(&b, "security-group", g); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<span class="cv-nowrap">1 critical package,</span>`, `<span class="cv-nowrap">1 known exploited</span>.`,
		`<span class="cv-nowrap">1 critical,</span> <span class="cv-nowrap">1 known exploited</span>`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the group lacks %s:\n%s", want, b.String())
		}
	}
}
