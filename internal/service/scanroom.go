package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Room for a scan (feature 044, D7). A scan reserves 512 MB of memory and needs
// 4 GiB of free disk for the database on the server that holds it. The numbers
// come from the host samples the sampler stores (025, 031), so a check calls no
// Nomad. A server with no sample, or one older than five minutes, doesn't block:
// not knowing is no reason to refuse.

// freeMemoryMB is what a server has left of its memory after what is reserved,
// and false when the sample doesn't say.
func freeMemoryMB(h store.HostSample) (int64, bool) {
	if h.MemoryTotalMB == nil || h.MemoryReservedMB == nil {
		return 0, false
	}
	return *h.MemoryTotalMB - *h.MemoryReservedMB, true
}

// roomReason says why the server can't take a scan now, in Caravana's words, or
// "" when it can: h is its newest host sample (nil when there is none) and disks
// that sample's filesystems.
func roomReason(name string, h *store.HostSample, disks []store.DiskSample, now time.Time) string {
	if h == nil || now.Sub(h.TS) > scanSampleStale {
		return ""
	}
	if free, ok := freeMemoryMB(*h); ok && free < nomadjob.ScanMemoryMB {
		return fmt.Sprintf("not enough free memory on server %s (%d MB, the scan needs %d MB)", name, max(free, 0), nomadjob.ScanMemoryMB)
	}
	for _, d := range disks {
		if strings.Contains(d.Holds, "nomad") && d.FreeMB < scanDiskNeededMB {
			return fmt.Sprintf("not enough free disk on server %s (%.1f GB free, the scanner's database needs %d GB)",
				name, float64(d.FreeMB)/1024, scanDiskNeededMB/1024)
		}
	}
	return ""
}

// scanNode is the server a scan runs on: the one that holds the scanner's volume,
// else (before the volume exists) the ready server with the most free memory.
// false when no server is known.
func (s *Service) scanNode(ctx context.Context) (serverNode, bool) {
	dir, err := s.directory(ctx)
	if err != nil || len(dir.nodes) == 0 {
		return serverNode{}, false
	}
	if id, found, err := s.deployer.ScanVolumeNode(ctx); err == nil && found {
		if n, ok := dir.byID(id); ok {
			return n, true
		}
	}
	var best serverNode
	bestFree, have := int64(-1), false
	for _, n := range dir.nodes {
		if !n.Ready || (n.Pool != "" && n.Pool != "default") {
			continue
		}
		free := int64(-1) // a server with no sample ranks after any that has one
		if h, err := s.store.NewestHostOf(ctx, n.ID); err == nil {
			if f, ok := freeMemoryMB(h); ok {
				free = f
			}
		}
		if !have || free > bestFree {
			best, bestFree, have = n, free, true
		}
	}
	return best, have
}

// scanRoom is roomReason for the server a scan would run on.
func (s *Service) scanRoom(ctx context.Context) string {
	node, ok := s.scanNode(ctx)
	if !ok {
		return ""
	}
	h, err := s.store.NewestHostOf(ctx, node.ID)
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		s.log.Warn("scan room: read the host sample", "error", err)
		return ""
	}
	rows, err := s.store.DiskRows(ctx, store.MinuteRes, h.TS, h.TS.Add(time.Second))
	if err != nil {
		s.log.Warn("scan room: read the disk samples", "error", err)
		return ""
	}
	var disks []store.DiskSample
	for _, d := range rows {
		if d.Node == node.ID {
			disks = append(disks, d)
		}
	}
	return roomReason(node.Name, &h, disks, s.clock.Now())
}

// scanRoomBeforeInstall is the install's check: the same words, since the
// install fails with the reason a scan would be skipped with.
func (s *Service) scanRoomBeforeInstall(ctx context.Context) string { return s.scanRoom(ctx) }
