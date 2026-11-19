package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// The deploy gate (feature 044, D9): with the image scanner installed and the
// owner's "refuse to deploy an image with a fixable critical vulnerability"
// on, a deploy scans each image it brings that has no result younger than a day
// before anything is replaced, and an image with a fixable critical finding
// fails the deploy. A scan that can't be made doesn't block: the deploy goes on
// with a warning saying so. Redeploys, rollbacks, promotions and the webhook all
// go through deployWarned, so they are gated the same way.

// ErrImageVulnerable is what an ImageVulnerableError unwraps to.
var ErrImageVulnerable = errors.New("the image has a known critical vulnerability with a fix")

// ImageVulnerableError is why the gate refused a deploy: the first service, in
// the app's order, whose image has a critical finding with a known fix.
type ImageVulnerableError struct {
	// Field is "services[i].image".
	Field   string
	Service string
	// Image is the digest that was scanned.
	Image string
	// Finding is the worst fixable critical finding of the image (the ones with
	// a known exploit first), and More how many other fixable critical findings
	// the image has.
	Finding vulns.Finding
	More    int
}

// Error reads like "web's image has CVE-2022-37434 (critical) in zlib 1.2.11-r3,
// fixed in 1.2.12-r2 and 2 more". It holds only what the scanner's database says
// and the package's name and version.
func (e *ImageVulnerableError) Error() string {
	f := e.Finding
	id := f.CVE
	if id == "" {
		id = f.Advisory
	}
	msg := fmt.Sprintf("%s's image has %s (%s) in %s %s, fixed in %s", e.Service, id, f.Severity, f.Package, f.Version,
		strings.Join(f.FixedIn, " or "))
	if e.More > 0 {
		msg += fmt.Sprintf(" and %d more", e.More)
	}
	return msg
}

// Unwrap lets callers test errors.Is(err, ErrImageVulnerable).
func (e *ImageVulnerableError) Unwrap() error { return ErrImageVulnerable }

// gateImages is the gate, for the digests a deploy of a is about to register. It
// returns the warnings for the images it couldn't check, or an
// *ImageVulnerableError. It does nothing, and asks nothing of Nomad, while the
// add-on isn't installed or the setting is off.
//
// A scan is waited for only for a part of the time the deploy has left (half of
// it): a deploy that has used its budget on a scan has none for the secrets and
// Nomad. A scan still going then isn't cancelled, and its result is there for
// the next deploy; this one is warned.
func (s *Service) gateImages(ctx context.Context, a app.App, digests map[string]string) ([]app.Warning, error) {
	installed, err := s.scannerInstalled(ctx)
	if err != nil || !installed {
		return nil, err
	}
	set, err := s.store.GetScanSettings(ctx)
	if err != nil || !set.RefuseFixableCritical {
		return nil, err
	}

	wait := s.budget
	if dl, ok := ctx.Deadline(); ok {
		wait = time.Until(dl)
	}
	waitCtx, cancel := context.WithTimeout(ctx, max(wait/2, 0))
	defer cancel()

	var (
		warnings []app.Warning
		checked  = map[string]string{} // image → why it wasn't checked, "" when it was
	)
	for i, svc := range a.Services {
		image := digests[svc.Name]
		if !scannable(image) {
			continue
		}
		reason, done := checked[image]
		if !done {
			reason = s.gateScan(ctx, waitCtx, image)
			checked[image] = reason
		}
		if reason != "" {
			warnings = append(warnings, app.Warning{
				Field: fmt.Sprintf("services[%d].image", i),
				Message: fmt.Sprintf("%s's image wasn't checked for vulnerabilities: %s; deployed without the check.",
					svc.Name, reason),
			})
		}
	}

	var verdict []string
	for _, svc := range a.Services {
		if image := digests[svc.Name]; checked[image] == "" && scannable(image) {
			verdict = append(verdict, image)
		}
	}
	found, err := s.store.FixableFindings(ctx, verdict, vulns.Critical)
	if err != nil {
		return nil, err
	}
	for i, svc := range a.Services {
		image := digests[svc.Name]
		list := found[image]
		if checked[image] != "" || len(list) == 0 {
			continue
		}
		return nil, &ImageVulnerableError{Field: fmt.Sprintf("services[%d].image", i), Service: svc.Name, Image: image,
			Finding: list[0].Finding, More: len(list) - 1}
	}
	return warnings, nil
}

// gateScan makes sure image has a scan result younger than a day, scanning it
// when it hasn't, and says why it couldn't: "" means a result is there. The
// reason is in Caravana's words, never the scanner's.
func (s *Service) gateScan(ctx, waitCtx context.Context, image string) string {
	if prior, err := s.store.GetImageScan(ctx, image); err == nil && prior.HasResult() &&
		s.clock.Now().Sub(prior.ScannedAt) < scanFreshFor {
		return ""
	}
	run, err := s.queueScan(ctx, image, store.ScanTriggerGate)
	if err != nil {
		s.log.Warn("the deploy gate couldn't queue a scan", "error", err)
		return "the scan couldn't be queued"
	}
	done, err := s.ScanWait(waitCtx, run.ID)
	if err != nil {
		return "the scan didn't finish in time"
	}
	if done.Status != store.ScanOK {
		if done.Reason != "" {
			return done.Reason
		}
		return "the scan didn't finish"
	}
	return ""
}
