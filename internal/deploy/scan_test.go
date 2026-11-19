package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

const (
	scanRunID       = "aaaaaaaaaaaa"
	scanJobID       = "_scan-" + scanRunID
	scanPublicRef   = "traefik/whoami@sha256:" + runDigest
	scanPrivateRef  = "registry.example.com:5000/team/app@sha256:" + runDigest
	scanSentinel    = "s3cret-sentinel-044-stderr"
	scanTextExample = "caravana-scan v1\ndb\t2026-11-19T06:23:17Z\tv6.1.10\t0.117.0\n"
)

var scanPrivateSet = map[string]bool{"registry.example.com:5000": true}

func TestEnsureScanVolume(t *testing.T) {
	ctx := context.Background()

	t.Run("creates it once, on the node Nomad picks", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVolumeNodes("node-7")
		node, err := d.EnsureScanVolume(ctx)
		if err != nil || node != "node-7" {
			t.Fatalf("node = %q, err = %v", node, err)
		}
		node, err = d.EnsureScanVolume(ctx)
		if err != nil || node != "node-7" {
			t.Fatalf("second call: node = %q, err = %v", node, err)
		}
		creates := nomad.VolumeCreates()
		if len(creates) != 1 || creates[0].Name != "caravana-scanner" || creates[0].NodeID != "" || creates[0].NodePool != "default" {
			t.Errorf("creates = %+v, want one, without a node, in the default pool", creates)
		}
	})

	t.Run("a volume whose name only starts the same is another volume", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if _, err := d.createVolume(ctx, "caravana-scanner-old", "node-1", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := d.EnsureScanVolume(ctx); err != nil {
			t.Fatal(err)
		}
		if got := nomad.HostVolumes(); len(got) != 2 {
			t.Errorf("volumes = %v, want both", got)
		}
	})

	t.Run("Nomad refusing the token is said as for any volume", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FailVolumes(403)
		if _, err := d.EnsureScanVolume(ctx); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("error = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestDeleteScanVolume(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	d.Timing.VolumeDeleteWait = 2 * time.Second

	if err := d.DeleteScanVolume(ctx); err != nil {
		t.Fatalf("delete of nothing: %v", err)
	}
	if _, err := d.EnsureScanVolume(ctx); err != nil {
		t.Fatal(err)
	}
	// An app's volume must stay.
	if _, err := d.createVolume(ctx, "whoami_data", "node-1", nil); err != nil {
		t.Fatal(err)
	}

	// A scan holds the volume, and lets go a moment later.
	if err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPublicRef}, nomadjob.ScanOptions{}, labSettings, nil), nil); err != nil {
		t.Fatal(err)
	}
	nomad.StartRun(scanJobID)
	go func() {
		time.Sleep(50 * time.Millisecond)
		nomad.SetCopies(scanJobID)
	}()
	if err := d.DeleteScanVolume(ctx); err != nil {
		t.Fatalf("DeleteScanVolume: %v", err)
	}
	if got := nomad.HostVolumes(); len(got) != 1 || got[0] != "whoami_data" {
		t.Errorf("volumes = %v, want only the app's", got)
	}
}

