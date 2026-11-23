package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

const (
	buildAppID    = "web"
	buildID       = "abcdef012345"
	buildJobID    = "web_build-" + buildID
	buildImage    = "ghcr.io/acme/web"
	buildDigest   = "sha256:" + runDigest
	buildKeySent  = "s3cret-sentinel-050-private-key"
	buildTokSent  = "s3cret-sentinel-050-token"
	buildPushSent = registrySecret
)

func buildSpec(access string, repo string, cache int) nomadjob.BuildSpec {
	return nomadjob.BuildSpec{AppID: buildAppID, BuildID: buildID, Service: "web",
		Build: app.Build{Repository: repo, Access: access}, Image: buildImage, CacheMB: cache, MemoryMB: 2048, CPUMHz: 2000}
}

func TestGitAccess(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if _, found, err := d.ReadGitAccess(ctx, buildAppID, "web"); found || err != nil {
		t.Fatalf("read with none = %v, %v", found, err)
	}
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	key := GitAccess{Kind: app.BuildAccessDeployKey, PrivateKey: buildKeySent, PublicKey: "ssh-ed25519 AAAA caravana web/web", CreatedAt: at}
	if err := d.SaveGitAccess(ctx, buildAppID, "web", key); err != nil {
		t.Fatal(err)
	}
	if got := nomad.Variable("caravana/builds/web/web"); got["kind"] != "deploy-key" || got["private_key"] != buildKeySent || got["token"] != "" {
		t.Errorf("variable = %v", keysOf(got))
	}
	got, found, err := d.ReadGitAccess(ctx, buildAppID, "web")
	if err != nil || !found || got != key {
		t.Fatalf("read = %+v, %v, %v", got, found, err)
	}

	// A token replaces the key: no private key stays behind.
	tok := GitAccess{Kind: app.BuildAccessToken, Username: "x-access-token", Token: buildTokSent, CreatedAt: at}
	if err := d.SaveGitAccess(ctx, buildAppID, "web", tok); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable("caravana/builds/web/web"); v["private_key"] != "" || v["token"] != buildTokSent {
		t.Errorf("a token left the key: %v", keysOf(v))
	}
	if err := d.SaveGitAccess(ctx, buildAppID, "web", GitAccess{Kind: "password"}); err == nil {
		t.Error("an unknown kind was saved")
	}

	if err := d.SaveGitAccess(ctx, buildAppID, "worker", key); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveGitAccess(ctx, "web2", "web", key); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteGitAccessOfApp(ctx, buildAppID); err != nil {
		t.Fatal(err)
	}
	if nomad.Variable("caravana/builds/web/web") != nil || nomad.Variable("caravana/builds/web/worker") != nil {
		t.Error("the app's access survived")
	}
	if nomad.Variable("caravana/builds/web2/web") == nil {
		t.Error("another app's access was deleted (its name starts the same)")
	}
	if err := d.DeleteGitAccess(ctx, buildAppID, "web"); err != nil {
		t.Errorf("deleting nothing: %v", err)
	}
}

