package nodeagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/certs"
)

// Dropping the ACME certificates an upload covers (feature 056). Traefik keeps serving and renewing a
// certificate it already holds, and one for an exact name beats an uploaded wildcard (feature 034,
// research R5), so after an upload the hosts it covers keep presenting their earlier certificate. The
// store is a folder only root can read on the first server, so the console hands this agent a request
// (an ID and the names of its uploads, nothing else), and the agent removes from each store file the
// certificates whose every name those names cover. The installer runs the same rule through
// `caravana node drop-certificates`. Nothing of a certificate, key or account is ever printed, logged or
// reported: only the names of what was dropped.

// Where Traefik's store is, under the Agent's Root: the host volume traefik-acme.
const traefikStoreDir = "/var/lib/caravana-host/traefik"

const (
	// maxStoreFile is the largest store file that is read.
	maxStoreFile = 32 << 20
	// maxDropNames is the most names a request may carry; maxDroppedEntries the most entries reported.
	maxDropNames      = 1000
	maxDroppedEntries = 100
)

// The states of a certificate drop outcome.
const (
	DropRunning = "running"
	DropDropped = "dropped"
	DropNothing = "nothing"
	DropFailed  = "failed"
	DropRefused = "refused"
)

// CertificateDropRequest is the answer's certificateDrop block: drop the stored ACME certificates whose
// every name Names covers. ID is the request's, 32 lowercase hex digits. Neither is trusted until
// applyCertificateDrop has checked both.
type CertificateDropRequest struct {
	ID    string   `json:"id"`
	Names []string `json:"names"`
}

// CertificateDropOutcome is how the last request this agent handled went. It is kept in state.json and
// reported to the console. Dropped lists the certificates removed, each as its names joined with "+",
// main name first.
type CertificateDropOutcome struct {
	ID      string   `json:"id"`
	State   string   `json:"state"` // running, dropped, nothing, failed or refused
	Dropped []string `json:"dropped,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

// validate says a stored outcome is one this agent wrote.
func (o *CertificateDropOutcome) validate() error {
	if o == nil {
		return nil
	}
	switch {
	case !cleanIDRE.MatchString(o.ID):
		return errors.New("state.json: certificateDrop.id isn't 32 lowercase hex characters")
	case o.State != DropRunning && o.State != DropDropped && o.State != DropNothing && o.State != DropFailed && o.State != DropRefused:
		return errors.New("state.json: certificateDrop.state isn't running, dropped, nothing, failed or refused")
	case len(o.Dropped) > maxDroppedEntries:
		return errors.New("state.json: certificateDrop.dropped has too many entries")
	}
	return nil
}

var (
	hostLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// storeFileRE names the files of Traefik's store: the installer's acme.json and one acme-<12 hex>.json
	// per issuer (deploy.ACMEStorage).
	storeFileRE = regexp.MustCompile(`^acme(-[0-9a-f]{12})?\.json$`)
)

// ValidDropName says a name is one a drop request may carry: a lowercase host name (labels of a-z, 0-9
// and -, none starting or ending with a dash, 1 to 63 characters each, at least two labels, at most 253
// characters in all), or "*." and such a host name.
func ValidDropName(name string) bool {
	host := strings.TrimPrefix(name, "*.")
	if len(name) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if len(l) > 63 || !hostLabelRE.MatchString(l) {
			return false
		}
	}
	return true
}

// DropCovered is the rule: it removes from one Traefik store file (its JSON, as read) every certificate
// whose every name (domain.main and domain.sans) is covered by a name of names (exactly, or as a wildcard
// *.rest for one label more: certs.Covers), and returns the file to write and what it removed, each as
// the certificate's names joined with "+". Everything it doesn't change is kept: the other members of a
// resolver (the account), the kept certificates, the other resolvers. When nothing is covered the input
// is returned as it was. An empty file, whitespace and null are no certificates; anything that isn't an
// object of resolvers is an error, which never quotes the input.
//
// A certificate with a name that isn't a host name (ValidDropName, after lower-casing) is never dropped:
// the names reported go to the console, and an ACME store only holds host names.
func DropCovered(store []byte, names []string) (out []byte, dropped []string, err error) {
	if len(bytes.TrimSpace(store)) == 0 {
		return store, nil, nil
	}
	var resolvers map[string]json.RawMessage
	if err := json.Unmarshal(store, &resolvers); err != nil {
		return nil, nil, errors.New("isn't a Traefik certificate store")
	}
	changed := false
	for name, raw := range resolvers {
		var resolver map[string]json.RawMessage
		if json.Unmarshal(raw, &resolver) != nil || resolver == nil {
			continue // not an object: nothing of Traefik's to change here
		}
		var certificates []json.RawMessage
		if json.Unmarshal(resolver["Certificates"], &certificates) != nil || len(certificates) == 0 {
			continue
		}
		kept := make([]json.RawMessage, 0, len(certificates))
		for _, c := range certificates {
			if hosts, ok := coveredHosts(c, names); ok {
				dropped = append(dropped, strings.Join(hosts, "+"))
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) == len(certificates) {
			continue
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return nil, nil, errors.New("couldn't be written again")
		}
		resolver["Certificates"] = encoded
		if resolvers[name], err = json.Marshal(resolver); err != nil {
			return nil, nil, errors.New("couldn't be written again")
		}
		changed = true
	}
	if !changed {
		return store, nil, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(resolvers); err != nil {
		return nil, nil, errors.New("couldn't be written again")
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), dropped, nil
}

// coveredHosts reads one stored certificate and says whether every one of its names is covered. It
// returns the names (lowercase, main first) when it is. A certificate that can't be read is kept.
func coveredHosts(raw json.RawMessage, names []string) ([]string, bool) {
	var c struct {
		Domain struct {
			Main string   `json:"main"`
			Sans []string `json:"sans"`
		} `json:"domain"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	var hosts []string
	for _, h := range append([]string{c.Domain.Main}, c.Domain.Sans...) {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return nil, false
	}
	for _, h := range hosts {
		if !ValidDropName(h) || !certs.Covers(names, h) {
			return nil, false
		}
	}
	return hosts, true
}

