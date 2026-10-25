package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

const (
	copyA = "1111aaaa-0000-4000-8000-000000000001"
	copyB = "2222bbbb-0000-4000-8000-000000000002"
	copyD = "4444dddd-0000-4000-8000-000000000004"
)

// webStats is Nomad's stats for the service task "web" (the task is named after the service).
func webStats(ticks float64, usedMB uint64) api.AllocResourceUsage {
	return api.AllocResourceUsage{Timestamp: 1, Tasks: map[string]*api.TaskResourceUsage{"web": {ResourceUsage: &api.ResourceUsage{
		CpuStats:    &api.CpuStats{TotalTicks: ticks},
		MemoryStats: &api.MemoryStats{Usage: usedMB << 20, Measured: []string{"Usage"}},
	}}}}
}

// US4: CPU and memory in the Running tab's table. runningEnv's whoami uses the test deployer's
// defaults: 100 MHz and 64 MB reserved, a 256 MB ceiling.
func TestRunningTabShowsCPUAndMemory(t *testing.T) {
	e := runningEnv(t)
	e.Nomad.SetStats(copyA, webStats(42, 120))
	e.Nomad.SetStats(copyB, webStats(90, 240)) // over 90 % of 256 MB
	// copyD has no stats: Nomad answers 404 for it.

	_, body := e.get(t, "/apps/whoami")

	t.Run("columns CPU and Memory follow Up for", func(t *testing.T) {
		head := body[strings.Index(body, `<div class="cv-copies-head`):]
		head = head[:strings.Index(head, "</div>")]
		if got := text(head); got != "Copy Service Status Version Restarts Up for CPU Memory" {
			t.Errorf("the head reads %q", got)
		}
	})

	t.Run("a copy shows the number over what it is measured against, and a meter", func(t *testing.T) {
		row := copyRow(t, body, copyA)
		if got := text(row); !strings.Contains(got, "42 MHz of 100 MHz reserved 120 MB of 256 MB ceiling, 64 reserved") {
			t.Errorf("the row reads %q", got)
		}
		if !strings.Contains(row, `<meter class="cv-meter" min="0" max="100" value="46"`) {
			t.Errorf("the meter isn't 120 of 256 MB (46 %%):\n%s", row)
		}
		if strings.Contains(row, "close to its ceiling") || strings.Contains(row, "cv-meter-warn") {
			t.Errorf("a copy at 47 %% of its ceiling is marked:\n%s", row)
		}
	})

	t.Run("90 percent of the ceiling is marked with the mark and the words", func(t *testing.T) {
		row := copyRow(t, body, copyB)
		for _, want := range []string{"cv-meter-warn", `<span class="cv-oom"><span class="cv-mark cv-mark-warning" aria-hidden="true"></span>close to its ceiling</span>`} {
			if !strings.Contains(row, want) {
				t.Errorf("the row lacks %q:\n%s", want, row)
			}
		}
	})

	t.Run("a copy Nomad didn't answer for says so, and the others still show numbers", func(t *testing.T) {
		row := copyRow(t, body, copyD)
		if got := text(row); !strings.Contains(got, "Nomad didn't answer for this copy.") {
			t.Errorf("the row reads %q", got)
		}
		if strings.Contains(row, "<meter") {
			t.Errorf("a copy with no numbers has a meter:\n%s", row)
		}
		if !strings.Contains(body, "42 MHz") {
			t.Error("one copy failing hid the numbers of the others")
		}
	})

	t.Run("a copy that isn't running has a dash in both cells", func(t *testing.T) {
		for _, id := range []string{"3333cccc-0000-4000-8000-000000000003", "5555eeee-0000-4000-8000-000000000005"} {
			row := copyRow(t, body, id)
			if got := strings.Count(row, "&mdash;") + strings.Count(row, "—"); got != 2 {
				t.Errorf("row %s has %d dashes, want 2:\n%s", id[:8], got, row)
			}
		}
	})

	t.Run("the part and the page draw the same", func(t *testing.T) {
		_, part := e.get(t, "/parts/apps/whoami/copies")
		if !strings.Contains(part, "42 MHz") || !strings.Contains(part, "close to its ceiling") {
			t.Errorf("the part lacks the numbers:\n%s", part)
		}
	})

	t.Run("the copies region also refreshes on usage", func(t *testing.T) {
		if !strings.Contains(body, `hx-get="/parts/apps/whoami/copies" hx-trigger="sse:copies, sse:usage, sse:sync"`) {
			t.Errorf("the region doesn't listen for usage:\n%s", body)
		}
	})

	t.Run("the table still draws when Nomad answers for no copy", func(t *testing.T) {
		e2 := runningEnv(t)
		_, b := e2.get(t, "/apps/whoami")
		if got := strings.Count(text(b), "Nomad didn't answer for this copy."); got != 3 {
			t.Errorf("%d copies say Nomad didn't answer, want the 3 that started", got)
		}
		if !strings.Contains(text(b), "1111aaaa web healthy") {
			t.Errorf("the rows are gone:\n%s", text(b))
		}
	})
}

