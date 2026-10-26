package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The minute's refresh never swaps the live region as a whole. The page's region says `hx-swap="none"`
// and what it fetches is out-of-band fragments by id: the issues, each chart, the table's meta, the
// notices, and each row (or the whole table body when the rows changed). The sort and range bars are
// not in the answer, so keyboard focus and an open hover survive.
func TestMonitoringLiveSwapsByID(t *testing.T) {
	e := monEnv(t)
	_, page := e.get(t, "/monitoring")
	for _, want := range []string{
		`id="mon-live" hx-get="/parts/monitoring" hx-trigger="sse:metrics" hx-swap="none" hx-include="#mon-known"`,
		`id="mon-known" name="known" value="`, `id="mon-notices"`, `id="mon-issues"`, `id="mon-table-slot"`, `id="mon-table-meta"`,
		`id="mon-row-whoami-web"`, `id="mon-row-whoami-web-link"`, `id="mon-row-whoami-web-logs"`, `id="mon-issue-whoami-web-memory-act"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The source notices are above the live region, not in it, and are not a live region themselves.
	if strings.Index(page, `id="mon-notices"`) > strings.Index(page, `id="mon-live"`) {
		t.Error("the notices are inside the live region")
	}
	if strings.Contains(page, `role="status" class="cv-notice"`) {
		t.Error("a notice is a live region: a screen reader would announce it again every minute")
	}
	// The sort bar is in the page and in no answer.
	if !strings.Contains(page, `aria-label="Sort"`) {
		t.Fatal("the page has no sort bar")
	}
	key := regexp.MustCompile(`id="mon-known" name="known" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if key == nil {
		t.Fatal("no key of the rows the page was drawn with")
	}

	// The same rows: each row alone, by id; no table body, no bars, no whole region.
	_, same := e.get(t, "/parts/monitoring?known="+key[1])
	for _, want := range []string{
		`hx-swap-oob="innerHTML:#mon-notices"`, `hx-swap-oob="innerHTML:#mon-issues"`, `hx-swap-oob="innerHTML:#mon-chart-requests"`,
		`hx-swap-oob="innerHTML:#mon-chart-errors"`, `hx-swap-oob="innerHTML:#mon-chart-p95"`, `hx-swap-oob="innerHTML:#mon-chart-memory"`,
		`hx-swap-oob="innerHTML:#mon-table-meta"`, `hx-swap-oob="innerHTML:#mon-row-whoami-web"`,
		`<input type="hidden" id="mon-known" name="known" value="` + key[1] + `" hx-swap-oob="true">`,
	} {
		if !strings.Contains(same, want) {
			t.Errorf("the refresh for the same rows lacks %q:\n%s", want, same[:min(len(same), 800)])
		}
	}
	for _, bad := range []string{`hx-swap-oob="innerHTML:#mon-table-slot"`, `aria-label="Sort"`, `aria-label="Time range"`, `id="mon-live"`, `<html`, `class="cv-mnfilter"`, `class="cv-mncharts"`} {
		if strings.Contains(same, bad) {
			t.Errorf("the refresh for the same rows holds %q", bad)
		}
	}

	// Rows that changed (a stale key, or none): the table body as a whole, still by id.
	for _, path := range []string{"/parts/monitoring?known=stale", "/parts/monitoring"} {
		_, whole := e.get(t, path)
		if !strings.Contains(whole, `hx-swap-oob="innerHTML:#mon-table-slot"`) || !strings.Contains(whole, `class="cv-mnrow" role="row" id="mon-row-whoami-web"`) ||
			strings.Contains(whole, `hx-swap-oob="innerHTML:#mon-row-whoami-web"`) {
			t.Errorf("%s: the changed rows don't come as a whole body:\n%s", path, text(whole))
		}
	}
	if resp, _ := e.get(t, "/parts/monitoring?known="+key[1]+"&range=2h"); resp.StatusCode != 400 {
		t.Errorf("a bad range with a key = %d, want 400", resp.StatusCode)
	}
}

// The key names the rows in their order, so a row that moved is a change.
func TestMonRowsKey(t *testing.T) {
	a, b := monRow{Anchor: "mon-row-a-web"}, monRow{Anchor: "mon-row-b-web"}
	if rowsKey([]monRow{a, b}) != rowsKey([]monRow{{Anchor: "mon-row-a-web"}, {Anchor: "mon-row-b-web"}}) || rowsKey([]monRow{a, b}) == rowsKey([]monRow{b, a}) ||
		rowsKey([]monRow{a}) == rowsKey([]monRow{a, b}) || rowsKey(nil) == rowsKey([]monRow{a}) {
		t.Error("rowsKey doesn't tell the rows and their order apart")
	}
}

// Notices carry no role="status" and the answer leaves an empty container empty.
func TestMonNoticesAreNotLive(t *testing.T) {
	s := newTestServer(t, newUIEnv(t, true, ""))
	render := func(reg *monRegion) string {
		var sb strings.Builder
		if err := s.pages["monitoring"].ExecuteTemplate(&sb, "mon-notices-body", reg); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	if got := render(&monRegion{}); got != "" {
		t.Errorf("no notices render %q, want nothing (the container is hidden while empty)", got)
	}
	got := render(&monRegion{Notices: []monNotice{{Mark: "warning", Text: nomadDeniedSentence()}}})
	if !strings.Contains(got, `class="cv-notice"`) || strings.Contains(got, "role=") {
		t.Errorf("notice = %s", got)
	}
}

// The CPU cell prints its figure beside the sparkline, in vCPU, used over reserved.
func TestMonCPUFigure(t *testing.T) {
	b := monBuild(service.MetricsView{Range: metrics.Range6h, At: monNow}, false)
	b.coreMHz = 1000
	for _, tc := range []struct {
		now      float64
		reserved *int64
		want     string
	}{
		{400, i64(500), "0.4/0.5 vCPU"},
		{40, i64(100), "0.04/0.1 vCPU"},
		{1200, i64(2000), "1.2/2 vCPU"},
		{400, nil, "0.4 vCPU"},
	} {
		if got := b.cpuRead(tc.now, tc.reserved); got != tc.want {
			t.Errorf("cpuRead(%v, %v) = %q, want %q", tc.now, tc.reserved, got, tc.want)
		}
	}
	b.coreMHz = 0
	if got := b.cpuRead(40, i64(100)); got != "40/100 MHz" {
		t.Errorf("without the host's cores = %q", got)
	}
}
