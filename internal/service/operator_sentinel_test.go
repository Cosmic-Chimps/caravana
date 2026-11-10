package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 027, T033 (SC-006): what the Operator view reads must never carry a secret or a command's
// output. Each sentinel stands for one place a leak could come from, and the test then reads every
// surface of this package: Activity, Updates, Host (and the needs-you list and the host's copies),
// every notification that was queued, the server log and the SQLite file. The pages and the API
// answers are read by the same story in internal/ui/operator_sentinel_test.go and
// internal/api/operator_sentinel_test.go, since those packages import this one.
const (
	opsSecretSentinel = "s3cret-sentinel-027-secret" // a service secret's value
	opsOutputSentinel = "s3cret-sentinel-027-output" // what a command printed
	opsProSentinel    = "s3cret-sentinel-027-pro"    // text in pro security-status that isn't a package name
	opsAptSentinel    = "s3cret-sentinel-027-apt"    // a line of apt's history.log that isn't a package
	opsNotesSentinel  = "s3cret-sentinel-027-notes"  // text in a release's notes
)

// opsSentinelPro answers like pro security-status, with text in fields caravana reads past.
const opsSentinelPro = `{"packages":[` +
	`{"package":"openssl","service_name":"standard-security","status":"upgrade_available","description":"` + opsProSentinel + `"},` +
	`{"package":"linux-image-6.8.0-149-generic","service_name":"standard-security","status":"upgrade_available"}],` +
	`"summary":{"num_standard_security_updates":2,"note":"` + opsProSentinel + `"},"notice":"` + opsProSentinel + `"}`

// opsSentinelHost is a host with a kernel waiting for a reboot, updates to apply and an apt history
// whose command line carries a sentinel.
func opsSentinelHost(now time.Time) fstest.MapFS {
	history := aptRun(autoRun, "/usr/bin/unattended-upgrade", "Upgrade: libc6:amd64 (1, 2)") +
		aptRun(manualRun, "apt install --password="+opsAptSentinel+" htop", "Install: htop:amd64 (3.3.0-4build1)") +
		aptRun(kernelRun, "/usr/bin/unattended-upgrade", "Upgrade: linux-image-virtual:amd64 (1, 2)\nInstall: linux-image-6.8.0-149-generic:amd64 (6.8.0-149.149, automatic)")
	return fstest.MapFS{
		"etc/os-release":                            {Data: []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n")},
		"var/log/apt/history.log":                   {Data: []byte(history)},
		"var/lib/update-notifier/updates-available": {Data: []byte("\n14 updates can be applied immediately.\n9 of these updates are standard security updates.\n"), ModTime: now.Add(-time.Hour)},
		"run/reboot-required":                       {Data: []byte("*** System restart required ***\n"), ModTime: now.Add(-2 * time.Hour)},
		"run/reboot-required.pkgs":                  {Data: []byte("linux-image-6.8.0-149-generic\n")},
		"boot/vmlinuz-6.8.0-149-generic":            {},
		"etc/apt/apt.conf.d/20auto-upgrades":        {Data: []byte("APT::Periodic::Unattended-Upgrade \"1\";\n")},
	}
}

// opsSentinelLab is a service on the fakes with the sentinels planted, one channel that wants every
// kind of message, a log that keeps everything (debug level) and a store whose file is known.
type opsSentinelLab struct {
	*env
	dir     string
	logs    *syncBuffer
	gh      *fakeGitHub
	channel store.NotificationChannel
	now     time.Time
}

func newOpsSentinelLab(t *testing.T) *opsSentinelLab {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.nomad.Succeed()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, log)
	e.nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	e.nomad.SetHostStats(deploytest.LabHostStats())
	e.nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", []*api.NodeEvent{
		{Subsystem: "Cluster", Message: "Node registered", Timestamp: osNow0.Add(-24 * time.Hour)},
	})
	e.svc.sampler.dockerRoot = "/var/lib/docker"
	e.svc.metricsGap = time.Minute
	parkSender(e.svc)

	l := &opsSentinelLab{env: e, dir: dir, logs: logs, gh: newFakeGitHub(t), now: osNow0}
	l.svc.clock.(*fakeClock).Set(l.now)
	in := channelInput(notify.KindNtfy, "https://ntfy.example.com/sentinel")
	in.Name, in.Events = "all", []string{EventProblems, EventDeployFailed, EventDeploySucceeded}
	ch, err := l.svc.CreateNotificationChannel(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	l.channel = ch.NotificationChannel.NotificationChannel

	// The release server's notes carry a sentinel: they are public and shown on purpose.
	l.svc.SetVersion("0.15.0")
	l.svc.SetReleaseURL(l.gh.URL + "/repos/x/releases/latest")
	l.svc.SetReleasePinURLs(l.gh.URL+"/download", l.gh.URL+"/raw")
	l.gh.set(func(g *fakeGitHub) {
		g.etag = `"e1"`
		g.release = strings.Replace(releaseBody("v0.16.0", ""), `\n- One thing.`, `\n- One thing, `+opsNotesSentinel+`.`, 1)
	})
	return l
}

