package service

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Reading image scans (feature 044, D12) and the scanner's settings. These are
// the models the API and the console draw; nothing here is the scanner's own
// text.

// ScanSettings are the owner's settings for scanning.
type ScanSettings struct {
	// NotifyLevel is "critical" or "high": the lowest severity that opens a
	// needs-you item.
	NotifyLevel string
	// RefuseFixableCritical makes a deploy of an image with a fixable critical
	// finding fail (the gate).
	RefuseFixableCritical bool
	// DailyHour is the UTC hour, 0 to 23, the daily rescan starts at or after.
	DailyHour int
	// DatabaseURL is the scanner's database update URL (a mirror's latest.json),
	// https:// or empty for the scanner's own.
	DatabaseURL string
}

// ScanSettings returns the settings (the defaults before the owner saved any).
func (s *Service) ScanSettings(ctx context.Context) (ScanSettings, error) {
	set, err := s.store.GetScanSettings(ctx)
	if err != nil {
		return ScanSettings{}, err
	}
	return ScanSettings{NotifyLevel: set.NotifyLevel, RefuseFixableCritical: set.RefuseFixableCritical,
		DailyHour: set.DailyHour, DatabaseURL: set.DatabaseURL}, nil
}

// SetScanSettings validates and stores the settings. A bad value is an
// *app.FieldError naming it (notifyLevel, dailyHour, databaseUrl); turning the
// gate on while the image scanner isn't installed is ErrScannerNotInstalled.
func (s *Service) SetScanSettings(ctx context.Context, in ScanSettings) (ScanSettings, error) {
	in.DatabaseURL = strings.TrimSpace(in.DatabaseURL)
	switch {
	case in.NotifyLevel != "critical" && in.NotifyLevel != "high":
		return ScanSettings{}, app.NewFieldError("notifyLevel", "must be critical or high")
	case in.DailyHour < 0 || in.DailyHour > 23:
		return ScanSettings{}, app.NewFieldError("dailyHour", "must be an hour from 0 to 23 (UTC)")
	}
	if err := checkDatabaseURL(in.DatabaseURL); err != nil {
		return ScanSettings{}, err
	}
	if in.RefuseFixableCritical {
		installed, err := s.scannerInstalled(ctx)
		if err != nil {
			return ScanSettings{}, err
		}
		if !installed {
			return ScanSettings{}, ErrScannerNotInstalled
		}
	}
	if err := s.store.SaveScanSettings(ctx, store.ScanSettings{NotifyLevel: in.NotifyLevel,
		RefuseFixableCritical: in.RefuseFixableCritical, DailyHour: in.DailyHour, DatabaseURL: in.DatabaseURL}); err != nil {
		return ScanSettings{}, err
	}
	s.publish(Change{What: ChangeScans})
	s.publish(Change{What: ChangeAttention}) // the notify level moves what is listed
	return in, nil
}

// checkDatabaseURL accepts an empty URL or an https:// one with a host and no
// user name or password (the URL is kept in the clear).
func checkDatabaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Scheme != "https" || u.Hostname() == "":
		return app.NewFieldError("databaseUrl", "must be an https:// address, or empty to use the scanner's own")
	case u.User != nil:
		return app.NewFieldError("databaseUrl", "must not hold a user name or password")
	}
	return nil
}

// ScanFinding is a stored finding with what the readers add: whether it can be
// fixed and what to do about it, in Caravana's words.
type ScanFinding struct {
	store.StoredFinding
	Fixable     bool
	Remediation string
}

// ImageScanView is an image's stored scan: the counts, the newest try and, when
// asked for, the findings.
type ImageScanView struct {
	store.ImageScan
	Findings []ScanFinding
}

// ServiceScan is one service's image and what is known of its scan.
type ServiceScan struct {
	Service string
	Image   string
	// Servers are the servers running a copy of the service now; empty when
	// that isn't known (the deployment isn't the live one, or Nomad couldn't be
	// read).
	Servers []string
	// Scan is the stored scan; nil before the image's first.
	Scan *ImageScanView
	// Open is the run queued or running for the image; nil when none is.
	Open *store.ScanRun
}

// AppScanView is an app's scans: one entry per service of the live deployment.
type AppScanView struct {
	App string
	// Installed says the image scanner is installed. When it isn't, Services is
	// empty and the page says so.
	Installed bool
	Services  []ServiceScan
}

// AppScan reads the scans of the images the app's live deployment (its newest
// healthy one) runs. An app that doesn't exist is store.ErrNotFound; one with no
// healthy deployment has no services.
func (s *Service) AppScan(ctx context.Context, appID string) (AppScanView, error) {
	if _, err := s.store.GetApp(ctx, appID); err != nil {
		return AppScanView{}, err
	}
	view := AppScanView{App: appID}
	installed, err := s.scannerInstalled(ctx)
	if err != nil || !installed {
		return view, err
	}
	view.Installed = true
	dep, err := s.store.LatestHealthyDeployment(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return view, nil
	}
	if err != nil {
		return AppScanView{}, err
	}
	view.Services, err = s.serviceScans(ctx, appID, dep.Deployment.Digests, true)
	return view, err
}

