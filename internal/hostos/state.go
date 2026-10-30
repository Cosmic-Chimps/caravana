package hostos

import (
	"bufio"
	"bytes"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// What the host's files say about its operating system. Page 3 of feature 027
// grows this file with the rest of ReadState (updates waiting, unattended
// upgrades, the distribution); the reboot part comes first because page 2's
// "reboot needed" pill needs it.

// The reboot marker and its package list: Ubuntu's notify-reboot-required and
// unattended-upgrades write them, and a boot clears them (/run is a tmpfs).
const (
	rebootFile     = "run/reboot-required"
	rebootPkgsFile = "run/reboot-required.pkgs"
	bootDir        = "boot"
)

// historyScan is how many of the newest apt runs are searched for a kernel.
const historyScan = 10

// Reboot says whether the host needs a reboot and why.
type Reboot struct {
	// Required is true while the marker file exists.
	Required bool
	// Since is when the marker was written (its modification time); zero when not
	// required or not known.
	Since time.Time
	// Packages are the packages that asked for the reboot, one per line of the
	// marker's list, in the file's order. Empty when the list is missing.
	Packages []string
	// InstalledKernel is the newest kernel release installed on the host
	// ("6.8.0-149-generic"), read whether or not a reboot is pending: the highest
	// linux-image version in the newest apt runs, else the highest
	// /boot/vmlinuz-*. Empty when neither says. The running kernel is the one
	// Nomad reports, so a page can say "6.8.0-149 is installed, but 6.8.0-147 is
	// still running".
	InstalledKernel string
}

// ReadReboot reads the reboot marker, its list and the installed kernel. A
// missing file means "no" and "not known", never an error: a host that isn't
// Debian or Ubuntu simply has none of them.
func ReadReboot(fsys fs.FS) Reboot {
	var r Reboot
	if info, err := fs.Stat(fsys, rebootFile); err == nil && !info.IsDir() {
		r.Required = true
		r.Since = info.ModTime().UTC()
	}
	if r.Required {
		r.Packages = readLines(fsys, rebootPkgsFile)
	}
	r.InstalledKernel = installedKernel(fsys)
	return r
}

// ListedIn says whether the run installed or upgraded a package the marker's
// list names: the run that made a reboot necessary.
func (r Reboot) ListedIn(run Run) bool {
	if len(r.Packages) == 0 {
		return false
	}
	for _, list := range [][]Package{run.Upgraded, run.Installed} {
		for _, p := range list {
			if slices.Contains(r.Packages, p.Name) {
				return true
			}
		}
	}
	return false
}

// readLines reads a small text file as its non-empty lines; nil when it can't
// be read.
func readLines(fsys fs.FS, name string) []string {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// installedKernel is the newest installed kernel release. apt's history is read
// first because it is what the last update did; the boot directory is the
// fallback for a host whose history has rotated away or never had a kernel.
func installedKernel(fsys fs.FS) string {
	runs, _ := ReadHistory(fsys, historyScan) // what was read is used, an error or not
	for _, run := range runs {
		best := ""
		for _, list := range [][]Package{run.Installed, run.Upgraded} {
			for _, p := range list {
				if rel, ok := kernelRelease(p.Name); ok && (best == "" || KernelNewer(rel, best)) {
					best = rel
				}
			}
		}
		if best != "" {
			return best
		}
	}
	entries, err := fs.ReadDir(fsys, bootDir)
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range entries {
		rel, ok := strings.CutPrefix(path.Base(e.Name()), "vmlinuz-")
		if ok && startsWithDigit(rel) && (best == "" || KernelNewer(rel, best)) {
			best = rel
		}
	}
	return best
}

// kernelRelease reads a kernel's release from the name of its image package:
// "linux-image-6.8.0-149-generic" is "6.8.0-149-generic". The metapackages
// ("linux-image-virtual") have no digit after the prefix and are not releases.
func kernelRelease(pkg string) (string, bool) {
	rel, ok := strings.CutPrefix(pkg, "linux-image-")
	if !ok {
		return "", false
	}
	rel = strings.TrimPrefix(rel, "unsigned-")
	if !startsWithDigit(rel) || strings.HasSuffix(rel, "-dbgsym") {
		return "", false
	}
	return rel, true
}

func startsWithDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }
