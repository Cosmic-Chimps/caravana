package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// The phases of a copy on the node, for the server page (feature 027): the one word its mark and its
// row say. A copy that Nomad has told to stop is stopping whatever its status still says.
const (
	CopyPhaseRunning  = "running"
	CopyPhaseStarting = "starting"
	CopyPhaseStopping = "stopping"
	CopyPhaseFailed   = "failed"
)

// HostCopy is one copy on the node, with the app it belongs to and what it uses now against what its
// service reserves. It is the per-app listCopies' copy, across apps (that operation is the API's
// parity source: the server page reads the service, and no operation of its own is added).
type HostCopy struct {
	Copy
	AppID string
	// Place is the app's project, environment and name.
	Place app.Place
	// Phase is running, starting, stopping or failed.
	Phase string
	// Usage is what the copy uses now and the service's reservation and limit. Nil for a copy that
	// has neither (a failed one). A copy that is starting has the reservation and None set.
	Usage *CopyUsage
}

// HostCopies lists every copy of an app that Nomad has on the node: the ones that run or are
// starting, and the failed ones of the newest version (the same filter as an app's own list, since
// a failed copy is what the page must show first). Jobs that aren't apps (Traefik, a backup run)
// are left out, as the Overview's count leaves them.
//
// Each running copy has its CPU and memory now (the Running tab's source, deploy.Usage), and the
// service task's reservation and memory limit from the allocation itself (feature 025's
// reservations), so a service whose stored app has since changed still shows what Nomad holds. A
// copy Nomad can't give numbers for has Usage.Error set and doesn't affect the others; reservations
// that can't be read leave the reserved numbers at zero.
//
// The list is ordered problems first (failed, starting, stopping), then by project, environment,
// app name, service, start time and ID. When Nomad can't be read the error is deploy's
// (ErrNomadUnavailable): a list of copies has no stored fallback.
func (s *Service) HostCopies(ctx context.Context) ([]HostCopy, error) {
	byJob, err := s.deployer.AllCopies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the copies on the node: %w", err)
	}
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the copies on the node: %w", err)
	}

	var out []HostCopy
	var shown []deploy.Copy
	for _, rec := range recs {
		for _, c := range byJob[rec.App.ID] {
			if c.Terminal && c.Status != deploy.CopyFailed {
				continue
			}
			out = append(out, HostCopy{Copy: c, AppID: rec.App.ID, Place: rec.Place, Phase: copyPhase(c)})
			shown = append(shown, c)
		}
	}
	if len(out) == 0 {
		return out, nil
	}

	usage := s.deployer.Usage(ctx, shown)
	res, resErr := s.deployer.Reservations(ctx)
	if resErr != nil {
		s.log.Warn("can't read the copies' reservations for the server page", "error", resErr)
	}
	reserved := map[string]deploy.Reservation{}
	for _, r := range res.Copies {
		reserved[r.CopyID] = r
	}
	for i := range out {
		out[i].Usage = hostCopyUsage(usage, reserved, out[i].ID)
	}

	slices.SortStableFunc(out, func(a, b HostCopy) int {
		return cmp.Or(
			phaseRank(a.Phase)-phaseRank(b.Phase),
			strings.Compare(a.Place.Project, b.Place.Project),
			strings.Compare(a.Place.Environment, b.Place.Environment),
			strings.Compare(a.Place.Name, b.Place.Name),
			strings.Compare(a.AppID, b.AppID),
			strings.Compare(a.Service, b.Service),
			compareStart(a.StartedAt, b.StartedAt),
			strings.Compare(a.ID, b.ID),
		)
	})
	return out, nil
}

// copyPhase is the one word for a copy.
func copyPhase(c deploy.Copy) string {
	switch {
	case c.Status == deploy.CopyFailed:
		return CopyPhaseFailed
	case c.Stopping:
		return CopyPhaseStopping
	case c.Status == deploy.CopyStarting:
		return CopyPhaseStarting
	}
	return CopyPhaseRunning
}

// phaseRank orders the phases problems first: failed, starting, stopping, then running.
func phaseRank(phase string) int {
	switch phase {
	case CopyPhaseFailed:
		return 0
	case CopyPhaseStarting:
		return 1
	case CopyPhaseStopping:
		return 2
	}
	return 3
}

// hostCopyUsage puts a copy's measurement and reservation together; nil when it has neither.
func hostCopyUsage(usage map[string]deploy.CopyUsage, reserved map[string]deploy.Reservation, id string) *CopyUsage {
	u, haveUsage := usage[id]
	r, haveRes := reserved[id]
	if !haveUsage && !haveRes {
		return nil
	}
	cu := CopyUsage{}
	if haveRes {
		cu.CPUReservedMHz, cu.MemoryReservedMB, cu.MemoryCeilingMB = r.ServiceCPUMHz, r.ServiceMemoryMB, r.ServiceMemoryLimitMB()
	}
	switch {
	case !haveUsage:
		cu.None = true // not measured (yet): no value, which is not 0
	case errors.Is(u.Err, deploy.ErrUsageNone):
		cu.None = true
	case errors.Is(u.Err, deploy.ErrUsageNotCollected):
		cu.Error = errNotCollecting
	case u.Err != nil:
		cu.Error = errNoUsage
	default:
		cu.CPUMHz, cu.MemoryMB = u.CPUMHz, u.MemoryMB
		cu.NearCeiling = cu.MemoryCeilingMB > 0 && u.MemoryMB*10 >= cu.MemoryCeilingMB*9
	}
	return &cu
}

// compareStart orders start times oldest first, a copy that hasn't started last.
func compareStart(a, b *time.Time) int {
	switch {
	case a != nil && b != nil:
		return a.Compare(*b)
	case a != nil:
		return -1
	case b != nil:
		return 1
	}
	return 0
}