func TestStartScan(t *testing.T) {
	ctx := context.Background()

	t.Run("a public image: the job is registered and there is no variable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveRegistrySecret(ctx, "registry.example.com:5000", "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		if err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPublicRef}, nomadjob.ScanOptions{}, labSettings, scanPrivateSet), scanPrivateSet); err != nil {
			t.Fatal(err)
		}
		if nomad.Job(scanJobID) == nil || nomad.Registrations() != 1 {
			t.Errorf("job = %v, registrations = %d", nomad.Job(scanJobID), nomad.Registrations())
		}
		if got := nomad.Variable("nomad/jobs/" + scanJobID); got != nil {
			t.Errorf("variable = %v, want none: a public image needs no credentials", keysOf(got))
		}
	})

	t.Run("a private image: the job's own variable holds the credentials, the job holds none", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveRegistrySecret(ctx, "registry.example.com:5000", "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		job := nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPrivateRef}, nomadjob.ScanOptions{}, labSettings, scanPrivateSet)
		if err := d.StartScan(ctx, job, scanPrivateSet); err != nil {
			t.Fatal(err)
		}
		got := nomad.Variable("nomad/jobs/" + scanJobID)
		if got["GRYPE_REGISTRY_AUTH_AUTHORITY"] != "registry.example.com:5000" || got["GRYPE_REGISTRY_AUTH_USERNAME"] != "deploy" ||
			got["GRYPE_REGISTRY_AUTH_PASSWORD"] != registrySecret || len(got) != 3 {
			t.Errorf("variable items = %v", keysOf(got))
		}
		if strings.Contains(string(mustJSON(t, nomad.Job(scanJobID))), registrySecret) {
			t.Error("the registered job holds the password")
		}
	})

	t.Run("an SBOM rescan and a database update read no variable, whatever the registry holds", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveRegistrySecret(ctx, "registry.example.com:5000", "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		rescan := nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPrivateRef, FromSBOM: true}, nomadjob.ScanOptions{}, labSettings, scanPrivateSet)
		if err := d.StartScan(ctx, rescan, scanPrivateSet); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable("nomad/jobs/" + scanJobID); got != nil {
			t.Errorf("an SBOM rescan got a variable: %v", keysOf(got))
		}
		update := nomadjob.ScanJob("bbbbbbbbbbbb", nomadjob.ScanSource{}, nomadjob.ScanOptions{UpdateOnly: true}, labSettings, scanPrivateSet)
		if err := d.StartScan(ctx, update, scanPrivateSet); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable("nomad/jobs/_scan-bbbbbbbbbbbb"); got != nil {
			t.Errorf("a database update got a variable: %v", keysOf(got))
		}
		if nomad.Registrations() != 2 {
			t.Errorf("registrations = %d, want both jobs", nomad.Registrations())
		}
	})

	t.Run("credentials removed since the job was built leave the scan anonymous", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		job := nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPrivateRef}, nomadjob.ScanOptions{}, labSettings, scanPrivateSet)
		if err := d.StartScan(ctx, job, scanPrivateSet); err != nil {
			t.Fatal(err)
		}
		got := nomad.Variable("nomad/jobs/" + scanJobID)
		if len(got) != 1 || got[runMarkerItem] != scanJobID {
			t.Errorf("variable items = %v, want only the marker (Nomad refuses an empty variable)", keysOf(got))
		}
	})

	t.Run("Nomad refusing the job is a RunStartError, and the variable is taken back", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveRegistrySecret(ctx, "registry.example.com:5000", "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		nomad.RejectNext(400, "invalid job")
		err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPrivateRef}, nomadjob.ScanOptions{}, labSettings, scanPrivateSet), scanPrivateSet)
		var start *RunStartError
		if !errors.As(err, &start) || !errors.Is(err, ErrNomadRejected) {
			t.Fatalf("error = %v, want a *RunStartError", err)
		}
		if nomad.Variable("nomad/jobs/"+scanJobID) != nil || nomad.Job(scanJobID) != nil {
			t.Error("a refused scan left its variable or job in Nomad")
		}
	})

	t.Run("a Nomad that can't be asked is not a RunStartError", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPublicRef}, nomadjob.ScanOptions{}, labSettings, nil), nil)
		var start *RunStartError
		if err == nil || errors.As(err, &start) {
			t.Errorf("error = %v, want a plain Nomad failure", err)
		}
	})
}

func TestScanStateResultAndPurge(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	const allocID = scanJobID + "-alloc"

	if _, err := d.ScanState(ctx, scanJobID); !errors.Is(err, ErrRunGone) {
		t.Errorf("state of a job Nomad lacks: %v, want ErrRunGone", err)
	}
	if err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPublicRef}, nomadjob.ScanOptions{}, labSettings, nil), nil); err != nil {
		t.Fatal(err)
	}
	if st, err := d.ScanState(ctx, scanJobID); err != nil || !st.Found || st.Terminal {
		t.Fatalf("state before an allocation = %+v, %v", st, err)
	}
	nomad.StartRun(scanJobID)
	if st, _ := d.ScanState(ctx, scanJobID); st.Terminal || st.AllocID != allocID || st.TaskStartedAt == nil {
		t.Errorf("running state = %+v", st)
	}

	// No file yet: not found, and not an error.
	if text, found, err := d.ScanResult(ctx, allocID); found || text != "" || err != nil {
		t.Errorf("result before the scan wrote it = %q, %v, %v", text, found, err)
	}
	nomad.FinishRunExit(scanJobID, 0)
	nomad.WriteAllocFile(allocID, nomadjob.ScanResultFile, scanTextExample)
	st, _ := d.ScanState(ctx, scanJobID)
	if !st.Terminal || st.ExitCode == nil || *st.ExitCode != 0 {
		t.Errorf("finished state = %+v", st)
	}
	text, found, err := d.ScanResult(ctx, allocID)
	if err != nil || !found || text != scanTextExample {
		t.Fatalf("result = %q, %v, %v", text, found, err)
	}
	if _, err := vulns.Parse(scanPublicRef, text); err != nil {
		t.Errorf("Parse: %v", err)
	}

	// A file over the limit comes back one byte over it, which Parse refuses.
	nomad.WriteAllocFile(allocID, nomadjob.ScanResultFile, strings.Repeat("x", vulns.MaxResultBytes+4096))
	text, _, err = d.ScanResult(ctx, allocID)
	if err != nil || len(text) != vulns.MaxResultBytes+1 {
		t.Errorf("long result: %d bytes, %v; want %d", len(text), err, vulns.MaxResultBytes+1)
	}
	if _, err := vulns.Parse(scanPublicRef, text); err == nil {
		t.Error("Parse accepted a result over the limit")
	}

	// Purge erases the job and any variable it had; a second purge is done.
	if err := d.PurgeScan(ctx, scanJobID); err != nil {
		t.Fatal(err)
	}
	if !nomad.Purged(scanJobID) {
		t.Error("the job wasn't purged")
	}
	if err := d.PurgeScan(ctx, scanJobID); err != nil {
		t.Errorf("second purge: %v", err)
	}
}

