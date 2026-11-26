package compose

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func TestSetResources(t *testing.T) {
	tests := []struct {
		name string
		text string
		r    Resources
		want string
	}{
		{
			name: "deploy.replicas rewritten in place",
			text: "services:\n  web:\n    image: a:1\n    deploy:\n      replicas: 2   # two\n",
			r:    Resources{Replicas: new(3)},
			want: "services:\n  web:\n    image: a:1\n    deploy:\n      replicas: 3   # two\n",
		},
		{
			name: "scale rewritten when there is no deploy.replicas",
			text: "services:\n  web:\n    image: a:1\n    scale: 2\n",
			r:    Resources{Replicas: new(4)},
			want: "services:\n  web:\n    image: a:1\n    scale: 4\n",
		},
		{
			name: "replicas added under an existing deploy",
			text: "services:\n  web:\n    image: a:1\n    deploy:\n      # resources\n      resources:\n        limits:\n          cpus: \"0.5\"\n",
			r:    Resources{Replicas: new(2)},
			want: "services:\n  web:\n    image: a:1\n    deploy:\n      replicas: 2\n      # resources\n      resources:\n        limits:\n          cpus: \"0.5\"\n",
		},
		{
			name: "deploy added with replicas, at the service's indentation",
			text: "services:\n    web:   # the site\n        # the image\n        image: a:1\n",
			r:    Resources{Replicas: new(2)},
			want: "services:\n    web:   # the site\n        deploy:\n            replicas: 2\n        # the image\n        image: a:1\n",
		},
		{
			name: "mem_reservation and mem_limit rewritten, quoting kept",
			text: "services:\n  web:\n    image: a:1\n    mem_reservation: '256m'\n    mem_limit: \"1g\" # ceiling\n",
			r:    Resources{MemReservationMB: new(512), MemLimitMB: new(2048)},
			want: "services:\n  web:\n    image: a:1\n    mem_reservation: '512m'\n    mem_limit: \"2048m\" # ceiling\n",
		},
		{
			name: "mem keys added when absent, other services untouched",
			text: "# app\nservices:\n  web:\n    image: a:1\n\n  db:\n    image: postgres:17\n",
			r:    Resources{MemReservationMB: new(300), MemLimitMB: new(600)},
			want: "# app\nservices:\n  web:\n    mem_limit: 600m\n    mem_reservation: 300m\n    image: a:1\n\n  db:\n    image: postgres:17\n",
		},
		{
			name: "deploy.resources memory rewritten in place",
			text: "services:\n  web:\n    image: a:1\n    deploy:\n      resources:\n        limits:\n          memory: 512M\n        reservations: {memory: 128M}\n",
			r:    Resources{MemReservationMB: new(256), MemLimitMB: new(768)},
			want: "services:\n  web:\n    image: a:1\n    deploy:\n      resources:\n        limits:\n          memory: 768m\n        reservations: {memory: 256m}\n",
		},
		{
			name: "replicas stated twice: both rewritten",
			text: "services:\n  web:\n    image: a:1\n    scale: 2\n    deploy:\n      replicas: '2'\n",
			r:    Resources{Replicas: new(3)},
			want: "services:\n  web:\n    image: a:1\n    scale: 3\n    deploy:\n      replicas: '3'\n",
		},
		{
			name: "mem_limit stated twice: both rewritten",
			text: "services:\n  web:\n    image: a:1\n    mem_limit: 512m # top\n    deploy:\n      resources:\n        limits:\n          memory: 512m\n",
			r:    Resources{MemLimitMB: new(1024)},
			want: "services:\n  web:\n    image: a:1\n    mem_limit: 1024m # top\n    deploy:\n      resources:\n        limits:\n          memory: 1024m\n",
		},
		{
			name: "mem_reservation stated twice on one line: both rewritten",
			text: "services:\n  web: {image: a:1, mem_reservation: 128m, deploy: {resources: {reservations: {memory: 128m}}}}\n",
			r:    Resources{MemReservationMB: new(64)},
			want: "services:\n  web: {image: a:1, mem_reservation: 64m, deploy: {resources: {reservations: {memory: 64m}}}}\n",
		},
		{
			name: "both forms stated, only one value changed",
			text: "services:\n  web:\n    image: a:1\n    mem_limit: 1g\n    mem_reservation: 256m\n    deploy:\n      resources:\n        limits: {memory: 1g}\n        reservations: {memory: 256m}\n",
			r:    Resources{MemReservationMB: new(300)},
			want: "services:\n  web:\n    image: a:1\n    mem_limit: 1g\n    mem_reservation: 300m\n    deploy:\n      resources:\n        limits: {memory: 1g}\n        reservations: {memory: 300m}\n",
		},
		{
			name: "a value in a flow mapping is rewritten in place",
			text: "services:\n  web: {image: a:1, scale: 2}\n",
			r:    Resources{Replicas: new(5)},
			want: "services:\n  web: {image: a:1, scale: 5}\n",
		},
		{
			name: "same value leaves the text as it is",
			text: "services:\n  web:\n    image: a:1\n    mem_limit:    512m\n",
			r:    Resources{MemLimitMB: new(512)},
			want: "services:\n  web:\n    image: a:1\n    mem_limit:    512m\n",
		},
		{
			name: "CRLF line endings kept",
			text: "services:\r\n  web:\r\n    image: a:1\r\n",
			r:    Resources{Replicas: new(2), MemLimitMB: new(64)},
			want: "services:\r\n  web:\r\n    mem_limit: 64m\r\n    deploy:\r\n      replicas: 2\r\n    image: a:1\r\n",
		},
		{
			name: "nothing set",
			text: "services:\n  web:\n    image: a:1\n",
			want: "services:\n  web:\n    image: a:1\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SetResources(tt.text, "web", tt.r)
			if err != nil {
				t.Fatalf("SetResources() = %v", err)
			}
			if got != tt.want {
				t.Errorf("SetResources() =\n%q\nwant\n%q", got, tt.want)
			}
			// Compose's own rules (a value stated twice must be equal, say) hold for the result, and
			// it makes the values written.
			a, _, err := Translate("app", Request{Compose: got})
			if err != nil {
				t.Fatalf("Translate(edited) = %v", err)
			}
			var svc app.Service
			for _, s := range a.Services {
				if s.Name == "web" {
					svc = s
				}
			}
			if n := tt.r.Replicas; n != nil && (svc.Replicas == nil || *svc.Replicas != *n) {
				t.Errorf("replicas = %v, want %d", svc.Replicas, *n)
			}
			if n := tt.r.MemReservationMB; n != nil && svc.Resources.MemoryMB != *n {
				t.Errorf("memory = %d, want %d", svc.Resources.MemoryMB, *n)
			}
			if n := tt.r.MemLimitMB; n != nil && svc.Resources.MemoryMaxMB != *n {
				t.Errorf("ceiling = %d, want %d", svc.Resources.MemoryMaxMB, *n)
			}
		})
	}
}