// deliveries are every notification queued, whatever its kind.
func (l *opsSentinelLab) deliveries(t *testing.T) []store.Delivery {
	t.Helper()
	got, err := l.store.ListDeliveries(context.Background(), l.channel.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestOperatorViewKeepsSecretsAndOutputOut(t *testing.T) {
	ctx := context.Background()
	l := newOpsSentinelLab(t)

	// Source 1: a service secret deployed through the fakes (its value is in the secret store only).
	a := whoami()
	a.Services[0].Secrets = []app.Secret{{Name: "API_KEY", Value: new(opsSecretSentinel)}}
	deployHealthy(t, l.env, a)
	if got := l.secrets.Live("whoami")["web"]["API_KEY"]; got != opsSecretSentinel {
		t.Fatalf("the secret store holds %q: the secret wasn't planted", got)
	}
	l.nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})

	// A deploy that fails (a tag that doesn't exist): one deploy.failed notification.
	broken := whoami()
	broken.ID = "broken"
	broken.Services[0].Domains = nil
	broken.Services[0].Image = "traefik/whoami:nope"
	broken.Services[0].Secrets = []app.Secret{{Name: "API_KEY", Value: new(opsSecretSentinel)}}
	if rec, err := l.svc.Deploy(ctx, broken, app.TriggerManual); err == nil {
		waitFinal(t, l.store, rec.Deployment.ID)
	}

	// Source 2: a command's output (and its arguments).
	l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
		return deploytest.ExecReply{Stdout: opsOutputSentinel + "\n", Stderr: opsOutputSentinel + "-err\n"}
	})
	res, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"echo", opsOutputSentinel}, app.TriggerConsole)
	if err != nil || !strings.Contains(res.Stdout, opsOutputSentinel) {
		t.Fatalf("RunCommand = %+v, %v: the output wasn't planted", res, err)
	}

	// Sources 3 and 4: apt's history and pro's answer. The sampler reads the files, the owner asks for
	// the package names.
	l.svc.SetHostRoot(opsSentinelHost(l.now))
	l.svc.SetProRunner(&fakePro{available: true, out: []byte(opsSentinelPro)})
	if err := l.svc.sampleTick(ctx, l.now); err != nil {
		t.Fatal(err)
	}
	if err := l.svc.CheckPackages(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the package names", func() bool {
		v, err := l.svc.Updates(ctx)
		return err == nil && !v.Checking.Packages && !v.OS.Updates.PackagesCheckedAt.IsZero()
	})

	// Source 5: the release's notes, from the fake release server.
	l.svc.releaseCheck(ctx)

	// The needs-you list: the reboot opens, and the notifier queues its message.
	if err := l.svc.checkProblems(ctx); err != nil {
		t.Fatal(err)
	}

	// The planted data is really there (the sentinel that is shown on purpose is shown, and the
	// notifications exist); otherwise "absent" below would prove nothing.
	updates, err := l.svc.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprintf("%+v", updates.Caravana.Notes), opsNotesSentinel) {
		t.Errorf("the release notes weren't read: %+v", updates.Caravana)
	}
	if len(updates.OS.Updates.Packages) != 2 || !updates.OS.Updates.PackagesKernel {
		t.Errorf("the package names weren't read: %+v", updates.OS.Updates)
	}
	kinds := map[string]bool{}
	waitFor(t, "the deploy.failed message", func() bool {
		for _, d := range l.deliveries(t) {
			kinds[d.EventType] = true
		}
		return kinds[notify.EventDeployFailed]
	})
	for _, want := range []string{notify.EventDeployFailed, notify.EventProblemOpened, notify.EventCaravanaRelease} {
		if !kinds[want] {
			t.Errorf("no %s notification was queued (got %v)", want, kinds)
		}
	}
	// A second package check, ten minutes later, that fails with pro's own words (which hold a
	// sentinel): the service logs that it failed, never what pro said.
	l.svc.clock.(*fakeClock).Advance(11 * time.Minute)
	l.svc.SetProRunner(&fakePro{available: true, err: errors.New("pro: " + opsProSentinel)})
	if err := l.svc.CheckPackages(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed check", func() bool {
		v, err := l.svc.Updates(ctx)
		return err == nil && !v.Checking.Packages && v.OS.Updates.PackagesError != ""
	})
	if updates, err = l.svc.Updates(ctx); err != nil {
		t.Fatal(err)
	}
	activity, err := l.svc.Activity(ctx, ActivityQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprintf("%+v", activity), "echo ran in whoami") {
		t.Errorf("the activity lacks the command: %+v", activity)
	}

	// Every surface of this package, as the text a leak would be found in.
	host, err := l.svc.Host(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	attention, err := l.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	copies, err := l.svc.HostCopies(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	asText := func(v any) string {
		raw, _ := json.Marshal(v)
		return fmt.Sprintf("%+v\n%s", v, raw)
	}
	var notifications strings.Builder
	for _, d := range l.deliveries(t) {
		fmt.Fprintf(&notifications, "%+v\n%+v\n", d, payloadOf(t, d))
	}
	surfaces := map[string]string{
		"Activity":                            asText(activity),
		"Updates":                             asText(updates),
		"Host":                                asText(host),
		"Attention":                           asText(attention),
		"HostCopies":                          asText(copies),
		"the notifications":                   notifications.String(),
		"the log":                             l.logs.String(),
		"the SQLite file and every row in it": rawStore(t, l.dir),
	}
	for name, text := range surfaces {
		for _, s := range []string{opsSecretSentinel, opsOutputSentinel, opsProSentinel, opsAptSentinel} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q", name, s)
			}
		}
	}
	// Release notes are public and shown on purpose (Updates has them), but they are never logged.
	if strings.Contains(l.logs.String(), opsNotesSentinel) {
		t.Error("the log holds the release's notes")
	}
	// The log is not empty (pro's failure was logged), so absence isn't for lack of lines.
	if !strings.Contains(l.logs.String(), "read package names with pro failed") {
		t.Errorf("the log lacks pro's failure:\n%s", l.logs.String())
	}
}
