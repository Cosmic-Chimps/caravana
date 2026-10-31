package hostos

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"
)

// The folders under testdata are host roots a reviewer can copy onto a host
// (or give CARAVANA_HOST_ROOT) to stage a state; here each is read as it is.
func TestReadStateFixtures(t *testing.T) {
	tests := []struct {
		dir  string
		want func(State) string // "" when right
	}{
		{"ubuntu-kernel-reboot", func(s State) string {
			switch {
			case !s.Readable || s.Distro != Distro{"ubuntu", "24.04"}:
				return "distro"
			case !s.Reboot.Required || !reflect.DeepEqual(s.Reboot.Packages, []string{"linux-image-6.8.0-149-generic"}):
				return "reboot"
			case s.Reboot.InstalledKernel != "6.8.0-149-generic":
				return "installed kernel"
			case !s.Updates.Known || s.Updates.Total != 14 || s.Updates.Security != 9 || s.Updates.CheckedAt.IsZero():
				return "updates"
			case s.Unattended.Enabled == nil || !*s.Unattended.Enabled || s.Unattended.AutoReboot == nil || *s.Unattended.AutoReboot || s.Unattended.AutoRebootTime != "":
				return "unattended"
			}
			return ""
		}},
		{"ubuntu-libc-reboot", func(s State) string {
			switch {
			case !s.Reboot.Required || !reflect.DeepEqual(s.Reboot.Packages, []string{"libc6"}):
				return "reboot"
			case s.Reboot.InstalledKernel != "6.8.0-147-generic":
				return "installed kernel " + s.Reboot.InstalledKernel
			case !s.Updates.Known || s.Updates.Total != 3 || s.Updates.Security != 0:
				return "updates: a missing security line is 0"
			}
			return ""
		}},
		{"ubuntu-security-unattended-on", func(s State) string {
			switch {
			case s.Reboot.Required:
				return "reboot"
			case s.Updates.Total != 14 || s.Updates.Security != 9:
				return "updates"
			case s.Unattended.Enabled == nil || !*s.Unattended.Enabled:
				return "unattended should be on"
			}
			return ""
		}},
		{"ubuntu-security-unattended-off", func(s State) string {
			if s.Unattended.Enabled == nil || *s.Unattended.Enabled {
				return "unattended should be off"
			}
			return ""
		}},
		{"ubuntu-auto-reboot-0200", func(s State) string {
			switch {
			case s.Unattended.AutoReboot == nil || !*s.Unattended.AutoReboot || s.Unattended.AutoRebootTime != "02:00":
				return "auto reboot at 02:00 (a // comment after the value, and a block comment elsewhere)"
			case !s.Updates.Known || s.Updates.Total != 0 || s.Updates.Security != 0:
				return "zero updates are known, not unknown"
			}
			return ""
		}},
		{"ubuntu-clean", func(s State) string {
			if !s.Updates.Known || s.Updates.Total != 0 || s.Reboot.Required {
				return "a clean host"
			}
			return ""
		}},
		{"debian-host", func(s State) string {
			switch {
			case !s.Readable || s.Distro != Distro{"debian", "12"}:
				return "distro"
			case s.Updates.Known:
				return "Debian has no updates-available: not known"
			case s.Unattended.Enabled == nil || !*s.Unattended.Enabled || s.Unattended.AutoReboot != nil:
				return "unattended: 20auto-upgrades alone says on, the other file is missing"
			}
			return ""
		}},
		{"non-debian-host", func(s State) string {
			switch {
			case s.Readable || s.Distro.ID != "fedora":
				return "a Fedora host is not readable"
			case s.Updates.Known || s.Unattended.Enabled != nil:
				return "nothing else is read"
			}
			return ""
		}},
	}
	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			got := ReadState(os.DirFS(filepath.Join("testdata", tc.dir)))
			if msg := tc.want(got); msg != "" {
				t.Errorf("%s: %+v", msg, got)
			}
		})
	}
}

func TestReadStateEmptyHost(t *testing.T) {
	got := ReadState(fstest.MapFS{})
	if got.Readable || got.Updates.Known || got.Unattended.Enabled != nil || got.Reboot.Required {
		t.Errorf("an empty root = %+v, want nothing known", got)
	}
}

