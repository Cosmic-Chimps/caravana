package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Builds (feature 050, plan D5). A deployment of an app with a built service
// has a build first: Caravana clones the repository, builds its Dockerfile and
// pushes the image as a Nomad batch job, and then deploys the digest the push
// reported like any pinned image. The deploy call answers at once with the
// deployment pending and its build queued; a goroutine of the process (runBuild)
// does the rest, holding no lock of the app while it builds, so Stop, Restart and
// the backups go on. A failed build fails the deployment before anything is
// registered, so the running version keeps running.
//
// Secrets: the deploy key's private half, a token and the push credentials are in
// Nomad Variables only. This file never reads one into a log line, an error or a
// record; the build's output is masked of the values of the token and the push
// password before it is kept.

// The refusals of checkBuild. Each comes in a *RefusalError that names the
// field at fault, and the API maps each to its status and code.
var (
	// ErrGitAccessMissing: the build reads its repository with a deploy key or a
	// token, and the service has none of that kind stored (409).
	ErrGitAccessMissing = errors.New("the build needs a stored deploy key or token of the kind its access names: make one under Configure › Git access")
	// ErrGitHostKeyUnknown: an SSH repository on a host Caravana has no key for (422).
	ErrGitHostKeyUnknown = errors.New("this SSH host's key isn't known to Caravana: give it as hostKey (the output of ssh-keyscan -t ed25519 <host>, without the host), or use github.com or gitlab.com")
	// ErrBuildRegistryMissing: no registry entry covers the image to push to (422).
	ErrBuildRegistryMissing = errors.New("no registry entry covers the image to push to: add one under Settings › Registries with credentials that may push")
	// ErrBuildNotFound: the deployment didn't build (404).
	ErrBuildNotFound = errors.New("the deployment has no build")
)

// BuildError is why a build failed, as a deployment's error: "build failed: <reason>"
// and the last lines of the build's output, masked.
type BuildError struct {
	// Reason is Caravana's sentence, such as "the repository could not be read".
	Reason string
	// Tail is the last lines of the build's output.
	Tail []string
}

func (e *BuildError) Error() string {
	text := "build failed: " + e.Reason
	if len(e.Tail) > 0 {
		text += "\n" + strings.Join(e.Tail, "\n")
	}
	return text
}

// The reasons a build ends without an image that no marker line of the build's
// output gives (those are nomadjob's Build* phrases).
const (
	buildReasonRestarted = "Caravana restarted during the build"
	buildReasonLost      = "the build job was lost"
	buildReasonPush      = "the image could not be pushed"
	buildReasonMemory    = "the build ran out of memory: raise its limit in Settings › Builds"
	buildReasonNoStart   = "the build job could not start"
)

// buildTailLines is how many of the build's last lines a failed deployment's error carries.
const buildTailLines = 20

// The reasons a running or queued build is cancelled, as the cancel cause of its context.
var (
	errBuildSuperseded = errors.New("replaced by a newer deployment")
	errBuildStopped    = errors.New("the app was stopped")
	errBuildDeleted    = errors.New("the app was deleted")
)

// builtImage is the image a build pushed, for deployRecorded to deploy.
type builtImage struct {
	service string
	// pinned is repo@sha256:digest.
	pinned string
}

// prepinned is the map ResolveWith takes: service name to its pinned image. nil
// when the deployment built nothing.
func (b *builtImage) prepinned() map[string]string {
	if b == nil {
		return nil
	}
	return map[string]string{b.service: b.pinned}
}

// buildState is what the service keeps of builds in memory: the queue and the
// slots, and the build going for each app.
type buildState struct {
	mu      sync.Mutex
	running int                  // builds holding a slot
	queue   []string             // IDs of the builds waiting for a slot, oldest first
	runs    map[string]*buildRun // by app ID: the newest build, queued or going
	changed chan struct{}        // closed and replaced when a slot frees, a build leaves the queue or the limit changes

	poll     time.Duration // the longest between two reads of a build's job (an event makes one sooner)
	slotPoll time.Duration // the longest a queued build waits before looking again
	timeout  time.Duration // when positive, every build's limit in place of the settings' (tests)
}