// HasTraefikStore says Traefik's store folder is on this server: the first server, where the proxy runs.
func (a *Agent) HasTraefikStore() bool {
	fi, err := os.Stat(a.path(traefikStoreDir))
	return err == nil && fi.IsDir()
}

// storeFileError is a store file that couldn't be dealt with. It names the file's base name and a fixed
// reason, never what the file holds.
type storeFileError struct{ base, reason string }

func (e *storeFileError) Error() string { return e.base + " " + e.reason }

// pendingStore is a store file with what it will hold.
type pendingStore struct {
	path    string
	updated []byte
}

// DropCoveredCertificates runs the rule over every store file of the folder (acme.json and
// acme-<12 hex>.json, regular files only: a symlink or another file is left alone, as is anything over
// 32 MiB, which is an error) and returns what it removed. All files are read and checked before any is
// written, so a file that isn't a store changes none. With dryRun nothing is written. A missing folder
// has no certificates.
func (a *Agent) DropCoveredCertificates(names []string, dryRun bool) ([]string, error) {
	dir := a.path(traefikStoreDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, &storeFileError{"the store folder", "couldn't be read"}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var (
		dropped []string
		pending []pendingStore
	)
	for _, e := range entries {
		if !storeFileRE.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi.Size() > maxStoreFile {
			return nil, &storeFileError{e.Name(), "is larger than 32 MiB"}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, &storeFileError{e.Name(), "couldn't be read"}
		}
		updated, gone, err := DropCovered(b, names)
		if err != nil {
			return nil, &storeFileError{e.Name(), err.Error()}
		}
		if len(gone) > 0 {
			dropped = append(dropped, gone...)
			pending = append(pending, pendingStore{path: path, updated: updated})
		}
	}
	if dryRun {
		return dropped, nil
	}
	for _, p := range pending {
		if err := rewriteFile(p.path, p.updated); err != nil {
			return nil, &storeFileError{filepath.Base(p.path), "couldn't be written"}
		}
	}
	return dropped, nil
}

// checkDropNames says the names of a request are ones the agent accepts: 1 to 1000, each a ValidDropName.
func checkDropNames(names []string) bool {
	if len(names) < 1 || len(names) > maxDropNames {
		return false
	}
	for _, n := range names {
		if !ValidDropName(n) {
			return false
		}
	}
	return true
}

// applyCertificateDrop is the sync's step for the drop request, and returns the state with what it
// recorded (the caller carries it on, so a later save doesn't lose it) and whether the step removed
// certificates, which is when the console must hear of it at once. A request:
//   - with an ID that isn't 32 lowercase hex digits can't be told to the console: nothing is recorded
//     and one line is logged;
//   - whose ID this agent already has a final outcome for is not run again (the outcome keeps being
//     reported); one it finds `running` (it was stopped in the middle) is run again, since the rule is
//     idempotent;
//   - with names that aren't host names, or on a server with no Traefik store, is `refused`;
//   - otherwise is run: `running` is saved first, then `dropped`, `nothing` or `failed`.
//
// It never returns an error: what goes wrong is in the outcome or on standard error, so the other
// steps of the sync go on. When state.json can't be written nothing is changed.
func (a *Agent) applyCertificateDrop(st State, req *CertificateDropRequest) (State, bool) {
	if req == nil {
		return st, false
	}
	if !cleanIDRE.MatchString(req.ID) {
		a.warn("caravana node sync: the console asked to drop certificates with an ID that isn't 32 hex digits. Ignored.")
		return st, false
	}
	if o := st.CertificateDrop; o != nil && o.ID == req.ID && o.State != DropRunning {
		return st, false
	}
	switch {
	case !checkDropNames(req.Names):
		return a.recordDrop(st, CertificateDropOutcome{ID: req.ID, State: DropRefused, Reason: "The names aren't host names this agent accepts."}), false
	case !a.HasTraefikStore():
		return a.recordDrop(st, CertificateDropOutcome{ID: req.ID, State: DropRefused, Reason: "This server doesn't hold Traefik's certificate store."}), false
	}
	running := a.recordDrop(st, CertificateDropOutcome{ID: req.ID, State: DropRunning})
	if running.CertificateDrop == st.CertificateDrop {
		return st, false // state.json can't be written: nothing is changed
	}
	st = running

	dropped, err := a.DropCoveredCertificates(req.Names, false)
	var sf *storeFileError
	switch {
	case errors.As(err, &sf):
		return a.recordDrop(st, CertificateDropOutcome{ID: req.ID, State: DropFailed, Reason: "Traefik's certificate store couldn't be changed: " + sf.Error() + "."}), false
	case err != nil:
		return a.recordDrop(st, CertificateDropOutcome{ID: req.ID, State: DropFailed, Reason: err.Error() + "."}), false
	case len(dropped) == 0:
		return a.recordDrop(st, CertificateDropOutcome{ID: req.ID, State: DropNothing}), false
	}
	if len(dropped) > maxDroppedEntries {
		dropped = dropped[:maxDroppedEntries]
	}
	a.out("Dropped the earlier certificates of %s.", strings.Join(dropped, ", "))
	next := a.recordDrop(st, CertificateDropOutcome{ID: req.ID, State: DropDropped, Dropped: dropped})
	return next, next.CertificateDrop != st.CertificateDrop
}

// recordDrop saves an outcome in state.json and returns the state with it. When the save fails the
// state is returned as it was: the request is seen again at the next sync.
func (a *Agent) recordDrop(st State, o CertificateDropOutcome) State {
	next := st
	next.CertificateDrop = &o
	if err := a.SaveState(next); err != nil {
		a.warn("caravana node sync: the outcome of the certificate drop wasn't saved: %v", err)
		return st
	}
	return next
}
