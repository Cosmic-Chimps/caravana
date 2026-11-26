package ui

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// The server page's vm.max_map_count row (feature 060): only when the agent said it, the number plain at or above
// what OpenSearch needs, and in the warning colour with the way out below it.
func TestMapCountFact(t *testing.T) {
	if got := mapCountFact(service.ServerView{}); got != nil {
		t.Errorf("unknown value: %+v", got)
	}
	ok := mapCountFact(service.ServerView{VMMaxMapCount: 1048576})
	if len(ok) != 1 || ok[0].Key != "vm.max_map_count" || ok[0].Value != "1048576" || ok[0].Mark != "" || ok[0].Note != "" {
		t.Errorf("a high value = %+v", ok)
	}
	low := mapCountFact(service.ServerView{VMMaxMapCount: 65530})
	if len(low) != 1 || low[0].Value != "65530" || low[0].Mark != "warning" ||
		low[0].Note != "OpenSearch and Elasticsearch need at least 262144: rerun the installer" {
		t.Errorf("a low value = %+v", low)
	}
	// Both lists of facts carry it.
	v := service.ServerView{Name: "fsn-1", VMMaxMapCount: 65530}
	for name, facts := range map[string][]srvFact{
		"remote":  remoteFacts(time.Now(), v),
		"managed": withManagedFacts(time.Now(), nil, v),
	} {
		found := false
		for _, f := range facts {
			found = found || f.Key == "vm.max_map_count"
		}
		if !found {
			t.Errorf("%s facts lack the row: %+v", name, facts)
		}
	}
}
