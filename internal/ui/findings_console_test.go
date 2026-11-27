package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Image findings at a glance (task 2npj), through the pages: the Security group last in the needs-you list, the
// headline that doesn't count findings as trouble, and the tag under an app's status. The findings are written
// into the store, as security_test.go does, so no scanner runs.

// scanFixableCritical installs the scanner and stores the one finding with a fix that every flagged app shares (they run the one scanned image).
func (e *consoleEnv) scanFixableCritical(t *testing.T) {
	t.Helper()
	e.setAddon(t, store.AddonInstalled, "")
	e.scanWith(t, secImageRef, secFindingFor("f1", "CVE-2024-0001", vulns.Critical, "zlib", "1.2.11-r3", "1.2.12-r0"))
}

// flagged seeds an app that runs the scanned image, live and healthy.
func (e *consoleEnv) flagged(t *testing.T, id string) {
	t.Helper()
	a := whoamiApp()
	a.ID = id
	a.Services[0].Domains[0].Host = strings.ReplaceAll(id, "--", "-") + ".example.com"
	e.seed(t, a, seedDeploy{ID: "d-" + id, Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	e.copies(id, "web", 7, 2, 0)
}

// needsRegion is the page's needs-you section, from its label to the next section.
func needsRegion(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, `aria-label="Needs you"`)
	end := strings.Index(page, `aria-label="Deploying now"`)
	if start < 0 || end < start {
		t.Fatalf("the page has no needs-you section:\n%s", page)
	}
	return page[start:end]
}

func TestSecurityGroupComesLastAndClosed(t *testing.T) {
	e := newConsoleEnv(t)
	e.scanFixableCritical(t)
	for _, id := range []string{"app-a", "app-b", "app-c", "app-d", "app-e"} {
		e.flagged(t, id)
	}
	// An app whose deploy failed: its row must come before the group, and it is the headline's trouble.
	e.seed(t, blogApp(), seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusFailed, Error: "no such image"})

	resp, page := e.get(t, "/apps")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps = %d", resp.StatusCode)
	}
	region := needsRegion(t, page)
	group := strings.Index(region, `id="needs-security"`)
	failed := strings.Index(region, `href="/apps/blog/deployments#deployment-b1"`)
	if group < 0 || failed < 0 || failed > group {
		t.Fatalf("the failed deploy (at %d) isn't before the Security group (at %d):\n%s", failed, group, region)
	}

	// One group, closed, with no package row; the five apps are lines inside it.
	open := regexp.MustCompile(`<details[^>]*id="needs-security"[^>]*>`).FindString(region)
	if open == "" || strings.Contains(open, " open") {
		t.Errorf("the group's tag = %q, want a <details> that is closed", open)
	}
	if n := strings.Count(region, `id="needs-security"`); n != 1 {
		t.Errorf("%d groups, want 1", n)
	}
	if !strings.Contains(region, "<summary") {
		t.Error("the group has no <summary>, so it wouldn't open without script")
	}
	body := region[group:]
	if n := strings.Count(body, ">Open findings</a>"); n != 5 {
		t.Errorf("%d lines in the group, want 5", n)
	}
	for _, id := range []string{"app-a", "app-b", "app-c", "app-d", "app-e"} {
		if !strings.Contains(body, `href="/apps/`+id+`/deployments#security"`) {
			t.Errorf("the group has no link for %s", id)
		}
	}
	got := text(body)
	for _, want := range []string{"5 apps have fixable vulnerabilities: 5 critical packages.", "1 critical"} {
		if !strings.Contains(got, want) {
			t.Errorf("the group lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "zlib") || strings.Contains(got, "CVE-2024-0001") {
		t.Errorf("the group has a package row:\n%s", got)
	}
	// An admin's head links to Operator › Security.
	if !strings.Contains(body, `href="/ops/security"`) || !strings.Contains(got, "Operator › Security") {
		t.Errorf("the owner's group lacks the Operator › Security link:\n%s", got)
	}
	// Findings aren't trouble: only the failed deploy is, and the five count as healthy.
	if got, want := h1Text(t, page), "1 app needs you: blog isn't running, and its last deploy failed. 5 apps healthy. 5 apps have fixable critical vulnerabilities."; got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}

	t.Run("the part draws the same group", func(t *testing.T) {
		_, part := e.get(t, "/parts/attention")
		if !strings.Contains(part, `id="needs-security"`) || strings.Count(part, ">Open findings</a>") != 5 {
			t.Errorf("/parts/attention:\n%s", part)
		}
	})
}