func newBuildState() buildState {
	return buildState{runs: map[string]*buildRun{}, changed: make(chan struct{}), poll: 5 * time.Second, slotPoll: 30 * time.Second}
}

// SetBuildTimingForTests shortens the waits of builds: poll is the longest
// between two reads of a build's job, timeout (when positive) is every build's
// limit in place of the settings'. Call it before a build starts.
func (s *Service) SetBuildTimingForTests(poll, timeout time.Duration) {
	s.build.mu.Lock()
	defer s.build.mu.Unlock()
	if poll > 0 {
		s.build.poll, s.build.slotPoll = poll, poll
	}
	s.build.timeout = timeout
}

// buildSlotsChanged wakes the builds waiting for a slot.
func (s *Service) buildSlotsChanged() {
	b := &s.build
	b.mu.Lock()
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

// buildRun is one build and the deploy that follows it.
type buildRun struct {
	app     app.App
	svc     int // the built service's index in app.Services
	rec     store.DeploymentRecord
	m       made
	state   *appState
	buildID string
	cancel  context.CancelCauseFunc
	// release gives the build's slot back, once: the deploy that follows a build
	// doesn't hold a place that a queued build could use.
	release func()
}

// newBuildRecord is the queued build row of a deployment of a.
func (s *Service) newBuildRecord(a app.App, i int) store.BuildRecord {
	svc := a.Services[i]
	b := svc.Build.Defaults()
	var id [6]byte
	_, _ = rand.Read(id[:]) // crypto/rand's Read never returns an error
	return store.BuildRecord{ID: hex.EncodeToString(id[:]), Service: svc.Name,
		Repository: b.Repository, Branch: b.Branch, Image: svc.Image}
}

// withBuildDefaults returns a with the built service's build source written
// out in full (branch, folder, Dockerfile, access), so what is stored says what
// runs. The services slice is copied: the caller's isn't changed.
func withBuildDefaults(a app.App) app.App {
	i, ok := a.BuiltService()
	if !ok {
		return a
	}
	a.Services = append([]app.Service(nil), a.Services...)
	b := a.Services[i].Build.Defaults()
	a.Services[i].Build = &b
	return a
}

// checkBuild is what a deploy of a built service checks that needs the
// installation (the validation of the source itself is app's): that a deploy key or
// token of the kind the access names is stored, that an SSH host's key is known,
// and that a registry entry covers the image to push to, unless the registry is
// on the machine itself. Each refusal is a *RefusalError naming its field.
func (s *Service) checkBuild(ctx context.Context, a app.App) error {
	i, ok := a.BuiltService()
	if !ok {
		return nil
	}
	svc := a.Services[i]
	b := svc.Build.Defaults()
	field := fmt.Sprintf("services[%d]", i)
	repo, err := app.ParseRepository(b.Repository)
	if err != nil {
		return app.NewFieldError(field+".build.repository", "%s", err)
	}

	if b.Access != app.BuildAccessPublic {
		g, found, err := s.deployer.ReadGitAccess(ctx, a.ID, svc.Name)
		if err != nil {
			return gitStoreError("read", err)
		}
		if !found || g.Kind != b.Access {
			return &RefusalError{Field: field + ".build.access", Err: fmt.Errorf("service %q: %w", svc.Name, ErrGitAccessMissing)}
		}
	}
	if repo.SSH && b.HostKey == "" && !app.HasBuiltinHostKeys(repo.Host, repo.Port) {
		return &RefusalError{Field: field + ".build.hostKey", Err: fmt.Errorf("service %q, host %s: %w", svc.Name, repo.Host, ErrGitHostKeyUnknown)}
	}

	ref, err := registry.Parse(svc.Image)
	if err != nil {
		return app.NewFieldError(field+".image", "%v", err)
	}
	if !registry.IsLoopback(ref.Registry) {
		private, err := s.privateRegistries(ctx)
		if err != nil {
			return err
		}
		if _, covered := nomadjob.PrivateRegistry(svc.Image, private); !covered {
			return &RefusalError{Field: field + ".image", Err: fmt.Errorf("service %q, image %s: %w", svc.Name, svc.Image, ErrBuildRegistryMissing)}
		}
	}
	return nil
}

// dropBuildCache deletes the build cache of an app whose new definition has no
// built service any more (the old one had), after cancelling the build that may
// still use it. A failure is a warning: the cache is deleted with the app at the latest.
func (s *Service) dropBuildCache(ctx context.Context, old, next app.App) []app.Warning {
	if _, was := old.BuiltService(); !was {
		return nil
	}
	if _, is := next.BuiltService(); is {
		return nil
	}
	s.cancelBuild(old.ID, errBuildSuperseded)
	if err := s.deployer.DeleteBuildCache(ctx, old.ID); err != nil {
		s.log.Warn("delete the build cache of an app that no longer builds", "app", old.ID, "error", err)
		return []app.Warning{{Message: "the app no longer builds from a repository, but its build cache could not be deleted; it is deleted with the app"}}
	}
	return nil
}

// startBuild registers the build of a freshly recorded deployment and starts it
// in the background. A build of the same app that is still queued or going is
// cancelled: the newest deploy wins.
func (s *Service) startBuild(state *appState, a app.App, svc int, rec store.DeploymentRecord, m made, buildID string) {
	ctx, cancel := context.WithCancelCause(s.background())
	r := &buildRun{app: a, svc: svc, rec: rec, m: m, state: state, buildID: buildID, cancel: cancel}
	s.build.mu.Lock()
	prev := s.build.runs[a.ID]
	s.build.runs[a.ID] = r
	s.build.mu.Unlock()
	if prev != nil {
		prev.cancel(errBuildSuperseded)
	}
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.runBuild(ctx, r)
	}()
}