// FR-015: nothing reads CPU or memory unless the Running tab is open, and only that tab's
// stream asks for "usage" events.
func TestOnlyTheRunningTabAsksForUsage(t *testing.T) {
	e := runningEnv(t)

	live := func(body string) string {
		i := strings.Index(body, `sse-connect="`)
		if i < 0 {
			return ""
		}
		rest := body[i+len(`sse-connect="`):]
		return rest[:strings.Index(rest, `"`)]
	}

	t.Run("the Running tab's stream carries tab=running", func(t *testing.T) {
		_, body := e.get(t, "/apps/whoami")
		if got := live(body); got != "/live/apps/whoami?tab=running" {
			t.Errorf("stream = %q", got)
		}
	})
	t.Run("the other tabs' streams don't", func(t *testing.T) {
		for _, tab := range []string{"/deployments", "/configure", "/logs", "/backups"} {
			_, body := e.get(t, "/apps/whoami"+tab)
			if got := live(body); strings.Contains(got, "tab=running") {
				t.Errorf("%s: stream = %q", tab, got)
			}
		}
	})
}

func TestLiveUsageEvents(t *testing.T) {
	e := runningEnv(t)
	e.Server.live.usageEvery = 20 * time.Millisecond

	t.Run("with tab=running a usage event comes on every interval", func(t *testing.T) {
		ls := openStream(t, e.URL, "/live/apps/whoami?tab=running", e.Cookie.Value)
		ls.expect("sync")
		for range 3 {
			if got := ls.next(); got != "usage" {
				t.Fatalf("event = %q, want usage", got)
			}
		}
	})

	t.Run("without it none comes, and Nomad is not asked for stats", func(t *testing.T) {
		before := e.Nomad.StatsRequests()
		for _, path := range []string{"/live/apps/whoami", "/live/apps/whoami?tab=deployments", "/live/apps/whoami?watch=logs"} {
			ls := openStream(t, e.URL, path, e.Cookie.Value)
			ls.expect("sync")
			deadline := time.After(250 * time.Millisecond)
		wait:
			for {
				select {
				case line := <-ls.lines:
					if line == "event: usage" {
						t.Fatalf("%s sent a usage event", path)
					}
				case <-deadline:
					break wait
				}
			}
		}
		if n := e.Nomad.StatsRequests() - before; n != 0 {
			t.Errorf("%d stats requests with no Running tab open", n)
		}
	})

	t.Run("the event stops when the page leaves", func(t *testing.T) {
		ls := openStream(t, e.URL, "/live/apps/whoami?tab=running", e.Cookie.Value)
		ls.expect("sync")
		ls.close()
		time.Sleep(100 * time.Millisecond) // the hook's context ends with the request; nothing to assert but no hang at cleanup
	})
}