func TestStopScan(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	if err := d.StopScan(ctx, scanJobID); !errors.Is(err, ErrRunGone) {
		t.Errorf("stop of a job Nomad lacks: %v, want ErrRunGone", err)
	}
	if err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPublicRef}, nomadjob.ScanOptions{}, labSettings, nil), nil); err != nil {
		t.Fatal(err)
	}
	nomad.StartRun(scanJobID)
	if err := d.StopScan(ctx, scanJobID); err != nil {
		t.Fatal(err)
	}
	// Stopped, not purged: the allocation and its output are still there.
	if nomad.Stops(scanJobID) != 1 || nomad.Purged(scanJobID) {
		t.Errorf("stops = %d, purged = %v; want one stop and no purge", nomad.Stops(scanJobID), nomad.Purged(scanJobID))
	}
	if st, err := d.ScanState(ctx, scanJobID); err != nil || !st.Found {
		t.Errorf("state after the stop = %+v, %v", st, err)
	}
}

func TestPurgeScanDeletesTheVariable(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	if err := d.SaveRegistrySecret(ctx, "registry.example.com:5000", "deploy", registrySecret); err != nil {
		t.Fatal(err)
	}
	if err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPrivateRef}, nomadjob.ScanOptions{}, labSettings, scanPrivateSet), scanPrivateSet); err != nil {
		t.Fatal(err)
	}
	if nomad.Variable("nomad/jobs/"+scanJobID) == nil {
		t.Fatal("no variable to delete")
	}
	if err := d.PurgeScan(ctx, scanJobID); err != nil {
		t.Fatal(err)
	}
	if nomad.Variable("nomad/jobs/"+scanJobID) != nil {
		t.Error("the credentials were left in Nomad")
	}
}

// grypeStderr are what the lab saw (research R4), each as Grype prints it.
var grypeStderr = map[string]string{
	"bogus db url, empty cache": `[0000]  WARN unable to check for vulnerability database update
[0000]  WARN unable to check for vulnerability database update
[0000]  WARN error updating db error=unable to download listing: Get "https://127.0.0.1:9/latest.json": dial tcp 127.0.0.1:9: connect: connection refused
[0002] ERROR failed to load vulnerability db: database does not exist
`,
	"database too old": `[0002] ERROR failed to load vulnerability db: the vulnerability database was built 10 hours ago (max allowed age is 1 hour)
`,
	"database too old, update failed too": `[0000]  WARN current database is invalid error=the vulnerability database was built 10 hours ago (max allowed age is 1 hour)
[0000]  WARN unable to check for vulnerability database update
[0000]  WARN error updating db error=unable to download listing: Get "https://127.0.0.1:9/latest.json": dial tcp 127.0.0.1:9: connect: connection refused
[0002] ERROR failed to load vulnerability db: the vulnerability database was built 10 hours ago (max allowed age is 1 hour)
`,
	"sbom file missing": `[0003] ERROR failed to catalog: unable to open file /cache/sbom/nope.cdx.json: open /cache/sbom/nope.cdx.json: no such file or directory
`,
	"sbom file not an sbom": `[0003] ERROR failed to catalog: unable to decode sbom: sbom format not recognized
`,
	"missing image": `[0004] ERROR failed to catalog: errors occurred attempting to resolve 'alpine@sha256:0000': 
  - oci-registry: failed to get image descriptor from registry: GET https://index.docker.io/v2/library/alpine/manifests/sha256:0000: MANIFEST_UNKNOWN: manifest unknown; unknown manifest name=library/alpine revision=sha256:0000
  - oci-model: failed to fetch descriptor: GET https://index.docker.io/v2/library/alpine/manifests/sha256:0000: MANIFEST_UNKNOWN: manifest unknown
`,
	"unreachable registry": `[0002] ERROR failed to catalog: errors occurred attempting to resolve '127.0.0.1:9/x/y@sha256:0000':
  - oci-registry: failed to get image descriptor from registry: Get "https://127.0.0.1:9/v2/": dial tcp 127.0.0.1:9: connect: connection refused; Get "http://127.0.0.1:9/v2/": dial tcp 127.0.0.1:9: connect: connection refused
`,
	"docker hub, anonymous, private or missing repository": `[0003] ERROR failed to catalog: errors occurred attempting to resolve 'docker.io/x/y@sha256:0000':
  - oci-registry: failed to get image descriptor from registry: GET https://index.docker.io/v2/x/y/manifests/sha256:0000: UNAUTHORIZED: authentication required; [map[Action:pull Class: Name:x/y Type:repository]]
`,
	"docker hub, wrong credentials": `[0003] ERROR failed to catalog: errors occurred attempting to resolve 'docker.io/library/alpine@sha256:0000':
  - oci-registry: failed to get image descriptor from registry: GET https://auth.docker.io/token?scope=repository%3Alibrary%2Falpine%3Apull&service=registry.docker.io: unexpected status code 401 Unauthorized: {"details":"incorrect username or password"}
`,
	"ghcr, private": `[0002] ERROR failed to catalog: errors occurred attempting to resolve 'ghcr.io/x/y:latest':
  - oci-registry: failed to get image descriptor from registry: GET https://ghcr.io/token?scope=repository%3Ax%2Fy%3Apull&service=ghcr.io: DENIED: requested access to the resource is denied
`,
	// A scan that ended well also logs warnings; only an ERROR counts.
	"only warnings":    "[0000]  WARN unable to check for vulnerability database update\n[0001]  WARN error updating db error=x\n",
	"nothing":          "",
	"an unknown error": "[0001] ERROR something nobody foresaw\n",
}

