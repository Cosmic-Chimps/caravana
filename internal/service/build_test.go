package service

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The tests of builds (feature 050) run the real flow against the fake Nomad: a
// deploytest.BuildPlayer stands in for Git and BuildKit and ends each build job the
// way the test says.

const (
	buildCommit  = "4e0a77c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7"
	buildDigest  = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	buildTokenS  = "s3cret-sentinel-050-svc-token"
	buildKeyS    = "s3cret-sentinel-050-svc-key"
	buildPushS   = "s3cret-sentinel-050-svc-push"
	builtImageID = "127.0.0.1:5001/acme/web" // a loopback registry: no registry entry is needed
)

// buildLab is a service with its store's directory known, a log that keeps
// everything, and a player of builds.
type buildLab struct {
	*env
	dir    string
	logs   *syncBuffer
	player *deploytest.BuildPlayer
}

// newBuildLab starts a service on the fakes. play says how each build ends; nil
// makes every build succeed.
func newBuildLab(t *testing.T, play func(deploytest.PlayedBuild) deploytest.BuildPlay) *buildLab {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.nomad.Succeed()
	e.nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, log)
	e.svc.SetBuildTimingForTests(5*time.Millisecond, 0)
	if play == nil {
		play = func(deploytest.PlayedBuild) deploytest.BuildPlay { return succeedingBuild() }
	}
	return &buildLab{env: e, dir: dir, logs: logs, player: deploytest.PlayBuilds(t, e.nomad, play)}
}

func succeedingBuild() deploytest.BuildPlay {
	return deploytest.BuildPlay{Commit: buildCommit, Author: "Ana Ñandú", Subject: "Retry supplier timeouts", Digest: buildDigest,
		SourceOut: "caravana-build: cloned commit " + buildCommit + "\n", BuildOut: "#1 [1/2] FROM busybox\n", BuildErr: "caravana-build: pushed " + builtImageID + "\n"}
}

// builtApp is a one-service app built from a repository on this machine and pushed to a
// registry on this machine, so it needs neither Git access nor a registry entry.
func builtApp() app.App {
	a := whoami()
	a.ID, a.Name = "shop", "shop"
	a.Services[0].Image = builtImageID
	a.Services[0].Domains = []app.Domain{{Host: "shop.localhost", Port: "http"}}
	a.Services[0].Build = &app.Build{Repository: "http://127.0.0.1:8098/app.git"}
	return a
}

