package hostos

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// Counts for a host without Ubuntu's updates-available file (Debian has none):
// they come from `apt list --upgradable`, which real.go runs and which is read
// here as plain text. Nothing in this file runs a program.

// ErrAptUnavailable says there is no apt command on the host.
var ErrAptUnavailable = errors.New("the apt command isn't on this host")

const (
	// aptStampFile is touched by apt's own periodic job after every successful
	// `apt update`.
	aptStampFile = "var/lib/apt/periodic/update-success-stamp"
	// aptListsDir is where `apt update` writes the package lists; its
	// modification time moves when a list file is added or removed.
	aptListsDir = "var/lib/apt/lists"
	// dpkgStatusFile changes whenever a package is installed, upgraded or removed.
	dpkgStatusFile = "var/lib/dpkg/status"

	// maxUpgradableLines is how many lines of apt's answer are read: a host with
	// more waiting packages than this is far from fine either way.
	maxUpgradableLines = 10000
)

// ParseUpgradable reads the standard output of `apt list --upgradable` run with
// LC_ALL=C. Each package is a line such as
//
//	curl/bookworm-security 7.88.1-10+deb12u8 amd64 [upgradable from: 7.88.1-10+deb12u5]
//
// and the suites between the name and the first space, separated by commas, say
// where the new version comes from: a suite ending in "-security" is a security
// update. Blank lines, "Listing...", and apt's "WARNING:" and "N:" notes are not
// packages. The result is Known even with no package line: apt answered, and
// nothing is waiting. CheckedAt is left for the caller (see ListsRefreshedAt).
func ParseUpgradable(out []byte) Updates {
	u := Updates{Known: true}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for i := 0; i < maxUpgradableLines && sc.Scan(); i++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "Listing") || strings.HasPrefix(line, "WARNING:") || isAptNote(line) {
			continue
		}
		nameSuites, _, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		_, suites, found := strings.Cut(nameSuites, "/")
		if !found || suites == "" {
			continue
		}
		u.Total++
		for suite := range strings.SplitSeq(suites, ",") {
			if strings.HasSuffix(suite, "-security") {
				u.Security++
				break
			}
		}
	}
	return u
}

// isAptNote says the line is apt's "N: ..." note ("N: Unable to locate...").
func isAptNote(line string) bool {
	return strings.HasPrefix(line, "N:") || strings.HasPrefix(line, "E:")
}

// ListsRefreshedAt is when the host last refreshed its package lists: the time
// of apt's update-success stamp, else of the lists directory, else zero.
func ListsRefreshedAt(fsys fs.FS) time.Time {
	for _, name := range []string{aptStampFile, aptListsDir} {
		if info, err := fs.Stat(fsys, name); err == nil {
			return info.ModTime().UTC()
		}
	}
	return time.Time{}
}

// AptStamp is a key made from the times of the update stamp, the lists
// directory and dpkg's status file. When it changes, the list of upgradable
// packages may have changed, so it is worth running apt again. A file that can't
// be read counts as "none".
func AptStamp(fsys fs.FS) string {
	parts := make([]string, 0, 3)
	for _, name := range []string{aptStampFile, aptListsDir, dpkgStatusFile} {
		if info, err := fs.Stat(fsys, name); err == nil {
			parts = append(parts, strconv.FormatInt(info.ModTime().UnixNano(), 10))
		} else {
			parts = append(parts, "none")
		}
	}
	return strings.Join(parts, "|")
}