// DeploymentScan reads the scans of the images one deployment of the app ran. A
// deployment of another app is store.ErrNotFound. The servers are given only for
// the live deployment: the others' copies are gone or replaced.
func (s *Service) DeploymentScan(ctx context.Context, appID, deploymentID string) (AppScanView, error) {
	rec, err := s.GetDeployment(ctx, appID, deploymentID)
	if err != nil {
		return AppScanView{}, err
	}
	view := AppScanView{App: appID}
	installed, err := s.scannerInstalled(ctx)
	if err != nil || !installed {
		return view, err
	}
	view.Installed = true
	live, err := s.store.LatestHealthyDeployment(ctx, appID)
	isLive := err == nil && live.Deployment.ID == deploymentID
	view.Services, err = s.serviceScans(ctx, appID, rec.Deployment.Digests, isLive)
	return view, err
}

// serviceScans builds the entries for digests (service → image), with findings.
func (s *Service) serviceScans(ctx context.Context, appID string, digests map[string]string, withServers bool) ([]ServiceScan, error) {
	open, err := s.openRunsByImage(ctx)
	if err != nil {
		return nil, err
	}
	var lookup *serverLookup
	if withServers {
		lookup = s.newServerLookup(ctx)
	}
	names := make([]string, 0, len(digests))
	for name := range digests {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []ServiceScan
	for _, name := range names {
		image := digests[name]
		if !scannable(image) {
			continue
		}
		entry := ServiceScan{Service: name, Image: image, Open: open[image]}
		if lookup != nil {
			entry.Servers = lookup.of(appID, name)
		}
		if entry.Scan, err = s.imageScanView(ctx, image, true); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// imageScanView reads one image's stored scan, nil when it has none.
func (s *Service) imageScanView(ctx context.Context, image string, findings bool) (*ImageScanView, error) {
	scan, err := s.store.GetImageScan(ctx, image)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := &ImageScanView{ImageScan: scan}
	if findings && scan.HasResult() {
		stored, err := s.store.ImageFindings(ctx, image)
		if err != nil {
			return nil, err
		}
		v.Findings = make([]ScanFinding, len(stored))
		for i, f := range stored {
			v.Findings[i] = ScanFinding{StoredFinding: f, Fixable: f.Fixable(), Remediation: f.Remediation()}
		}
	}
	return v, nil
}

// openRunsByImage maps each image with a queued or running scan to that run.
func (s *Service) openRunsByImage(ctx context.Context) (map[string]*store.ScanRun, error) {
	runs, err := s.store.OpenScanRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*store.ScanRun{}
	for i := range runs {
		if runs[i].Image != "" {
			out[runs[i].Image] = &runs[i]
		}
	}
	return out, nil
}

// ScanUse is one service that runs an image, and where.
type ScanUse struct {
	App, Service string
	Servers      []string
}

// ImageScanSummary is one image the installation runs: who runs it and the
// counts of its scan, without the findings.
type ImageScanSummary struct {
	Image  string
	UsedBy []ScanUse
	// Scan is the stored scan; nil for an image not scanned yet.
	Scan *store.ImageScan
	// Open is the run queued or running for it.
	Open *store.ScanRun
}

// ImageScans lists every image an app's live deployment runs, in order of first
// use, with its scan. It is empty while the image scanner isn't installed.
func (s *Service) ImageScans(ctx context.Context) ([]ImageScanSummary, error) {
	installed, err := s.scannerInstalled(ctx)
	if err != nil || !installed {
		return nil, err
	}
	uses, err := s.liveUses(ctx)
	if err != nil {
		return nil, err
	}
	open, err := s.openRunsByImage(ctx)
	if err != nil {
		return nil, err
	}
	lookup := s.newServerLookup(ctx)
	var out []ImageScanSummary
	for _, image := range distinctImages(uses) {
		sum := ImageScanSummary{Image: image, Open: open[image]}
		for _, u := range uses {
			if u.Image == image {
				sum.UsedBy = append(sum.UsedBy, ScanUse{App: u.App, Service: u.Service, Servers: lookup.of(u.App, u.Service)})
			}
		}
		if scan, err := s.store.GetImageScan(ctx, image); err == nil {
			sum.Scan = &scan
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

// ScanRuns lists the newest scan runs, newest first, at most n (the newest 50
// when n isn't positive).
func (s *Service) ScanRuns(ctx context.Context, n int) ([]store.ScanRun, error) {
	if n <= 0 {
		n = 50
	}
	return s.store.ListScanRuns(ctx, n)
}

// serverLookup answers which servers run a service, reading Nomad's copies and
// the server directory once, on the first question. A Nomad that can't be read
// gives no server, never an error: the page shows the scan without them.
type serverLookup struct {
	s      *Service
	ctx    context.Context
	loaded bool
	byJob  map[string][]deploy.Copy
	dir    serverDirectory
}

func (s *Service) newServerLookup(ctx context.Context) *serverLookup {
	return &serverLookup{s: s, ctx: ctx}
}

// of returns the names of the servers with a copy of the service that hasn't
// ended, sorted.
func (l *serverLookup) of(appID, service string) []string {
	if !l.loaded {
		l.loaded = true
		copies, err := l.s.deployer.AllCopies(l.ctx)
		if err != nil {
			l.s.log.Warn("scans: read the copies for the servers of each image", "error", err)
			return nil
		}
		l.byJob = copies
		if dir, err := l.s.directory(l.ctx); err == nil {
			l.dir = dir
		}
	}
	var names []string
	for _, c := range l.byJob[appID] {
		if c.Service != service || c.Terminal || c.Node == "" {
			continue
		}
		name := c.Node
		for _, n := range l.dir.nodes {
			if n.NodeName == c.Node && n.Name != "" {
				name = n.Name
			}
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