func (l *buildLab) deploy(t *testing.T, a app.App) store.DeploymentRecord {
	t.Helper()
	rec, err := l.svc.Deploy(context.Background(), a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return rec
}

// everywhere is every place a secret could leak to, as one text: the data directory's files
// (the SQLite file and its log), the server log, the fake Nomad's registered jobs and the
// text of every deployment and build record.
func (l *buildLab) everywhere(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(l.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		b.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString(l.logs.String())
	for _, id := range l.nomad.JobIDs("") {
		b.WriteString(mustMarshal(t, l.nomad.Job(id)))
	}
	return b.String()
}

func TestBuildDeploysTheDigestTheBuildPushed(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)

	rec := l.deploy(t, builtApp())
	// The call answers at once: pending, with its build queued (or already going).
	if rec.Status != store.StatusPending || rec.Build == nil || rec.Build.ID == "" || rec.Build.Service != "web" ||
		rec.Build.Repository != "http://127.0.0.1:8098/app.git" || rec.Build.Branch != "main" || rec.Build.Image != builtImageID {
		t.Fatalf("record = status %s, build %+v", rec.Status, rec.Build)
	}
	final := waitFinal(t, l.store, rec.Deployment.ID)
	if final.Status != store.StatusHealthy {
		t.Fatalf("deployment = %s (%s), want healthy", final.Status, final.Error)
	}
	// The digest the push reported is what runs: the image is never looked up.
	if got, want := final.Deployment.Digests["web"], builtImageID+"@"+buildDigest; got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}
	b, err := l.store.GetBuildOfDeployment(ctx, rec.Deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BuildSucceeded || b.Commit != buildCommit || b.Author != "Ana Ñandú" || b.Subject != "Retry supplier timeouts" ||
		b.Digest != buildDigest || b.Image != builtImageID+":git-4e0a77c1b2d3" || b.StartedAt.IsZero() || b.EndedAt.IsZero() ||
		!strings.Contains(b.Log, "FROM busybox") || !strings.Contains(b.Log, "--- build ---") {
		t.Errorf("build = %+v", b)
	}
	// The origin is the commit, as a CI deploy's would be.
	if o := final.Deployment.Origin; o == nil || o.Commit != buildCommit || o.Ref != "refs/heads/main" || o.Actor != "Ana Ñandú" || o.Repository != "127.0.0.1/app" {
		t.Errorf("origin = %+v", o)
	}
	// The stored app says what runs: the defaults are written out.
	stored, err := l.store.GetApp(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := *stored.App.Services[0].Build; got != (app.Build{Repository: "http://127.0.0.1:8098/app.git", Branch: "main", Path: ".", Dockerfile: "Dockerfile", Access: "public"}) {
		t.Errorf("stored build = %+v", got)
	}
	// The build job is gone with its variable, and the app's job runs the pushed digest.
	for _, id := range l.nomad.JobIDs("shop_build-") {
		if !l.nomad.Purged(id) {
			t.Errorf("build job %s wasn't purged", id)
		}
	}
	job := l.nomad.Job("shop")
	if job == nil || !strings.Contains(mustMarshal(t, job), builtImageID+"@"+buildDigest) {
		t.Errorf("the app's job doesn't run the pushed digest: %v", job)
	}
	// The job of the build: loopback repository and registry mean the host network and an insecure push.
	played := l.player.Played()
	if len(played) != 1 {
		t.Fatalf("builds played = %d", len(played))
	}
	pb := played[0]
	if pb.BuildEnv["BUILD_INSECURE"] != "1" || pb.SourceEnv["BUILD_REPOSITORY"] != "http://127.0.0.1:8098/app.git" ||
		pb.Job.TaskGroups[0].Networks[0].Mode != "host" || pb.Job.TaskGroups[0].Tasks[0].Config["network_mode"] != "host" {
		t.Errorf("job env = %v / %v, networks = %+v", pb.SourceEnv, pb.BuildEnv, pb.Job.TaskGroups[0].Networks)
	}
	if m := pb.Job.TaskGroups[0].Tasks[1].Resources; *m.MemoryMB != 2048 || *m.CPU != 2000 {
		t.Errorf("build resources = %+v, want the settings' defaults", m)
	}
}

func TestAFailedBuildFailsTheDeploymentAndRegistersNothing(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, func(deploytest.PlayedBuild) deploytest.BuildPlay {
		return deploytest.BuildPlay{Fail: nomadjob.BuildCloneFailed, SourceOut: "caravana-build: cloning branch main of http://127.0.0.1:8098/app.git\n",
			SourceErr: "fatal: unable to access 'http://127.0.0.1:8098/app.git/': Failed to connect\n"}
	})
	rec := l.deploy(t, builtApp())
	final := waitFinal(t, l.store, rec.Deployment.ID)
	if final.Status != store.StatusFailed {
		t.Fatalf("deployment = %s, want failed", final.Status)
	}
	if !strings.HasPrefix(final.Error, "build failed: the repository could not be read\n") || !strings.Contains(final.Error, "Failed to connect") {
		t.Errorf("error = %q", final.Error)
	}
	b, _ := l.store.GetBuildOfDeployment(ctx, rec.Deployment.ID)
	if b.Status != store.BuildFailed || !strings.HasPrefix(b.Error, "build failed: the repository could not be read") || !strings.Contains(b.Log, "Failed to connect") {
		t.Errorf("build = %+v", b)
	}
	// Nothing was registered for the app: the running version, if any, is left alone.
	if l.nomad.Job("shop") != nil {
		t.Error("a failed build registered the app's job")
	}
	// The failure is told like any failed deploy.
	if _, ok := deployEvent(final, ""); !ok {
		t.Error("the failed deployment makes no notification")
	}
}

func TestABuildThatReportsNoDigestFails(t *testing.T) {
	l := newBuildLab(t, func(deploytest.PlayedBuild) deploytest.BuildPlay {
		p := succeedingBuild()
		p.Digest = ""
		return p
	})
	rec := l.deploy(t, builtApp())
	final := waitFinal(t, l.store, rec.Deployment.ID)
	if final.Status != store.StatusFailed || !strings.HasPrefix(final.Error, "build failed: "+nomadjob.BuildNoDigest) {
		t.Errorf("deployment = %s %q", final.Status, final.Error)
	}
}

func TestTheNewestDeployWinsOverABuildInProgress(t *testing.T) {
	ctx := context.Background()
	hold := true
	l := newBuildLab(t, func(pb deploytest.PlayedBuild) deploytest.BuildPlay {
		p := succeedingBuild()
		if hold {
			hold = false
			p.Hold = true
		}
		return p
	})
	first := l.deploy(t, builtApp())
	// The player has seen the first job (and held it) before the second deploy comes.
	waitFor(t, "the first build's job", func() bool { return len(l.player.Played()) == 1 })
	second := l.deploy(t, builtApp())

	if got := waitFinal(t, l.store, first.Deployment.ID); got.Status != store.StatusSuperseded || !strings.Contains(got.Error, second.Deployment.ID) {
		t.Errorf("first = %s %q, want superseded by the second", got.Status, got.Error)
	}
	if b, _ := l.store.GetBuildOfDeployment(ctx, first.Deployment.ID); b.Status != store.BuildCancelled {
		t.Errorf("first build = %s, want cancelled", b.Status)
	}
	if got := waitFinal(t, l.store, second.Deployment.ID); got.Status != store.StatusHealthy {
		t.Errorf("second = %s %q, want healthy", got.Status, got.Error)
	}
	// The cancelled build's job was stopped and purged, so it holds no volume or variable.
	waitFor(t, "the first job to be purged", func() bool {
		for _, id := range l.nomad.JobIDs("shop_build-") {
			if id == "shop_build-"+first.Build.ID && !l.nomad.Purged(id) {
				return false
			}
		}
		return true
	})
}

func TestBuildsWaitForAFreeSlot(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, func(deploytest.PlayedBuild) deploytest.BuildPlay {
		p := succeedingBuild()
		p.Hold = true
		return p
	})
	a, b := builtApp(), builtApp()
	b.ID, b.Name = "shop2", "shop2"
	b.Services[0].Domains = []app.Domain{{Host: "shop2.localhost", Port: "http"}}
	first := l.deploy(t, a)
	waitFor(t, "the first build", func() bool { return len(l.nomad.JobIDs("shop_build-")) == 1 })
	second := l.deploy(t, b)

	// One build at once (the default): the second stays queued while the first runs.
	time.Sleep(60 * time.Millisecond)
	if got := l.nomad.JobIDs("shop2_build-"); len(got) != 0 {
		t.Fatalf("the second build started while the first runs: %v", got)
	}
	if rb, _ := l.store.GetBuildOfDeployment(ctx, second.Deployment.ID); rb.Status != store.BuildQueued {
		t.Errorf("second build = %s, want queued", rb.Status)
	}
	l.player.Finish("shop_build-"+first.Build.ID, succeedingBuild())
	waitFor(t, "the second build to start", func() bool { return len(l.nomad.JobIDs("shop2_build-")) == 1 })

	// A higher concurrency lets a queued build start at once.
	if _, err := l.svc.SetBuildSettings(ctx, BuildSettings{MemoryMB: 2048, CPUMHz: 2000, TimeoutMinutes: 30, Concurrency: 2, CacheMB: 5120}); err != nil {
		t.Fatal(err)
	}
	c := builtApp()
	c.ID, c.Name = "shop3", "shop3"
	c.Services[0].Domains = []app.Domain{{Host: "shop3.localhost", Port: "http"}}
	l.deploy(t, c)
	waitFor(t, "the third build to start beside the second", func() bool { return len(l.nomad.JobIDs("shop3_build-")) == 1 })
}

