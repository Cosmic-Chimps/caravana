package compose

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func translateLimits(t *testing.T, serviceYAML string) (app.App, []app.Warning, error) {
	t.Helper()
	file := "services:\n  web:\n    image: opensearchproject/opensearch:2.19.1\n" + serviceYAML
	return Translate("shop", Request{Compose: file})
}

// Compose's ulimits become the service's: a number is soft and hard, and the long form names each.
func TestUlimits(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want map[string]app.Ulimit
	}{
		{"none", "", nil},
		{"the number form", "    ulimits:\n      nofile: 65536\n", map[string]app.Ulimit{"nofile": {Soft: 65536, Hard: 65536}}},
		{"unlimited memlock as a number", "    ulimits:\n      memlock: -1\n", map[string]app.Ulimit{"memlock": {Soft: -1, Hard: -1}}},
		{"soft and hard, as Wazuh writes them",
			"    ulimits:\n      memlock:\n        soft: -1\n        hard: -1\n      nofile:\n        soft: 65536\n        hard: 65536\n",
			map[string]app.Ulimit{"memlock": {Soft: -1, Hard: -1}, "nofile": {Soft: 65536, Hard: 65536}}},
		{"a soft limit below the hard one", "    ulimits:\n      nofile:\n        soft: 1024\n        hard: 4096\n", map[string]app.Ulimit{"nofile": {Soft: 1024, Hard: 4096}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, warnings, err := translateLimits(t, tt.yaml)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if got := a.Services[0].Ulimits; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Ulimits = %+v, want %+v", got, tt.want)
			}
			for _, w := range warnings {
				if strings.Contains(w.Field, "ulimits") {
					t.Errorf("a warning about ulimits: %+v", w)
				}
			}
		})
	}
}

func TestUlimitRefusals(t *testing.T) {
	tests := []struct {
		name, yaml, field string
	}{
		{"core", "    ulimits:\n      core: 0\n", "services.web.ulimits.core"},
		{"nproc in the long form", "    ulimits:\n      nproc:\n        soft: 1\n        hard: 2\n", "services.web.ulimits.nproc"},
		{"nofile zero", "    ulimits:\n      nofile: 0\n", "services.web.ulimits.nofile"},
		{"nofile above the kernel's", "    ulimits:\n      nofile: 2000000\n", "services.web.ulimits.nofile"},
		{"soft above hard", "    ulimits:\n      nofile:\n        soft: 2000\n        hard: 1000\n", "services.web.ulimits.nofile.soft"},
		{"nofile unlimited", "    ulimits:\n      nofile:\n        soft: 100\n        hard: -1\n", "services.web.ulimits.nofile.hard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := translateLimits(t, tt.yaml)
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}

// Compose's sysctls, as a map or a list, become the service's; vm.max_map_count is answered by the host.
func TestSysctls(t *testing.T) {
	want := map[string]string{"net.core.somaxconn": "1024", "net.ipv4.tcp_keepalive_time": "600"}
	for name, y := range map[string]string{
		"map form":  "    sysctls:\n      net.core.somaxconn: 1024\n      net.ipv4.tcp_keepalive_time: 600\n",
		"list form": "    sysctls:\n      - net.core.somaxconn=1024\n      - net.ipv4.tcp_keepalive_time=600\n",
	} {
		t.Run(name, func(t *testing.T) {
			a, _, err := translateLimits(t, y)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if got := a.Services[0].Sysctls; !reflect.DeepEqual(got, want) {
				t.Errorf("Sysctls = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMapCountSysctlIsAWarning(t *testing.T) {
	for _, v := range []string{"262144", "65530", "1"} {
		t.Run(v, func(t *testing.T) {
			a, warnings, err := translateLimits(t, "    sysctls:\n      - vm.max_map_count="+v+"\n      - net.core.somaxconn=1024\n")
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if got, want := a.Services[0].Sysctls, map[string]string{"net.core.somaxconn": "1024"}; !reflect.DeepEqual(got, want) {
				t.Errorf("Sysctls = %+v, want only the namespaced one %+v", got, want)
			}
			var found bool
			for _, w := range warnings {
				if w.Field == "services.web.sysctls.vm.max_map_count" {
					found = true
					if !strings.Contains(w.Message, "set on every server by the installer (262144)") {
						t.Errorf("message = %q", w.Message)
					}
				}
			}
			if !found {
				t.Errorf("no warning on services.web.sysctls.vm.max_map_count: %+v", warnings)
			}
		})
	}

	// Alone, it leaves the service with no sysctls at all.
	a, _, err := translateLimits(t, "    sysctls:\n      vm.max_map_count: 262144\n")
	if err != nil || a.Services[0].Sysctls != nil {
		t.Errorf("only vm.max_map_count: %+v, %v", a.Services[0].Sysctls, err)
	}
}

func TestSysctlRefusals(t *testing.T) {
	tests := []struct {
		name, yaml, field string
	}{
		{"more map count than the installer sets", "    sysctls:\n      vm.max_map_count: 524288\n", "services.web.sysctls.vm.max_map_count"},
		{"a map count that is no number", "    sysctls:\n      vm.max_map_count: lots\n", "services.web.sysctls.vm.max_map_count"},
		{"a server setting", "    sysctls:\n      kernel.shmmax: 68719476736\n", "services.web.sysctls.kernel.shmmax"},
		{"swappiness", "    sysctls:\n      - vm.swappiness=1\n", "services.web.sysctls.vm.swappiness"},
		{"a name without a value", "    sysctls:\n      - net.core.somaxconn\n", "services.web.sysctls.net.core.somaxconn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := translateLimits(t, tt.yaml)
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}
