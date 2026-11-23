package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
)

// Builds (feature 050). A build is one Nomad batch job, built by
// nomadjob.BuildJob, that clones a repository and builds and pushes its image.
// This file is the Nomad side: the Git access variables, the build cache's
// volume, starting a build with the variables its job reads, reading how far it
// got, what it produced and what it printed, and purging it. Package service
// decides what to build and when, and records the outcome.
//
// Every call takes the job's ID as a plain string: this package doesn't know
// how the ID is made. No value of a key, token or password is ever in an error.

// ErrGitAccessMissing: a build needs a stored deploy key or token that isn't
// there (it was removed since the deploy was accepted).
var ErrGitAccessMissing = errors.New("the service has no stored Git access")

// gitAccessPrefix is where a built service's Git access is kept, one variable
// per service.
const gitAccessPrefix = "caravana/builds/"

// The items of a service's Git access variable. A deploy key has Kind,
// PrivateKey, PublicKey and CreatedAt; a token has Kind, Username, Token and
// CreatedAt.
const (
	GitAccessKind       = "kind"
	GitAccessPrivateKey = "private_key"
	GitAccessPublicKey  = "public_key"
	GitAccessUsername   = "username"
	GitAccessToken      = "token"
	GitAccessCreatedAt  = "created_at"
)

// GitAccessPath is the Nomad Variable that holds the Git access of service of
// appID: "caravana/builds/<app>/<service>". Deleting the app deletes it
// (DeleteGitAccessOfApp).
func GitAccessPath(appID, service string) string {
	return gitAccessPrefix + appID + "/" + service
}

// GitAccess is a service's stored way into its repository. It holds secrets:
// callers answer with the public key and the kind only.
type GitAccess struct {
	// Kind is app.BuildAccessDeployKey or app.BuildAccessToken.
	Kind string
	// PrivateKey (OpenSSH PEM) and PublicKey (an authorized_keys line) are a
	// deploy key's two halves.
	PrivateKey, PublicKey string
	// Username and Token are a token's.
	Username, Token string
	// CreatedAt is when the access was made.
	CreatedAt time.Time
}

// ReadGitAccess returns the Git access stored for the service. found is false
// when there is none. Errors name the action, never the values.
func (d *Deployer) ReadGitAccess(ctx context.Context, appID, service string) (g GitAccess, found bool, err error) {
	items, found, err := d.readVariableIfAny(ctx, GitAccessPath(appID, service))
	if err != nil || !found {
		return GitAccess{}, false, err
	}
	g = GitAccess{Kind: items[GitAccessKind], PrivateKey: items[GitAccessPrivateKey], PublicKey: items[GitAccessPublicKey],
		Username: items[GitAccessUsername], Token: items[GitAccessToken]}
	if t, perr := time.Parse(time.RFC3339, items[GitAccessCreatedAt]); perr == nil {
		g.CreatedAt = t
	}
	return g, true, nil
}

// SaveGitAccess keeps g as the service's Git access, replacing what was stored.
func (d *Deployer) SaveGitAccess(ctx context.Context, appID, service string, g GitAccess) error {
	items := map[string]string{GitAccessKind: g.Kind, GitAccessCreatedAt: g.CreatedAt.UTC().Format(time.RFC3339)}
	switch g.Kind {
	case app.BuildAccessDeployKey:
		items[GitAccessPrivateKey], items[GitAccessPublicKey] = g.PrivateKey, g.PublicKey
	case app.BuildAccessToken:
		items[GitAccessUsername], items[GitAccessToken] = g.Username, g.Token
	default:
		return fmt.Errorf("save Git access of %s/%s: %q is not a kind of access", appID, service, g.Kind)
	}
	return d.writeVariable(ctx, GitAccessPath(appID, service), items)
}

// DeleteGitAccess forgets the service's Git access. One that isn't there is done.
func (d *Deployer) DeleteGitAccess(ctx context.Context, appID, service string) error {
	return d.deleteVariable(ctx, GitAccessPath(appID, service))
}

// DeleteGitAccessOfApp forgets the Git access of every service of appID, for an
// app that is deleted.
func (d *Deployer) DeleteGitAccessOfApp(ctx context.Context, appID string) error {
	prefix := gitAccessPrefix + appID + "/"
	metas, _, err := d.nomad.Variables().PrefixList(prefix, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return d.volumeError("list the Git access of "+appID, err)
	}
	for _, m := range metas {
		if strings.HasPrefix(m.Path, prefix) {
			if err := d.deleteVariable(ctx, m.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildCacheOwner is the user and group of the build cache directory: the
// rootless builder runs as uid 1000.
const buildCacheOwner = "1000"

// ensureBuildCache creates appID's build cache volume when Nomad doesn't have
// it and waits until it is ready. With avoidIngress the volume is made on a
// node the build job's constraint allows, and a volume that was made on the
// proxy's server before there was another one is deleted and made again: it is
// a cache, and a job that mounts a volume can only run where the volume is.
func (d *Deployer) ensureBuildCache(ctx context.Context, appID string, avoidIngress bool) error {
	name := nomadjob.BuildCacheVolume(appID)
	have, err := d.listVolumes(ctx, name)
	if err != nil {
		return err
	}
	var constraints []*api.Constraint
	if avoidIngress {
		constraints = []*api.Constraint{{LTarget: "${meta.caravana_ingress}", Operand: "!=", RTarget: "true"}}
	}
	if stub, ok := have[name]; ok {
		onProxy := false
		if avoidIngress && stub.NodeID != "" {
			node, err := d.nodeInfo(ctx, stub.NodeID)
			if err != nil {
				return err
			}
			onProxy = node.Meta[ingressMeta] == "true"
		}
		if !onProxy {
			if stub.State != api.HostVolumeStateReady {
				return d.waitReady(ctx, stub.ID, name)
			}
			return nil
		}
		if err := d.deleteVolume(ctx, name, stub.ID); err != nil {
			return err
		}
	}
	vol, err := d.createVolumeWith(ctx, name, "", constraints, map[string]string{"uid": buildCacheOwner, "gid": buildCacheOwner})
	if err != nil {
		return err
	}
	return d.waitReady(ctx, vol.ID, name)
}

// ingressMeta is the node metadata that marks the server that holds the proxy
// (written by the node agent).
const ingressMeta = "caravana_ingress"

// BuildAvoidsIngress says builds should be kept off the proxy's server: the
// cluster has a ready, eligible node that isn't the one with the proxy. With a
// single server (or none the agent manages) it is false, and builds run there.
func (d *Deployer) BuildAvoidsIngress(ctx context.Context) (bool, error) {
	nodes, err := d.Nodes(ctx)
	if err != nil {
		return false, err
	}
	for _, n := range nodes {
		if n.Status != "ready" || !n.Eligible || n.Draining {
			continue
		}
		info, err := d.nodeInfo(ctx, n.ID)
		if err != nil {
			return false, err
		}
		if info.Meta[ingressMeta] != "true" {
			return true, nil
		}
	}
	return false, nil
}

// DeleteBuildCache deletes appID's build cache volume and what it holds. Nomad
// refuses while a build job still mounts it, which is retried as for an app's
// volumes. A volume that isn't there is deleted.
func (d *Deployer) DeleteBuildCache(ctx context.Context, appID string) error {
	name := nomadjob.BuildCacheVolume(appID)
	have, err := d.listVolumes(ctx, name)
	if err != nil {
		return err
	}
	if stub, ok := have[name]; ok {
		return d.deleteVolume(ctx, name, stub.ID)
	}
	return nil
}

// BuildInput is what StartBuild needs besides the job: what its variable holds.
type BuildInput struct {
	// AppID and Service name the stored Git access to read.
	AppID, Service string
	// Repository is the address the job clones; a token's credential line is
	// made for its scheme, host and port.
	Repository string
	// Access is how the job reads the repository (app.BuildAccess*).
	Access string
	// KnownHosts is the text of the deploy-key job's known_hosts file.
	KnownHosts string
	// Image is the repository the job pushes to; PushEntry is the registry
	// entry (a host, or a host and path) whose credentials it pushes with, or
	// "" to push without.
	Image, PushEntry string
	// CacheMB and AvoidIngress are the job's: with a cache, its volume is made
	// first, on a node the constraint allows.
	CacheMB      int
	AvoidIngress bool
}

// StartBuild makes the build cache's volume when the job uses one, writes the
// job's own variable (the only one its tasks can read) and registers the job.
// The variable holds what the job's templates read: the deploy key or the
// token's credential line, the host keys, and the registry's push credentials
// as a Docker config. The job holds no value. A variable left behind by a
// failed start is deleted again.
//
// A deploy key or token the service no longer has is ErrGitAccessMissing.
// Credentials that went since the caller chose them leave the push anonymous,
// as an app's pull is after a removal. Nomad saying no is a *RunStartError; a
// Nomad that couldn't be reached is ErrNomadUnavailable, unwrapped.
func (d *Deployer) StartBuild(ctx context.Context, job *api.Job, in BuildInput) error {
	if job == nil || job.ID == nil {
		return errors.New("start build: the job has no ID")
	}
	path := nomadjob.BuildVariablePath(*job.ID)
	if in.CacheMB > 0 {
		if err := d.ensureBuildCache(ctx, in.AppID, in.AvoidIngress); err != nil {
			return err
		}
	}
	var items map[string]string // nil: the job reads no variable
	if readsVariable(job, path) {
		items = map[string]string{}
		switch in.Access {
		case app.BuildAccessDeployKey:
			g, err := d.gitAccessOf(ctx, in, app.BuildAccessDeployKey)
			if err != nil {
				return err
			}
			items[nomadjob.BuildItemDeployKey] = g.PrivateKey
			items[nomadjob.BuildItemKnownHosts] = in.KnownHosts
		case app.BuildAccessToken:
			g, err := d.gitAccessOf(ctx, in, app.BuildAccessToken)
			if err != nil {
				return err
			}
			line, err := gitCredentialLine(in.Repository, g.Username, g.Token)
			if err != nil {
				return fmt.Errorf("start build of %s: %w", in.AppID, err)
			}
			items[nomadjob.BuildItemGitCredentials] = line
		}
		if templateReads(job, nomadjob.BuildItemDockerConfig) {
			config := "{}\n"
			if in.PushEntry != "" {
				user, pass, found, err := d.RegistryCredentials(ctx, in.PushEntry)
				if err != nil {
					return err
				}
				if found {
					if config, err = dockerConfig(in.Image, user, pass); err != nil {
						return fmt.Errorf("start build of %s: %w", in.AppID, err)
					}
				}
			}
			items[nomadjob.BuildItemDockerConfig] = config
		}
		if !anyValue(items) {
			items[runMarkerItem] = *job.ID // Nomad refuses a variable with no value
		}
	}
	return d.registerWithVariable(ctx, job, path, items)
}

// gitAccessOf reads the stored access and checks it is of the wanted kind.
func (d *Deployer) gitAccessOf(ctx context.Context, in BuildInput, kind string) (GitAccess, error) {
	g, found, err := d.ReadGitAccess(ctx, in.AppID, in.Service)
	if err != nil {
		return GitAccess{}, err
	}
	if !found || g.Kind != kind || (kind == app.BuildAccessDeployKey && g.PrivateKey == "") || (kind == app.BuildAccessToken && g.Token == "") {
		return GitAccess{}, fmt.Errorf("%w: %s/%s", ErrGitAccessMissing, in.AppID, in.Service)
	}
	return g, nil
}

// templateReads says one of the job's templates renders item.
func templateReads(job *api.Job, item string) bool {
	for _, g := range job.TaskGroups {
		for _, t := range g.Tasks {
			for _, tmpl := range t.Templates {
				if tmpl.EmbeddedTmpl != nil && strings.Contains(*tmpl.EmbeddedTmpl, "{{ ."+item+" }}") {
					return true
				}
			}
		}
	}
	return false
}

// gitCredentialLine is the line of git's credential store for a token: the
// repository's scheme, host and port with the user name and token
// percent-encoded as the store expects.
func gitCredentialLine(repository, username, token string) (string, error) {
	scheme, _, ok := strings.Cut(repository, "://")
	if !ok || (scheme != "https" && scheme != "http") {
		return "", errors.New("a token needs an http or https repository")
	}
	repo, err := app.ParseRepository(repository)
	if err != nil {
		return "", errors.New("the repository address is not valid")
	}
	host := repo.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // an IPv6 address
	}
	if repo.Port != 0 {
		host = fmt.Sprintf("%s:%d", host, repo.Port)
	}
	u := url.URL{Scheme: scheme, User: url.UserPassword(username, token), Host: host}
	return u.String() + "\n", nil
}

// dockerConfig is the config.json that lets the builder push image with the
// credentials: {"auths": {"<registry>": {"auth": base64(user:password)}}}.
// Docker Hub's entry is keyed as Docker does.
func dockerConfig(image, user, pass string) (string, error) {
	ref, err := registry.Parse(image)
	if err != nil {
		return "", fmt.Errorf("the image %q is not valid", image)
	}
	host := ref.Registry
	if host == "docker.io" {
		host = "https://index.docker.io/v1/"
	}
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	doc, err := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": auth}}})
	if err != nil {
		return "", err
	}
	return string(doc) + "\n", nil
}

// BuildState reads the state of the build job jobID: what ScheduleRunState says
// of a run whose main task is the builder. A job Nomad doesn't have is
// ErrRunGone. A build whose source task failed has no exit code: the build task
// never ran.
func (d *Deployer) BuildState(ctx context.Context, jobID string) (RunState, error) {
	return d.ScheduleRunState(ctx, jobID, nomadjob.BuildTask)
}

// BuildResult is what a build that ended left in its allocation.
type BuildResult struct {
	// Commit, Author and Subject are what the source task cloned; empty when
	// it didn't get that far.
	Commit, Author, Subject string
	// Digest is the manifest digest the registry has for the pushed image,
	// "sha256:…"; empty when the build wrote none.
	Digest string
}

// BuildResult reads the two files of the build in allocation allocID: what was
// cloned and the digest BuildKit reported. A file that isn't there leaves its
// fields empty. The values are plain data the repository and the registry gave
// and are never logged.
func (d *Deployer) BuildResult(ctx context.Context, allocID string) (BuildResult, error) {
	var r BuildResult
	text, found, err := d.readCopyFile(ctx, allocID, nomadjob.BuildSourceFile)
	if err != nil {
		return BuildResult{}, err
	}
	if found {
		r.Commit, r.Author, r.Subject = parseSourceEnv(text)
	}
	text, found, err = d.readCopyFile(ctx, allocID, nomadjob.BuildResultFile)
	if err != nil {
		return BuildResult{}, err
	}
	if found {
		var meta struct {
			Digest string `json:"containerimage.digest"`
		}
		if json.Unmarshal([]byte(text), &meta) == nil && digestRE.MatchString(meta.Digest) {
			r.Digest = meta.Digest
		}
	}
	return r, nil
}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// commitRE is a Git object name: the commit the source task read.
var commitRE = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// parseSourceEnv reads the source file the clone leaves: COMMIT, AUTHOR and
// SUBJECT, each base64. A value that isn't valid base64 (or a commit that isn't
// hex) is left empty.
func parseSourceEnv(text string) (commit, author, subject string) {
	for line := range strings.Lines(text) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			continue
		}
		v := strings.ToValidUTF8(string(raw), "�")
		switch key {
		case "COMMIT":
			if commitRE.MatchString(v) {
				commit = v
			}
		case "AUTHOR":
			author = v
		case "SUBJECT":
			subject = v
		}
	}
	return commit, author, subject
}

// BuildLogBytes is how much of a build's output BuildLog returns.
const BuildLogBytes = 64 << 10

// buildLogSeparator is the line between the source task's output and the
// builder's.
const buildLogSeparator = "--- build ---"

// BuildLog reads what the build in allocation allocID printed: the source task's
// stdout and stderr, a separator line, then the builder's stdout and stderr,
// cut to the last 64 KiB. cut says the output was longer. The text is the
// repository's and the Dockerfile's output: it can hold anything, a secret
// included, so it is returned and not logged, and the caller masks it before
// keeping it. An allocation or file Nomad no longer has is no output.
func (d *Deployer) BuildLog(ctx context.Context, allocID string) (log string, cut bool, err error) {
	var b strings.Builder
	for i, task := range []string{nomadjob.BuildSourceTask, nomadjob.BuildTask} {
		if i > 0 {
			b.WriteString(buildLogSeparator + "\n")
		}
		stdout, stdoutCut, err := d.tail(ctx, allocID, task, LogStdout, BuildLogBytes)
		if err != nil {
			return "", false, err
		}
		stderr, stderrCut, err := d.tail(ctx, allocID, task, LogStderr, BuildLogBytes)
		if err != nil {
			return "", false, err
		}
		cut = cut || stdoutCut || stderrCut
		b.Write(stdout)
		b.Write(stderr)
	}
	log = b.String()
	if len(log) > BuildLogBytes {
		log, cut = tailOfText(log, BuildLogBytes), true
	}
	return log, cut, nil
}

// tailOfText is the last n bytes of s, starting on a line and on a character.
func tailOfText(s string, n int) string {
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// StopBuild stops the build job jobID: a deregister without purge, so Nomad
// kills the tasks and keeps the allocation, whose output the service can still
// read. A job Nomad doesn't have is ErrRunGone.
func (d *Deployer) StopBuild(ctx context.Context, jobID string) error {
	return d.StopScheduleRun(ctx, jobID)
}

// PurgeBuild erases the build job jobID with its allocations and deletes its
// variable, which may hold a key or credentials. A job or variable Nomad
// doesn't have is done. Call it after the result and the log are read: the
// purge takes the allocation's files and output with it.
func (d *Deployer) PurgeBuild(ctx context.Context, jobID string) error {
	if err := d.purgeJob(ctx, jobID); err != nil {
		return err
	}
	return d.deleteVariable(ctx, nomadjob.BuildVariablePath(jobID))
}

// BuildJob is a build job Nomad has.
type BuildJob struct {
	// ID is the job's ID; AppID and BuildID are its parts.
	ID, AppID, BuildID string
}

var buildJobRE = regexp.MustCompile(`^(.+)_build-([0-9a-f]{12})$`)

// BuildJobs lists the build jobs Nomad has, for the app (or for every app when
// appID is ""): what a process that ended left behind.
func (d *Deployer) BuildJobs(ctx context.Context, appID string) ([]BuildJob, error) {
	prefix := ""
	if appID != "" {
		prefix = nomadjob.AppJobPrefix(appID)
	}
	stubs, err := d.listJobs(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var out []BuildJob
	for _, s := range stubs {
		if m := buildJobRE.FindStringSubmatch(s.ID); m != nil && (appID == "" || m[1] == appID) {
			out = append(out, BuildJob{ID: s.ID, AppID: m[1], BuildID: m[2]})
		}
	}
	return out, nil
}