// cancelBuild cancels the build queued or going for appID, if any, for cause.
func (s *Service) cancelBuild(appID string, cause error) {
	s.build.mu.Lock()
	r := s.build.runs[appID]
	s.build.mu.Unlock()
	if r != nil {
		r.cancel(cause)
	}
}

// leaveBuild forgets r as its app's build, when it still is, and frees its context.
func (s *Service) leaveBuild(r *buildRun) {
	s.build.mu.Lock()
	if s.build.runs[r.app.ID] == r {
		delete(s.build.runs, r.app.ID)
	}
	s.build.mu.Unlock()
	r.cancel(nil)
}

// awaitBuildSlot waits until the build is the oldest queued and fewer builds than
// the settings' concurrency run, and takes the slot (release it with
// releaseBuildSlot). It gives up with the cause when the build is cancelled, and
// with errBuildSuperseded when its deployment is no longer the app's newest.
func (s *Service) awaitBuildSlot(ctx context.Context, r *buildRun) error {
	b := &s.build
	id := r.buildID
	b.mu.Lock()
	b.queue = append(b.queue, id)
	slotPoll := b.slotPoll
	b.mu.Unlock()
	leave := func(why error) error {
		b.mu.Lock()
		for i, q := range b.queue {
			if q == id {
				b.queue = append(b.queue[:i], b.queue[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		s.buildSlotsChanged()
		return why
	}
	for {
		switch {
		case ctx.Err() != nil:
			return leave(context.Cause(ctx))
		case s.latestOf(r.app.ID) != r.rec.Deployment.ID:
			return leave(errBuildSuperseded)
		}
		limit := 1
		if set, err := s.store.GetBuildSettings(ctx); err == nil {
			limit = set.Concurrency
		}
		b.mu.Lock()
		if len(b.queue) > 0 && b.queue[0] == id && b.running < limit {
			b.queue = b.queue[1:]
			b.running++
			b.mu.Unlock()
			s.buildSlotsChanged() // the next in line may take a second slot
			return nil
		}
		woke := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-woke:
		case <-time.After(slotPoll):
		}
	}
}

func (s *Service) releaseBuildSlot() {
	s.build.mu.Lock()
	s.build.running--
	s.build.mu.Unlock()
	s.buildSlotsChanged()
}

// buildPlan is what a build's job is made from.
type buildPlan struct {
	spec nomadjob.BuildSpec
	in   deploy.BuildInput
	// masks are the secret values the build's output is cleaned of.
	masks []string
	// timeout is how long the build may run.
	timeout time.Duration
}

// planBuild makes the job's spec and the variable's input for the build of r,
// from the settings and the cluster as they are now.
func (s *Service) planBuild(ctx context.Context, r *buildRun, set store.BuildSettings) (buildPlan, error) {
	svc := r.app.Services[r.svc]
	b := svc.Build.Defaults()
	repo, err := app.ParseRepository(b.Repository)
	if err != nil {
		return buildPlan{}, fmt.Errorf("the repository address is not valid")
	}
	ref, err := registry.Parse(svc.Image)
	if err != nil {
		return buildPlan{}, fmt.Errorf("the image to push to is not valid")
	}
	private, err := s.privateRegistries(ctx)
	if err != nil {
		return buildPlan{}, err
	}
	entry, covered := nomadjob.PrivateRegistry(svc.Image, private)

	// Off the proxy's server when there is another one. A cluster whose nodes
	// can't be read builds where Nomad puts it.
	avoid, err := s.deployer.BuildAvoidsIngress(ctx)
	if err != nil {
		s.log.Warn("build: read the cluster's servers; the build may run on the proxy's", "app", r.app.ID, "error", err)
		avoid = false
	}
	spec := nomadjob.BuildSpec{
		AppID: r.app.ID, BuildID: r.buildID, Service: svc.Name, Build: b, Image: svc.Image,
		// A loopback registry speaks plain HTTP, with or without credentials (as for a pull).
		PushInsecure:    registry.IsLoopback(ref.Registry),
		PushCredentials: covered,
		HostNetwork:     repo.Loopback() || registry.IsLoopback(ref.Registry),
		AvoidIngress:    avoid,
		CacheMB:         set.CacheMB, MemoryMB: set.MemoryMB, CPUMHz: set.CPUMHz,
	}
	in := deploy.BuildInput{AppID: r.app.ID, Service: svc.Name, Repository: b.Repository, Access: b.Access,
		Image: svc.Image, PushEntry: entry, CacheMB: set.CacheMB, AvoidIngress: avoid}
	if repo.SSH {
		in.KnownHosts = nomadjob.KnownHosts(repo.Host, repo.Port, b.HostKey)
	}
	plan := buildPlan{spec: spec, in: in, masks: s.buildMasks(ctx, r.app.ID, svc.Name, entry), timeout: time.Duration(set.TimeoutMinutes) * time.Minute}
	s.build.mu.Lock()
	if s.build.timeout > 0 {
		plan.timeout = s.build.timeout
	}
	s.build.mu.Unlock()
	return plan, nil
}

// buildMasks are the secret values a build's output is cleaned of: the token (when
// the service has one) and the password the image is pushed with. A value that
// can't be read is left out: the mask is a second line of defence, since neither
// value is ever on a command line or in the environment of a task that prints it.
func (s *Service) buildMasks(ctx context.Context, appID, service, pushEntry string) []string {
	var out []string
	if g, found, err := s.deployer.ReadGitAccess(ctx, appID, service); err == nil && found && g.Token != "" {
		out = append(out, g.Token)
	}
	if pushEntry != "" {
		if _, pass, found, err := s.deployer.RegistryCredentials(ctx, pushEntry); err == nil && found && pass != "" {
			out = append(out, pass)
		}
	}
	return out
}

// urlCredentialsRE matches the user:password of a URL.
var urlCredentialsRE = regexp.MustCompile(`(://)[^/@\s:]+:[^/@\s]+@`)

// maskBuildLog replaces each secret value, and the credentials of any URL, with
// stars.
func maskBuildLog(log string, secrets []string) string {
	for _, v := range secrets {
		if v != "" {
			log = strings.ReplaceAll(log, v, "***")
		}
	}
	return urlCredentialsRE.ReplaceAllString(log, "${1}***@")
}

// runBuild builds, then deploys. ctx is cancelled for newest-wins, stop and
// delete (each with its cause), and by the process ending.
func (s *Service) runBuild(ctx context.Context, r *buildRun) {
	defer s.leaveBuild(r)
	appID := r.app.ID

	if err := s.awaitBuildSlot(ctx, r); err != nil {
		s.buildCancelled(r, err, "")
		return
	}
	r.release = sync.OnceFunc(s.releaseBuildSlot)
	defer r.release()

	set, err := s.store.GetBuildSettings(ctx)
	if err != nil {
		s.buildFailed(r, "", &BuildError{Reason: "the build settings could not be read"}, "", false, deploy.BuildResult{})
		return
	}
	plan, err := s.planBuild(ctx, r, set)
	if err != nil {
		s.buildFailed(r, "", &BuildError{Reason: err.Error()}, "", false, deploy.BuildResult{})
		return
	}
	job := s.deployer.BuildJob(plan.spec)
	jobID := *job.ID

	if err := s.deployer.StartBuild(ctx, job, plan.in); err != nil {
		if ctx.Err() != nil {
			s.buildCancelled(r, context.Cause(ctx), jobID)
			return
		}
		// What is said is Caravana's own words; Nomad's text may quote paths and is logged only.
		s.log.Warn("build: start the build job", "app", appID, "build", r.buildID, "error", err)
		reason := "Nomad could not start the build job"
		if errors.Is(err, deploy.ErrGitAccessMissing) {
			reason = "the Git access of the service is missing"
		}
		s.buildFailed(r, jobID, &BuildError{Reason: reason}, "", false, deploy.BuildResult{})
		return
	}
	if err := s.store.StartBuild(context.WithoutCancel(ctx), r.buildID, time.Now().UTC()); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("build: record the start", "build", r.buildID, "error", err)
	}
	s.deploymentChanged(appID)

	noted := false
	st, reason := s.awaitBuild(ctx, jobID, plan.timeout, func(node string) {
		if !noted {
			noted = true
			if err := s.store.SetBuildNode(context.WithoutCancel(ctx), r.buildID, node); err != nil && !errors.Is(err, store.ErrNotFound) {
				s.log.Warn("build: record the node", "build", r.buildID, "error", err)
			}
		}
	})
	if ctx.Err() != nil {
		s.buildCancelled(r, context.Cause(ctx), jobID)
		return
	}
	s.settleBuild(ctx, r, plan, jobID, st, reason)
}

// awaitBuild reads the build's job until it ends or the limit passes (then the
// job is stopped). reason is set for a job that is gone or timed out; a
// cancelled ctx returns with an empty reason and the caller looks at ctx.
func (s *Service) awaitBuild(ctx context.Context, jobID string, limit time.Duration, onNode func(string)) (deploy.RunState, string) {
	s.build.mu.Lock()
	poll := s.build.poll
	s.build.mu.Unlock()
	timeout := time.NewTimer(limit)
	defer timeout.Stop()
	for {
		woke := s.wakes.channel(buildJobsKey)
		st, err := s.deployer.BuildState(ctx, jobID)
		switch {
		case ctx.Err() != nil:
			return deploy.RunState{}, ""
		case errors.Is(err, deploy.ErrRunGone):
			return st, buildReasonLost
		case err == nil && st.NodeID != "":
			onNode(st.NodeID)
		}
		if err == nil && st.Terminal {
			return st, ""
		}
		// Any other error is a Nomad that didn't answer this time: ask again.
		select {
		case <-ctx.Done():
			return deploy.RunState{}, ""
		case <-woke:
		case <-time.After(poll):
		case <-timeout.C:
			stop := context.WithoutCancel(ctx)
			if err := s.deployer.StopBuild(stop, jobID); err != nil && !errors.Is(err, deploy.ErrRunGone) {
				s.log.Warn("stop a build that ran too long", "job", jobID, "error", err)
			}
			return st, "the build took longer than " + durationWords(limit)
		}
	}
}

// settleBuild reads what the ended job left, records the build, and either goes on
// to deploy the image or fails the deployment.
func (s *Service) settleBuild(ctx context.Context, r *buildRun, plan buildPlan, jobID string, st deploy.RunState, reason string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	var res deploy.BuildResult
	var log string
	var cut bool
	if st.AllocID != "" {
		var err error
		if res, err = s.deployer.BuildResult(rctx, st.AllocID); err != nil {
			s.log.Warn("build: read the result", "build", r.buildID, "error", err)
		}
		if log, cut, err = s.deployer.BuildLog(rctx, st.AllocID); err != nil {
			s.log.Warn("build: read the output", "build", r.buildID, "error", err)
		}
	}
	log = maskBuildLog(log, plan.masks)

	if reason == "" && st.ClientStatus == "complete" {
		if res.Digest == "" || res.Commit == "" {
			reason = nomadjob.BuildNoDigest
		}
	} else if reason == "" {
		reason = buildFailureReason(log, st)
	}
	if reason != "" {
		s.buildFailed(r, jobID, &BuildError{Reason: reason, Tail: lastLines(log, buildTailLines)}, log, cut, res)
		return
	}

	svc := r.app.Services[r.svc]
	ref, err := registry.Parse(svc.Image)
	if err != nil { // checked by planBuild
		s.buildFailed(r, jobID, &BuildError{Reason: "the image to push to is not valid"}, log, cut, res)
		return
	}
	end := store.BuildEnd{Status: store.BuildSucceeded, Commit: res.Commit, Author: res.Author, Subject: res.Subject,
		Image: svc.Image + ":" + nomadjob.BuildImageTag(res.Commit), Digest: res.Digest, Log: log, LogCut: cut}
	if err := s.store.EndBuild(rctx, r.buildID, end); err != nil {
		if errors.Is(err, store.ErrNotFound) { // the app was deleted meanwhile
			s.purgeBuild(rctx, jobID)
			return
		}
		s.log.Error("record a build that succeeded", "build", r.buildID, "error", err)
	}
	s.purgeBuild(rctx, jobID)
	s.deploymentChanged(r.app.ID)

	// The origin of the deployment is the CI's when it gave one, else the commit.
	rec := r.rec
	if rec.Deployment.Origin == nil || rec.Deployment.Origin.IsZero() {
		origin := buildOrigin(*svc.Build, res)
		if err := s.store.SetDeploymentOrigin(rctx, rec.Deployment.ID, origin); err != nil {
			s.log.Warn("build: record the origin", "deployment", rec.Deployment.ID, "error", err)
		} else {
			rec.Deployment.Origin = &origin
		}
	}
	s.leaveBuild(r) // a newer deploy from here on supersedes the deployment, not the build
	r.release()
	_, _ = s.deployRecorded(context.WithoutCancel(ctx), r.state, rec, r.app, r.m, nil,
		&builtImage{service: svc.Name, pinned: ref.Pinned(res.Digest)})
}

// buildFailed records a failed build and fails its deployment with the build's
// error, which tells the owner. Nothing was registered, so the running version
// keeps running. jobID is purged when it isn't "".
func (s *Service) buildFailed(r *buildRun, jobID string, be *BuildError, log string, cut bool, res deploy.BuildResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	end := store.BuildEnd{Status: store.BuildFailed, Commit: res.Commit, Author: res.Author, Subject: res.Subject,
		Error: be.Error(), Log: log, LogCut: cut}
	if err := s.store.EndBuild(ctx, r.buildID, end); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("record a build that failed", "build", r.buildID, "error", err)
	}
	if jobID != "" {
		s.purgeBuild(ctx, jobID)
	}
	s.fail(r.rec, be)
}

// buildCancelled settles a build that was cancelled or whose process is ending.
// The cause says which: a newer deployment replaced it, the app was stopped, the
// app was deleted (its rows went with it) or Caravana is shutting down (the build
// stays open and the next start settles it).
func (s *Service) buildCancelled(r *buildRun, cause error, jobID string) {
	if !errors.Is(cause, errBuildSuperseded) && !errors.Is(cause, errBuildStopped) && !errors.Is(cause, errBuildDeleted) {
		return // shutdown
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if jobID != "" {
		if err := s.deployer.StopBuild(ctx, jobID); err != nil && !errors.Is(err, deploy.ErrRunGone) {
			s.log.Warn("build: stop a cancelled build", "build", r.buildID, "error", err)
		}
		s.purgeBuild(ctx, jobID)
	}
	if errors.Is(cause, errBuildDeleted) {
		return
	}
	if err := s.store.EndBuild(ctx, r.buildID, store.BuildEnd{Status: store.BuildCancelled, Error: cause.Error()}); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("record a build that was cancelled", "build", r.buildID, "error", err)
	}
	id := r.rec.Deployment.ID
	if errors.Is(cause, errBuildStopped) {
		s.fail(r.rec, &BuildError{Reason: "the app was stopped before the build finished"})
		return
	}
	msg := fmt.Sprintf("replaced by deployment %s before it was built", s.latestOf(r.app.ID))
	if upd, err := s.write(id, store.DeploymentUpdate{Status: store.StatusSuperseded, Error: msg}); err == nil {
		s.notifyDeploy(upd)
	} else if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrInvalidTransition) {
		s.log.Error("record superseded deployment", "deployment", id, "error", err)
	}
}

