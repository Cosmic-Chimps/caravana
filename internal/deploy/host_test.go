package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

func labNode(n *deploytest.Nomad, stats api.HostStats) {
	n.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	n.SetHostStats(stats)
}

func TestHostStats(t *testing.T) {
	ctx := context.Background()

	t.Run("the lab's node: one filesystem holds both, no other mount is listed", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		h, err := d.HostStats(ctx, "/var/lib/docker")
		if err != nil {
			t.Fatalf("HostStats: %v", err)
		}
		if h.NodeID != "node-1" || h.NodeName != "lab" {
			t.Errorf("node = %q %q", h.NodeID, h.NodeName)
		}
		if h.MemoryUsedMB != 1680 || h.MemoryTotalMB != 1949 {
			t.Errorf("memory = %d of %d MB, want 1680 of 1949", h.MemoryUsedMB, h.MemoryTotalMB)
		}
		if h.CPUUsedMHz != 485 || h.CPUTotalMHz != 4000 || h.CPUCores != 2 {
			t.Errorf("cpu = %d of %d MHz over %d cores, want 485 of 4000 over 2", h.CPUUsedMHz, h.CPUTotalMHz, h.CPUCores)
		}
		if h.ReservedCPUMHz != 0 || h.ReservedMemoryMB != 0 {
			t.Errorf("node reserved = %d MHz, %d MB, want none", h.ReservedCPUMHz, h.ReservedMemoryMB)
		}
		if len(h.Disks) != 1 {
			t.Fatalf("disks = %+v, want one", h.Disks)
		}
		got := h.Disks[0]
		want := Disk{Mount: "/", HoldsNomad: true, HoldsDocker: true, UsedMB: 5945, TotalMB: 18771, FreeMB: 12810}
		if got != want || got.Holds() != "nomad,docker" {
			t.Errorf("disk = %+v (holds %q), want %+v", got, got.Holds(), want)
		}
	})

	t.Run("Docker's root on another mount is a second row, by the longest prefix", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		stats := deploytest.LabHostStats()
		stats.DiskStats = append(stats.DiskStats,
			&api.HostDiskStats{Device: "/dev/vdc", Mountpoint: "/var/lib/docker", Size: 50 << 30, Used: 10 << 30, Available: 40 << 30},
			&api.HostDiskStats{Device: "/dev/vdd", Mountpoint: "/var/lib/dock", Size: 1 << 30, Used: 1 << 20, Available: 1 << 30}, // not a prefix of a path
			&api.HostDiskStats{Device: "/dev/vde", Mountpoint: "/var", Size: 5 << 30, Used: 1 << 30, Available: 4 << 30})          // shorter
		labNode(nomad, stats)
		h, err := d.HostStats(ctx, "/var/lib/docker/")
		if err != nil {
			t.Fatalf("HostStats: %v", err)
		}
		if len(h.Disks) != 2 {
			t.Fatalf("disks = %+v, want two", h.Disks)
		}
		root, docker := h.Disks[0], h.Disks[1]
		if root.Mount != "/" || root.Holds() != "nomad" || docker.Mount != "/var/lib/docker" || docker.Holds() != "docker" {
			t.Errorf("disks = %+v", h.Disks)
		}
		if docker.TotalMB != 51200 || docker.UsedMB != 10240 || docker.FreeMB != 40960 {
			t.Errorf("docker disk = %+v", docker)
		}
	})

	t.Run("a root deeper than any mount falls back to the mount that holds it", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		stats := deploytest.LabHostStats()
		stats.DiskStats = append(stats.DiskStats, &api.HostDiskStats{Device: "/dev/vdc", Mountpoint: "/var", Size: 5 << 30, Used: 1 << 30, Available: 4 << 30})
		labNode(nomad, stats)
		h, err := d.HostStats(ctx, "/var/lib/docker")
		if err != nil {
			t.Fatalf("HostStats: %v", err)
		}
		if len(h.Disks) != 2 || h.Disks[1].Mount != "/var" || h.Disks[1].Holds() != "docker" {
			t.Errorf("disks = %+v, want / (nomad) and /var (docker)", h.Disks)
		}
	})

	t.Run("Nomad's data on its own mount is found by size and use", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		stats := deploytest.LabHostStats()
		data := &api.HostDiskStats{Device: "/dev/vdb1", Mountpoint: "/opt/nomad", Size: 100 << 30, Used: 20 << 30, Available: 80 << 30}
		stats.DiskStats = append(stats.DiskStats, data)
		alloc := *data
		alloc.Device, alloc.Mountpoint = "", ""
		alloc.Used += 1 << 20 // written between two reads: still the same mount
		stats.AllocDirStats = &alloc
		labNode(nomad, stats)
		h, err := d.HostStats(ctx, "/var/lib/docker")
		if err != nil {
			t.Fatalf("HostStats: %v", err)
		}
		if len(h.Disks) != 2 || h.Disks[0].Mount != "/" || h.Disks[0].Holds() != "docker" ||
			h.Disks[1].Mount != "/opt/nomad" || h.Disks[1].Holds() != "nomad" {
			t.Errorf("disks = %+v", h.Disks)
		}
	})

	t.Run("an alloc dir that matches no mount lists no mount for Nomad", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		stats := deploytest.LabHostStats()
		stats.AllocDirStats = &api.HostDiskStats{Size: 123, Used: 5}
		labNode(nomad, stats)
		h, err := d.HostStats(ctx, "/var/lib/docker")
		if err != nil {
			t.Fatalf("HostStats: %v", err)
		}
		if len(h.Disks) != 1 || h.Disks[0].Holds() != "docker" {
			t.Errorf("disks = %+v, want only Docker's", h.Disks)
		}
	})

	t.Run("the cloud-init CD is never listed, whatever the roots", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		for _, root := range []string{"", "/", "/var/lib/docker", "/mnt/lima-cidata-other", "relative"} {
			h, err := d.HostStats(ctx, root)
			if err != nil {
				t.Fatalf("HostStats(%q): %v", root, err)
			}
			for _, disk := range h.Disks {
				if strings.Contains(disk.Mount, "cidata") || disk.Mount == "/boot" || disk.Mount == "/boot/efi" {
					t.Errorf("root %q lists %+v", root, disk)
				}
			}
		}
	})

	t.Run("no Docker root leaves Docker's filesystem out", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		h, err := d.HostStats(ctx, "")
		if err != nil || len(h.Disks) != 1 || h.Disks[0].Holds() != "nomad" {
			t.Errorf("disks = %+v, err = %v", h.Disks, err)
		}
	})

	t.Run("what the node holds back is reported", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(),
			&api.NodeReservedResources{Cpu: api.NodeReservedCpuResources{CpuShares: 200}, Memory: api.NodeReservedMemoryResources{MemoryMB: 256}})
		nomad.SetHostStats(deploytest.LabHostStats())
		h, err := d.HostStats(ctx, "")
		if err != nil || h.ReservedCPUMHz != 200 || h.ReservedMemoryMB != 256 {
			t.Errorf("host = %+v, err = %v", h, err)
		}
	})

	t.Run("a token without node:read is ErrNodeReadDenied, an ErrNomadUnavailable that says what to do", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		nomad.FailNodeReads(403)
		_, err := d.HostStats(ctx, "")
		if !errors.Is(err, ErrNodeReadDenied) || !errors.Is(err, ErrNomadUnavailable) {
			t.Fatalf("err = %v, want ErrNodeReadDenied and ErrNomadUnavailable", err)
		}
		if msg := err.Error(); !strings.Contains(msg, "node:read") || !strings.Contains(msg, "run the installer again") {
			t.Errorf("message = %q", msg)
		}
	})

	t.Run("other failures are ErrNomadUnavailable, not denied", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		nomad.FailNodeReads(500)
		_, err := d.HostStats(ctx, "")
		if !errors.Is(err, ErrNomadUnavailable) || errors.Is(err, ErrNodeReadDenied) {
			t.Errorf("err = %v", err)
		}
		nomad.FailNodeReads(0)
		nomad.Down()
		_, err = d.HostStats(ctx, "")
		if !errors.Is(err, ErrNomadUnavailable) || errors.Is(err, ErrNodeReadDenied) {
			t.Errorf("Nomad down: err = %v", err)
		}
	})

	t.Run("a node that isn't ready, or none, is an error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if _, err := d.HostStats(ctx, ""); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("no node: err = %v", err)
		}
		nomad.SetNode("node-1", "lab", "down", deploytest.LabNodeResources(), nil)
		nomad.SetHostStats(deploytest.LabHostStats())
		if _, err := d.HostStats(ctx, ""); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("down node: err = %v", err)
		}
	})

	t.Run("the node's resources are read once in ten minutes, its stats every time", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		labNode(nomad, deploytest.LabHostStats())
		now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
		d.cache.now = func() time.Time { return now }

		for range 3 {
			if _, err := d.HostStats(ctx, ""); err != nil {
				t.Fatalf("HostStats: %v", err)
			}
			now = now.Add(time.Minute)
		}
		list, info, stats := nomad.NodeRequests()
		if list != 3 || info != 1 || stats != 3 {
			t.Errorf("requests = list %d, info %d, stats %d; want 3, 1, 3", list, info, stats)
		}

		now = now.Add(8 * time.Minute) // 11 minutes since the read
		if _, err := d.HostStats(ctx, ""); err != nil {
			t.Fatalf("HostStats: %v", err)
		}
		if _, info, _ = nomad.NodeRequests(); info != 2 {
			t.Errorf("info requests after the ttl = %d, want 2", info)
		}
	})
}

func TestMountOfPath(t *testing.T) {
	disks := []*api.HostDiskStats{
		{Mountpoint: "/"}, {Mountpoint: "/var"}, {Mountpoint: "/var/lib/docker"}, {Mountpoint: "/va"},
	}
	tests := []struct{ path, want string }{
		{"/var/lib/docker", "/var/lib/docker"},
		{"/var/lib/docker/", "/var/lib/docker"},
		{"/var/lib/docker/overlay2", "/var/lib/docker"},
		{"/var/lib/dockerx", "/var"}, // not a path prefix
		{"/var/log", "/var"},
		{"/vault", "/"},
		{"/srv", "/"},
		{"", ""},
		{"var/lib", ""},
	}
	for _, tc := range tests {
		got := ""
		if m := mountOfPath(disks, tc.path); m != nil {
			got = m.Mountpoint
		}
		if got != tc.want {
			t.Errorf("mountOfPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	if m := mountOfPath([]*api.HostDiskStats{{Mountpoint: "/boot"}}, "/var"); m != nil {
		t.Errorf("a path no mount holds gave %+v", m)
	}
}