func TestStartBuild(t *testing.T) {
	ctx := context.Background()
	repo := "https://github.com/acme/web.git"
	sshRepo := "ssh://git@git.example.com:2222/acme/web.git"
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	t.Run("a public build with no push credentials: no variable, a cache volume owned by uid 1000", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: repo, Access: app.BuildAccessPublic, Image: buildImage, CacheMB: 1024}
		if err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessPublic, repo, 1024)), in); err != nil {
			t.Fatal(err)
		}
		if nomad.Job(buildJobID) == nil {
			t.Fatal("the job wasn't registered")
		}
		if v := nomad.Variable("nomad/jobs/" + buildJobID); v != nil {
			t.Errorf("variable = %v, want none", keysOf(v))
		}
		creates := nomad.VolumeCreates()
		if len(creates) != 1 || creates[0].Name != "web_caravana-build" || creates[0].NodeID != "" || creates[0].NodePool != "default" ||
			creates[0].Parameters != "gid=1000; uid=1000" || creates[0].Constraints != "" {
			t.Errorf("volume creates = %+v", creates)
		}
		// The next build finds it.
		if err := d.StartBuild(ctx, nomadjob.BuildJob(nomadBuildSpec2(repo)), in); err != nil {
			t.Fatal(err)
		}
		if n := len(nomad.VolumeCreates()); n != 1 {
			t.Errorf("volume creates = %d, want the cache made once", n)
		}
	})

	t.Run("no cache: no volume", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: repo, Access: app.BuildAccessPublic, Image: buildImage}
		if err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessPublic, repo, 0)), in); err != nil {
			t.Fatal(err)
		}
		if len(nomad.VolumeCreates()) != 0 {
			t.Errorf("volume creates = %+v", nomad.VolumeCreates())
		}
	})

	t.Run("avoiding the ingress makes the volume with the job's constraint", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: repo, Access: app.BuildAccessPublic, Image: buildImage, CacheMB: 100, AvoidIngress: true}
		spec := buildSpec(app.BuildAccessPublic, repo, 100)
		spec.AvoidIngress = true
		if err := d.StartBuild(ctx, nomadjob.BuildJob(spec), in); err != nil {
			t.Fatal(err)
		}
		creates := nomad.VolumeCreates()
		if len(creates) != 1 || creates[0].Constraints != `${meta.caravana_ingress} != true` {
			t.Errorf("creates = %+v", creates)
		}
	})

	t.Run("a cache made on the proxy's server is made again elsewhere once there is another", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("node-1", "one", "ready", deploytest.LabNodeResources(), nil)
		nomad.SetNodeStaticMeta("node-1", map[string]string{"caravana_ingress": "true"})
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: repo, Access: app.BuildAccessPublic, Image: buildImage, CacheMB: 100}
		if err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessPublic, repo, 100)), in); err != nil {
			t.Fatal(err)
		}
		if node := nomad.HostVolumeNode("web_caravana-build"); node == "" {
			t.Fatal("no volume")
		}
		nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
		nomad.SetVolumeNodes("node-2")
		in.AvoidIngress = true
		spec := buildSpec(app.BuildAccessPublic, repo, 100)
		spec.AvoidIngress, spec.BuildID = true, "bbbbbbbbbbbb"
		if err := d.StartBuild(ctx, nomadjob.BuildJob(spec), in); err != nil {
			t.Fatal(err)
		}
		if node := nomad.HostVolumeNode("web_caravana-build"); node != "node-2" {
			t.Errorf("the cache is on %q, want node-2", node)
		}
		if n := len(nomad.VolumeCreates()); n != 2 {
			t.Errorf("volume creates = %d, want 2", n)
		}
	})

	t.Run("a deploy key: the job's variable holds the key and the host keys, the job neither", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveGitAccess(ctx, buildAppID, "web", GitAccess{Kind: app.BuildAccessDeployKey, PrivateKey: buildKeySent, PublicKey: "ssh-ed25519 AAAA", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: sshRepo, Access: app.BuildAccessDeployKey, KnownHosts: "known hosts text\n", Image: buildImage}
		if err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessDeployKey, sshRepo, 0)), in); err != nil {
			t.Fatal(err)
		}
		v := nomad.Variable("nomad/jobs/" + buildJobID)
		if v[nomadjob.BuildItemDeployKey] != buildKeySent || v[nomadjob.BuildItemKnownHosts] != "known hosts text\n" || len(v) != 2 {
			t.Errorf("variable = %v", keysOf(v))
		}
		if strings.Contains(string(mustJSON(t, nomad.Job(buildJobID))), buildKeySent) {
			t.Error("the registered job holds the private key")
		}
	})

	t.Run("a deploy key that is gone, or is a token, is ErrGitAccessMissing and registers nothing", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: sshRepo, Access: app.BuildAccessDeployKey, Image: buildImage}
		job := nomadjob.BuildJob(buildSpec(app.BuildAccessDeployKey, sshRepo, 0))
		if err := d.StartBuild(ctx, job, in); !errors.Is(err, ErrGitAccessMissing) {
			t.Fatalf("with none: %v", err)
		}
		if err := d.SaveGitAccess(ctx, buildAppID, "web", GitAccess{Kind: app.BuildAccessToken, Token: buildTokSent, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
		err := d.StartBuild(ctx, job, in)
		if !errors.Is(err, ErrGitAccessMissing) {
			t.Fatalf("with a token: %v", err)
		}
		if strings.Contains(err.Error(), buildTokSent) {
			t.Error("the error holds the token")
		}
		if nomad.Registrations() != 0 || nomad.Variable("nomad/jobs/"+buildJobID) != nil {
			t.Error("a refused start left a job or variable")
		}
	})

	t.Run("a token: the credential line is for the repository's scheme, host and port, encoded", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveGitAccess(ctx, buildAppID, "web", GitAccess{Kind: app.BuildAccessToken, Username: "ana@x", Token: "p/w:" + buildTokSent, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
		r := "https://git.example.com:8443/acme/web.git"
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: r, Access: app.BuildAccessToken, Image: buildImage}
		if err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessToken, r, 0)), in); err != nil {
			t.Fatal(err)
		}
		v := nomad.Variable("nomad/jobs/" + buildJobID)
		want := "https://ana%40x:p%2Fw%3As3cret-sentinel-050-token@git.example.com:8443\n"
		if v[nomadjob.BuildItemGitCredentials] != want {
			t.Errorf("credential line = %q, want %q", v[nomadjob.BuildItemGitCredentials], want)
		}
	})

	t.Run("push credentials become a Docker config, and a missing entry an empty one", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveRegistrySecret(ctx, "ghcr.io", "deploy", buildPushSent); err != nil {
			t.Fatal(err)
		}
		spec := buildSpec(app.BuildAccessPublic, repo, 0)
		spec.PushCredentials = true
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: repo, Access: app.BuildAccessPublic, Image: buildImage, PushEntry: "ghcr.io"}
		if err := d.StartBuild(ctx, nomadjob.BuildJob(spec), in); err != nil {
			t.Fatal(err)
		}
		v := nomad.Variable("nomad/jobs/" + buildJobID)
		auth := base64.StdEncoding.EncodeToString([]byte("deploy:" + buildPushSent))
		if v[nomadjob.BuildItemDockerConfig] != `{"auths":{"ghcr.io":{"auth":"`+auth+`"}}}`+"\n" {
			t.Errorf("docker config = %q", v[nomadjob.BuildItemDockerConfig])
		}
		if strings.Contains(string(mustJSON(t, nomad.Job(buildJobID))), buildPushSent) {
			t.Error("the registered job holds the password")
		}

		// Credentials removed since the caller chose the entry: anonymous.
		if err := d.DeleteRegistrySecret(ctx, "ghcr.io"); err != nil {
			t.Fatal(err)
		}
		spec.BuildID = "bbbbbbbbbbbb"
		if err := d.StartBuild(ctx, nomadjob.BuildJob(spec), in); err != nil {
			t.Fatal(err)
		}
		if v := nomad.Variable("nomad/jobs/web_build-bbbbbbbbbbbb"); v[nomadjob.BuildItemDockerConfig] != "{}\n" {
			t.Errorf("docker config = %q, want {}", v[nomadjob.BuildItemDockerConfig])
		}
	})

	t.Run("Nomad refusing the job is a RunStartError and the variable is taken back", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveGitAccess(ctx, buildAppID, "web", GitAccess{Kind: app.BuildAccessDeployKey, PrivateKey: buildKeySent, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
		nomad.RejectNext(400, "invalid job")
		in := BuildInput{AppID: buildAppID, Service: "web", Repository: sshRepo, Access: app.BuildAccessDeployKey, Image: buildImage}
		err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessDeployKey, sshRepo, 0)), in)
		var start *RunStartError
		if !errors.As(err, &start) {
			t.Fatalf("error = %v, want a *RunStartError", err)
		}
		if nomad.Variable("nomad/jobs/"+buildJobID) != nil {
			t.Error("a refused build left its variable")
		}
	})
}