func TestABuildThatTakesTooLongIsStopped(t *testing.T) {
	l := newBuildLab(t, func(deploytest.PlayedBuild) deploytest.BuildPlay {
		return deploytest.BuildPlay{Hold: true}
	})
	l.svc.SetBuildTimingForTests(5*time.Millisecond, 120*time.Millisecond)
	rec := l.deploy(t, builtApp())
	final := waitFinal(t, l.store, rec.Deployment.ID)
	if final.Status != store.StatusFailed || !strings.HasPrefix(final.Error, "build failed: the build took longer than 120ms") {
		t.Errorf("deployment = %s %q", final.Status, final.Error)
	}
	if l.nomad.Stops("shop_build-"+rec.Build.ID) == 0 {
		t.Error("the job wasn't stopped")
	}
}

func TestBuildRefusalsComeBeforeAnythingIsSaved(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)

	cases := []struct {
		name   string
		mutate func(a *app.App)
		field  string
		want   error
	}{
		{"a deploy key that isn't stored", func(a *app.App) {
			a.Services[0].Build = &app.Build{Repository: "ssh://git@github.com/acme/web.git", Access: app.BuildAccessDeployKey}
		}, "services[0].build.access", ErrGitAccessMissing},
		{"a token that isn't stored", func(a *app.App) {
			a.Services[0].Build = &app.Build{Repository: "https://github.com/acme/web.git", Access: app.BuildAccessToken}
			a.Services[0].Image = "127.0.0.1:5001/acme/web"
		}, "services[0].build.access", ErrGitAccessMissing},
		{"an SSH host with no known key", func(a *app.App) {
			a.Services[0].Build = &app.Build{Repository: "ssh://git@git.example.com/acme/web.git", Access: app.BuildAccessDeployKey}
			l.storeKey(t, "shop")
		}, "services[0].build.hostKey", ErrGitHostKeyUnknown},
		{"an image on a registry no entry covers", func(a *app.App) {
			a.Services[0].Image = "registry.example.com/acme/web"
		}, "services[0].image", ErrBuildRegistryMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := builtApp()
			tc.mutate(&a)
			_, err := l.svc.Deploy(ctx, a, app.TriggerManual)
			var ref *RefusalError
			if !errors.As(err, &ref) || ref.Field != tc.field || !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want a refusal of %s: %v", err, tc.field, tc.want)
			}
			if _, err := l.store.GetApp(ctx, "shop"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("the app was saved: %v", err)
			}
			if recent, _ := l.store.ListRecentDeployments(ctx, 10); len(recent) != 0 {
				t.Errorf("a deployment was recorded: %+v", recent)
			}
		})
	}

	// A registry entry covering the image is enough, and a stored key of the right kind too.
	if _, err := l.svc.SaveRegistry(ctx, "registry.example.com", "deploy", new(buildPushS)); err != nil {
		t.Fatal(err)
	}
	a := builtApp()
	a.Services[0].Image = "registry.example.com/acme/web"
	if _, err := l.svc.Deploy(ctx, a, app.TriggerManual); err != nil {
		t.Errorf("with an entry: %v", err)
	}
}

