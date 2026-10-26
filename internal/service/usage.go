package service

import (
	"context"
	"errors"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// What a copy shows when it has no usage: Nomad didn't answer for it, or its
// node answers but isn't collecting task stats (an upgraded host that hasn't
// had the installer run again).
const (
	errNoUsage       = "Nomad didn't answer for this copy."
	errNotCollecting = "Nomad isn't collecting usage on this node; run the installer again to turn it on."
)

// CopyUsage is what one running copy uses now, next to what its service
// reserves (data-model "Copy usage"). When Error is set the numbers are zero.
type CopyUsage struct {
	CPUMHz   int // used now
	MemoryMB int // used now

	CPUReservedMHz   int // the service's reservation, defaults applied
	MemoryReservedMB int
	MemoryCeilingMB  int  // what the copy may burst to before the kernel stops it
	NearCeiling      bool // MemoryMB is 90% or more of MemoryCeilingMB

	Error string // set instead of the numbers when Nomad couldn't give them

	// None is set instead of the numbers when Nomad answered but has nothing
	// measured for the copy (a copy that isn't running, or no memory kind
	// measured): no value, which is not 0 MB and is not an error (feature 025).
	None bool
}

// CopiesWithUsage is Copies plus the CPU and memory in use for each copy that
// is running, keyed by copy ID (research R7). Copies that have ended or not
// started have no entry; a copy Nomad can't give numbers for has an entry
// with Error set, and doesn't affect the others (FR-016). A service that is
// no longer in the stored app has no reservation to compare with, so its
// copies have no entry either.
//
// The reservation and ceiling are the stored app's, with the defaults the job
// is built with. NearCeiling is decided here, so the API and the console agree.
// Errors are those of Copies.
func (s *Service) CopiesWithUsage(ctx context.Context, appID string) ([]Copy, map[string]CopyUsage, error) {
	a, copies, err := s.appCopies(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]Copy, len(copies))
	for _, c := range copies {
		byID[c.ID] = c
	}
	services := map[string]app.Service{}
	for _, svc := range a.Services {
		services[svc.Name] = svc
	}

	out := map[string]CopyUsage{}
	for id, u := range s.deployer.Usage(ctx, copies) {
		svc, ok := services[byID[id].Service]
		if !ok {
			continue
		}
		cpu, mem, ceiling := s.deployer.Reserved(svc.Resources)
		cu := CopyUsage{CPUReservedMHz: cpu, MemoryReservedMB: mem, MemoryCeilingMB: ceiling}
		switch {
		case errors.Is(u.Err, deploy.ErrUsageNone):
			cu.None = true
		case errors.Is(u.Err, deploy.ErrUsageNotCollected):
			cu.Error = errNotCollecting
		case u.Err != nil:
			cu.Error = errNoUsage
		default:
			cu.CPUMHz, cu.MemoryMB = u.CPUMHz, u.MemoryMB
			cu.NearCeiling = u.MemoryMB*10 >= ceiling*9
		}
		out[id] = cu
	}
	return copies, out, nil
}
