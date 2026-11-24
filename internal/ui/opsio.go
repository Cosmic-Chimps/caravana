package ui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Network and disk I/O on the server page (feature 055): the "Network and disk" section of
// /ops/servers/{name}. The numbers are rates between the last two syncs of the server's node agent
// (service.HostIO), so a number is an average over about half a minute. A server without an agent, or
// whose agent doesn't say, shows "—" and one sentence that says why.

// srvIO is the Network and disk section.
type srvIO struct {
	Rows []ioRow
	// Why says why the rows are "—"; empty when the rates are known.
	Why sentence
}

// ioRow is one row: the label, the value, and a note whose interface and device names are in mono.
type ioRow struct {
	Label string
	Value string
	None  bool
	Note  sentence
}

// bytesRateText is a rate of bytes per second written as the console writes sizes: "0 B/s", "340 KB/s",
// "1.2 MB/s".
func bytesRateText(bps int64) string { return humanBytes(bps) + "/s" }

// holdsWords says what a device holds, in a fixed order: "Nomad", "Docker" or "Nomad and Docker".
func holdsWords(holds []string) string {
	var words []string
	for _, h := range []struct{ key, word string }{{"nomad", "Nomad"}, {"docker", "Docker"}} {
		if slices.Contains(holds, h.key) {
			words = append(words, h.word)
		}
	}
	return strings.Join(words, " and ")
}

// ioWhy is the sentence that says why a server's I/O isn't known.
func ioWhy(now time.Time, io service.HostIO) sentence {
	switch io.Reason {
	case service.IONoAgent:
		return plainS("Network and disk throughput need Caravana's agent on the server. This one isn't a server Caravana manages.")
	case service.IOAgentTooOld:
		return plainS("The agent on this server is older and doesn't report network and disk throughput. Run the installer on it again to update it.")
	case service.IOStale:
		if io.Since != nil {
			mins := max(1, int(now.Sub(*io.Since)/time.Minute))
			return plainS("No report from this server's agent for " + plural(mins, "minute") + ", so there are no current numbers.")
		}
		return plainS("No recent report from this server's agent, so there are no current numbers.")
	}
	return plainS("Waiting for the agent's next report. Throughput is measured between two of them, 30 seconds apart.")
}

// ioSectionOf builds the section from what Host says about the server's I/O at now.
func ioSectionOf(now time.Time, io service.HostIO) *srvIO {
	out := &srvIO{}
	if !io.Known() {
		for _, label := range []string{"Network in", "Network out", "Disk read", "Disk write", "Disk busy"} {
			out.Rows = append(out.Rows, ioRow{Label: label, Value: "—", None: true})
		}
		out.Why = ioWhy(now, io)
		return out
	}

	var def, mesh *service.IONetRate
	for i := range io.Net {
		switch io.Net[i].Kind {
		case "default":
			def = &io.Net[i]
		case "mesh":
			mesh = &io.Net[i]
		}
	}
	netNote := sentence{}
	if def != nil {
		netNote = sentence{mono(def.Name)}
	}
	netRow := func(label string, v *int64) ioRow {
		if v == nil {
			return ioRow{Label: label, Value: "—", None: true, Note: plainS("no reading of the default interface yet")}
		}
		return ioRow{Label: label, Value: bytesRateText(*v), Note: netNote}
	}
	out.Rows = append(out.Rows, netRow("Network in", io.NetRxBps), netRow("Network out", io.NetTxBps))
	if mesh != nil {
		out.Rows = append(out.Rows, ioRow{Label: "Between servers", Value: "in " + bytesRateText(mesh.RxBps) + " · out " + bytesRateText(mesh.TxBps),
			Note: join(sentence{mono(mesh.Name)}, plainS(" · carried by the network above"))})
	}

	diskNote := sentence{}
	switch len(io.Disks) {
	case 0:
	case 1:
		diskNote = sentence{mono(io.Disks[0].Name)}
		if w := holdsWords(io.Disks[0].Holds); w != "" {
			diskNote = join(diskNote, plainS(" · "+w))
		}
	default:
		for i, d := range io.Disks {
			if i > 0 {
				diskNote = append(diskNote, plain(" · "))
			}
			diskNote = append(diskNote, mono(d.Name))
		}
	}
	diskRow := func(label string, v *int64, f func(int64) string) ioRow {
		if v == nil {
			return ioRow{Label: label, Value: "—", None: true, Note: plainS("no device of Nomad's data or Docker's root found")}
		}
		return ioRow{Label: label, Value: f(*v), Note: diskNote}
	}
	percent := func(v int64) string { return strconv.FormatInt(v, 10) + " %" }
	out.Rows = append(out.Rows, diskRow("Disk read", io.DiskReadBps, bytesRateText), diskRow("Disk write", io.DiskWriteBps, bytesRateText),
		diskRow("Disk busy", io.DiskBusyPercent, percent))
	if len(io.Disks) > 1 {
		for _, d := range io.Disks {
			out.Rows = append(out.Rows, ioRow{Label: "Disk",
				Value: "read " + bytesRateText(d.ReadBps) + " · write " + bytesRateText(d.WriteBps) + " · busy " + percent(d.BusyPercent),
				Note:  join(sentence{mono(d.Name)}, plainS(" · "+holdsWords(d.Holds)))})
		}
	}
	return out
}
