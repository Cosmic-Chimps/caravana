package nomadjob

import (
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Builds (feature 050, plan D4). A build is one Nomad batch job per deployment
// of a built service: a prestart task clones the repository, the main task
// builds the Dockerfile with rootless BuildKit and pushes the image, and
// Caravana reads the digest the push reported. Like Translate, everything here
// is pure.

// BuildJobID is the job of build buildID of appID. It starts with the app's job
// prefix (AppJobPrefix), so it goes with the app's other helper jobs when the
// app is deleted.
func BuildJobID(appID, buildID string) string { return appID + "_build-" + buildID }

// BuildCacheVolumeName is the app volume name of the build cache (the part
// after "<app>_"). An app's own volumes may not start with "caravana-"
// (app.ReservedVolumePrefix), so it can never clash.
const BuildCacheVolumeName = app.ReservedVolumePrefix + "build"

// BuildCacheVolume is the Nomad host volume that keeps appID's build cache.
func BuildCacheVolume(appID string) string { return VolumeName(appID, BuildCacheVolumeName) }

// BuildVariablePath is where a build job reads its credentials and host keys:
// its own variable, the only one a job with this ID can read (see
// BackupVariablePath).
func BuildVariablePath(jobID string) string { return BackupVariablePath(jobID) }

// The items of a build job's variable. Caravana writes them before it registers
// the job and purges the variable with the job. None is in the job itself.
const (
	// BuildItemDeployKey is the private key (OpenSSH PEM) of a deploy-key build.
	BuildItemDeployKey = "deploy_key"
	// BuildItemGitCredentials is the line of the credential store (git's
	// "store" helper) of a token build: https://user:token@host, percent-encoded.
	BuildItemGitCredentials = "git_credentials"
	// BuildItemKnownHosts is the known_hosts text of a deploy-key build (KnownHosts).
	BuildItemKnownHosts = "known_hosts"
	// BuildItemDockerConfig is the config.json with the credentials to push
	// the image, when the registry entry that covers it has some.
	BuildItemDockerConfig = "docker_config"
)

// The group and tasks of a build job, and where each leaves its result,
// relative to the allocation's directory (the tasks see it as /alloc/...).
const (
	BuildGroup      = "build"
	BuildSourceTask = "source"
	BuildTask       = "build"
	// BuildSourceFile is written by the source task: COMMIT, AUTHOR and SUBJECT,
	// each base64 so no value can break the file.
	BuildSourceFile = "alloc/data/caravana/source.env"
	// BuildResultFile is BuildKit's metadata file: containerimage.digest is the
	// manifest digest the registry has, which is what Caravana pins.
	BuildResultFile = "alloc/data/caravana/build.json"
)

// BuildMarker starts every line the build scripts print to say what they are
// doing or why they stop, so Caravana can word the failure from the log
// (the phrases below follow it).
const BuildMarker = "caravana-build:"

// The reasons the scripts give after BuildMarker, which Caravana words a
// failure from. BuildDockerfileMissing and BuildFolderMissing are followed by
// the path.
const (
	BuildCloneFailed       = "the repository could not be read"
	BuildDockerfileMissing = "no Dockerfile at "
	BuildFolderMissing     = "no folder in the repository at "
	BuildNoDigest          = "the build did not report an image digest"
	BuildFailed            = "the build failed"
)

// What the job gives its tasks.
const (
	buildBuilderFlags    = "--oci-worker-no-process-sandbox"
	buildCacheMount      = "/cache"
	buildCacheRequest    = "cache"
	buildDockerConfigDir = "/secrets/docker"
	buildSourceMemoryMB  = 128
	buildSourceCPUMHz    = 200
	buildSourceLogFiles  = 2
	buildSourceLogMB     = 1
	buildLogFiles        = 3
	buildLogMB           = 4
)

// BuildImageTag is the tag a build of commit is pushed under: "git-" and the
// first 12 characters of the commit.
func BuildImageTag(commit string) string {
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return "git-" + commit
}

// BuildSpec is what BuildJob needs to make a build's job.
type BuildSpec struct {
	// AppID, BuildID (12 hex) and Service name the build.
	AppID, BuildID, Service string
	// Build is the source. BuildJob fills its defaults in.
	Build app.Build
	// Image is the repository the build pushes to: the service's image, without
	// a tag or digest. The image pushed is Image:git-<commit12>.
	Image string
	// PushInsecure pushes over plain HTTP: a registry on the node's loopback,
	// with credentials or without.
	PushInsecure bool
	// PushCredentials says the job's variable has BuildItemDockerConfig.
	PushCredentials bool
	// HostNetwork puts the job on the node's network, on the group and in both
	// tasks' docker config (the group's mode alone leaves Docker's bridge, as
	// for a scan, 044 R5): for a repository or a registry on the node's loopback.
	HostNetwork bool
	// AvoidIngress keeps the build off the server that holds the proxy.
	AvoidIngress bool
	// CacheMB is the size the build cache may grow to before the source task
	// empties it. 0 means no cache: no volume, no cache flags.
	CacheMB int
	// MemoryMB and CPUMHz are what the build task reserves.
	MemoryMB, CPUMHz int
	// BuilderImage and GitImage are the images of the build and source tasks,
	// pinned by digest. Empty means DefaultBuilderImage and DefaultGitImage.
	BuilderImage, GitImage string
}

// BuildJob is the job of one build, as plan D4 describes it:
//
//   - A batch job that is never retried: no restart and no reschedule, so a
//     failed build fails once and Caravana says why. Meta carries
//     caravana.app, caravana.service and caravana.build.
//   - One group on Docker's bridge (or the host's network, see
//     BuildSpec.HostNetwork), optionally kept off the proxy's server.
//   - Task "source" (prestart, GitImage) runs BuildSourceScript; task "build"
//     (main, BuilderImage) runs BuildScript. Both share /alloc/data, where the
//     clone and the results are, and, with a cache, the app's cache volume at
//     /cache.
//   - The job holds no secret. A deploy key, a token, the host keys and the
//     push credentials are items of the job's own variable (BuildVariablePath),
//     rendered into files by templates that never restart a task. Every value
//     the scripts read is plain env: the repository address, the branch, the
//     folder, the image.
//   - The build task runs rootless BuildKit, which needs seccomp and AppArmor
//     off for its own sandbox (research R1); the docker plugin's
//     allow_privileged stays false.
//
// The job must be purged after the build is read: it is a batch job that
// nothing else removes, and its variable may hold credentials.
func BuildJob(spec BuildSpec) *api.Job {
	id := BuildJobID(spec.AppID, spec.BuildID)
	b := spec.Build.Defaults()
	j := batchJob(id, spec.AppID, spec.Service)
	j.Meta["caravana.build"] = spec.BuildID

	sourceEnv := map[string]string{
		"BUILD_REPOSITORY": b.Repository,
		"BUILD_BRANCH":     b.Branch,
		"BUILD_PATH":       b.Path,
		"BUILD_DOCKERFILE": b.Dockerfile,
		"BUILD_ACCESS":     b.Access,
	}
	buildEnv := map[string]string{
		"BUILD_IMAGE":      spec.Image,
		"BUILD_PATH":       b.Path,
		"BUILD_DOCKERFILE": b.Dockerfile,
		"BUILDKITD_FLAGS":  buildBuilderFlags,
	}
	if spec.PushInsecure {
		buildEnv["BUILD_INSECURE"] = "1"
	}
	if spec.PushCredentials {
		buildEnv["DOCKER_CONFIG"] = buildDockerConfigDir
	}
	if spec.CacheMB > 0 {
		sourceEnv["BUILD_CACHE_MB"] = strconv.Itoa(spec.CacheMB)
		buildEnv["BUILD_CACHE_MB"] = strconv.Itoa(spec.CacheMB)
	}

	vars := BuildVariablePath(id)
	var sourceTemplates []*api.Template
	switch b.Access {
	case app.BuildAccessDeployKey:
		sourceTemplates = append(sourceTemplates,
			itemTemplate(vars, BuildItemDeployKey, "secrets/deploy_key", "0400"),
			itemTemplate(vars, BuildItemKnownHosts, "local/known_hosts", "0444"))
	case app.BuildAccessToken:
		sourceTemplates = append(sourceTemplates, itemTemplate(vars, BuildItemGitCredentials, "secrets/git-credentials", "0400"))
	}
	var buildTemplates []*api.Template
	if spec.PushCredentials {
		// The rootless builder runs as uid 1000, not root.
		buildTemplates = append(buildTemplates, itemTemplate(vars, BuildItemDockerConfig, "secrets/docker/config.json", "0444"))
	}

	source := &api.Task{
		Name:      BuildSourceTask,
		Driver:    "docker",
		Lifecycle: &api.TaskLifecycle{Hook: "prestart"},
		Config: map[string]any{
			"image":      spec.gitImage(),
			"entrypoint": []string{"sh", "-c"},
			"args":       []string{BuildSourceScript},
		},
		Env:       sourceEnv,
		Templates: sourceTemplates,
		Resources: &api.Resources{CPU: new(buildSourceCPUMHz), MemoryMB: new(buildSourceMemoryMB)},
		LogConfig: &api.LogConfig{MaxFiles: new(buildSourceLogFiles), MaxFileSizeMB: new(buildSourceLogMB)},
	}
	build := &api.Task{
		Name:   BuildTask,
		Driver: "docker",
		Config: map[string]any{
			"image":        spec.builderImage(),
			"entrypoint":   []string{"sh", "-c"},
			"args":         []string{BuildScript},
			"security_opt": []string{"seccomp=unconfined", "apparmor=unconfined"},
		},
		Env:       buildEnv,
		Templates: buildTemplates,
		Resources: &api.Resources{CPU: new(spec.CPUMHz), MemoryMB: new(spec.MemoryMB)},
		LogConfig: &api.LogConfig{MaxFiles: new(buildLogFiles), MaxFileSizeMB: new(buildLogMB)},
	}

	g := batchGroup(BuildGroup, source, build)
	if spec.HostNetwork {
		g.Networks = []*api.NetworkResource{{Mode: "host"}}
		source.Config["network_mode"] = "host"
		build.Config["network_mode"] = "host"
	}
	if spec.AvoidIngress {
		g.Constraints = []*api.Constraint{{LTarget: "${meta.caravana_ingress}", Operand: "!=", RTarget: "true"}}
	}
	if spec.CacheMB > 0 {
		g.Volumes = map[string]*api.VolumeRequest{buildCacheRequest: {
			Name:           buildCacheRequest,
			Type:           "host",
			Source:         BuildCacheVolume(spec.AppID),
			AccessMode:     "single-node-single-writer",
			AttachmentMode: "file-system",
		}}
		for _, t := range g.Tasks {
			t.VolumeMounts = []*api.VolumeMount{{
				Volume:      new(buildCacheRequest),
				Destination: new(buildCacheMount),
				ReadOnly:    new(false),
			}}
		}
	}
	j.TaskGroups = []*api.TaskGroup{g}
	return j
}

// BuildImages are the images of a build's two tasks under these settings: the
// builder and the Git image. Clean-up keeps them while an app has a built
// service, so a build never waits on a pull it could have avoided.
func (s Settings) BuildImages() []string {
	return []string{s.builderImage(), s.gitImage()}
}

// builderImage is the builder image these settings use.
func (s Settings) builderImage() string {
	if s.BuilderImage == "" {
		return DefaultBuilderImage
	}
	return s.BuilderImage
}

// gitImage is the Git image these settings use.
func (s Settings) gitImage() string {
	if s.GitImage == "" {
		return DefaultGitImage
	}
	return s.GitImage
}

func (s BuildSpec) builderImage() string {
	if s.BuilderImage == "" {
		return DefaultBuilderImage
	}
	return s.BuilderImage
}

func (s BuildSpec) gitImage() string {
	if s.GitImage == "" {
		return DefaultGitImage
	}
	return s.GitImage
}

// itemTemplate writes one item of the job's own variable to a file with the
// given permissions. It never restarts the task: the variable is written before
// the job is registered and never changes.
func itemTemplate(path, item, dest, perms string) *api.Template {
	return &api.Template{
		EmbeddedTmpl: new("{{ with nomadVar " + strconv.Quote(path) + " }}{{ ." + item + " }}{{ end }}"),
		DestPath:     new(dest),
		Perms:        new(perms),
		ChangeMode:   new("noop"),
	}
}

// DefaultBuilderImage is BuildKit v0.26.2, the rootless image, pinned by digest
// (research R1).
const DefaultBuilderImage = "moby/buildkit:v0.26.2-rootless@sha256:0ffa2fcf6b8757c47d569b3ef0f03f9d5eb3b9ff5ce68d858f994f89b749da0c"

// DefaultGitImage is the alpine/git image the source task clones with, pinned
// by digest (research R1).
const DefaultGitImage = "alpine/git@sha256:29cff25811f790b9f72fc266bf8009a5a911714c0e03c31c47339c50909d9aef"

// BuiltinKnownHosts are the SSH host keys of github.com and gitlab.com, as
// known_hosts lines (research R2: ssh-keyscan, compared with the providers'
// published fingerprints on 2026-11-23). app.KnownGitHosts names the same hosts.
const BuiltinKnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf
gitlab.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBFSMqzJeV9rUzU4kWitGjeR4PWSa29SPqJ1fVkhtj3Hw9xjLVXVYrU9QlYWrOLXBpQ6KWjbjTDTdDkoohFzgbEY=
gitlab.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCsj2bNKTBSpIYDEGk9KxsGh3mySTRgMtXL583qmBpzeQ+jqCMRgBqB98u3z++J1sKlXHWfM9dyhSevkMwSbhoR8XIq/U0tCNyokEi/ueaBMCvbcTHhO7FcwzY92WK4Yt0aGROY5qX2UKSeOvuP4D6TPqKF1onrSzH9bx9XUf2lEdWT/ia1NEKjunUqu1xOB/StKDHMoX4/OKyIzuS0q/T1zOATthvasJFoPrAjkohTyaDUz2LN5JoH839hViyEG82yB+MjcFV5MU3N1l1QL3cVUCh93xSaua1N85qivl+siMkPGbO5xR/En4iEY6K2XPASUEMaieWVNTRCtJ4S8H+9
`

// KnownHosts is the known_hosts text of a deploy-key build of host: the
// built-in keys of a host Caravana knows on port 22 (port 0 means 22), and the
// key the owner gave (hostKey, "<type> <base64>"). A host on another port is
// written [host]:port, as ssh looks it up. It is empty for a host with neither.
func KnownHosts(host string, port int, hostKey string) string {
	var b strings.Builder
	if app.HasBuiltinHostKeys(host, port) {
		for line := range strings.Lines(BuiltinKnownHosts) {
			if name, _, _ := strings.Cut(line, " "); strings.EqualFold(name, host) {
				b.WriteString(line)
			}
		}
	}
	if hostKey != "" {
		spec := host
		if port != 0 && port != 22 {
			spec = "[" + host + "]:" + strconv.Itoa(port)
		}
		b.WriteString(spec + " " + hostKey + "\n")
	}
	return b.String()
}
