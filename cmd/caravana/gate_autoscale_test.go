package main

// The autoscale gate step (feature 043): a service with `scale` grows under
// load and shrinks after it, a deploy at the scaled count keeps it, and every
// step is recorded with its reason. It is in its own file only because
// gate_test.go is long; runGate lists it with the other steps.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// loadGenerator calls https://host/ about rate times a second until stopped,
// through the same address hostStatus uses, and counts what it sent and what
// didn't answer 200. Every worker keeps its connection alive; Traefik picks a
// copy per request, not per connection.
type loadGenerator struct {
	sent, failed atomic.Int64
	mu           sync.Mutex
	firstFailure string
	failedAt     []time.Time // when the first 100 failed, for the log
	stop         chan struct{}
	done         sync.WaitGroup
}

// startLoad starts workers workers, each making rate/workers calls a second.
func (g *gate) startLoad(host string, rate, workers int) *loadGenerator {
	addr, url := "127.0.0.1:8443", "https://"+host+":8443/"
	if g.target.Public != nil {
		addr, url = net.JoinHostPort(g.target.Public.IP, "443"), "https://"+host+"/"
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: host}, // routing only, as hostStatus
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			MaxIdleConnsPerHost: workers,
		},
	}
	l := &loadGenerator{stop: make(chan struct{})}
	period := time.Duration(workers) * time.Second / time.Duration(rate)
	for i := 0; i < workers; i++ {
		l.done.Add(1)
		go func(offset time.Duration) {
			defer l.done.Done()
			select { // spread the workers over one period
			case <-l.stop:
				return
			case <-time.After(offset):
			}
			tick := time.NewTicker(period)
			defer tick.Stop()
			for {
				l.sent.Add(1)
				resp, err := client.Get(url)
				status := 0
				if err == nil {
					status = resp.StatusCode
					resp.Body.Close()
				}
				if err != nil || status != http.StatusOK {
					l.failed.Add(1)
					l.mu.Lock()
					if l.firstFailure == "" {
						l.firstFailure = fmt.Sprintf("status %d, error %v", status, err)
					}
					if len(l.failedAt) < 100 {
						l.failedAt = append(l.failedAt, time.Now())
					}
					l.mu.Unlock()
				}
				select {
				case <-l.stop:
					return
				case <-tick.C:
				}
			}
		}(time.Duration(i) * period / time.Duration(workers))
	}
	return l
}

// end stops the calls and says how many were sent and how many failed.
func (l *loadGenerator) end() (sent, failed int64) {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
	l.done.Wait()
	return l.sent.Load(), l.failed.Load()
}

// scaleStep is a `scale` entry of listAppActions.
type scaleStep struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Trigger   string `json:"trigger"`
	Status    string `json:"status"`
	Service   string `json:"service"`
	FromCount *int   `json:"fromCount"`
	ToCount   *int   `json:"toCount"`
	Reason    string `json:"reason"`
	Error     string `json:"error"`
}

// scaleSteps is the app's scale actions, oldest first.
func (g *gate) scaleSteps(t *testing.T, appID string) []scaleStep {
	t.Helper()
	var all []scaleStep
	g.getJSON(t, "/v1/apps/"+appID+"/actions", &all)
	var out []scaleStep
	for i := len(all) - 1; i >= 0; i-- { // the API answers newest first
		if all[i].Action == "scale" {
			out = append(out, all[i])
		}
	}
	return out
}

// groupCount is the count of the job's first group, as Nomad has it now, and
// the canary count of its update block.
func (g *gate) groupCount(t *testing.T, jobID string) (count, canary int) {
	t.Helper()
	job, _, err := g.target.Nomad.Jobs().Info(jobID, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", jobID, err)
	}
	tg := job.TaskGroups[0]
	if tg.Count != nil {
		count = *tg.Count
	}
	if tg.Update != nil && tg.Update.Canary != nil {
		canary = *tg.Update.Canary
	}
	return count, canary
}

// serviceStatus is the API's status of the app: the service's wanted and healthy copies, and the live version.
func (g *gate) serviceStatus(t *testing.T, appID, service string) (wanted, healthy int, live *uint64) {
	t.Helper()
	var list struct {
		Apps []struct {
			App struct {
				ID string `json:"id"`
			} `json:"app"`
			Status struct {
				LiveVersion *uint64 `json:"liveVersion"`
				Services    []struct {
					Name          string `json:"name"`
					WantedCopies  int    `json:"wantedCopies"`
					HealthyCopies int    `json:"healthyCopies"`
				} `json:"services"`
			} `json:"status"`
		} `json:"apps"`
	}
	g.getJSON(t, "/v1/apps", &list)
	for _, a := range list.Apps {
		if a.App.ID != appID {
			continue
		}
		for _, s := range a.Status.Services {
			if s.Name == service {
				return s.WantedCopies, s.HealthyCopies, a.Status.LiveVersion
			}
		}
	}
	t.Fatalf("listApps has no service %s/%s", appID, service)
	return 0, 0, nil
}

