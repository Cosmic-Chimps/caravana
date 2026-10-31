package hostos

import (
	"bufio"
	"bytes"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What the host's files say about its operating system (feature 027): whether
// a reboot is pending, how many updates wait and how many of them are security
// updates, whether the host installs updates and reboots by itself, and which
// distribution it is. Every one of them is a file the host's own tools write
// and anyone may read; nothing here runs a program, and a file that is missing
// or odd says "not known", never an error.

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

// The files ReadState reads besides the reboot marker.
const (
	osReleaseFile      = "etc/os-release"
	updatesFile        = "var/lib/update-notifier/updates-available"
	autoUpgradesFile   = "etc/apt/apt.conf.d/20auto-upgrades"
	unattendedConfFile = "etc/apt/apt.conf.d/50unattended-upgrades"

	// The apt options read, lower-cased as readAptConf keys them.
	optPeriodicUnattended = "apt::periodic::unattended-upgrade"
	optAutoReboot         = "unattended-upgrade::automatic-reboot"
	optAutoRebootTime     = "unattended-upgrade::automatic-reboot-time"

	maxStateFileBytes   = 1 << 20 // none of these files is near a megabyte
	maxUpdatesFileLines = 100
)

// UpdatesStaleAfter is how old Ubuntu's list of waiting updates may be before it
// is no longer a fact to judge by: the list is as fresh as the host's last
// `apt update`, which a host that updates itself runs daily.
const UpdatesStaleAfter = 48 * time.Hour

// Distro is the distribution, from /etc/os-release.
type Distro struct {
	ID      string // "ubuntu", "debian"
	Version string // "24.04"
}

// Updates is what Ubuntu's motd file says about waiting updates.
type Updates struct {
	// Known is false when the file is missing or says neither number.
	Known bool
	// Total is how many updates can be applied, Security how many of those are
	// standard security updates (0 when the file has no such line).
	Total, Security int
	// CheckedAt is the file's modification time: when the host last refreshed
	// its list of updates.
	CheckedAt time.Time
}

// Stale says the list is older than UpdatesStaleAfter at now.
func (u Updates) Stale(now time.Time) bool { return now.Sub(u.CheckedAt) > UpdatesStaleAfter }

// Unattended is how the host updates itself, from apt's configuration. A nil
// field means the file that holds it couldn't be read.
type Unattended struct {
	// Enabled is whether unattended-upgrades is switched on
	// (APT::Periodic::Unattended-Upgrade).
	Enabled *bool
	// AutoReboot is whether it reboots by itself after an update that needs
	// one; AutoRebootTime is when ("02:00" or "now"), only when AutoReboot is
	// true and the file says so.
	AutoReboot     *bool
	AutoRebootTime string
}

// State is everything the host's files say about its operating system.
type State struct {
	// Readable is true when the host is Debian or Ubuntu, the two families whose
	// files these are. On a host that is not, only Distro and Reboot are filled.
	Readable   bool
	Distro     Distro
	Reboot     Reboot
	Updates    Updates
	Unattended Unattended
}

// ReadState reads the host's operating system files through fsys. It never
// fails: a file that is missing leaves its part of the state as "not known".
func ReadState(fsys fs.FS) State {
	var st State
	st.Distro, st.Readable = readDistro(fsys)
	st.Reboot = ReadReboot(fsys)
	if !st.Readable {
		return st
	}
	st.Updates = readUpdates(fsys)
	st.Unattended = readUnattended(fsys)
	return st
}

// readDistro reads /etc/os-release. ok is true for Debian, Ubuntu and what says
// it is like one of them (ID_LIKE), which covers Ubuntu's derivatives.
func readDistro(fsys fs.FS) (d Distro, ok bool) {
	b, err := readSmall(fsys, osReleaseFile)
	if err != nil {
		return Distro{}, false
	}
	var like string
	for _, line := range strings.Split(string(b), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "ID":
			d.ID = strings.ToLower(value)
		case "VERSION_ID":
			d.Version = value
		case "ID_LIKE":
			like = strings.ToLower(value)
		}
	}
	family := append(strings.Fields(like), d.ID)
	return d, slices.Contains(family, "debian") || slices.Contains(family, "ubuntu")
}

