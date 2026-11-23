package nomadjob

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// buildCase is one shape of build job.
type buildCase struct {
	name string
	spec BuildSpec
}

func buildSpecBase() BuildSpec {
	return BuildSpec{
		AppID: "shop", BuildID: "3f9a0c1d2e4b", Service: "web",
		Build:    app.Build{Repository: "https://github.com/acme/shop.git"},
		Image:    "ghcr.io/acme/shop",
		CacheMB:  5120,
		MemoryMB: 2048, CPUMHz: 2000,
	}
}

// buildCases are the shapes of build job: a public repository, a deploy key
// and a token (the two with push credentials), a repository and registry on the
// node's loopback, and no cache.
func buildCases() []buildCase {
	public := buildSpecBase()

	key := buildSpecBase()
	key.Build = app.Build{Repository: "git@github.com:acme/shop.git", Branch: "release", Path: "services/web", Dockerfile: "docker/Dockerfile.prod", Access: app.BuildAccessDeployKey}
	key.PushCredentials = true
	key.AvoidIngress = true

	token := buildSpecBase()
	token.Build.Access = app.BuildAccessToken
	token.PushCredentials = true

	host := buildSpecBase()
	host.Build = app.Build{Repository: "http://127.0.0.1:8098/app.git"}
	host.Image = "127.0.0.1:5001/gate/app"
	host.PushInsecure = true
	host.HostNetwork = true

	none := buildSpecBase()
	none.CacheMB = 0
	none.BuilderImage = "registry.example.com/mirror/buildkit@sha256:" + strings.Repeat("a", 64)
	none.GitImage = "registry.example.com/mirror/git@sha256:" + strings.Repeat("b", 64)

	return []buildCase{
		{"build-public", public}, {"build-deploy-key", key}, {"build-token", token},
		{"build-host-network", host}, {"build-no-cache", none},
	}
}