// purgeBuild erases the build's job and variable; a failure is logged, and the
// start-up sweep removes what is left.
func (s *Service) purgeBuild(ctx context.Context, jobID string) {
	if err := s.deployer.PurgeBuild(ctx, jobID); err != nil {
		s.log.Warn("build: purge the build job", "job", jobID, "error", err)
	}
}

// buildFailureReason words why a build job that ended without an image failed,
// from the last line of its output that gives a reason (the scripts print one
// after nomadjob.BuildMarker before they exit) and the job's own state.
func buildFailureReason(log string, st deploy.RunState) string {
	reason := ""
	for line := range strings.Lines(log) {
		line = strings.TrimSpace(line)
		text, ok := strings.CutPrefix(line, nomadjob.BuildMarker+" ")
		if !ok {
			continue
		}
		switch {
		case text == nomadjob.BuildCloneFailed, text == nomadjob.BuildFailed, text == nomadjob.BuildNoDigest,
			strings.HasPrefix(text, nomadjob.BuildDockerfileMissing), strings.HasPrefix(text, nomadjob.BuildFolderMissing):
			reason = text
		}
	}
	switch {
	case reason == nomadjob.BuildFailed && strings.Contains(strings.ToLower(log), "failed to push"):
		return buildReasonPush
	case reason != "":
		return reason
	case st.OOMKilled:
		return buildReasonMemory
	case st.StartFailed:
		return buildReasonNoStart
	}
	return nomadjob.BuildFailed
}

// lastLines is the last n non-empty lines of text, each cut to 400 bytes.
func lastLines(text string, n int) []string {
	var lines []string
	for line := range strings.Lines(text) {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			continue
		}
		if len(line) > 400 {
			line = strings.ToValidUTF8(line[:400], "") + "…"
		}
		lines = append(lines, line)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// buildOrigin is the origin of a deployment built from a repository: the commit,
// the branch, the author and the repository. Each part is cleaned to fit what
// app.Origin accepts, and a part that still doesn't is left out.
func buildOrigin(b app.Build, res deploy.BuildResult) app.Origin {
	b = b.Defaults()
	repository := ""
	if repo, err := app.ParseRepository(b.Repository); err == nil {
		repository = repo.Host + "/" + strings.TrimSuffix(strings.TrimSuffix(repo.Path, "/"), ".git")
	}
	o := app.Origin{Commit: res.Commit, Ref: "refs/heads/" + b.Branch, Actor: printableText(res.Author, 100), Repository: printableText(repository, 200)}.Normalized()
	for o.Validate() != nil {
		switch {
		case o.Repository != "":
			o.Repository = ""
		case o.Actor != "":
			o.Actor = ""
		case o.Ref != "":
			o.Ref = ""
		default:
			return app.Origin{}
		}
	}
	return o
}

// printableText keeps the printable characters of s, at most max bytes.
func printableText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "")
	}
	return strings.TrimSpace(s)
}

