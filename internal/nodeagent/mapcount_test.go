package nodeagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestVMMaxMapCount(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  int64
	}{
		"the installer's floor": {map[string]string{"proc/sys/vm/max_map_count": procFixture(t, "max_map_count")}, 262144},
		"the kernel's default":  {map[string]string{"proc/sys/vm/max_map_count": "65530\n"}, 65530},
		"no file":               {nil, 0},
		"not a number":          {map[string]string{"proc/sys/vm/max_map_count": "lots\n"}, 0},
		"negative":              {map[string]string{"proc/sys/vm/max_map_count": "-1\n"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestAgent(t, nil)
			if tc.files != nil {
				a.writeFiles(t, tc.files)
			}
			if got := a.vmMaxMapCount(); got != tc.want {
				t.Fatalf("vmMaxMapCount = %d, want %d", got, tc.want)
			}
		})
	}
}

// The report carries the value, and leaves it out when /proc can't say.
func TestReportHasTheMapCount(t *testing.T) {
	a := newTestAgent(t, nil)
	st := State{Version: 1, Index: 1, Name: "one", Pool: "default", Role: RoleServer, Servers: []int{1}, First: true}
	b, _ := json.Marshal(a.Report(context.Background(), st))
	if strings.Contains(string(b), "vmMaxMapCount") {
		t.Errorf("an unknown value is in the JSON: %s", b)
	}
	a.writeFiles(t, map[string]string{"proc/sys/vm/max_map_count": "262144\n"})
	r := a.Report(context.Background(), st)
	if r.VMMaxMapCount != 262144 {
		t.Fatalf("report = %d", r.VMMaxMapCount)
	}
	b, _ = json.Marshal(r)
	if !strings.Contains(string(b), `"vmMaxMapCount":262144`) {
		t.Errorf("the JSON lacks it: %s", b)
	}
}
