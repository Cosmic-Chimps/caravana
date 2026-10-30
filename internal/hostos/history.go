// Package hostos reads what the host's operating system says about itself
// from plain files: apt's history of package runs, the reboot-required marker
// and the installed kernels (feature 027). It is pure: it reads through an
// fs.FS, so tests give it a fake root and the lab a folder of fixtures, and
// only real.go touches the real file system.
package hostos

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"
)

// The files of apt's history, relative to the root (no leading slash, as fs.FS
// wants). Debian and Ubuntu rotate the log monthly: the one before the current
// is history.log.1.gz.
const (
	historyFile    = "var/log/apt/history.log"
	historyRotated = "var/log/apt/history.log.1.gz"
)

// Limits on what one read takes in, so a runaway file can't use the host's
// memory: a month of history is a few hundred kilobytes.
const (
	maxHistoryBytes = 32 << 20
	// maxHistoryLine is the longest line (an Upgrade: line lists every package of
	// the run, with two versions each).
	maxHistoryLine = 4 << 20
)

// Package is one entry of an Upgrade:, Install: or Remove: line: its name, its
// architecture and its versions. Old is empty for an install, New for a removal.
type Package struct {
	Name, Arch string
	Old, New   string
}

// Run is one block of apt's history: one apt command that changed packages.
type Run struct {
	// Start and End are the block's Start-Date and End-Date. apt writes them in
	// the host's local time without a zone; they are read as UTC, which is what a
	// server set up by Caravana's installer runs in. End is zero for a block that
	// has no End-Date (apt was killed).
	Start, End time.Time
	// Command is the Commandline: line as written.
	Command string
	// Automatic says unattended-upgrades made the run; false means someone ran apt.
	Automatic bool
	// Upgraded, Installed and Removed are the packages the run changed. Removed
	// has the Remove: and Purge: lines.
	Upgraded, Installed, Removed []Package
}

// Packages is how many packages the run upgraded or installed. A run that only
// removed packages (an autoremove) updated none.
func (r Run) Packages() int { return len(r.Upgraded) + len(r.Installed) }

// unattendedCommand is the Commandline of the automatic runs.
const unattendedCommand = "/usr/bin/unattended-upgrade"

// ReadHistory reads apt's history from the root and returns up to n runs,
// newest first. It reads the current log and, when that has fewer than n runs,
// the previous month's rotated one. A file that doesn't exist is skipped; one
// that can't be read or understood is skipped too and the error comes back with
// the runs that were read, so a caller may use both.
func ReadHistory(fsys fs.FS, n int) ([]Run, error) {
	if n < 1 {
		return nil, nil
	}
	var runs []Run
	var firstErr error
	for _, name := range []string{historyFile, historyRotated} {
		got, err := readHistoryFile(fsys, name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
			firstErr = fmt.Errorf("read apt history %q: %w", name, err)
		}
		runs = append(runs, got...)
		if len(runs) >= n {
			break
		}
	}
	// Newest first. Stable, so the blocks of one file keep their order for
	// equal start times.
	slices.SortStableFunc(runs, func(a, b Run) int { return b.Start.Compare(a.Start) })
	if len(runs) > n {
		runs = runs[:n]
	}
	return runs, firstErr
}

// readHistoryFile parses one history file, gunzipped when its name says so.
func readHistoryFile(fsys fs.FS, name string) ([]Run, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(name, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	}
	return parseHistory(io.LimitReader(r, maxHistoryBytes))
}

// parseHistory reads blocks of "Key: value" lines separated by blank lines. A
// block without a usable Start-Date is dropped; an unknown key is ignored (apt
// adds some, like Requested-By and Error).
func parseHistory(r io.Reader) ([]Run, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxHistoryLine)
	var runs []Run
	var cur Run
	have := false
	flush := func() {
		if have && !cur.Start.IsZero() {
			runs = append(runs, cur)
		}
		cur, have = Run{}, false
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r ")
		if line == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch key {
		case "Start-Date":
			flush() // a block that forgot its blank line
			cur.Start, have = parseAptTime(value), true
		case "End-Date":
			cur.End = parseAptTime(value)
		case "Commandline":
			cur.Command = value
			words := strings.Fields(value)
			cur.Automatic = len(words) > 0 && words[0] == unattendedCommand
		case "Upgrade":
			cur.Upgraded = append(cur.Upgraded, parsePackages(value, kindUpgrade)...)
		case "Install":
			cur.Installed = append(cur.Installed, parsePackages(value, kindInstall)...)
		case "Remove", "Purge":
			cur.Removed = append(cur.Removed, parsePackages(value, kindRemove)...)
		}
	}
	if err := sc.Err(); err != nil {
		return runs, err
	}
	flush()
	return runs, nil
}

// parseAptTime reads "2026-10-24  06:06:23" (two spaces) as UTC; zero when it
// isn't one.
func parseAptTime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", strings.Join(strings.Fields(s), " "))
	if err != nil {
		return time.Time{}
	}
	return t
}

// The three kinds of package line, which put their versions in different
// places.
type lineKind int

const (
	kindUpgrade lineKind = iota // (old, new)
	kindInstall                 // (version) or (version, automatic)
	kindRemove                  // (version)
)

// parsePackages reads "name:arch (old, new), name:arch (old, new), ...". The
// versions have a comma inside the parentheses, so the list is walked entry by
// entry, not split on commas. A damaged tail is dropped; what came before it is
// kept.
func parsePackages(s string, kind lineKind) []Package {
	var out []Package
	for s = strings.TrimSpace(s); s != ""; {
		open := strings.Index(s, " (")
		if open < 0 {
			break
		}
		rest := s[open+2:]
		end := strings.Index(rest, ")")
		if end < 0 {
			break
		}
		out = append(out, packageOf(s[:open], rest[:end], kind))
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest[end+1:]), ","))
	}
	return out
}

// packageOf builds a Package from "name:arch" and the text inside its
// parentheses.
func packageOf(nameArch, versions string, kind lineKind) Package {
	p := Package{Name: nameArch}
	if name, arch, ok := strings.Cut(nameArch, ":"); ok {
		p.Name, p.Arch = name, arch
	}
	first, second, two := strings.Cut(versions, ", ")
	first, second = strings.TrimSpace(first), strings.TrimSpace(second)
	switch kind {
	case kindUpgrade:
		p.Old = first
		if two {
			p.New = second
		}
	case kindInstall:
		p.New = first
	case kindRemove:
		p.Old = first
	}
	return p
}