// recoverBuilds settles the builds a previous process left queued or running:
// a build doesn't outlive the process that ran it. Each fails with its
// deployment, so resume doesn't pick the deployment up, and the owner is told as
// for any failed deploy. The jobs Nomad still has go in the background
// (sweepBuildJobs): Start never waits for Nomad.
func (s *Service) recoverBuilds(ctx context.Context) error {
	open, err := s.store.OpenBuilds(ctx)
	if err != nil {
		return fmt.Errorf("settle the builds running at the last stop: %w", err)
	}
	for _, b := range open {
		be := &BuildError{Reason: buildReasonRestarted}
		if err := s.store.EndBuild(ctx, b.ID, store.BuildEnd{Status: store.BuildFailed, Error: be.Error()}); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("settle the build %s: %w", b.ID, err)
		}
		rec, err := s.store.GetDeployment(ctx, b.DeploymentID)
		if err != nil {
			continue
		}
		if upd, err := s.store.UpdateDeployment(ctx, b.DeploymentID, store.DeploymentUpdate{Status: store.StatusFailed, Error: be.Error()}); err == nil {
			s.notifyDeploy(upd)
		} else if !errors.Is(err, store.ErrInvalidTransition) {
			s.log.Warn("settle the deployment of an interrupted build", "deployment", rec.Deployment.ID, "error", err)
		}
	}
	s.backupWatcher.Add(1)
	go func() {
		defer s.backupWatcher.Done()
		s.sweepBuildJobs(ctx)
	}()
	return nil
}