func TestClassifyScanFailure(t *testing.T) {
	want := map[string]string{
		"bogus db url, empty cache":                            ScanDBDownload,
		"database too old":                                     ScanDBOld,
		"database too old, update failed too":                  ScanDBOld,
		"sbom file missing":                                    ScanSBOMMissing,
		"sbom file not an sbom":                                ScanSBOMMissing,
		"missing image":                                        ScanImageRead,
		"unreachable registry":                                 ScanImageRead,
		"docker hub, anonymous, private or missing repository": ScanRegistryAuth,
		"docker hub, wrong credentials":                        ScanRegistryAuth,
		"ghcr, private":                                        ScanRegistryAuth,
		"only warnings":                                        ScanOther,
		"nothing":                                              ScanOther,
		"an unknown error":                                     ScanOther,
	}
	for name, stderr := range grypeStderr {
		if got := classifyScanFailure(stderr); got != want[name] {
			t.Errorf("%s: class = %q, want %q", name, got, want[name])
		}
	}
	if len(want) != len(grypeStderr) {
		t.Error("every sample needs an expected class")
	}
}

// TestScanFailureReadsTheAllocationAndNeverReturnsItsText reads a scan's
// stderr through Nomad and checks only a class comes back.
func TestScanFailureReadsTheAllocationAndNeverReturnsItsText(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	if err := d.StartScan(ctx, nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPublicRef}, nomadjob.ScanOptions{}, labSettings, nil), nil); err != nil {
		t.Fatal(err)
	}
	nomad.FinishRunExit(scanJobID, 1)
	nomad.SetRunLogs(scanJobID, nil, []byte(grypeStderr["missing image"]+"registry "+scanSentinel+"\n"))
	got := d.ScanFailure(ctx, scanJobID+"-alloc")
	if got != ScanImageRead {
		t.Errorf("class = %q, want %q", got, ScanImageRead)
	}
	if strings.Contains(got, scanSentinel) {
		t.Error("the class holds the scanner's text")
	}

	// Only the end of stderr is read: an error older than 4 KiB isn't seen.
	nomad.SetRunLogs(scanJobID, nil, []byte(grypeStderr["missing image"]+strings.Repeat("noise noise noise\n", 400)))
	if got := d.ScanFailure(ctx, scanJobID+"-alloc"); got != ScanOther {
		t.Errorf("class = %q for an error beyond the last 4 KiB, want %q", got, ScanOther)
	}

	// An allocation Nomad doesn't have, or a Nomad that can't be asked, is "other".
	if got := d.ScanFailure(ctx, "no-such-alloc"); got != ScanOther {
		t.Errorf("class for no allocation = %q", got)
	}
	nomad.Down()
	if got := d.ScanFailure(ctx, scanJobID+"-alloc"); got != ScanOther {
		t.Errorf("class for a Nomad that is down = %q", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadsVariable(t *testing.T) {
	pub := nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPublicRef}, nomadjob.ScanOptions{}, labSettings, nil)
	priv := nomadjob.ScanJob(scanRunID, nomadjob.ScanSource{Image: scanPrivateRef}, nomadjob.ScanOptions{}, labSettings, scanPrivateSet)
	if readsVariable(pub, "nomad/jobs/"+scanJobID) || !readsVariable(priv, "nomad/jobs/"+scanJobID) {
		t.Error("readsVariable must be true only for the private job")
	}
}