// autoscaleStep (feature 043): a whoami service scaled 1 to 3 on 5 requests a
// second per copy, with the shortest cooldown (60 s). The lab's sampler ticks
// every 10 s, so a step up reads 30 s of rows and a step down 50 s. Then:
//
//  1. a load generator calls the domain about 25 times a second: the group's
//     count goes 1, 2, 3 within 6 minutes, one step at a time, each an app
//     action `scale` with the trigger autoscale and a reason that names the
//     requests a second; the API's status counts the 3 copies wanted;
//  2. at its maximum and still over its target the service is a needs-you
//     item, scale_at_max;
//  3. a deploy with one more env value, still under load: the deployment ends
//     healthy, the group runs 3 copies with 3 canaries, and the live version
//     is that deployment's (the scale steps' job versions read as the version
//     they scaled);
//  4. the generator stops: the count goes 3, 2, 1 within 6 minutes, the same
//     way, and the needs-you item is gone;
//  5. throughout, the count never left 1 to 3 (sampled every 2 s), exactly
//     four steps were made (none refused, none extra), and none of the
//     generator's calls failed: a scale's new copy is in Traefik before it
//     listens, and the router's retry middleware sends those calls to a copy
//     that does (task 6rn5; before it, 0 to 31 of about 4000 calls were 502s
//     at the scale-ups).
func (g *gate) autoscaleStep(t *testing.T) {
	const id = "lab043"
	host := g.extraHost(id)

	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // leftovers from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(remove)

	web := func(rev string) map[string]any {
		svc := map[string]any{
			"name": "web", "image": "traefik/whoami:latest",
			"ports":     []map[string]any{{"name": "http", "containerPort": 80}},
			"domains":   []map[string]any{{"host": host, "port": "http"}},
			"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
			"resources": map[string]any{"memoryMB": 32, "memoryMaxMB": 64},
			"scale":     map[string]any{"min": 1, "max": 3, "target": map[string]any{"requestsPerCopy": 5}, "cooldownSeconds": 60},
		}
		if rev != "" {
			svc["env"] = []map[string]any{{"name": "REV", "value": rev}}
		}
		return svc
	}
	deploy := func(rev string) gateDeployment {
		t.Helper()
		raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(web(rev)))
		var d gateDeployment
		if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
			t.Fatalf("deploy %s: %s", id, raw)
		}
		final := g.waitFinalOf(t, id, d.ID, 5*time.Minute)
		if final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
		return final
	}

	first := deploy("")
	g.waitHostAnswers(t, host)
	if count, canary := g.groupCount(t, id); count != 1 || canary != 1 {
		t.Fatalf("the first deploy runs %d copies with %d canaries, want 1 and 1 (the starting count is min)", count, canary)
	}

	// The count is sampled from here to the end.
	var lowest, highest atomic.Int64
	lowest.Store(99)
	sampling := make(chan struct{})
	var sampled sync.WaitGroup
	sampled.Add(1)
	go func() {
		defer sampled.Done()
		for {
			if job, _, err := g.target.Nomad.Jobs().Info(id, nil); err == nil && len(job.TaskGroups) > 0 && job.TaskGroups[0].Count != nil {
				n := int64(*job.TaskGroups[0].Count)
				for cur := lowest.Load(); n < cur && !lowest.CompareAndSwap(cur, n); cur = lowest.Load() {
				}
				for cur := highest.Load(); n > cur && !highest.CompareAndSwap(cur, n); cur = highest.Load() {
				}
			}
			select {
			case <-sampling:
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	stopSampling := func() {
		select {
		case <-sampling:
		default:
			close(sampling)
		}
		sampled.Wait()
	}
	t.Cleanup(stopSampling)

	load := g.startLoad(host, 25, 5)
	t.Cleanup(func() { load.end() })
	started := time.Now()

	// 1. Up, one step at a time.
	waitFor(t, "the group to reach 3 copies", 6*time.Minute, func() bool {
		count, _ := g.groupCount(t, id)
		return count == 3
	})
	t.Logf("3 copies after %v", time.Since(started).Round(time.Second))
	steps := g.scaleSteps(t, id)
	for _, s := range steps {
		t.Logf("scale step %d → %d (%s, %s): %s", *s.FromCount, *s.ToCount, s.Trigger, s.Status, s.Reason)
	}
	if len(steps) != 2 || *steps[0].FromCount != 1 || *steps[0].ToCount != 2 || *steps[1].FromCount != 2 || *steps[1].ToCount != 3 {
		t.Fatalf("scale steps = %+v, want 1 → 2 then 2 → 3", steps)
	}
	for _, s := range steps {
		if s.Trigger != "autoscale" || s.Status != "succeeded" || s.Service != "web" || !strings.Contains(s.Reason, "requests a second") {
			t.Errorf("step = %+v, want a succeeded autoscale step of web whose reason names the requests a second", s)
		}
	}
	waitFor(t, "the API to count 3 wanted copies, all healthy", 3*time.Minute, func() bool {
		wanted, healthy, _ := g.serviceStatus(t, id, "web")
		return wanted == 3 && healthy == 3
	})

	// 2. At its maximum and still over the target.
	waitFor(t, "scale_at_max on the needs-you list", 3*time.Minute, func() bool {
		var list struct {
			Items []attentionItem `json:"items"`
		}
		g.getJSON(t, "/v1/attention", &list)
		for _, it := range list.Items {
			if it.Kind == "scale_at_max" && it.AppID == id && it.Service == "web" {
				t.Logf("needs you: %s", it.Detail)
				return true
			}
		}
		return false
	})

	// 3. A deploy at 3 copies keeps them, with 3 canaries.
	redeployed := deploy("2")
	if count, canary := g.groupCount(t, id); count != 3 || canary != 3 {
		t.Errorf("after the deploy the group runs %d copies with %d canaries, want 3 and 3", count, canary)
	}
	if redeployed.JobVersion == nil || (first.JobVersion != nil && *redeployed.JobVersion <= *first.JobVersion) {
		t.Errorf("deployment job versions %v then %v", first.JobVersion, redeployed.JobVersion)
	}
	waitFor(t, "3 healthy copies of the new version", 3*time.Minute, func() bool {
		wanted, healthy, _ := g.serviceStatus(t, id, "web")
		return wanted == 3 && healthy == 3
	})
	_, _, live := g.serviceStatus(t, id, "web")
	if live == nil || *live != *redeployed.JobVersion {
		t.Errorf("the live version is %v, want the deployment's %d", live, *redeployed.JobVersion)
	}
	if n := len(g.scaleSteps(t, id)); n != 2 {
		t.Errorf("%d scale steps after the deploy, want the 2 of before it", n)
	}

	// 4. Down, once the load is gone.
	sent, failed := load.end()
	t.Logf("load: %d calls, %d failed %s", sent, failed, load.firstFailure)
	for _, at := range load.failedAt {
		t.Logf("  failed %v after the start", at.Sub(started).Round(time.Second))
	}
	stopped := time.Now()
	waitFor(t, "the group to come back to 1 copy", 6*time.Minute, func() bool {
		count, _ := g.groupCount(t, id)
		return count == 1
	})
	t.Logf("1 copy %v after the load stopped", time.Since(stopped).Round(time.Second))
	steps = g.scaleSteps(t, id)
	for _, s := range steps {
		t.Logf("scale step %d → %d (%s, %s): %s", *s.FromCount, *s.ToCount, s.Trigger, s.Status, s.Reason)
	}
	want := [][2]int{{1, 2}, {2, 3}, {3, 2}, {2, 1}}
	if len(steps) != len(want) {
		t.Fatalf("%d scale steps, want %d: %+v", len(steps), len(want), steps)
	}
	for i, s := range steps {
		if *s.FromCount != want[i][0] || *s.ToCount != want[i][1] || s.Trigger != "autoscale" || s.Status != "succeeded" || s.Reason == "" {
			t.Errorf("step %d = %+v, want %d → %d, succeeded, autoscale, with a reason", i, s, want[i][0], want[i][1])
		}
	}
	waitFor(t, "the API to count 1 wanted copy, healthy", 3*time.Minute, func() bool {
		wanted, healthy, _ := g.serviceStatus(t, id, "web")
		return wanted == 1 && healthy == 1
	})
	if _, _, live = g.serviceStatus(t, id, "web"); live == nil || *live != *redeployed.JobVersion {
		t.Errorf("after the scale-downs the live version is %v, want the deployment's %d", live, *redeployed.JobVersion)
	}
	var list struct {
		Items []attentionItem `json:"items"`
	}
	g.getJSON(t, "/v1/attention", &list)
	for _, it := range list.Items {
		if it.AppID == id {
			t.Errorf("the needs-you list still has an item for %s: %+v", id, it)
		}
	}
	g.waitHostAnswers(t, host)

	// 5. Never outside the bounds, and the requests answered.
	stopSampling()
	if lo, hi := lowest.Load(), highest.Load(); lo < 1 || hi > 3 {
		t.Errorf("the group's count went from %d to %d, want it always within 1 to 3", lo, hi)
	}
	if sent < 500 {
		t.Errorf("the generator sent only %d calls", sent)
	}
	if failed != 0 {
		t.Errorf("%d of %d calls failed (the first: %s), want none", failed, sent, load.firstFailure)
	}
}