// The lines of Ubuntu's updates-available file. Two wordings exist: the one of
// current releases ("14 updates can be applied immediately." and "9 of these
// updates are standard security updates.") and the older one ("14 packages can
// be updated." and "9 updates are security updates."). The line about
// "additional security updates ... ESM" starts with a number too but matches
// neither.
var (
	updatesTotalLine = regexp.MustCompile(`(?i)^(\d+) (?:updates? can be (?:applied|installed) immediately|packages? can be updated)\.?`)
	updatesSecLine   = regexp.MustCompile(`(?i)^(\d+) (?:of these updates? (?:is|are) (?:a )?standard security updates?|updates? (?:is|are) security updates?)\.?`)
)

// readUpdates reads the counts and the file's age. A security line that isn't
// there is 0 (apt leaves it out when there are none); a file with neither line
// is "not known".
func readUpdates(fsys fs.FS) Updates {
	b, err := readSmall(fsys, updatesFile)
	if err != nil {
		return Updates{}
	}
	var u Updates
	var haveTotal, haveSecurity bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for i := 0; sc.Scan() && i < maxUpdatesFileLines; i++ {
		line := strings.TrimSpace(sc.Text())
		if m := updatesTotalLine.FindStringSubmatch(line); m != nil && !haveTotal {
			u.Total, haveTotal = atoiOr0(m[1]), true
		} else if m := updatesSecLine.FindStringSubmatch(line); m != nil && !haveSecurity {
			u.Security, haveSecurity = atoiOr0(m[1]), true
		}
	}
	if !haveTotal && !haveSecurity {
		return Updates{}
	}
	u.Total = max(u.Total, u.Security)
	u.Known = true
	if info, err := fs.Stat(fsys, updatesFile); err == nil {
		u.CheckedAt = info.ModTime().UTC()
	}
	return u
}

// atoiOr0 reads a run of digits, with 0 for one too long to be a count.
func atoiOr0(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// readUnattended reads whether unattended-upgrades is on, and whether and when
// it reboots. Each file answers for its own fields: a host with no
// 20auto-upgrades still shows its reboot setting.
func readUnattended(fsys fs.FS) Unattended {
	var u Unattended
	if conf, ok := readAptConf(fsys, autoUpgradesFile); ok {
		u.Enabled = new(truthy(conf[optPeriodicUnattended]))
	}
	if conf, ok := readAptConf(fsys, unattendedConfFile); ok {
		u.AutoReboot = new(truthy(conf[optAutoReboot]))
		if *u.AutoReboot {
			if at := conf[optAutoRebootTime]; at == "now" || rebootTime.MatchString(at) {
				u.AutoRebootTime = at
			}
		}
	}
	return u
}

var rebootTime = regexp.MustCompile(`^\d{1,2}:\d{2}$`)

// truthy reads an apt option as on: true, yes or on, or a number above zero
// (the periodic options count days).
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on":
		return true
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return err == nil && n > 0
}

var (
	aptBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	aptStatement    = regexp.MustCompile(`([A-Za-z0-9_.:+/-]+)\s+"([^"]*)"\s*;`)
)

// readAptConf reads the simple `Name "value";` statements of an apt.conf.d file
// into a map by lower-cased name, the last one winning, as apt reads them.
// Comments (//, # and /* */) are dropped first, so a commented-out line counts
// for nothing. ok is false when the file can't be read.
func readAptConf(fsys fs.FS, name string) (conf map[string]string, ok bool) {
	b, err := readSmall(fsys, name)
	if err != nil {
		return nil, false
	}
	text := aptBlockComment.ReplaceAllString(string(b), "")
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if line = stripLineComment(line); strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	conf = map[string]string{}
	for _, m := range aptStatement.FindAllStringSubmatch(strings.Join(kept, "\n"), -1) {
		conf[strings.ToLower(m[1])] = m[2]
	}
	return conf, true
}

// stripLineComment cuts a // comment, or a whole # line, off a line, leaving a
// "//" inside quotes alone.
func stripLineComment(line string) string {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return ""
	}
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '"':
			inQuote = !inQuote
		case !inQuote && line[i] == '/' && i+1 < len(line) && line[i+1] == '/':
			return line[:i]
		}
	}
	return line
}

// readSmall reads a small file, refusing a directory or one past
// maxStateFileBytes.
func readSmall(fsys fs.FS, name string) ([]byte, error) {
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return nil, err
	}
	if info.IsDir() || info.Size() > maxStateFileBytes {
		return nil, fs.ErrInvalid
	}
	return fs.ReadFile(fsys, name)
}