func TestReadDistro(t *testing.T) {
	tests := []struct {
		name, data string
		want       Distro
		ok         bool
	}{
		{"ubuntu", "ID=ubuntu\nVERSION_ID=\"24.04\"\nID_LIKE=debian\n", Distro{"ubuntu", "24.04"}, true},
		{"a derivative", "ID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22.04\"\n", Distro{"pop", "22.04"}, true},
		{"debian, no like", "ID=debian\nVERSION_ID=\"12\"", Distro{"debian", "12"}, true},
		{"upper case and single quotes", "ID='Ubuntu'\nVERSION_ID='20.04'\n", Distro{"ubuntu", "20.04"}, true},
		{"fedora", "ID=fedora\nVERSION_ID=40\n", Distro{"fedora", "40"}, false},
		{"alpine is not like debian", "ID=alpine\nID_LIKE=\n", Distro{"alpine", ""}, false},
		{"junk", "this is not a file\n\x00\x01", Distro{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := readDistro(fstest.MapFS{osReleaseFile: {Data: []byte(tc.data)}})
			if got != tc.want || ok != tc.ok {
				t.Errorf("readDistro = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := readDistro(fstest.MapFS{}); ok {
		t.Error("a missing os-release is readable")
	}
}

func TestReadUpdates(t *testing.T) {
	checked := time.Date(2026, 10, 30, 6, 5, 0, 0, time.UTC)
	tests := []struct {
		name, data string
		want       Updates
	}{
		{"both lines", "\n14 updates can be applied immediately.\n9 of these updates are standard security updates.\nTo see these additional updates run: apt list --upgradable\n",
			Updates{Known: true, Total: 14, Security: 9, CheckedAt: checked}},
		{"no security line", "\n3 updates can be applied immediately.\n", Updates{Known: true, Total: 3, CheckedAt: checked}},
		{"zero", "0 updates can be applied immediately.\n", Updates{Known: true, CheckedAt: checked}},
		{"one of each", "1 update can be applied immediately.\n1 of these updates is a standard security update.\n",
			Updates{Known: true, Total: 1, Security: 1, CheckedAt: checked}},
		{"the ESM line is not a security count", "5 updates can be applied immediately.\n2 of these updates are standard security updates.\n7 additional security updates can be applied with ESM Apps.\n",
			Updates{Known: true, Total: 5, Security: 2, CheckedAt: checked}},
		{"the older wording", "14 packages can be updated.\n9 updates are security updates.\n", Updates{Known: true, Total: 14, Security: 9, CheckedAt: checked}},
		{"windows line ends and spaces", "  14 updates can be applied immediately.\r\n  9 of these updates are standard security updates.\r\n",
			Updates{Known: true, Total: 14, Security: 9, CheckedAt: checked}},
		{"security larger than the total lifts the total", "2 updates can be applied immediately.\n5 of these updates are standard security updates.\n",
			Updates{Known: true, Total: 5, Security: 5, CheckedAt: checked}},
		{"a number too long for a count is 0", "99999999999999999999 updates can be applied immediately.\n", Updates{Known: true, CheckedAt: checked}},
		{"neither line", "Expanded Security Maintenance is not enabled.\n", Updates{}},
		{"empty", "", Updates{}},
		{"binary", "\x00\x01\x02not text", Updates{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := readUpdates(fstest.MapFS{updatesFile: {Data: []byte(tc.data), ModTime: checked}})
			if got != tc.want {
				t.Errorf("readUpdates = %+v, want %+v", got, tc.want)
			}
		})
	}
	if got := readUpdates(fstest.MapFS{}); got != (Updates{}) {
		t.Errorf("a missing file = %+v", got)
	}
}

func TestUpdatesStale(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		age  time.Duration
		want bool
	}{{time.Hour, false}, {48 * time.Hour, false}, {48*time.Hour + time.Second, true}, {72 * time.Hour, true}} {
		if got := (Updates{Known: true, CheckedAt: now.Add(-c.age)}).Stale(now); got != c.want {
			t.Errorf("a list %s old: stale = %v, want %v", c.age, got, c.want)
		}
	}
}

func TestReadUnattended(t *testing.T) {
	bp := func(b bool) *bool { return &b }
	file := func(data string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(data)} }
	tests := []struct {
		name string
		fs   fstest.MapFS
		want Unattended
	}{
		{"no files", fstest.MapFS{}, Unattended{}},
		{"on", fstest.MapFS{autoUpgradesFile: file("APT::Periodic::Unattended-Upgrade \"1\";\n")}, Unattended{Enabled: bp(true)}},
		{"off", fstest.MapFS{autoUpgradesFile: file("APT::Periodic::Unattended-Upgrade \"0\";\n")}, Unattended{Enabled: bp(false)}},
		{"days count as on", fstest.MapFS{autoUpgradesFile: file("APT::Periodic::Unattended-Upgrade \"7\";\n")}, Unattended{Enabled: bp(true)}},
		{"the file without the option is off", fstest.MapFS{autoUpgradesFile: file("APT::Periodic::Update-Package-Lists \"1\";\n")}, Unattended{Enabled: bp(false)}},
		{"commented out is off", fstest.MapFS{autoUpgradesFile: file("//APT::Periodic::Unattended-Upgrade \"1\";\n# APT::Periodic::Unattended-Upgrade \"1\";\n")}, Unattended{Enabled: bp(false)}},
		{"the last statement wins", fstest.MapFS{autoUpgradesFile: file("APT::Periodic::Unattended-Upgrade \"1\";\nAPT::Periodic::Unattended-Upgrade \"0\";\n")}, Unattended{Enabled: bp(false)}},
		{"a block comment hides it", fstest.MapFS{autoUpgradesFile: file("/* APT::Periodic::Unattended-Upgrade \"1\";\n more */\n")}, Unattended{Enabled: bp(false)}},
		{"reboot at 02:00", fstest.MapFS{unattendedConfFile: file("Unattended-Upgrade::Automatic-Reboot \"true\";\nUnattended-Upgrade::Automatic-Reboot-Time \"02:00\";\n")},
			Unattended{AutoReboot: bp(true), AutoRebootTime: "02:00"}},
		{"reboot at once", fstest.MapFS{unattendedConfFile: file("Unattended-Upgrade::Automatic-Reboot \"true\";\n")}, Unattended{AutoReboot: bp(true)}},
		{"reboot now", fstest.MapFS{unattendedConfFile: file("Unattended-Upgrade::Automatic-Reboot \"true\";\nUnattended-Upgrade::Automatic-Reboot-Time \"now\";\n")},
			Unattended{AutoReboot: bp(true), AutoRebootTime: "now"}},
		{"a time without a reboot is not shown", fstest.MapFS{unattendedConfFile: file("Unattended-Upgrade::Automatic-Reboot \"false\";\nUnattended-Upgrade::Automatic-Reboot-Time \"02:00\";\n")},
			Unattended{AutoReboot: bp(false)}},
		{"a time that isn't one is dropped", fstest.MapFS{unattendedConfFile: file("Unattended-Upgrade::Automatic-Reboot \"true\";\nUnattended-Upgrade::Automatic-Reboot-Time \"tonight\";\n")},
			Unattended{AutoReboot: bp(true)}},
		{"the default file: everything commented", fstest.MapFS{unattendedConfFile: file("//Unattended-Upgrade::Automatic-Reboot \"true\";\n//Unattended-Upgrade::Automatic-Reboot-Time \"02:00\";\n")},
			Unattended{AutoReboot: bp(false)}},
		{"a // inside quotes is not a comment", fstest.MapFS{unattendedConfFile: file("Unattended-Upgrade::Mail \"a//b\"; Unattended-Upgrade::Automatic-Reboot \"yes\";\n")},
			Unattended{AutoReboot: bp(true)}},
		{"each file answers for its own", fstest.MapFS{autoUpgradesFile: file("APT::Periodic::Unattended-Upgrade \"1\";\n")}, Unattended{Enabled: bp(true)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := readUnattended(tc.fs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v (enabled %v, reboot %v)\nwant %+v", got, deref(got.Enabled), deref(got.AutoReboot), tc.want)
			}
		})
	}
}

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func TestReadStateRefusesAHugeFile(t *testing.T) {
	huge := make([]byte, maxStateFileBytes+1)
	got := ReadState(fstest.MapFS{
		osReleaseFile: {Data: []byte("ID=ubuntu\n")},
		updatesFile:   {Data: huge},
	})
	if !got.Readable || got.Updates.Known {
		t.Errorf("a file past the limit was read: %+v", got.Updates)
	}
}