func TestSetResourcesRefuses(t *testing.T) {
	tests := []struct {
		name string
		text string
		r    Resources
		want string // the whole error
	}{
		{
			name: "an anchored value",
			text: "services:\n  web:\n    image: a:1\n    mem_limit: &m 1g\n  db:\n    image: b:1\n    mem_limit: *m\n",
			r:    Resources{MemLimitMB: new(512)},
			want: "services.web.mem_limit: the value has an anchor",
		},
		{
			name: "an alias value",
			text: "x-mem: &m 1g\nservices:\n  web:\n    image: a:1\n    mem_limit: *m\n",
			r:    Resources{MemLimitMB: new(512)},
			want: "services.web.mem_limit: the value is an alias",
		},
		{
			name: "an anchored service",
			text: "services:\n  web: &web\n    image: a:1\n",
			r:    Resources{Replicas: new(2)},
			want: "services.web: the service has an anchor",
		},
		{
			name: "a merge key in the service",
			text: "x-base: &base\n  deploy:\n    replicas: 2\nservices:\n  web:\n    <<: *base\n    image: a:1\n",
			r:    Resources{Replicas: new(3)},
			want: "services.web: the service has a merge key (<<)",
		},
		{
			name: "an aliased deploy",
			text: "x-deploy: &d\n  replicas: 2\nservices:\n  web:\n    image: a:1\n    deploy: *d\n",
			r:    Resources{Replicas: new(3)},
			want: "services.web.deploy: it is an alias",
		},
		{
			name: "a variable",
			text: "services:\n  web:\n    image: a:1\n    deploy:\n      replicas: ${COPIES:-2}\n",
			r:    Resources{Replicas: new(3)},
			want: "services.web.deploy.replicas: the value holds a variable, which can't be rewritten",
		},
		{
			name: "a key to add into a flow mapping",
			text: "services:\n  web: {image: a:1}\n",
			r:    Resources{MemLimitMB: new(512)},
			want: "services.web.mem_limit: the mapping is written in flow style ({...}), so a key can't be added to it safely",
		},
		{
			name: "a twin that is an alias",
			text: "x-n: &n 2\nservices:\n  web:\n    image: a:1\n    deploy:\n      replicas: 2\n    scale: *n\n",
			r:    Resources{Replicas: new(3)},
			want: "services.web.scale: the value is an alias",
		},
		{
			name: "a tagged value",
			text: "services:\n  web:\n    image: a:1\n    scale: !!int 2\n",
			r:    Resources{Replicas: new(3)},
			want: "services.web.scale: the value has a tag",
		},
		{
			name: "an empty deploy",
			text: "services:\n  web:\n    image: a:1\n    deploy:\n",
			r:    Resources{Replicas: new(3)},
			want: "services.web.deploy: it is not a mapping",
		},
		{
			name: "a missing service",
			text: "services:\n  web:\n    image: a:1\n",
			r:    Resources{Replicas: new(3)},
			want: "services.api: the service is not in the file",
		},
		{
			name: "zero",
			text: "services:\n  web:\n    image: a:1\n",
			r:    Resources{MemLimitMB: new(0)},
			want: "services.web.mem_limit: must be 1 or more",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := "web"
			if strings.HasPrefix(tt.want, "services.api") {
				service = "api"
			}
			_, err := SetResources(tt.text, service, tt.r)
			var edit *EditError
			if !errors.As(err, &edit) {
				t.Fatalf("SetResources() = %v, want an *EditError", err)
			}
			if err.Error() != tt.want {
				t.Errorf("SetResources() = %q, want %q", err, tt.want)
			}
		})
	}
}

