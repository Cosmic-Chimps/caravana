package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The installer (deploy/install/install.sh) and the lab (deploy/dev/setup.sh)
// install the same Nomad and CNI plugins: the lab is where every feature is
// tested, so a host must run what the lab runs. Both check what they download.
func TestInstallerMatchesLab(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	installer, lab := read("../../deploy/install/install.sh"), read("../../deploy/dev/setup.sh")

	for _, name := range []string{"NOMAD_VERSION", "CNI_VERSION"} {
		pin := regexp.MustCompile(`(?m)^` + name + `=(\S+)$`)
		got, want := pin.FindStringSubmatch(installer), pin.FindStringSubmatch(lab)
		if got == nil || want == nil {
			t.Fatalf("%s is pinned in neither or only one of the scripts", name)
		}
		if got[1] != want[1] {
			t.Errorf("%s: the installer pins %s, the lab %s", name, got[1], want[1])
		}
	}

	// Every download is followed by a checksum check of what it fetched.
	if downloads, checks := strings.Count(installer, "curl -fsSL -o"), strings.Count(installer, "sha256sum"); downloads == 0 || checks < 3 {
		t.Errorf("the installer downloads %d files but checks checksums %d times; want a check for Nomad, CNI and the release", downloads, checks)
	}
}
