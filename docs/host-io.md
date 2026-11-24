# Network and disk throughput per server

Caravana's Monitoring and server pages already showed CPU, memory and disk space. Nomad's host numbers have
nothing about the network or about how hard the disk is working, which is what the owner of a small VM most
wants to know when a site is slow. Each server now says how many bytes per second go in and out of its network
and how many are read from and written to its disk, and how busy the disk is.

## Where the numbers come from

Every server Caravana manages runs its agent (`caravana node sync`, every 30 seconds, as root). It reads three
files the kernel keeps, and sends **cumulative counters since boot** in its report (the `io` block):

- `/proc/net/dev`: bytes received and sent.
- `/proc/diskstats`: bytes read and written (sectors times 512) and the milliseconds the device was busy.
- `/proc/self/mountinfo`: which device holds a folder.

It reports only:

- the network interface of the server's **default route** (what the internet sees of it), and `caravana-mesh`,
  the WireGuard interface between servers, when there is one. Never the interfaces that come and go with the
  apps (`veth*`, `docker*`, `cni*`, `br-*`, `nomad`, `caravana0`) and never the loopback;
- the **block devices** that hold Nomad's data folder (`/opt/nomad/data`) and Docker's root (what
  `docker info` says; `/var/lib/docker` otherwise): one entry when they are the same disk. Loop, RAM and zram
  devices are skipped, and a filesystem that has no device in `/proc/diskstats` (btrfs, for one) gives no entry.

Caravana turns two reports into a **rate**: the counter's difference over the time between them. It does this when
the reports are 20 to 120 seconds apart; a counter that went down (a reboot, a driver that was reloaded) makes no
rate for that pair. Rates are averages over about half a minute, not instants. The agent keeps nothing between
runs and Caravana keeps only the previous report of each server, in memory: after Caravana restarts, the first
rates come with the second report.

## What the console shows

- **Operator › Servers › (server)**: a **Network and disk** section: network in and out of the default
  interface, the bytes on the mesh beside them, disk read, disk write and how busy the disk was (the busiest
  device's share of the time, 0 to 100 %), with the interface and device names. One device that holds both
  Nomad's data and Docker's root is named once.
- **Monitoring › This host**: two small charts over the page's range, **Network** (in solid, out dashed) and
  **Disk** (read solid, write dashed), with a table for screen readers like the other charts. A minute with no
  reading is a gap, never a zero.

When there are no numbers the section says why instead of showing zeros:

| Reason | What it means |
| --- | --- |
| `no_agent` | The node isn't a server Caravana manages (the dev lab's, or a Nomad node nobody added). |
| `agent_too_old` | The agent syncs but sends no `io` block: an older agent. Run the installer on it again. |
| `stale` | No report for 3 minutes. |
| `waiting` | The first rates aren't ready yet (Caravana just started, a counter reset, or two reports were too far apart). |

## The API

`GET /v1/host` (and `?server=<name>`) has an `io` object: `reason` (null when known), the default interface's
`network.rxBytesPerSec` and `txBytesPerSec` with each interface, and `disk.readBytesPerSec`,
`writeBytesPerSec` and `busyPercent` with each device. Every number is `null`, never 0, when it isn't known.
The mesh interface is listed under `network.interfaces` and is **not** added to the totals: its bytes leave on the
default interface again, so adding them would count them twice.

## What is kept

The sampler (once a minute) writes each server's fresh rates into its host row, so Monitoring can draw them over
the 1 hour, 6 hour, 24 hour and 7 day ranges. Rows are kept as the other host numbers are: minute rows for 48
hours, 10-minute rows (the median of the slot) for 8 days. Only the numbers are kept; the counters are not.

## Not here

Alerts or needs-you items for I/O, per-container I/O, IOPS and latency, and per-device history of a second
device (the totals are summed). To upgrade a server's agent, run the installer on it again.
