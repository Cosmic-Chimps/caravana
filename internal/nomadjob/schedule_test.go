package nomadjob

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

const (
	pinnedWorker = "ghcr.io/acme/shop@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pinnedRates  = "acme/rates@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// scheduleApp is a one-service app with env, a secret, a command of its own
// (which a run replaces) and a web port (which a run doesn't have).
func scheduleApp() app.App {
	return app.App{
		ID:   "shop",
		Name: "shop",
		Services: []app.Service{{
			Name:       "web",
			Image:      "ghcr.io/acme/shop:v1",
			Entrypoint: []string{"/entry"},
			Command:    "serve",
			Ports:      []app.Port{{Name: "http", ContainerPort: 8080}},
			Env:        []app.EnvVar{{Name: "LOG_LEVEL", Value: "info"}, {Name: "REGION", Value: "eu"}},
			Secrets:    []app.Secret{{Name: "API_KEY"}},
			Health:     &app.HealthCheck{},
			Domains:    []app.Domain{{Host: "shop.example.com"}},
			Resources:  app.Resources{CPUMHz: 200, MemoryMB: 128, MemoryMaxMB: 512},
		}},
	}
}

// scheduleMultiApp has two services with ports, so the app shares names.
func scheduleMultiApp() app.App {
	return app.App{
		ID:   "rates",
		Name: "rates",
		Services: []app.Service{
			{Name: "api", Image: "acme/rates:1", Ports: []app.Port{{Name: "http", ContainerPort: 8080}}, Health: &app.HealthCheck{}},
			{Name: "cache", Image: "redis:7", Ports: []app.Port{{Name: "redis", ContainerPort: 6379}}},
		},
	}
}

// TestScheduleRunJob compares run jobs with their golden files.
func TestScheduleRunJob(t *testing.T) {
	one := scheduleApp()
	multi := scheduleMultiApp()
	cases := []struct {
		name string
		job  *api.Job
	}{
		{"schedule-run", ScheduleRunJob(one, "r1", one.Services[0], pinnedWorker, []string{"node", "scripts/sync-rates.js", "--full"}, 512, labSettings(), map[string]bool{"ghcr.io": true})},
		{"schedule-run-multi", ScheduleRunJob(multi, "r2", multi.Services[0], pinnedRates, []string{"rates-report"}, 256, labSettings(), nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(tc.job, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", tc.name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs; run with -update and read the diff", path)
			}
		})
	}
}

// TestScheduleRunJobShape checks what the golden files can't say by looking:
// nothing takes traffic, the command replaces the service's own, the run is
// never retried and the secrets stay out of the job.
func TestScheduleRunJobShape(t *testing.T) {
	one := scheduleApp()
	svc := one.Services[0]
	command := []string{"node", "scripts/sync-rates.js", "--full"}
	j := ScheduleRunJob(one, "r1", svc, pinnedWorker, command, 512, labSettings(), map[string]bool{"ghcr.io": true})

	if *j.ID != "shop_run-r1" || *j.ID != ScheduleRunJobID("shop", "r1") || *j.Type != "batch" || j.Periodic != nil {
		t.Errorf("job = %s %s periodic %v", *j.ID, *j.Type, j.Periodic)
	}
	if j.Meta["caravana.app"] != "shop" || j.Meta["caravana.service"] != "web" || j.Meta["caravana.run"] != "r1" {
		t.Errorf("meta = %v", j.Meta)
	}
	if ScheduleRunVariablePath(*j.ID) != "nomad/jobs/shop_run-r1" || !strings.HasPrefix(*j.ID, AppJobPrefix("shop")) {
		t.Errorf("variable path %q, job %q", ScheduleRunVariablePath(*j.ID), *j.ID)
	}

	tg := j.TaskGroups[0]
	if *tg.Name != "web" || len(tg.Tasks) != 1 {
		t.Fatalf("group %s with %d tasks", *tg.Name, len(tg.Tasks))
	}
	// No traffic by construction: no registration, no port, no volume.
	if len(tg.Services) != 0 {
		t.Errorf("services = %+v", tg.Services)
	}
	for _, n := range tg.Networks {
		if len(n.DynamicPorts) != 0 || len(n.ReservedPorts) != 0 {
			t.Errorf("network has ports: %+v", n)
		}
	}
	if len(tg.Volumes) != 0 || len(tg.Tasks[0].VolumeMounts) != 0 {
		t.Error("the run mounts a volume")
	}
	if *tg.RestartPolicy.Attempts != 0 || *tg.ReschedulePolicy.Attempts != 0 {
		t.Error("a failed run would be retried")
	}

	task := tg.Tasks[0]
	if task.Name != "web" || task.Driver != "docker" {
		t.Errorf("task = %s %s", task.Name, task.Driver)
	}
	if got := task.Config["image"]; got != pinnedWorker {
		t.Errorf("image = %v", got)
	}
	if got := task.Config["entrypoint"]; !reflect.DeepEqual(got, []string{"node"}) {
		t.Errorf("entrypoint = %#v", got)
	}
	if got := task.Config["args"]; !reflect.DeepEqual(got, []string{"scripts/sync-rates.js", "--full"}) {
		t.Errorf("args = %#v", got)
	}
	if _, ok := task.Config["command"]; ok {
		t.Error("the service's own command is still set")
	}
	if got := task.Config["auth"]; !reflect.DeepEqual(got, wantAuth("ghcr.io")) {
		t.Errorf("auth = %#v", got)
	}
	if !reflect.DeepEqual(task.Env, map[string]string{"LOG_LEVEL": "info", "REGION": "eu"}) {
		t.Errorf("env = %v", task.Env)
	}
	if len(task.Templates) != 1 || !strings.Contains(*task.Templates[0].EmbeddedTmpl, `nomadVar "nomad/jobs/shop_run-r1"`) {
		t.Errorf("templates = %+v", task.Templates)
	}
	// The service's own secrets template is not used: the run's variable is.
	if strings.Contains(*task.Templates[0].EmbeddedTmpl, "nomad/jobs/shop/web") {
		t.Error("the run reads the app's own variable")
	}
	r := task.Resources
	if *r.CPU != 200 || *r.MemoryMB != 512 || *r.MemoryMaxMB != 512 {
		t.Errorf("resources = cpu %d mem %d max %d", *r.CPU, *r.MemoryMB, *r.MemoryMaxMB)
	}
	if task.KillTimeout == nil || task.KillTimeout.Seconds() != 10 {
		t.Errorf("kill timeout = %v", task.KillTimeout)
	}
	if task.LogConfig == nil || *task.LogConfig.MaxFiles != 2 || *task.LogConfig.MaxFileSizeMB != 1 {
		t.Errorf("log config = %+v", task.LogConfig)
	}

	// A command of one word has an entrypoint and no args.
	j = ScheduleRunJob(one, "r3", svc, pinnedWorker, []string{"migrate"}, 256, labSettings(), nil)
	task = j.TaskGroups[0].Tasks[0]
	if !reflect.DeepEqual(task.Config["entrypoint"], []string{"migrate"}) {
		t.Errorf("entrypoint = %#v", task.Config["entrypoint"])
	}
	if _, ok := task.Config["args"]; ok {
		t.Errorf("args = %#v", task.Config["args"])
	}
	if _, ok := task.Config["auth"]; ok {
		t.Error("a public image has an auth block")
	}
}