func nomadBuildSpec2(repo string) nomadjob.BuildSpec {
	s := buildSpec(app.BuildAccessPublic, repo, 1024)
	s.BuildID = "bbbbbbbbbbbb"
	return s
}

func TestBuildStateResultLogAndPurge(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	const allocID = buildJobID + "-alloc"
	repo := "https://github.com/acme/web.git"

	if _, err := d.BuildState(ctx, buildJobID); !errors.Is(err, ErrRunGone) {
		t.Errorf("state of a job Nomad lacks: %v", err)
	}
	in := BuildInput{AppID: buildAppID, Service: "web", Repository: repo, Access: app.BuildAccessPublic, Image: buildImage}
	if err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessPublic, repo, 0)), in); err != nil {
		t.Fatal(err)
	}
	if st, err := d.BuildState(ctx, buildJobID); err != nil || !st.Found || st.Terminal {
		t.Fatalf("state before an allocation = %+v, %v", st, err)
	}
	if res, err := d.BuildResult(ctx, allocID); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("result of a copy Nomad lacks = %+v, %v", res, err)
	}

	nomad.StartRun(buildJobID)
	if st, _ := d.BuildState(ctx, buildJobID); st.Terminal || st.AllocID != allocID {
		t.Errorf("running state = %+v", st)
	}
	if res, err := d.BuildResult(ctx, allocID); err != nil || res != (BuildResult{}) {
		t.Errorf("result before the build wrote any = %+v, %v", res, err)
	}

	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	nomad.WriteAllocFile(allocID, nomadjob.BuildSourceFile, "COMMIT="+enc("4e0a77c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7")+"\nAUTHOR="+enc("Ana Ñandú")+"\nSUBJECT="+enc("Retry: \"timeouts\"\nline 2")+"\n")
	nomad.WriteAllocFile(allocID, nomadjob.BuildResultFile, `{"containerimage.digest":"`+buildDigest+`","containerimage.descriptor":{}}`)
	nomad.SetTaskLogs(allocID, "source", []byte("cloning\n"), []byte("warning: x\n"))
	nomad.SetTaskLogs(allocID, "build", []byte("#1 building\n"), []byte("caravana-build: pushed\n"))
	nomad.FinishRunExit(buildJobID, 0)

	st, _ := d.BuildState(ctx, buildJobID)
	if !st.Terminal || st.ExitCode == nil || *st.ExitCode != 0 {
		t.Errorf("finished state = %+v", st)
	}
	res, err := d.BuildResult(ctx, allocID)
	want := BuildResult{Commit: "4e0a77c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7", Author: "Ana Ñandú", Subject: "Retry: \"timeouts\"\nline 2", Digest: buildDigest}
	if err != nil || res != want {
		t.Errorf("result = %+v, %v; want %+v", res, err, want)
	}

	log, cut, err := d.BuildLog(ctx, allocID)
	wantLog := "cloning\nwarning: x\n--- build ---\n#1 building\ncaravana-build: pushed\n"
	if err != nil || cut || log != wantLog {
		t.Errorf("log = %q, cut %v, %v; want %q", log, cut, err, wantLog)
	}

	// A bad digest or commit is dropped, not trusted.
	nomad.WriteAllocFile(allocID, nomadjob.BuildResultFile, `{"containerimage.digest":"latest"}`)
	nomad.WriteAllocFile(allocID, nomadjob.BuildSourceFile, "COMMIT="+enc("not a commit")+"\n")
	if res, _ := d.BuildResult(ctx, allocID); res.Digest != "" || res.Commit != "" {
		t.Errorf("result = %+v, want the bad values dropped", res)
	}

	// A long log keeps its end.
	long := strings.Repeat("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde\n", 4000)
	nomad.SetTaskLogs(allocID, "build", []byte(long), []byte("the end\n"))
	log, cut, err = d.BuildLog(ctx, allocID)
	if err != nil || !cut || len(log) > BuildLogBytes || !strings.HasSuffix(log, "the end\n") || !strings.HasPrefix(log, "0123") {
		t.Errorf("long log: %d bytes, cut %v, err %v, ends %q", len(log), cut, err, log[max(0, len(log)-20):])
	}

	jobs, err := d.BuildJobs(ctx, "")
	if err != nil || len(jobs) != 1 || jobs[0] != (BuildJob{ID: buildJobID, AppID: buildAppID, BuildID: buildID}) {
		t.Errorf("BuildJobs = %+v, %v", jobs, err)
	}
	if other, _ := d.BuildJobs(ctx, "web2"); len(other) != 0 {
		t.Errorf("BuildJobs of another app = %+v", other)
	}

	if err := d.StopBuild(ctx, buildJobID); err != nil {
		t.Fatal(err)
	}
	if err := d.StopBuild(ctx, "web_build-ffffffffffff"); !errors.Is(err, ErrRunGone) {
		t.Errorf("stopping a job Nomad lacks = %v", err)
	}
	if err := d.PurgeBuild(ctx, buildJobID); err != nil {
		t.Fatal(err)
	}
	if !nomad.Purged(buildJobID) {
		t.Error("the job wasn't purged")
	}
	if err := d.PurgeBuild(ctx, buildJobID); err != nil {
		t.Errorf("second purge: %v", err)
	}
}