// storeKey stores a deploy key for the web service of appID.
func (l *buildLab) storeKey(t *testing.T, appID string) {
	t.Helper()
	g := deploy.GitAccess{Kind: app.BuildAccessDeployKey, PrivateKey: buildKeyS, PublicKey: "ssh-ed25519 AAAA", CreatedAt: time.Now()}
	if err := l.svc.deployer.SaveGitAccess(context.Background(), appID, "web", g); err != nil {
		t.Fatal(err)
	}
}

func TestARedeployChecksTheGitAccessAgain(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	a := builtApp()
	a.Services[0].Build = &app.Build{Repository: "ssh://git@github.com/acme/web.git", Access: app.BuildAccessDeployKey}
	l.storeKey(t, "shop")
	rec := l.deploy(t, a)
	if got := waitFinal(t, l.store, rec.Deployment.ID); got.Status != store.StatusHealthy {
		t.Fatalf("first deploy = %s %q", got.Status, got.Error)
	}
	// The deploy key is in the job's own variable, and never in the job.
	pb := l.player.Played()[0]
	if pb.Variable[nomadjob.BuildItemDeployKey] != buildKeyS || pb.Variable[nomadjob.BuildItemKnownHosts] == "" {
		t.Errorf("the job's variable = %v", pb.Variable)
	}
	if strings.Contains(l.everywhere(t), buildKeyS) {
		t.Error("the private key is in the data directory, the log or a registered job")
	}

	if err := l.svc.RemoveGitAccess(ctx, "shop", "web"); err != nil {
		t.Fatal(err)
	}
	_, err := l.svc.Redeploy(ctx, "shop", app.TriggerWebhook)
	if !errors.Is(err, ErrGitAccessMissing) {
		t.Fatalf("redeploy without the key = %v, want ErrGitAccessMissing", err)
	}
	if list, _ := l.store.ListDeployments(ctx, "shop", 0); len(list) != 1 {
		t.Errorf("a refused redeploy recorded a deployment: %d", len(list))
	}
}

