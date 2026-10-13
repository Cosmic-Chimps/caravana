package service

import (
	"context"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// errNoUsage is what a copy shows when Nomad gave no usage for it.
const errNoUsage = "Nomad didn't answer for this copy."

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
		if u.Err != nil {
			cu.Error = errNoUsage
		} else {
			cu.CPUMHz, cu.MemoryMB = u.CPUMHz, u.MemoryMB
			cu.NearCeiling = u.MemoryMB*10 >= ceiling*9
		}
		out[id] = cu
	}
	return copies, out, nil
}