func TestSecurityGroupAloneIsNotNothing(t *testing.T) {
	e := newConsoleEnv(t)
	e.scanFixableCritical(t)
	e.flagged(t, "app-a")
	_, page := e.get(t, "/apps")
	if strings.Contains(page, "Nothing needs you.") {
		t.Error(`the page says "Nothing needs you." with a Security group in the list`)
	}
	if !strings.Contains(needsRegion(t, page), `id="needs-security"`) {
		t.Error("no group")
	}
	// One app: the app is named, in mono, and the headline's app is healthy.
	if got := text(needsRegion(t, page)); !strings.Contains(got, "app-a has fixable vulnerabilities: 1 critical package.") {
		t.Errorf("the group's sentence:\n%s", got)
	}
	if got, want := h1Text(t, page), "1 app healthy. 1 app has fixable critical vulnerabilities."; got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}
}

// rowOf is the Apps table's row of one app.
func (e *consoleEnv) rowOf(t *testing.T, id string) string {
	t.Helper()
	_, page := e.get(t, "/apps")
	for _, row := range strings.Split(page, `<div class="cv-row cv-apps-row"`)[1:] {
		if strings.Contains(row, `href="/apps/`+id+`"`) {
			return row
		}
	}
	t.Fatalf("the Apps table has no row for %s:\n%s", id, page)
	return ""
}

func TestFindingsTagUnderTheStatus(t *testing.T) {
	t.Run("with findings", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.scanFixableCritical(t)
		e.flagged(t, "app-a")
		row := e.rowOf(t, "app-a")
		i := strings.Index(row, "cv-c-status")
		j := strings.Index(row, "cv-c-name")
		if i < 0 || j < i {
			t.Fatalf("the row:\n%s", row)
		}
		cell := row[i:j]
		for _, want := range []string{`class="cv-find cv-find-critical"`, `href="/apps/app-a/deployments#security"`, `cv-mark cv-mark-failed`} {
			if !strings.Contains(cell, want) {
				t.Errorf("the status cell lacks %q:\n%s", want, cell)
			}
		}
		// Under the pill, in the same cell, and a count of packages.
		if p, f := strings.Index(cell, "cv-pill"), strings.Index(cell, "cv-find"); p < 0 || f < p {
			t.Errorf("the tag (%d) isn't after the pill (%d)", f, p)
		}
		if got := text(cell); !strings.Contains(got, "healthy 1 critical") {
			t.Errorf("the status cell reads %q, want the pill's word and then 1 critical", got)
		}
	})

	t.Run("without findings", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.setAddon(t, store.AddonInstalled, "")
		// Nothing with a fix: a critical that has none.
		e.scanWith(t, secImageRef, secFindingFor("f2", "CVE-2024-0002", vulns.Critical, "busybox", "1.35.0", ""))
		e.flagged(t, "app-a")
		_, page := e.get(t, "/apps")
		if strings.Contains(page, "cv-find") || strings.Contains(page, "needs-security") {
			t.Errorf("the page draws findings with none that has a fix:\n%s", text(page))
		}
	})

	t.Run("the scanner is not installed", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.scanWith(t, secImageRef, secFindingFor("f1", "CVE-2024-0001", vulns.Critical, "zlib", "1.2.11-r3", "1.2.12-r0"))
		e.flagged(t, "app-a")
		_, page := e.get(t, "/apps")
		if strings.Contains(page, "cv-find") || strings.Contains(page, "needs-security") || strings.Contains(h1Text(t, page), "vulnerabilities") {
			t.Errorf("the page draws findings without the scanner:\n%s", text(page))
		}
		if !strings.Contains(page, "Nothing needs you.") {
			t.Error(`the page doesn't say "Nothing needs you."`)
		}
	})

	t.Run("the apps part", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.scanFixableCritical(t)
		e.flagged(t, "app-a")
		_, part := e.get(t, "/parts/apps")
		if !strings.Contains(part, "cv-find cv-find-critical") || !strings.Contains(part, `id="needs-security"`) {
			t.Errorf("the apps part lacks the tag or the group:\n%s", part)
		}
	})
}