// TestBuildJob compares each shape with its golden file.
func TestBuildJob(t *testing.T) {
	for _, tc := range buildCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(BuildJob(tc.spec), "", "  ")
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

func buildJobs() map[string]*api.Job {
	jobs := map[string]*api.Job{}
	for _, tc := range buildCases() {
		jobs[tc.name] = BuildJob(tc.spec)
	}
	return jobs
}

// TestBuildJobShape checks what the golden files can't say by looking.
func TestBuildJobShape(t *testing.T) {
	for name, j := range buildJobs() {
		if *j.ID != "shop_build-3f9a0c1d2e4b" || *j.Type != "batch" || j.Periodic != nil {
			t.Errorf("%s: job = %s %s periodic %v", name, *j.ID, *j.Type, j.Periodic)
		}
		if !strings.HasPrefix(*j.ID, AppJobPrefix("shop")) {
			t.Errorf("%s: %s lacks the app's job prefix: deleting the app wouldn't find it", name, *j.ID)
		}
		if j.Meta["caravana.app"] != "shop" || j.Meta["caravana.service"] != "web" || j.Meta["caravana.build"] != "3f9a0c1d2e4b" {
			t.Errorf("%s: meta = %v", name, j.Meta)
		}
		g := j.TaskGroups[0]
		if *g.Name != "build" || *g.RestartPolicy.Attempts != 0 || *g.ReschedulePolicy.Attempts != 0 {
			t.Errorf("%s: group %s must not be retried by Nomad", name, *g.Name)
		}
		if len(g.Tasks) != 2 || g.Tasks[0].Name != "source" || g.Tasks[1].Name != "build" {
			t.Fatalf("%s: tasks = %v, want source then build", name, g.Tasks)
		}
		source, build := g.Tasks[0], g.Tasks[1]
		if l := source.Lifecycle; l == nil || l.Hook != "prestart" || l.Sidecar {
			t.Errorf("%s: source lifecycle = %+v, want a prestart that ends", name, l)
		}
		if build.Lifecycle != nil {
			t.Errorf("%s: the build is the main task, lifecycle = %+v", name, build.Lifecycle)
		}
		if source.Config["image"] != DefaultGitImage && name != "build-no-cache" {
			t.Errorf("%s: source image = %v", name, source.Config["image"])
		}
		if build.Config["image"] != DefaultBuilderImage && name != "build-no-cache" {
			t.Errorf("%s: build image = %v", name, build.Config["image"])
		}
		for _, task := range g.Tasks {
			if !equalStrings(task.Config["entrypoint"], []string{"sh", "-c"}) {
				t.Errorf("%s/%s: entrypoint = %v", name, task.Name, task.Config["entrypoint"])
			}
		}
		if got := source.Config["args"].([]string); len(got) != 1 || got[0] != BuildSourceScript {
			t.Errorf("%s: source args are not BuildSourceScript", name)
		}
		if got := build.Config["args"].([]string); len(got) != 1 || got[0] != BuildScript {
			t.Errorf("%s: build args are not BuildScript", name)
		}
		if !equalStrings(build.Config["security_opt"], []string{"seccomp=unconfined", "apparmor=unconfined"}) || build.Env["BUILDKITD_FLAGS"] != "--oci-worker-no-process-sandbox" {
			t.Errorf("%s: rootless BuildKit needs security_opt and BUILDKITD_FLAGS: %v %v", name, build.Config["security_opt"], build.Env)
		}
		if _, ok := source.Config["security_opt"]; ok {
			t.Errorf("%s: only the build task asks for an unconfined profile", name)
		}
		if _, ok := build.Config["privileged"]; ok {
			t.Errorf("%s: the build must not be privileged: allow_privileged stays false", name)
		}
		if *build.Resources.MemoryMB != 2048 || *build.Resources.CPU != 2000 || *source.Resources.MemoryMB != 128 {
			t.Errorf("%s: resources = build %+v, source %+v", name, build.Resources, source.Resources)
		}
		// The job holds no secret: its templates read only its own variable.
		for _, task := range g.Tasks {
			for _, tmpl := range task.Templates {
				if *tmpl.ChangeMode != "noop" {
					t.Errorf("%s/%s: %s restarts the task", name, task.Name, *tmpl.DestPath)
				}
				if !strings.Contains(*tmpl.EmbeddedTmpl, `nomadVar "nomad/jobs/shop_build-3f9a0c1d2e4b"`) {
					t.Errorf("%s/%s: template %q doesn't read the job's own variable", name, task.Name, *tmpl.EmbeddedTmpl)
				}
			}
		}
	}
}

// templates are the destination and permissions of a task's templates.
func templates(task *api.Task) map[string]string {
	out := map[string]string{}
	for _, tmpl := range task.Templates {
		out[*tmpl.DestPath] = *tmpl.Perms
	}
	return out
}

func equalStrings(v any, want []string) bool {
	got, ok := v.([]string)
	if !ok || len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Access decides which files the source task gets, and the push credentials
// decide the build task's.
func TestBuildJobTemplates(t *testing.T) {
	jobs := buildJobs()
	for _, tc := range []struct {
		name          string
		source, build map[string]string
		dockerConfig  bool
	}{
		{"build-public", map[string]string{}, map[string]string{}, false},
		{"build-deploy-key", map[string]string{"secrets/deploy_key": "0400", "local/known_hosts": "0444"}, map[string]string{"secrets/docker/config.json": "0444"}, true},
		{"build-token", map[string]string{"secrets/git-credentials": "0400"}, map[string]string{"secrets/docker/config.json": "0444"}, true},
		{"build-host-network", map[string]string{}, map[string]string{}, false},
	} {
		g := jobs[tc.name].TaskGroups[0]
		if got := templates(g.Tasks[0]); len(got) != len(tc.source) || !sameMap(got, tc.source) {
			t.Errorf("%s: source templates = %v, want %v", tc.name, got, tc.source)
		}
		if got := templates(g.Tasks[1]); !sameMap(got, tc.build) {
			t.Errorf("%s: build templates = %v, want %v", tc.name, got, tc.build)
		}
		_, has := g.Tasks[1].Env["DOCKER_CONFIG"]
		if has != tc.dockerConfig {
			t.Errorf("%s: DOCKER_CONFIG set = %v, want %v", tc.name, has, tc.dockerConfig)
		}
		if tc.dockerConfig && g.Tasks[1].Env["DOCKER_CONFIG"] != "/secrets/docker" {
			t.Errorf("%s: DOCKER_CONFIG = %q", tc.name, g.Tasks[1].Env["DOCKER_CONFIG"])
		}
	}
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The network, the proxy's server and the cache are options of the job.
func TestBuildJobOptions(t *testing.T) {
	jobs := buildJobs()

	// Only the loopback build gets the node's network, on the group and in both
	// docker configs (the group's mode alone leaves Docker's bridge).
	for name, j := range jobs {
		g := j.TaskGroups[0]
		wantMode, wantDocker := "bridge", any(nil)
		if name == "build-host-network" {
			wantMode, wantDocker = "host", "host"
		}
		if len(g.Networks) != 1 || g.Networks[0].Mode != wantMode {
			t.Errorf("%s: networks = %+v, want %s", name, g.Networks, wantMode)
		}
		for _, task := range g.Tasks {
			if task.Config["network_mode"] != wantDocker {
				t.Errorf("%s/%s: network_mode = %v, want %v", name, task.Name, task.Config["network_mode"], wantDocker)
			}
		}
	}

	// Only the build that asked is kept off the ingress.
	for name, j := range jobs {
		c := j.TaskGroups[0].Constraints
		if name == "build-deploy-key" {
			if len(c) != 1 || c[0].LTarget != "${meta.caravana_ingress}" || c[0].Operand != "!=" || c[0].RTarget != "true" {
				t.Errorf("%s: constraints = %+v, want the ingress avoided", name, c)
			}
		} else if len(c) != 0 {
			t.Errorf("%s: constraints = %+v, want none", name, c)
		}
	}

	// The cache: a writable volume of the app at /cache in both tasks, or none.
	for name, j := range jobs {
		g := j.TaskGroups[0]
		if name == "build-no-cache" {
			if len(g.Volumes) != 0 || len(g.Tasks[0].VolumeMounts) != 0 || len(g.Tasks[1].VolumeMounts) != 0 {
				t.Errorf("%s: no cache means no volume: %+v", name, g.Volumes)
			}
			if _, ok := g.Tasks[0].Env["BUILD_CACHE_MB"]; ok {
				t.Errorf("%s: BUILD_CACHE_MB is set without a cache", name)
			}
			continue
		}
		v := g.Volumes["cache"]
		if v == nil || v.Type != "host" || v.Source != "shop_caravana-build" || v.ReadOnly {
			t.Errorf("%s: volume = %+v, want the app's writable cache volume", name, v)
		}
		for _, task := range g.Tasks {
			if m := task.VolumeMounts; len(m) != 1 || *m[0].Destination != "/cache" || *m[0].Volume != "cache" || *m[0].ReadOnly {
				t.Errorf("%s/%s: mounts = %+v", name, task.Name, m)
			}
			if task.Env["BUILD_CACHE_MB"] != "5120" {
				t.Errorf("%s/%s: BUILD_CACHE_MB = %q", name, task.Name, task.Env["BUILD_CACHE_MB"])
			}
		}
	}

	// Insecure pushes only when asked.
	for name, j := range jobs {
		_, insecure := j.TaskGroups[0].Tasks[1].Env["BUILD_INSECURE"]
		if insecure != (name == "build-host-network") {
			t.Errorf("%s: BUILD_INSECURE set = %v", name, insecure)
		}
	}
}

// The source task gets the defaults written out, so the script reads no empty value.
func TestBuildJobEnvHasTheDefaults(t *testing.T) {
	env := BuildJob(buildSpecBase()).TaskGroups[0].Tasks[0].Env
	for k, want := range map[string]string{
		"BUILD_REPOSITORY": "https://github.com/acme/shop.git", "BUILD_BRANCH": "main", "BUILD_PATH": ".",
		"BUILD_DOCKERFILE": "Dockerfile", "BUILD_ACCESS": "public",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
}

func TestBuildNames(t *testing.T) {
	if got := BuildJobID("shop", "3f9a0c1d2e4b"); got != "shop_build-3f9a0c1d2e4b" {
		t.Errorf("BuildJobID = %q", got)
	}
	if got := BuildCacheVolume("shop"); got != "shop_caravana-build" {
		t.Errorf("BuildCacheVolume = %q", got)
	}
	if got := BuildImageTag("4e0a77c1b2d3aaaaaaaa"); got != "git-4e0a77c1b2d3" {
		t.Errorf("BuildImageTag = %q", got)
	}
	// An app's own volume can't have the cache's name.
	a := app.Volume{Name: BuildCacheVolumeName, Path: "/d"}
	if !strings.HasPrefix(a.Name, app.ReservedVolumePrefix) {
		t.Errorf("the cache volume's name %q doesn't start with the reserved prefix", a.Name)
	}
}

// The default images are pinned by digest, as the other helper images are.
func TestBuildDefaultImagesArePinned(t *testing.T) {
	for _, img := range []string{DefaultBuilderImage, DefaultGitImage} {
		if !pinnedImageRE.MatchString(img) {
			t.Errorf("%s is not pinned by digest", img)
		}
	}
}

func TestKnownHosts(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample"
	tests := []struct {
		name string
		host string
		port int
		key  string
		want string // lines the text must hold, in order; "" for none at all
	}{
		{"github built in", "github.com", 0, "", "github.com ssh-ed25519 \ngithub.com ecdsa-sha2-nistp256 \ngithub.com ssh-rsa "},
		{"gitlab built in", "gitlab.com", 22, "", "gitlab.com ssh-ed25519 \ngitlab.com ecdsa-sha2-nistp256 \ngitlab.com ssh-rsa "},
		{"unknown host without a key", "git.example.com", 0, "", ""},
		{"github on another port needs a key", "github.com", 2222, "", ""},
		{"own key", "git.example.com", 0, key, "git.example.com " + key + "\n"},
		{"own key on a port", "git.example.com", 2222, key, "[git.example.com]:2222 " + key + "\n"},
		{"own key with port 22", "git.example.com", 22, key, "git.example.com " + key + "\n"},
		{"key given for a known host is added", "github.com", 0, key, "github.com ssh-rsa "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := KnownHosts(tc.host, tc.port, tc.key)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("KnownHosts = %q, want none", got)
				}
				return
			}
			if !strings.HasSuffix(got, "\n") {
				t.Errorf("KnownHosts = %q must end with a newline", got)
			}
			// Each wanted line is a prefix of a line of the text, in order.
			rest := got
			for _, want := range strings.Split(strings.TrimSuffix(tc.want, "\n"), "\n") {
				i := strings.Index(rest, want)
				if i < 0 {
					t.Fatalf("KnownHosts = %q lacks %q (in order)", got, want)
				}
				rest = rest[i+len(want):]
			}
			if tc.host != "github.com" && tc.host != "gitlab.com" || tc.port > 22 {
				if strings.Contains(got, "github.com") || strings.Contains(got, "gitlab.com") {
					t.Errorf("KnownHosts = %q holds another host's keys", got)
				}
			}
			if tc.host == "github.com" && tc.key == "" && strings.Contains(got, "gitlab.com") {
				t.Errorf("KnownHosts(github.com) = %q holds gitlab's keys", got)
			}
		})
	}
}

// BuiltinKnownHosts holds exactly the hosts app.KnownGitHosts names, and every
// line is host, type, key.
func TestBuiltinKnownHostsMatchTheModel(t *testing.T) {
	hosts := map[string]int{}
	for line := range strings.Lines(BuiltinKnownHosts) {
		f := strings.Fields(line)
		if len(f) != 3 {
			t.Fatalf("known_hosts line %q must be host, type and key", line)
		}
		hosts[f[0]]++
	}
	if len(hosts) != len(app.KnownGitHosts) {
		t.Errorf("BuiltinKnownHosts has %v, app.KnownGitHosts has %v", hosts, app.KnownGitHosts)
	}
	for _, h := range app.KnownGitHosts {
		if hosts[h] != 3 {
			t.Errorf("BuiltinKnownHosts has %d lines for %s, want an ed25519, an ecdsa and an rsa key", hosts[h], h)
		}
	}
}