// TestSetResourcesGolden edits a commented file the way the Services page does and compares it with
// testdata/resources/patch.golden.yaml; the edited file must translate to the values written.
func TestSetResourcesGolden(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("testdata", "resources", "patch.compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := SetResources(string(in), "server", Resources{Replicas: new(2), MemReservationMB: new(384), MemLimitMB: new(1536)})
	if err != nil {
		t.Fatalf("SetResources() = %v", err)
	}
	goldenPath := filepath.Join("testdata", "resources", "patch.golden.yaml")
	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("SetResources() =\n%s\nwant\n%s", got, want)
	}

	a, _, err := Translate("patch", Request{Compose: got})
	if err != nil {
		t.Fatalf("Translate() = %v", err)
	}
	for _, svc := range a.Services {
		if svc.Name != "server" {
			continue
		}
		if svc.Replicas == nil || *svc.Replicas != 2 || svc.Resources.MemoryMB != 384 || svc.Resources.MemoryMaxMB != 1536 {
			t.Errorf("server = replicas %v, %+v; want 2, 384 MB up to 1536 MB", svc.Replicas, svc.Resources)
		}
	}
}

func TestCheckResources(t *testing.T) {
	if err := CheckResources("services:\n  web:\n    image: a:1\n", "web", true); err != nil {
		t.Errorf("CheckResources(plain) = %v, want nil", err)
	}
	if err := CheckResources("services:\n  web: {image: a:1}\n", "web", true); err == nil {
		t.Error("CheckResources(flow) = nil, want a refusal")
	}
	// A service whose copies aren't offered isn't refused for its replicas.
	text := "x-d: &d\n  replicas: 1\nservices:\n  web:\n    image: a:1\n    mem_limit: 1g\n    mem_reservation: 256m\n    scale: &s 1\n"
	if err := CheckResources(text, "web", false); err != nil {
		t.Errorf("CheckResources(no replicas) = %v, want nil", err)
	}
	if err := CheckResources(text, "web", true); err == nil {
		t.Error("CheckResources(anchored scale) = nil, want a refusal")
	}
}
