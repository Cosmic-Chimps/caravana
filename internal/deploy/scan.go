package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Image scans (feature 044). A scan is one Nomad batch job, built by
// nomadjob.ScanJob, that reads an image from its registry and writes the
// findings to a file of its allocation. This file is the Nomad side: it keeps
// the scanner's database volume, starts a scan, reads how far it got and what
// it found, and purges it. Package service decides what to scan and when, and
// records the outcome.
//
// Every call takes the job's ID as a plain string: this package doesn't know
// how the ID is made.

// EnsureScanVolume creates the host volume that keeps the scanner's database
// when Nomad doesn't have it, waits until it is ready and returns the Nomad
// node it is on. Like an app's volume, it is placed once (Nomad picks a node
// of the default pool) and never moves, so scans always run on that node.
func (d *Deployer) EnsureScanVolume(ctx context.Context) (nodeID string, err error) {
	have, err := d.listVolumes(ctx, nomadjob.ScanVolume)
	if err != nil {
		return "", err
	}
	if stub, ok := have[nomadjob.ScanVolume]; ok {
		if stub.State != api.HostVolumeStateReady {
			if err := d.waitReady(ctx, stub.ID, nomadjob.ScanVolume); err != nil {
				return "", err
			}
		}
		return stub.NodeID, nil
	}
	vol, err := d.createVolume(ctx, nomadjob.ScanVolume, "", nil)
	if err != nil {
		return "", err
	}
	if vol.NodeID == "" {
		return "", fmt.Errorf("%w: Nomad answered no node for volume %s", ErrNomadRejected, nomadjob.ScanVolume)
	}
	if err := d.waitReady(ctx, vol.ID, nomadjob.ScanVolume); err != nil {
		return "", err
	}
	return vol.NodeID, nil
}

// ScanVolumeNode returns the Nomad node that holds the scanner's database
// volume, and false when the volume doesn't exist (yet). It creates nothing:
// the service asks it to know where a scan will run before it starts one.
func (d *Deployer) ScanVolumeNode(ctx context.Context) (nodeID string, found bool, err error) {
	have, err := d.listVolumes(ctx, nomadjob.ScanVolume)
	if err != nil {
		return "", false, err
	}
	stub, ok := have[nomadjob.ScanVolume]
	if !ok {
		return "", false, nil
	}
	return stub.NodeID, true, nil
}

// ScanJob builds the job of scan run runID with this deployer's settings (the
// scanner's image among them). See nomadjob.ScanJob.
func (d *Deployer) ScanJob(runID string, src nomadjob.ScanSource, opt nomadjob.ScanOptions, private map[string]bool) *api.Job {
	return nomadjob.ScanJob(runID, src, opt, d.settings, private)
}

// DeleteScanVolume deletes the scanner's database volume and the database in
// it (it comes back with the next scan). Nomad refuses while a scan job still
// holds the volume, which is retried as for an app's volumes. A volume that is
// not there is deleted.
func (d *Deployer) DeleteScanVolume(ctx context.Context) error {
	have, err := d.listVolumes(ctx, nomadjob.ScanVolume)
	if err != nil {
		return err
	}
	if stub, ok := have[nomadjob.ScanVolume]; ok {
		return d.deleteVolume(ctx, nomadjob.ScanVolume, stub.ID)
	}
	return nil
}

// StartScan registers a scan job, built by nomadjob.ScanJob. A registry scan of
// an image on a registry Caravana holds credentials for (an entry of private,
// as for ScanJob) first gets them in the job's own variable, the only one its
// task can read: the job holds no value. The other shapes (an SBOM rescan, a
// database update) read no variable. A variable left behind by a failed start
// is deleted again.
//
// Nomad saying no is a *RunStartError; a Nomad that couldn't be reached is
// ErrNomadUnavailable, unwrapped.
func (d *Deployer) StartScan(ctx context.Context, job *api.Job, private map[string]bool) error {
	if job == nil || job.ID == nil {
		return errors.New("start scan: the job has no ID")
	}
	path := nomadjob.ScanVariablePath(*job.ID)
	var items map[string]string // nil: the job reads no variable
	if readsVariable(job, path) {
		image := job.Meta["caravana.image"]
		items = map[string]string{runMarkerItem: *job.ID} // Nomad refuses a variable with no value
		if entry, ok := nomadjob.PrivateRegistry(image, private); ok {
			user, pass, found, err := d.RegistryCredentials(ctx, entry)
			if err != nil {
				return err
			}
			// Credentials that went since the caller read the set leave the
			// scan anonymous, as an app's pull is after a removal.
			if found {
				items = nomadjob.ScanRegistryItems(image, user, pass)
			}
		}
	}
	return d.registerWithVariable(ctx, job, path, items)
}

// readsVariable says one of the job's templates reads the variable at path.
func readsVariable(job *api.Job, path string) bool {
	for _, g := range job.TaskGroups {
		for _, t := range g.Tasks {
			for _, tmpl := range t.Templates {
				if tmpl.EmbeddedTmpl != nil && strings.Contains(*tmpl.EmbeddedTmpl, path) {
					return true
				}
			}
		}
	}
	return false
}

// ScanState reads the state of the scan job jobID: what ScheduleRunState says
// of a run whose task is the scanner. A job Nomad doesn't have is ErrRunGone.
func (d *Deployer) ScanState(ctx context.Context, jobID string) (RunState, error) {
	return d.ScheduleRunState(ctx, jobID, nomadjob.ScanTask)
}

