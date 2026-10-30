package hostos

import (
	"os"
	"reflect"
	"testing"
	"testing/fstest"
	"time"
)

func TestReadRebootFixtures(t *testing.T) {
	got := ReadReboot(os.DirFS("testdata/host"))
	if !got.Required || got.Since.IsZero() {
		t.Errorf("required = %v since %v", got.Required, got.Since)
	}
	if !reflect.DeepEqual(got.Packages, []string{"linux-image-6.8.0-149-generic"}) {
		t.Errorf("packages = %v", got.Packages)
	}
	if got.InstalledKernel != "6.8.0-149-generic" {
		t.Errorf("installed kernel = %q", got.InstalledKernel)
	}
}

func TestReadReboot(t *testing.T) {
	since := time.Date(2026, 10, 28, 6, 13, 30, 0, time.UTC)
	file := func(data string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(data), ModTime: since} }
	history := func(pkgLine string) *fstest.MapFile {
		return file("\nStart-Date: 2026-10-28  06:12:44\nCommandline: /usr/bin/unattended-upgrade\n" + pkgLine + "\nEnd-Date: 2026-10-28  06:13:30\n")
	}
	tests := []struct {
		name string
		fs   fstest.MapFS
		want Reboot
	}{
		{"an empty host", fstest.MapFS{}, Reboot{}},
		{"no marker, a kernel installed",
			fstest.MapFS{historyFile: history("Install: linux-image-6.8.0-149-generic:amd64 (6.8.0-149.149, automatic)")},
			Reboot{InstalledKernel: "6.8.0-149-generic"}},
		{"a marker with its list",
			fstest.MapFS{rebootFile: file("*** System restart required ***\n"), rebootPkgsFile: file("libc6\nlinux-base\n\n")},
			Reboot{Required: true, Since: since, Packages: []string{"libc6", "linux-base"}}},
		{"a marker without its list",
			fstest.MapFS{rebootFile: file("")},
			Reboot{Required: true, Since: since}},
		{"the list alone is not a marker",
			fstest.MapFS{rebootPkgsFile: file("libc6\n")},
			Reboot{}},
		{"a metapackage is not a kernel release",
			fstest.MapFS{historyFile: history("Upgrade: linux-image-virtual:amd64 (6.8.0.147.153, 6.8.0.149.155)"),
				"boot/vmlinuz-6.8.0-147-generic": file("")},
			Reboot{InstalledKernel: "6.8.0-147-generic"}},
		{"the highest of several kernels in a run, by number",
			fstest.MapFS{historyFile: history("Install: linux-image-6.8.0-9-generic:amd64 (6.8.0-9.9), linux-image-6.8.0-149-generic:amd64 (6.8.0-149.149)")},
			Reboot{InstalledKernel: "6.8.0-149-generic"}},
		{"the boot directory when the history has no kernel",
			fstest.MapFS{historyFile: history("Upgrade: curl:amd64 (1, 2)"),
				"boot/vmlinuz-6.8.0-9-generic": file(""), "boot/vmlinuz-6.8.0-147-generic": file(""),
				"boot/vmlinuz": file(""), "boot/initrd.img-6.8.0-147-generic": file("")},
			Reboot{InstalledKernel: "6.8.0-147-generic"}},
		{"an older run's kernel when the newest run has none",
			fstest.MapFS{historyFile: file("\nStart-Date: 2026-10-20  06:00:00\nCommandline: apt\nInstall: linux-image-6.8.0-147-generic:amd64 (6.8.0-147.147)\nEnd-Date: 2026-10-20  06:01:00\n\n" +
				"Start-Date: 2026-10-28  06:12:44\nCommandline: apt\nUpgrade: curl:amd64 (1, 2)\nEnd-Date: 2026-10-28  06:13:30\n")},
			Reboot{InstalledKernel: "6.8.0-147-generic"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReadReboot(tc.fs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestRebootListedIn(t *testing.T) {
	r := Reboot{Required: true, Packages: []string{"linux-image-6.8.0-149-generic"}}
	kernelRun := Run{Upgraded: []Package{{Name: "curl"}, {Name: "linux-image-6.8.0-149-generic"}}}
	freshKernel := Run{Installed: []Package{{Name: "linux-image-6.8.0-149-generic"}}}
	other := Run{Upgraded: []Package{{Name: "curl"}}}
	removal := Run{Removed: []Package{{Name: "linux-image-6.8.0-149-generic"}}}
	for name, c := range map[string]struct {
		r    Reboot
		run  Run
		want bool
	}{
		"an upgrade of a listed package": {r, kernelRun, true},
		"an install of a listed package": {r, freshKernel, true},
		"a run of others":                {r, other, false},
		"a removal is not an update":     {r, removal, false},
		"no list, no match":              {Reboot{Required: true}, kernelRun, false},
	} {
		if got := c.r.ListedIn(c.run); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestKernelNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"6.8.0-149-generic", "6.8.0-147-generic", true},
		{"6.8.0-147-generic", "6.8.0-149-generic", false},
		{"6.8.0-149-generic", "6.8.0-9-generic", true}, // 149 is more than 9, though "1" < "9" as text
		{"6.10.0-1-generic", "6.8.0-149-generic", true},
		{"6.8.0-149-generic", "6.8.0-149-generic", false},
		{"6.8.0-149-generic", "6.8.0-149", true}, // the one with more after it
		{"6.8.0-0149-generic", "6.8.0-149-generic", false},
		{"6.8.0-149-lowlatency", "6.8.0-149-generic", true}, // words compare as text
		{"99999999999999999999999-generic", "9-generic", true},
	} {
		if got := KernelNewer(c.a, c.b); got != c.want {
			t.Errorf("KernelNewer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