func TestARollbackAndAPinnedRedeployNeverBuild(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	if _, err := l.svc.SaveRegistry(ctx, "ghcr.io", "deploy", new(buildPushS)); err != nil {
		t.Fatal(err)
	}
	l.registry.AddIndex("acme/web", "git-4e0a77c1b2d3", buildDigest)
	a := builtApp()
	a.Services[0].Image = "ghcr.io/acme/web"
	rec := l.deploy(t, a)
	if final := waitFinal(t, l.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("deployment = %s %q", final.Status, final.Error)
	}
	waitFor(t, "the lock", func() bool {
		st := l.svc.stateOf("shop")
		if !st.mu.TryLock() {
			return false
		}
		st.mu.Unlock()
		return true
	})
	again, err := l.svc.RedeployVersion(ctx, "shop", rec.Deployment.ID, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if again.Build != nil {
		t.Errorf("a redeploy of a version has a build: %+v", again.Build)
	}
	if got := len(l.player.Played()); got != 1 {
		t.Errorf("builds played = %d, want only the first", got)
	}
}

func TestBuildsStartedBeforeARestartAreFailed(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	if _, err := l.store.SaveApp(ctx, withBuildDefaults(builtApp()).WithoutSecretValues(), nil); err != nil {
		t.Fatal(err)
	}
	a := builtApp()
	dep := app.Deployment{ID: "d-restart", Trigger: app.TriggerManual, Digests: map[string]string{}}
	if _, err := l.store.CreateDeploymentWithBuild(ctx, "shop", dep, &a, nil, "",
		store.BuildRecord{ID: "aaaaaaaaaaaa", Service: "web", Repository: "http://127.0.0.1:8098/app.git", Branch: "main", Image: builtImageID}); err != nil {
		t.Fatal(err)
	}
	// A job the last process left, whose build is no longer open, and one of a live build.
	l.nomad.AddForeignJob(&api.Job{ID: new("shop_build-aaaaaaaaaaaa"), Name: new("shop_build-aaaaaaaaaaaa")})

	// A second service on the same store and Nomad is the restart.
	svc := startService(t, l.store, l.registry, l.nomad, l.secrets)
	_ = svc
	got := waitFinal(t, l.store, "d-restart")
	if got.Status != store.StatusFailed || got.Error != "build failed: Caravana restarted during the build" {
		t.Errorf("deployment = %s %q", got.Status, got.Error)
	}
	if b, _ := l.store.GetBuild(ctx, "aaaaaaaaaaaa"); b.Status != store.BuildFailed {
		t.Errorf("build = %s, want failed", b.Status)
	}
	waitFor(t, "the stray job to be purged", func() bool { return l.nomad.Purged("shop_build-aaaaaaaaaaaa") })
}

func TestBuildSecretsAreOnlyInNomadVariables(t *testing.T) {
	ctx := context.Background()
	hold := true
	l := newBuildLab(t, func(pb deploytest.PlayedBuild) deploytest.BuildPlay {
		p := succeedingBuild()
		// The build's own output prints the token and the password it was given.
		p.BuildErr = "fatal: could not read from https://ana:" + buildTokenS + "@github.com/acme/web.git using " + buildTokenS + " and " + buildPushS + "\n" +
			"caravana-build: " + nomadjob.BuildFailed + "\n"
		if hold {
			p.Fail = nomadjob.BuildFailed
			hold = false
		}
		return p
	})
	if _, err := l.svc.SaveRegistry(ctx, "registry.example.com", "deploy", new(buildPushS)); err != nil {
		t.Fatal(err)
	}
	a := builtApp()
	a.Services[0].Image = "registry.example.com/acme/web"
	a.Services[0].Build = &app.Build{Repository: "https://github.com/acme/web.git", Access: app.BuildAccessToken}
	if _, err := l.svc.SetGitToken(ctx, "shop", "web", "", buildTokenS); err == nil {
		t.Fatal("a token for an app that doesn't exist yet was accepted")
	}
	if _, err := l.svc.CreateAppOnly(ctx, a); err != nil {
		t.Fatal(err)
	}
	view, err := l.svc.SetGitToken(ctx, "shop", "web", "", buildTokenS)
	if err != nil || view.Kind != app.BuildAccessToken || view.Username != "x-access-token" {
		t.Fatalf("SetGitToken = %+v, %v", view, err)
	}
	rec := l.deploy(t, a)
	final := waitFinal(t, l.store, rec.Deployment.ID)
	if final.Status != store.StatusFailed {
		t.Fatalf("deployment = %s %q, want failed (the first build fails)", final.Status, final.Error)
	}
	// The job read the token and the push credentials from its own variable.
	pb := l.player.Played()[0]
	if !strings.Contains(pb.Variable[nomadjob.BuildItemGitCredentials], buildTokenS) || !strings.Contains(pb.Variable[nomadjob.BuildItemDockerConfig], `"registry.example.com"`) {
		t.Errorf("the job's variable = %v", pb.Variable)
	}
	// The error and the kept log are masked, and the key and token are nowhere but Nomad's variables.
	b, _ := l.store.GetBuildOfDeployment(ctx, rec.Deployment.ID)
	for _, text := range []string{final.Error, b.Error, b.Log} {
		if strings.Contains(text, buildTokenS) || strings.Contains(text, buildPushS) {
			t.Errorf("a secret is in %q", text)
		}
	}
	if !strings.Contains(b.Log, "***") {
		t.Errorf("the log has no mask: %q", b.Log)
	}

	// A deploy key too: made, shown by its public half, its private half in Nomad's variable only.
	if _, err := l.svc.NewDeployKey(ctx, "shop", "web"); err != nil {
		t.Fatal(err)
	}
	g, found, _ := l.svc.deployer.ReadGitAccess(ctx, "shop", "web")
	if !found || !strings.Contains(g.PrivateKey, "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatalf("the stored key = %+v", g)
	}
	view, err = l.svc.GitAccess(ctx, "shop", "web")
	if err != nil || view.Kind != app.BuildAccessDeployKey || !strings.HasPrefix(view.PublicKey, "ssh-ed25519 ") ||
		!strings.HasSuffix(view.PublicKey, " caravana shop/web") || !strings.HasPrefix(view.Fingerprint, "SHA256:") {
		t.Fatalf("GitAccess = %+v, %v", view, err)
	}
	if strings.Contains(view.PublicKey, "PRIVATE") || strings.Contains(fmtView(view), g.PrivateKey) {
		t.Error("the view holds the private key")
	}
	everything := l.everywhere(t)
	for name, secret := range map[string]string{"the token": buildTokenS, "the push password": buildPushS, "the private key": strings.Split(g.PrivateKey, "\n")[1]} {
		if strings.Contains(everything, secret) {
			t.Errorf("%s is in the data directory, the log or a registered job", name)
		}
	}
	// A token replaces the key, and removing it leaves nothing.
	if _, err := l.svc.SetGitToken(ctx, "shop", "web", "ana", buildTokenS); err != nil {
		t.Fatal(err)
	}
	if err := l.svc.RemoveGitAccess(ctx, "shop", "web"); err != nil {
		t.Fatal(err)
	}
	if err := l.svc.RemoveGitAccess(ctx, "shop", "web"); !errors.Is(err, ErrGitAccessNotFound) {
		t.Errorf("removing twice = %v", err)
	}
	if view, _ := l.svc.GitAccess(ctx, "shop", "web"); view.Kind != GitAccessNone {
		t.Errorf("after removal: %+v", view)
	}
	// Deleting the app deletes the access.
	l.storeKey(t, "shop")
	if err := l.svc.DeleteApp(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if got := l.nomad.Variable(deploy.GitAccessPath("shop", "web")); got != nil {
		t.Errorf("the access survived the app: %v", keysOfVar(got))
	}
}

func fmtView(v GitAccessView) string {
	return v.Kind + v.PublicKey + v.Fingerprint + v.Username
}

func keysOfVar(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestGitAccessRefusals(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	if _, err := l.svc.GitAccess(ctx, "nope", "web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app = %v", err)
	}
	if _, err := l.svc.CreateAppOnly(ctx, builtApp()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.GitAccess(ctx, "shop", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown service = %v", err)
	}
	var fe *app.FieldError
	for name, call := range map[string]func() error{
		"an empty token":       func() error { _, err := l.svc.SetGitToken(ctx, "shop", "web", "", "  "); return err },
		"a token with a space": func() error { _, err := l.svc.SetGitToken(ctx, "shop", "web", "", "a b"); return err },
		"a long token": func() error {
			_, err := l.svc.SetGitToken(ctx, "shop", "web", "", strings.Repeat("a", 5000))
			return err
		},
		"a bad user name": func() error { _, err := l.svc.SetGitToken(ctx, "shop", "web", "a b", "tok"); return err },
	} {
		if err := call(); !errors.As(err, &fe) {
			t.Errorf("%s = %v, want a field error", name, err)
		} else if strings.Contains(err.Error(), "tok") && name == "a bad user name" {
			t.Errorf("%s echoed the token: %v", name, err)
		}
	}
	// A service that isn't built has no Git access to make.
	plain := whoami()
	if _, err := l.svc.CreateAppOnly(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.NewDeployKey(ctx, "whoami", "web"); !errors.As(err, &fe) || fe.Field != "service" {
		t.Errorf("NewDeployKey of a plain service = %v", err)
	}
	if view, err := l.svc.GitAccess(ctx, "whoami", "web"); err != nil || view.Kind != GitAccessNone {
		t.Errorf("GitAccess of a plain service = %+v, %v", view, err)
	}
	// A store that can't be reached is an unavailable secret store.
	l.nomad.FailVariables(http.StatusForbidden)
	if _, err := l.svc.NewDeployKey(ctx, "shop", "web"); !errors.Is(err, secrets.ErrUnavailable) {
		t.Errorf("NewDeployKey with Nomad refusing variables = %v", err)
	}
}

func TestBuildSettings(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	got, err := l.svc.BuildSettings(ctx)
	if err != nil || got != (BuildSettings{MemoryMB: 2048, CPUMHz: 2000, TimeoutMinutes: 30, Concurrency: 1, CacheMB: 5120}) {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	for field, mutate := range map[string]func(*BuildSettings){
		"memoryMB":       func(s *BuildSettings) { s.MemoryMB = 100 },
		"cpuMHz":         func(s *BuildSettings) { s.CPUMHz = 40000 },
		"timeoutMinutes": func(s *BuildSettings) { s.TimeoutMinutes = 2 },
		"concurrency":    func(s *BuildSettings) { s.Concurrency = 9 },
		"cacheMB":        func(s *BuildSettings) { s.CacheMB = -1 },
	} {
		in := got
		mutate(&in)
		var fe *app.FieldError
		if _, err := l.svc.SetBuildSettings(ctx, in); !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: error = %v", field, err)
		}
	}
	in := BuildSettings{MemoryMB: 4096, CPUMHz: 3000, TimeoutMinutes: 45, Concurrency: 2, CacheMB: 0}
	if saved, err := l.svc.SetBuildSettings(ctx, in); err != nil || saved != in {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	// The next build runs with them, and a cache of 0 means no cache volume.
	rec := l.deploy(t, builtApp())
	waitFinal(t, l.store, rec.Deployment.ID)
	job := l.player.Played()[0].Job
	build := job.TaskGroups[0].Tasks[1]
	if *build.Resources.MemoryMB != 4096 || *build.Resources.CPU != 3000 || len(job.TaskGroups[0].Volumes) != 0 {
		t.Errorf("job = %+v, volumes %v", build.Resources, job.TaskGroups[0].Volumes)
	}
}

func TestOnlyABuiltAppsCacheIsDroppedWhenItStopsBuilding(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	rec := l.deploy(t, builtApp())
	waitFinal(t, l.store, rec.Deployment.ID)
	if got := l.nomad.HostVolumeNode("shop_caravana-build"); got == "" {
		t.Fatal("the build made no cache volume")
	}
	// The app is edited to run a plain image: the cache has no use.
	plain := whoami()
	plain.ID, plain.Name = "shop", "shop"
	plain.Services[0].Domains = []app.Domain{{Host: "shop.localhost", Port: "http"}}
	l.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	rec2, err := l.svc.Deploy(ctx, plain, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, l.store, rec2.Deployment.ID)
	if got := l.nomad.HostVolumeNode("shop_caravana-build"); got != "" {
		t.Errorf("the cache survived on node %q", got)
	}
}

func TestBuildHelpersAreKeptByCleanup(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	if _, err := l.svc.CreateAppOnly(ctx, builtApp()); err != nil {
		t.Fatal(err)
	}
	keep, err := l.svc.keepDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range l.svc.deployer.BuildImages() {
		d, _ := digestOf(image)
		found := false
		for _, k := range keep {
			found = found || k == d
		}
		if !found {
			t.Errorf("clean-up doesn't keep %s while an app builds", image)
		}
	}
}

func TestTheBuildCacheIsNoVolumeOfTheApp(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	rec := l.deploy(t, builtApp())
	waitFinal(t, l.store, rec.Deployment.ID)
	if l.nomad.HostVolumeNode("shop_caravana-build") == "" {
		t.Fatal("the build made no cache")
	}
	views, err := l.svc.Volumes(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if strings.HasPrefix(v.Name, app.ReservedVolumePrefix) {
			t.Errorf("the volumes panel lists Caravana's own volume %q", v.Name)
		}
	}
	// A volume of an app may not start with Caravana's prefix.
	a := builtApp()
	a.Services[0].Volumes = []app.Volume{{Name: "caravana-build", Path: "/c"}}
	a.Services[0].Replicas = new(1)
	var fe *app.FieldError
	if _, err := l.svc.Deploy(ctx, a, app.TriggerManual); !errors.As(err, &fe) || !strings.HasSuffix(fe.Field, ".volumes[0].name") {
		t.Errorf("a volume named caravana-build = %v", err)
	}
}

func TestAPromotionIntoABuiltTargetBuildsNothingAndNeedsNoGitAccess(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	if _, err := l.svc.CreateEnvironment(ctx, "default", "staging"); err != nil {
		t.Fatal(err)
	}
	// The source reads its repository with a token it has; the target, which takes the source's
	// build source, has no token stored: a deploy of it would be refused.
	if _, err := l.svc.SaveRegistry(ctx, "ghcr.io", "deploy", new(buildPushS)); err != nil {
		t.Fatal(err)
	}
	l.registry.AddIndex("acme/web", "git-4e0a77c1b2d3", buildDigest) // a promotion checks the image is still in its registry
	source := builtApp()
	source.Services[0].Image = "ghcr.io/acme/web"
	source.Services[0].Build = &app.Build{Repository: "https://git.example.com/acme/web.git", Access: app.BuildAccessToken}
	if _, err := l.svc.CreateAppOnly(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.SetGitToken(ctx, "shop", "web", "", buildTokenS); err != nil {
		t.Fatal(err)
	}
	rec, err := l.svc.Redeploy(ctx, "shop", app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitFinal(t, l.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("deployment = %s %q", final.Status, final.Error)
	}
	waitFor(t, "the lock", func() bool {
		st := l.svc.stateOf("shop")
		if !st.mu.TryLock() {
			return false
		}
		st.mu.Unlock()
		return true
	})
	target := builtApp()
	target.ID, target.Name = "default--staging--shop", "shop"
	target.Services[0].Domains = nil
	target.Services[0].Image = "ghcr.io/acme/web"
	target.Services[0].Build = &app.Build{Repository: "https://git.example.com/acme/old.git", Access: app.BuildAccessPublic}
	if _, err := l.svc.CreateAppOnly(ctx, target); err != nil {
		t.Fatalf("CreateAppOnly: %v", err)
	}
	out, err := l.svc.Promote(ctx, "shop", "staging", Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out.Build != nil {
		t.Errorf("a promotion has a build: %+v", out.Build)
	}
	if got := len(l.player.Played()); got != 1 {
		t.Errorf("builds = %d, want only the source's", got)
	}
	if final := waitFinal(t, l.store, out.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("promotion = %s %q", final.Status, final.Error)
	}
	// The next plain deploy of the target is what is refused.
	if _, err := l.svc.Redeploy(ctx, "default--staging--shop", app.TriggerManual); !errors.Is(err, ErrGitAccessMissing) {
		t.Errorf("a deploy of the target = %v, want ErrGitAccessMissing", err)
	}
}

func TestCreateAppBuildsAPublicRepositoryAndRefusesOneThatNeedsAccess(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	private := builtApp()
	private.Services[0].Build = &app.Build{Repository: "ssh://git@github.com/acme/web.git", Access: app.BuildAccessDeployKey}
	var ref *RefusalError
	if _, err := l.svc.CreateApp(ctx, private, app.TriggerManual); !errors.As(err, &ref) || !errors.Is(err, ErrGitAccessMissing) {
		t.Fatalf("CreateApp of a private repository = %v, want ErrGitAccessMissing", err)
	}
	if _, err := l.store.GetApp(ctx, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the refused app was stored: %v", err)
	}

	rec, err := l.svc.CreateApp(ctx, builtApp(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Build == nil || rec.Status != store.StatusPending {
		t.Fatalf("record = %s, build %+v", rec.Status, rec.Build)
	}
	if final := waitFinal(t, l.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("deployment = %s %q", final.Status, final.Error)
	}
	stored, _ := l.store.GetApp(ctx, "shop")
	if b := stored.App.Services[0].Build; b == nil || b.Branch != "main" || b.Access != "public" {
		t.Errorf("stored build = %+v, want the defaults written", b)
	}
}
