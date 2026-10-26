package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// Feature 025: a copy Nomad answers for with no measurement (it isn't running) has no `usage` in
// listCopies?usage=true, like a copy that hasn't started: no value, not 0 MB and not an error.
func TestListCopiesUsageNoValueIsLeftOut(t *testing.T) {
	e := newTestEnv(t, "")
	seedHealthy(t, e, "whoami", 2, 7)
	started := time.Date(2026, 10, 4, 9, 5, 0, 0, time.UTC)
	running := func(id string) api.AllocationListStub {
		return api.AllocationListStub{ID: id, TaskGroup: "web", JobVersion: 7, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {StartedAt: started}}}
	}
	e.Nomad.SetCopies("whoami", running("a1"), running("a2"))
	e.Nomad.SetStats("a1", deploytest.AllocUsage(map[string]deploytest.TaskUsage{
		"web": {CPUTicks: 5, UsageBytes: 24 << 20, CacheBytes: 18 << 20, RSSBytes: 3 << 20}}))
	e.Nomad.SetStats("a2", deploytest.DeadAllocUsage("web"))

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/copies?usage=true", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET copies?usage=true = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "CopyList")
	got := map[string]copyBody{}
	for _, c := range decodeAs[struct {
		Copies []copyBody `json:"copies"`
	}](t, raw).Copies {
		got[c.ID] = c
	}
	if u := got["a1"].Usage; u == nil || u.MemoryMB == nil || *u.MemoryMB != 6 {
		t.Errorf("a1 usage = %+v, want 6 MB (24 less the 18 of page cache)", u)
	}
	if u := got["a2"].Usage; u != nil {
		t.Errorf("a2 usage = %+v, want none", u)
	}
}