// TestScheduleRunJobMemory: the ceiling is the service's, never below the
// reservation of the run.
func TestScheduleRunJobMemory(t *testing.T) {
	one := scheduleApp()
	svc := one.Services[0] // reserves 128, ceiling 512
	for _, tc := range []struct{ memoryMB, wantMax int }{
		{256, 512},
		{512, 512},
		{1024, 1024},
		{2048, 2048},
	} {
		j := ScheduleRunJob(one, "r", svc, pinnedWorker, []string{"x"}, tc.memoryMB, labSettings(), nil)
		r := j.TaskGroups[0].Tasks[0].Resources
		if *r.MemoryMB != tc.memoryMB || *r.MemoryMaxMB != tc.wantMax {
			t.Errorf("memoryMB %d: reserves %d, ceiling %d, want ceiling %d", tc.memoryMB, *r.MemoryMB, *r.MemoryMaxMB, tc.wantMax)
		}
	}

	// A service with no resources of its own gets the installation's defaults.
	svc.Resources = app.Resources{}
	j := ScheduleRunJob(one, "r", svc, pinnedWorker, []string{"x"}, 128, labSettings(), nil)
	r := j.TaskGroups[0].Tasks[0].Resources
	if *r.CPU != 100 || *r.MemoryMB != 128 || *r.MemoryMaxMB != 256 {
		t.Errorf("default resources = cpu %d mem %d max %d", *r.CPU, *r.MemoryMB, *r.MemoryMaxMB)
	}
}

// TestScheduleRunJobNames: an app that shares names gets the sidecar, after
// the service's task; a one-service app doesn't.
func TestScheduleRunJobNames(t *testing.T) {
	multi := scheduleMultiApp()
	j := ScheduleRunJob(multi, "r", multi.Services[0], pinnedRates, []string{"x"}, 256, labSettings(), nil)
	tasks := j.TaskGroups[0].Tasks
	if len(tasks) != 2 || tasks[0].Name != "api" || tasks[1].Name != namesTaskName {
		t.Fatalf("tasks = %d", len(tasks))
	}
	if !tasks[1].Lifecycle.Sidecar || tasks[1].Lifecycle.Hook != "prestart" {
		t.Errorf("sidecar lifecycle = %+v", tasks[1].Lifecycle)
	}
	if len(j.TaskGroups[0].Services) != 0 {
		t.Error("the run registers a service")
	}

	one := scheduleApp()
	j = ScheduleRunJob(one, "r", one.Services[0], pinnedWorker, []string{"x"}, 256, labSettings(), nil)
	if n := len(j.TaskGroups[0].Tasks); n != 1 {
		t.Errorf("a one-service app's run has %d tasks", n)
	}
}

// TestScheduleRunJobHoldsNoSecret: the stored env is plain env; a secret's
// value is never an input here, and the job names the variable only.
func TestScheduleRunJobHoldsNoSecret(t *testing.T) {
	one := scheduleApp()
	one.Services[0].Secrets = []app.Secret{{Name: "API_KEY", Value: new("s3cr3t-marker")}}
	j := ScheduleRunJob(one, "r1", one.Services[0], pinnedWorker, []string{"x"}, 256, labSettings(), nil)
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("s3cr3t-marker")) {
		t.Error("the job holds a secret value")
	}
}
