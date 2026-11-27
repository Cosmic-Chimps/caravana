package ui

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Image findings (feature 044) are debt with a known fix, not something happening now. The console
// therefore draws them quietly: the needs-you list holds one Security group after every other item,
// the Apps headline keeps them out of "trouble", and an app's row has a small tag under its status.
// The counts come from the needs-you items the page already reads (one item per app, service and
// package, at or above the owner's notify level, none for a stopped app or without the scanner, and
// only the apps the viewer may see), so every place counts the same packages and the API is unchanged.

// findings counts one app's packages with a fixable finding: Critical and High by their worst advisory,
// KEV the packages with a known exploited advisory.
type findings struct{ Critical, High, KEV int }

// findingsOf counts the image_vulnerable items by app.
func findingsOf(items []service.AttentionItem) map[string]findings {
	out := map[string]findings{}
	for _, it := range items {
		if it.Kind != service.AttentionImageVulnerable || it.AppID == "" {
			continue
		}
		f := out[it.AppID]
		if it.Level == metrics.DiskFailed {
			f.Critical++
		} else {
			f.High++
		}
		if it.KEV {
			f.KEV++
		}
		out[it.AppID] = f
	}
	return out
}

// listedApps are the apps with an item on the needs-you list that is trouble: the findings aren't, so an
// app whose only items are findings counts by its run state, in the headline and in the state filter.
func listedApps(items []service.AttentionItem) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		if it.Kind != service.AttentionImageVulnerable {
			out[it.AppID] = true
		}
	}
	return out
}

// worst is the level of the worst finding: "critical", "high", or "" for none.
func (f findings) worst() string {
	switch {
	case f.Critical > 0:
		return "critical"
	case f.High > 0:
		return "high"
	}
	return ""
}

// add is the sum of two apps' counts.
func (f findings) add(g findings) findings {
	return findings{f.Critical + g.Critical, f.High + g.High, f.KEV + g.KEV}
}

// counts is "9 critical, 4 high, 1 known exploited": only the counts above zero. With packages it counts
// them by name: "9 critical packages, 4 high packages".
func (f findings) counts(packages bool) string {
	var words []string
	level := func(n int, word string) {
		switch {
		case n == 0:
		case packages:
			words = append(words, plural(n, word+" package"))
		default:
			words = append(words, fmt.Sprintf("%d %s", n, word))
		}
	}
	level(f.Critical, "critical")
	level(f.High, "high")
	if f.KEV > 0 {
		words = append(words, fmt.Sprintf("%d known exploited", f.KEV))
	}
	return strings.Join(words, ", ")
}

// findingsTag is the line under an app's status pill: the worst level's mark and its count, a link to
// the app's Security panel. It is nil for an app with no findings.
type findingsTag struct {
	Mark  string // the status kind of the mark: failed for critical, warning for high
	Level string // "critical" or "high"
	Count int
	Href  string
	Words string // what a screen reader hears
}

// tagOf is the tag of an app's findings; nil when there are none.
func tagOf(id string, f findings) *findingsTag {
	level := f.worst()
	if level == "" {
		return nil
	}
	t := &findingsTag{Mark: "warning", Level: level, Count: f.High, Href: "/apps/" + url.PathEscape(id) + "/deployments#security"}
	if level == "critical" {
		t.Mark, t.Count = "failed", f.Critical
	}
	t.Words = plural(t.Count, level+" package") + " with a fixable vulnerability"
	return t
}

// tagsOf is every app's tag by app ID.
func tagsOf(found map[string]findings) map[string]*findingsTag {
	out := make(map[string]*findingsTag, len(found))
	for id, f := range found {
		if t := tagOf(id, f); t != nil {
			out[id] = t
		}
	}
	return out
}

// securityGroup is the one row of the needs-you list that holds every image finding: a closed <details>
// whose summary says how many apps and packages, and whose body has a line per app.
type securityGroup struct {
	Mark string   // failed when any finding is critical, else warning
	Lead sentence // "3 apps", or the app's name: set bold
	Rest string   // " have fixable vulnerabilities: 14 critical packages, 2 known exploited."
	Apps []securityApp
	// OperatorHref is Operator › Security, for the installation's admins; "" for everyone else.
	OperatorHref string
}

// securityApp is one app's line inside the group.
type securityApp struct {
	Label  sentence // the app's place label, as the needs-you sentences write it
	Counts string   // "9 critical, 1 known exploited"
	Href   string
}

// securityGroupOf makes the group out of the image_vulnerable items; nil when there are none. admin says
// whether the viewer is the installation's admin (or Caravana itself, which has no principal).
func securityGroupOf(items []service.AttentionItem, pl places, admin bool) *securityGroup {
	found := findingsOf(items)
	if len(found) == 0 {
		return nil
	}
	first := map[string]service.AttentionItem{} // each app's first item: where the app is
	order := make([]string, 0, len(found))
	for _, it := range items {
		if _, ok := found[it.AppID]; ok && it.Kind == service.AttentionImageVulnerable {
			if _, seen := first[it.AppID]; !seen {
				first[it.AppID] = it
				order = append(order, it.AppID)
			}
		}
	}
	// The worst first, then by ID, so the order doesn't depend on the items'.
	slices.SortFunc(order, func(a, b string) int {
		fa, fb := found[a], found[b]
		return cmp.Or(cmp.Compare(fb.Critical, fa.Critical), cmp.Compare(fb.High, fa.High), cmp.Compare(a, b))
	})
	g := &securityGroup{Mark: "warning"}
	var sum findings
	for _, id := range order {
		sum = sum.add(found[id])
		g.Apps = append(g.Apps, securityApp{Label: appLabel(first[id], pl), Counts: found[id].counts(false),
			Href: "/apps/" + url.PathEscape(id) + "/deployments#security"})
	}
	if sum.Critical > 0 {
		g.Mark = "failed"
	}
	if len(order) == 1 {
		g.Lead = appLabel(first[order[0]], pl)
		g.Rest = " has fixable vulnerabilities: " + sum.counts(true) + "."
	} else {
		g.Lead = sentence{plain(fmt.Sprintf("%d apps", len(order)))}
		g.Rest = " have fixable vulnerabilities: " + sum.counts(true) + "."
	}
	if admin {
		g.OperatorHref = "/ops/security"
	}
	return g
}

// appLabel is where an app is, as the needs-you sentences say it: its ID in Default › production, else
// "<project> › <environment> › <name>" with the place muted (withoutPlace takes it away).
func appLabel(it service.AttentionItem, pl places) sentence {
	place := app.Place{Project: it.Project, Environment: it.Environment, Name: it.AppName}
	if atHome(place) || it.AppName == "" {
		return sentence{mono(it.AppID)}
	}
	return sentence{
		{Text: pl.display(place.Project) + " › ", Muted: true},
		{Text: place.Environment, Mono: true, Muted: true},
		{Text: " › ", Muted: true},
		mono(place.Name),
	}
}