// ScanResult reads the file the scan wrote in allocation allocID. found is
// false when the scanner didn't write one. The text may be longer than
// vulns.MaxResultBytes (one byte more is read, so the caller can tell): Parse
// refuses it. The text is the scanner's and holds the image's own strings: it
// is parsed, not logged.
func (d *Deployer) ScanResult(ctx context.Context, allocID string) (text string, found bool, err error) {
	return d.readCopyFileLimit(ctx, allocID, nomadjob.ScanResultFile, vulns.MaxResultBytes+1)
}

// The classes ScanFailure answers with. They are fixed words and never the
// scanner's text.
const (
	// ScanDBDownload: the vulnerability database couldn't be downloaded or
	// loaded.
	ScanDBDownload = "db_download"
	// ScanDBOld: the database in the cache is older than the scanner accepts
	// and couldn't be updated.
	ScanDBOld = "db_old"
	// ScanRegistryAuth: the image's registry asked for credentials Caravana
	// doesn't have, or refused those it gave.
	ScanRegistryAuth = "registry_auth"
	// ScanImageRead: the image couldn't be read from its registry (missing,
	// or the registry can't be reached).
	ScanImageRead = "image_read"
	// ScanSBOMMissing: the SBOM a rescan reads isn't on the volume (or isn't
	// one): the caller scans the image from its registry instead.
	ScanSBOMMissing = "sbom_missing"
	// ScanOther: the scanner failed for any other reason.
	ScanOther = "other"
)

// scanStderrBytes is how much of the scanner's stderr ScanFailure reads: the
// error is its last lines.
const scanStderrBytes = 4 << 10

// ScanFailure says why a scan that ended without a result failed: one of the
// Scan* classes, read from the end of the scanner's stderr in allocation
// allocID. The text itself never leaves this function: it can name registry
// addresses and the image, which the console, the log and the records must not
// quote (constitution VII). A stderr that can't be read is ScanOther.
func (d *Deployer) ScanFailure(ctx context.Context, allocID string) string {
	stderr, _, err := d.tail(ctx, allocID, nomadjob.ScanTask, LogStderr, scanStderrBytes)
	if err != nil {
		return ScanOther
	}
	return classifyScanFailure(string(stderr))
}

// classifyScanFailure reads Grype's stderr (lab, v0.117.0, 2026-11-19). Grype
// logs warnings and then one error ("[0002] ERROR failed to ..."), whose
// causes follow on indented lines; only the text from the first ERROR on is
// read, since a scan that ended well also logs warnings such as the failed
// update check. What the lab saw:
//
//   - a database that can't be downloaded and isn't cached: "failed to load
//     vulnerability db: database does not exist", after "error updating db
//     error=unable to download listing: ...";
//   - a cached database older than GRYPE_DB_MAX_ALLOWED_BUILT_AGE: "failed to
//     load vulnerability db: the vulnerability database was built 10 hours
//     ago (max allowed age is 1 hour)", also when the update failed too;
//   - the registry refusing: "failed to catalog: ... UNAUTHORIZED:
//     authentication required" (Docker Hub, anonymous), "DENIED: requested
//     access to the resource is denied" (ghcr.io), "unexpected status code
//     401 Unauthorized: incorrect username or password";
//   - an SBOM rescan whose file isn't there: "failed to catalog: unable to
//     open file /cache/sbom/x.cdx.json: ... no such file or directory", and
//     one that isn't an SBOM (empty or garbage): "failed to catalog: unable to
//     decode sbom: sbom format not recognized";
//   - an image that isn't there: "failed to catalog: ... MANIFEST_UNKNOWN:
//     manifest unknown", or "dial tcp ...: connect: connection refused".
//
// The SBOM class comes before the image one, since its line says "failed to
// catalog" too. The database classes come first because their line says "failed to load
// vulnerability db" whatever the cause, and the old-database one before the
// download one because both can be in the same text.
func classifyScanFailure(stderr string) string {
	at := strings.Index(stderr, " ERROR ")
	if at < 0 {
		return ScanOther
	}
	text := strings.ToLower(stderr[at:])
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(text, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("max allowed age", "database was built"):
		return ScanDBOld
	case has("vulnerability db", "unable to download listing", "error updating db"):
		return ScanDBDownload
	case has("unauthorized", "denied", "authentication required", "incorrect username or password", "status code 401", "status code 403"):
		return ScanRegistryAuth
	case has("unable to open file", "unable to decode sbom", "sbom format not recognized"):
		return ScanSBOMMissing
	case has("failed to catalog", "manifest unknown", "failed to get image descriptor", "connection refused", "no such host"):
		return ScanImageRead
	}
	return ScanOther
}

// StopScan stops the scan job jobID: a deregister without purge, so Nomad
// kills the task (after its kill timeout) and keeps the allocation, whose
// output the service can still read. It is what the 20-minute limit and the
// removal of the scanner use. A job Nomad doesn't have is ErrRunGone.
func (d *Deployer) StopScan(ctx context.Context, jobID string) error {
	return d.StopScheduleRun(ctx, jobID)
}

// PurgeScan erases the scan job jobID with its allocations and deletes its
// variable, which may hold registry credentials. A job or variable Nomad
// doesn't have is done. Call it after the result is read: the purge takes the
// allocation's files and output with it.
func (d *Deployer) PurgeScan(ctx context.Context, jobID string) error {
	if err := d.purgeJob(ctx, jobID); err != nil {
		return err
	}
	return d.deleteVariable(ctx, nomadjob.ScanVariablePath(jobID))
}