func TestBuildVariableIsDeletedWithThePurge(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.SetVariable("nomad/jobs/"+buildJobID, map[string]string{"deploy_key": buildKeySent})
	if err := d.PurgeBuild(ctx, buildJobID); err != nil {
		t.Fatal(err)
	}
	if nomad.Variable("nomad/jobs/"+buildJobID) != nil {
		t.Error("the purge left the variable")
	}
}

func TestDeleteBuildCache(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	if err := d.DeleteBuildCache(ctx, buildAppID); err != nil {
		t.Errorf("deleting a cache that isn't there: %v", err)
	}
	in := BuildInput{AppID: buildAppID, Service: "web", Repository: "https://github.com/acme/web.git", Access: app.BuildAccessPublic, Image: buildImage, CacheMB: 100}
	if err := d.StartBuild(ctx, nomadjob.BuildJob(buildSpec(app.BuildAccessPublic, in.Repository, 100)), in); err != nil {
		t.Fatal(err)
	}
	if _, err := d.createVolume(ctx, "web_data", "node-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteBuildCache(ctx, buildAppID); err != nil {
		t.Fatal(err)
	}
	if got := nomad.HostVolumes(); len(got) != 1 || got[0] != "web_data" {
		t.Errorf("volumes = %v, want only the app's own", got)
	}
}

