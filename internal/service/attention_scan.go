package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// image_vulnerable (feature 044, D11): a service's live image has an advisory at
// or above the owner's notify level (critical, or high) that has a known fix.
// One item per app, service and package, so a package with three advisories is
// one problem, named by its worst. Derived on every read from the stored
// findings, so it goes by itself when a deploy brings a fixed image, and it
// exists only while the image scanner is installed. A stopped app is not
// judged. A failed item is one with a critical advisory, else a warning.
//
// The key is image_vulnerable|<app>|<service>|<package>.

// scanItems is the kind's items for the stored apps. off is true while the
// image scanner isn't installed: the kind isn't judged then, so the notifier
// sends no "resolved" for it (judged.Scans).
func (s *Service) scanItems(ctx context.Context, apps []store.AppRecord) (items []AttentionItem, off bool, err error) {
	installed, err := s.scannerInstalled(ctx)
	if err != nil || !installed {
		return nil, true, err
	}
	set, err := s.store.GetScanSettings(ctx)
	if err != nil {
		return nil, false, err
	}
	min := vulns.Critical
	if set.NotifyLevel == "high" {
		min = vulns.High
	}
	uses, err := s.liveUses(ctx)
	if err != nil {
		return nil, false, err
	}
	var running []scanUse
	for _, u := range uses {
		if !u.Stopped {
			running = append(running, u)
		}
	}
	images := distinctImages(running)
	found, err := s.store.FixableFindings(ctx, images, min)
	if err != nil || len(found) == 0 {
		return nil, false, err
	}
	scans, err := s.store.ListImageScans(ctx)
	if err != nil {
		return nil, false, err
	}
	scannedAt := map[string]store.ImageScan{}
	for _, sc := range scans {
		scannedAt[sc.Image] = sc
	}
	places := map[string]store.AppRecord{}
	for _, rec := range apps {
		places[rec.App.ID] = rec
	}

	for _, u := range running {
		// found is in the order of the store: the worst first, then by package.
		var order []string
		byPackage := map[string][]store.StoredFinding{}
		for _, f := range found[u.Image] {
			if _, ok := byPackage[f.Package]; !ok {
				order = append(order, f.Package)
			}
			byPackage[f.Package] = append(byPackage[f.Package], f)
		}
		for _, pkg := range order {
			group := byPackage[pkg]
			worst := group[0]
			it := AttentionItem{Kind: AttentionImageVulnerable, AppID: u.App, Service: u.Service,
				Package: pkg, PackageVersion: worst.Version, FixedIn: worst.FixedIn, Advisory: advisoryOf(worst.Finding),
				Severity: worst.Severity.String(), Image: u.Image, MoreAdvisories: len(group) - 1,
				Level: metrics.DiskWarning, When: scannedAt[u.Image].ScannedAt}
			for _, f := range group {
				it.KEV = it.KEV || f.KEV
			}
			if worst.Severity == vulns.Critical {
				it.Level = metrics.DiskFailed
			}
			if rec, ok := places[u.App]; ok {
				it.Project, it.Environment, it.AppName = rec.Place.Project, rec.Place.Environment, rec.Place.Name
			}
			it.Detail = plainText(imageVulnerableParts(it, placeParts(func(s string) string { return s }, it, slashed(it.Service))))
			items = append(items, it)
		}
	}
	return items, false, nil
}

// advisoryOf is the ID the owner searches for: the CVE when the finding has one.
func advisoryOf(f vulns.Finding) string {
	if f.CVE != "" {
		return f.CVE
	}
	return f.Advisory
}

// imageVulnerableParts is the item's one wording. where is the service's place.
func imageVulnerableParts(it AttentionItem, where []TextPart) []TextPart {
	out := parts(mono(it.Package), " ", mono(it.PackageVersion), " in ", where, " has ", mono(it.Advisory),
		" ("+it.Severity+"), fixed in ", mono(strings.Join(it.FixedIn, " or ")))
	if it.MoreAdvisories > 0 {
		word := "advisories"
		if it.MoreAdvisories == 1 {
			word = "advisory"
		}
		out = append(out, txt(fmt.Sprintf(" and %d more %s", it.MoreAdvisories, word)))
	}
	out = append(out, txt("."))
	if it.KEV {
		out = append(out, txt(" Known exploited."))
	}
	return out
}