func TestFindingsOnTheProjectPage(t *testing.T) {
	e := newConsoleEnv(t)
	ctx := context.Background()
	if _, err := e.Svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	e.scanFixableCritical(t)
	e.flagged(t, "shop--staging--web")
	e.flagged(t, "app-home") // another place's: not in this rail

	resp, page := e.get(t, "/projects/shop?env=staging")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /projects/shop = %d", resp.StatusCode)
	}
	// The row is already one link: the tag is a span there, never a nested link.
	if !strings.Contains(page, `<span class="cv-find cv-find-critical">`) {
		t.Errorf("the project's row has no tag:\n%s", text(page))
	}
	if strings.Contains(page, `class="cv-find`) && regexp.MustCompile(`<a [^>]*class="cv-find`).MatchString(page) {
		t.Error("the tag is a link inside the row's link")
	}
	// The rail has the group alone, without the place in front of the app.
	if !strings.Contains(page, `id="needs-security"`) || !strings.Contains(page, "Needs you") {
		t.Fatalf("the rail lacks the group:\n%s", text(page))
	}
	rail := page[strings.Index(page, `id="needs-security"`):]
	rail = rail[:strings.Index(rail, "</details>")]
	if got := text(rail); strings.Contains(got, "Shop ›") || strings.Contains(got, "app-home") || !strings.Contains(got, "web has fixable vulnerabilities: 1 critical package.") {
		t.Errorf("the rail's group reads %q", got)
	}

	t.Run("the apps part carries the tag", func(t *testing.T) {
		_, part := e.get(t, "/parts/projects/shop/apps?env=staging")
		if !strings.Contains(part, `<span class="cv-find cv-find-critical">`) {
			t.Errorf("the apps part lacks the tag:\n%s", part)
		}
	})
	t.Run("the rail part carries the group", func(t *testing.T) {
		_, part := e.get(t, "/parts/projects/shop/rail?env=staging")
		if !strings.Contains(part, `id="needs-security"`) {
			t.Errorf("the rail part lacks the group:\n%s", part)
		}
	})
	t.Run("a project with none draws neither", func(t *testing.T) {
		_, page := e.get(t, "/projects/shop?env=production")
		if strings.Contains(page, "cv-find") || strings.Contains(page, "needs-security") {
			t.Errorf("production has no findings:\n%s", text(page))
		}
	})
}

func TestFindingsOnTheOperatorOverview(t *testing.T) {
	e := newConsoleEnv(t)
	e.scanFixableCritical(t)
	e.flagged(t, "app-a")
	e.flagged(t, "app-b")

	resp, page := e.get(t, "/ops")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops = %d", resp.StatusCode)
	}
	if n := strings.Count(page, `class="cv-find cv-find-critical"`); n != 2 {
		t.Errorf("%d tags in the apps table, want 2:\n%s", n, text(page))
	}
	if !strings.Contains(page, `id="needs-security"`) {
		t.Error("the Operator Overview's needs-you list lacks the group")
	}
	// The count beside "Needs you" counts what is drawn: the group is one.
	if !strings.Contains(page, `id="ops-needs-meta">1<`) {
		t.Errorf("the count beside Needs you isn't 1:\n%s", regexp.MustCompile(`ops-needs-meta[^<]*<`).FindString(page))
	}
	if strings.Contains(page, "Nothing needs you.") {
		t.Error(`the Operator Overview says "Nothing needs you." with a group`)
	}
	// Findings aren't trouble: the state filter's rows and the headline.
	if got := h1Text(t, page); !strings.Contains(got, "2 apps healthy. 2 apps have fixable critical vulnerabilities.") {
		t.Errorf("headline = %q", got)
	}
}

func TestMembersSeeTheirOwnAppsInTheGroup(t *testing.T) {
	e := newConsoleEnv(t)
	callers := walkPeople(t, e) // creates shop and other, shop › staging; owner, admin, member, ended guest
	e.scanFixableCritical(t)
	for _, id := range []string{"shop--staging--web", "shop--production--web", "other--production--web"} {
		e.flagged(t, id)
	}
	byName := map[string]*http.Cookie{}
	for _, c := range callers {
		byName[c.name] = c.cookie
	}

	member := func(path string) string {
		t.Helper()
		resp, body := e.do(t, request{path: path, cookies: []*http.Cookie{byName["member"]}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s as the member = %d", path, resp.StatusCode)
		}
		return body
	}
	page := member("/apps")
	region := needsRegion(t, page)
	if n := strings.Count(region, ">Open findings</a>"); n != 2 {
		t.Errorf("the member's group has %d lines, want 2 (shop's two):\n%s", n, text(region))
	}
	if strings.Contains(region, "other--production--web") || strings.Contains(text(region), "other") {
		t.Errorf("the member's group names the other project's app:\n%s", text(region))
	}
	if got := text(region); !strings.Contains(got, "2 apps have fixable vulnerabilities: 2 critical packages.") {
		t.Errorf("the member's group counts what isn't theirs:\n%s", got)
	}
	if strings.Contains(region, "/ops/security") || strings.Contains(text(region), "Operator › Security") {
		t.Error("a member's group links to Operator › Security")
	}
	if n := strings.Count(page, `class="cv-find cv-find-critical"`); n != 2 {
		t.Errorf("the member sees %d tags, want 2", n)
	}
	if strings.Contains(page, "other--production--web") {
		t.Error("the member's page names the other project's app")
	}

	// An admin sees all three and the link.
	_, adminPage := e.do(t, request{path: "/apps", cookies: []*http.Cookie{byName["admin"]}})
	region = needsRegion(t, adminPage)
	if n := strings.Count(region, ">Open findings</a>"); n != 3 || !strings.Contains(region, `href="/ops/security"`) {
		t.Errorf("the admin's group has %d lines (want 3) or lacks the link:\n%s", n, text(region))
	}
}