func TestBuildAvoidsIngress(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.SetNode("node-1", "one", "ready", deploytest.LabNodeResources(), nil)
	nomad.SetNodeStaticMeta("node-1", map[string]string{"caravana_ingress": "true"})
	if avoid, err := d.BuildAvoidsIngress(ctx); err != nil || avoid {
		t.Errorf("one server: avoid = %v, %v; want false", avoid, err)
	}
	nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	if avoid, err := d.BuildAvoidsIngress(ctx); err != nil || !avoid {
		t.Errorf("a second ready server: avoid = %v, %v; want true", avoid, err)
	}
	nomad.SetNodeEligibilityOf("node-2", "ineligible", false)
	if avoid, _ := d.BuildAvoidsIngress(ctx); avoid {
		t.Error("an ineligible server counts")
	}
	nomad.SetNodeEligibilityOf("node-2", "eligible", false)
	nomad.SetNodeStatusOf("node-2", "down")
	if avoid, _ := d.BuildAvoidsIngress(ctx); avoid {
		t.Error("a down server counts")
	}
}

func TestDockerConfigAndCredentialLine(t *testing.T) {
	cfg, err := dockerConfig("docker.io/acme/web", "u", "p")
	if err != nil || !strings.Contains(cfg, `"https://index.docker.io/v1/"`) {
		t.Errorf("docker hub config = %q, %v", cfg, err)
	}
	cfg, _ = dockerConfig("registry.example.com:5000/acme/web", "u", "p")
	if !strings.Contains(cfg, `"registry.example.com:5000"`) {
		t.Errorf("config = %q", cfg)
	}
	if _, err := gitCredentialLine("ssh://git@host/x", "u", "t"); err == nil {
		t.Error("a credential line for an SSH repository")
	}
	line, err := gitCredentialLine("http://127.0.0.1:8098/app.git", "x-access-token", "t")
	if err != nil || line != "http://x-access-token:t@127.0.0.1:8098\n" {
		t.Errorf("line = %q, %v", line, err)
	}
}

func TestResolveWithTakesTheBuildsImageAndRefusesToLookUpABuiltOne(t *testing.T) {
	ctx := context.Background()
	d, _, _ := newTestDeployer(t)
	a := app.App{ID: "shop", Name: "shop", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		{Name: "web", Image: buildImage, Build: &app.Build{Repository: "https://github.com/acme/web.git"}},
		{Name: "api", Image: "traefik/whoami:latest"},
	}}
	// The built service has no tag to look up: without the build's image it is a field error.
	_, _, err := d.Resolve(ctx, a)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services[0].image" {
		t.Fatalf("Resolve of a built service = %v, want a field error on its image", err)
	}
	pinned := buildImage + "@" + buildDigest
	_, _, err = d.ResolveWith(ctx, a, map[string]string{"web": pinned})
	// The other service is looked up as always (the fake registry doesn't know it here).
	var ie *ImageError
	if !errors.As(err, &ie) || ie.Field != "services[1].image" {
		t.Fatalf("ResolveWith = %v, want the second service to be looked up", err)
	}
	a.Services = a.Services[:1]
	digests, warnings, err := d.ResolveWith(ctx, a, map[string]string{"web": pinned})
	if err != nil || len(warnings) != 0 || digests["web"] != pinned {
		t.Errorf("ResolveWith = %v, %v, %v", digests, warnings, err)
	}
}

func TestBuildJobUsesTheDeployersImages(t *testing.T) {
	d, _, _ := newTestDeployer(t)
	job := d.BuildJob(buildSpec(app.BuildAccessPublic, "https://github.com/acme/web.git", 0))
	tasks := job.TaskGroups[0].Tasks
	if tasks[0].Config["image"] != nomadjob.DefaultGitImage || tasks[1].Config["image"] != nomadjob.DefaultBuilderImage {
		t.Errorf("images = %v / %v", tasks[0].Config["image"], tasks[1].Config["image"])
	}
	if got := d.BuildImages(); len(got) != 2 || got[0] != nomadjob.DefaultBuilderImage || got[1] != nomadjob.DefaultGitImage {
		t.Errorf("BuildImages = %v", got)
	}
}