// sweepBuildJobs purges the build jobs Nomad has whose build isn't open: what a
// process that ended left behind. A build that starts after Start has an open
// row before its job exists, so a live build is never swept.
func (s *Service) sweepBuildJobs(ctx context.Context) {
	jobs, err := s.deployer.BuildJobs(ctx, "")
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("sweep the build jobs of the last run", "error", err)
		}
		return
	}
	for _, j := range jobs {
		if b, err := s.store.GetBuild(ctx, j.BuildID); err == nil && !b.Final() {
			continue
		}
		s.purgeBuild(ctx, j.ID)
	}
}

// GetBuild returns the build of an app's deployment with its log. A build that is
// going has its log read from Nomad as it is now. An unknown app or deployment is
// store.ErrNotFound; a deployment that didn't build is ErrBuildNotFound.
func (s *Service) GetBuild(ctx context.Context, appID, deploymentID string) (store.BuildRecord, error) {
	if _, err := s.GetDeployment(ctx, appID, deploymentID); err != nil {
		return store.BuildRecord{}, err
	}
	b, err := s.store.GetBuildOfDeployment(ctx, deploymentID)
	if errors.Is(err, store.ErrNotFound) {
		return store.BuildRecord{}, fmt.Errorf("deployment %q: %w", deploymentID, ErrBuildNotFound)
	}
	if err != nil {
		return store.BuildRecord{}, err
	}
	if b.Status == store.BuildRunning {
		s.liveBuildLog(ctx, &b)
	}
	return b, nil
}

// liveBuildLog fills in the log of a build that is going from Nomad's output, masked.
// Any failure leaves it empty: the page says the build is running.
func (s *Service) liveBuildLog(ctx context.Context, b *store.BuildRecord) {
	st, err := s.deployer.BuildState(ctx, nomadjob.BuildJobID(b.AppID, b.ID))
	if err != nil || st.AllocID == "" {
		return
	}
	log, cut, err := s.deployer.BuildLog(ctx, st.AllocID)
	if err != nil {
		return
	}
	entry := ""
	if private, err := s.privateRegistries(ctx); err == nil {
		entry, _ = nomadjob.PrivateRegistry(b.Image, private)
	}
	b.Log, b.LogCut = maskBuildLog(log, s.buildMasks(ctx, b.AppID, b.Service, entry)), cut
}
